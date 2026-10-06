// Package app wires configuration into the services, so serve, worker and
// simcall build the same graph.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/admin"
	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
	fakeextract "github.com/EklavyaGoyal17/haalchaal/internal/extract/fake"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/maintenance"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify/cloud"
	fakenotify "github.com/EklavyaGoyal17/haalchaal/internal/notify/fake"
	"github.com/EklavyaGoyal17/haalchaal/internal/outbound"
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
	Messenger notify.Messenger
	Outbound  *outbound.Service
	Retention *maintenance.Retention
}

// Options override parts of the graph (simcall and tests).
type Options struct {
	Clock     clock.Clock
	Voice     voice.Provider
	Messenger notify.Messenger
	Extractor extract.Extractor
	Gate      *safety.Gate
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
		FollowUpTTL:         cfg.FollowUpTTL,
	}
	ex := opt.Extractor
	if ex == nil {
		var err error
		if ex, err = newExtractor(cfg); err != nil {
			return nil, err
		}
	}
	a.Calls.Extractor = ex
	a.Scheduler = &scheduler.Scheduler{Pool: pool, Clock: a.Clock, Log: log, Provider: a.Voice.Name()}

	a.Messenger = opt.Messenger
	if a.Messenger == nil {
		m, err := newMessenger(cfg, log)
		if err != nil {
			return nil, err
		}
		a.Messenger = m
	}
	review := ""
	if cfg.PublicBaseURL != "" {
		review = strings.TrimRight(cfg.PublicBaseURL, "/") + "/admin/review"
	}
	a.Outbound = &outbound.Service{
		Pool: pool, Clock: a.Clock, Log: log, Keyring: a.Keyring, Messenger: a.Messenger, Gate: gate,
		AdminPhones: cfg.AdminAlertPhones, ReviewURL: review,
		Timeouts: alerts.Timeouts{Emergency: cfg.AlertAckTimeout, Urgent: cfg.UrgentAckTimeout},
	}
	a.Retention = &maintenance.Retention{
		Pool: pool, Clock: a.Clock, Log: log, Voice: a.Voice, Location: cfg.DefaultTimezone,
		TranscriptTTL: time.Duration(cfg.RetentionTranscriptDays) * 24 * time.Hour,
		AuditTTL:      time.Duration(cfg.RetentionAuditDays) * 24 * time.Hour,
	}
	return a, nil
}

func newMessenger(cfg config.Config, log *slog.Logger) (notify.Messenger, error) {
	switch cfg.WhatsAppProvider {
	case "fake":
		secret, verify := cfg.WhatsAppAppSecret, cfg.WhatsAppVerifyToken
		if secret == "" {
			secret = randomHex()
			log.Warn("WHATSAPP_APP_SECRET not set: fake messenger uses a random secret for this process")
		}
		if verify == "" {
			verify = randomHex()
		}
		return fakenotify.New(secret, verify, log), nil
	case "cloud":
		return cloud.New(cfg.WhatsAppAPIVersion, cfg.WhatsAppPhoneNumberID, cfg.WhatsAppToken, cfg.WhatsAppAppSecret, cfg.WhatsAppVerifyToken)
	default:
		return nil, fmt.Errorf("WHATSAPP_PROVIDER %q is not supported", cfg.WhatsAppProvider)
	}
}

func randomHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newExtractor(cfg config.Config) (extract.Extractor, error) {
	switch cfg.LLMProvider {
	case "fake":
		return fakeextract.Extractor{}, nil
	default:
		return nil, fmt.Errorf("LLM_PROVIDER %q is not available yet", cfg.LLMProvider)
	}
}

func newVoice(cfg config.Config, log *slog.Logger) (voice.Provider, error) {
	switch cfg.VoiceProvider {
	case "fake":
		secret := cfg.VoiceWebhookSecret
		if secret == "" {
			secret = randomHex()
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
	w.Handle(jobs.KindProcessCall, a.Calls.ProcessCall)
	w.Handle(jobs.KindEscalateAlert, a.Outbound.EscalateAlert)
	w.Handle(jobs.KindSendAlert, a.Outbound.SendAlert)
	w.Handle(jobs.KindSendSummary, a.Outbound.SendSummary)
	w.Handle(alerts.KindAdminNotice, a.Outbound.AdminNotice)
	w.Handle(jobs.KindRetention, a.Retention.Run)
}

// NewWorker returns a job worker with every handler registered.
func (a *App) NewWorker() *jobs.Worker {
	w := &jobs.Worker{
		DB: a.Pool, Clock: a.Clock, Log: a.Log, Concurrency: a.Config.WorkerConcurrency,
		OnDeadLetter: a.Outbound.DeadLetter,
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
		if err := a.Retention.Schedule(ctx); err != nil && ctx.Err() == nil {
			a.Log.Error("schedule retention", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// NewAdmin builds the admin pages, or returns nil (with a warning) when no
// ADMIN_USERS are configured, which config only allows in dev.
func (a *App) NewAdmin() (*admin.Handler, error) {
	users, err := admin.ParseUsers(a.Config.AdminUsers)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		a.Log.Warn("ADMIN_USERS not set: admin pages disabled")
		return nil, nil
	}
	return admin.New(admin.Handler{
		Pool: a.Pool, Clock: a.Clock, Log: a.Log, Keyring: a.Keyring, Users: users,
		CSRFKey: a.Keyring.DeriveKey("admin-csrf/v1"), Calls: a.Calls, Location: a.Config.DefaultTimezone,
		RequireTLS: a.Config.AppEnv != config.EnvDev, TrustedProxies: a.Config.TrustedProxies,
	})
}
