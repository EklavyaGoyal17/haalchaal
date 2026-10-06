// Package app wires configuration into the services, so serve, worker and
// simcall build the same graph.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/fake"
)

// App holds the wired services.
type App struct {
	Config    config.Config
	Pool      *pgxpool.Pool
	Clock     clock.Clock
	Log       *slog.Logger
	Keyring   *crypto.Keyring
	Voice     voice.Provider
	Calls     *calls.Service
	Scheduler *scheduler.Scheduler
}

// Options override parts of the graph (simcall and tests).
type Options struct {
	Clock clock.Clock
	Voice voice.Provider
	Gate  *safety.Gate
}

// New builds the App. In dev without ENCRYPTION_KEYS it uses a throwaway key
// and says so loudly; outside dev, config loading already requires keys.
func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, opt Options) (*App, error) {
	a := &App{Config: cfg, Pool: pool, Log: log, Clock: opt.Clock, Keyring: cfg.Keyring}
	if a.Clock == nil {
		a.Clock = clock.Real{}
	}
	if a.Keyring == nil {
		if cfg.AppEnv != config.EnvDev {
			return nil, fmt.Errorf("ENCRYPTION_KEYS is required outside dev")
		}
		k, err := crypto.GenerateKey()
		if err != nil {
			return nil, err
		}
		if a.Keyring, err = crypto.ParseKeyring("1:"+k, "1"); err != nil {
			return nil, err
		}
		log.Warn("ENCRYPTION_KEYS not set: using a throwaway dev key; encrypted data will be unreadable after restart")
	}

	a.Voice = opt.Voice
	if a.Voice == nil {
		v, err := newVoice(cfg, log)
		if err != nil {
			return nil, err
		}
		a.Voice = v
	}
	gate := safety.FromConfig(cfg)
	if opt.Gate != nil {
		gate = *opt.Gate
	}
	a.Calls = &calls.Service{
		Pool: pool, Clock: a.Clock, Log: log, Keyring: a.Keyring, Voice: a.Voice, Gate: gate,
		RetryOffsets:        cfg.RetryOffsets,
		TranscriptRetention: time.Duration(cfg.RetentionTranscriptDays) * 24 * time.Hour,
	}
	a.Scheduler = &scheduler.Scheduler{Pool: pool, Clock: a.Clock, Log: log, Provider: a.Voice.Name()}
	return a, nil
}

func newVoice(cfg config.Config, log *slog.Logger) (voice.Provider, error) {
	switch cfg.VoiceProvider {
	case "fake":
		secret := cfg.VoiceWebhookSecret
		if secret == "" {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			secret = hex.EncodeToString(b)
			log.Warn("VOICE_WEBHOOK_SECRET not set: fake provider uses a random secret for this process")
		}
		return fake.New(secret), nil
	default:
		return nil, fmt.Errorf("VOICE_PROVIDER %q is not available yet", cfg.VoiceProvider)
	}
}

// RegisterHandlers attaches every job handler this build has to w.
func (a *App) RegisterHandlers(w *jobs.Worker) {
	w.Handle(jobs.KindPlaceCall, a.Calls.PlaceCall)
}

// NewWorker returns a job worker with every handler registered.
func (a *App) NewWorker() *jobs.Worker {
	w := &jobs.Worker{
		DB: a.Pool, Clock: a.Clock, Log: a.Log, Concurrency: a.Config.WorkerConcurrency,
		OnDeadLetter: func(_ context.Context, kind string, id int64, _ jobs.Payload) {
			a.Log.Error("job dead-lettered", "job_id", id, "kind", kind)
		},
	}
	a.RegisterHandlers(w)
	return w
}

// RunScheduler runs the scheduler tick and the stale-call sweep every minute
// until ctx is done.
func (a *App) RunScheduler(ctx context.Context) error {
	t := time.NewTicker(scheduler.TickInterval)
	defer t.Stop()
	for {
		if _, err := a.Scheduler.Tick(ctx); err != nil && ctx.Err() == nil {
			a.Log.Error("scheduler tick", "error", err)
		}
		if _, err := a.Calls.SweepStale(ctx); err != nil && ctx.Err() == nil {
			a.Log.Error("stale call sweep", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
