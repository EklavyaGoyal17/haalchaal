// Command haalchaal is the HaalChaal backend. Subcommands: serve, worker,
// migrate, simcall.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/httpapi"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
	"github.com/EklavyaGoyal17/haalchaal/internal/migrate"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
)

const usage = `usage: haalchaal <command>

commands:
  serve     run the HTTP server (webhooks and admin pages)
  worker    run the scheduler and job workers
  dev       run serve and worker in one process (local development)
  migrate   up | down | status: manage database migrations
  genkey    print a new encryption key entry for ENCRYPTION_KEYS
  simcall   replay a golden transcript through the pipeline (Milestone 3)
`

func main() {
	log := logging.New(os.Stdout, slog.LevelInfo)
	slog.SetDefault(log)

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd := os.Args[1]; cmd {
	case "serve":
		err = serve(ctx, log)
	case "migrate":
		err = runMigrate(ctx, log, os.Args[2:])
	case "genkey":
		err = genkey(os.Args[2:])
	case "worker":
		err = runWorker(ctx, log)
	case "dev":
		err = runDev(ctx, log)
	case "simcall":
		err = fmt.Errorf("%s is not implemented yet", cmd)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func loadConfig(log *slog.Logger) (config.Config, error) {
	cfg, warnings, err := config.Load(os.Getenv)
	for _, w := range warnings {
		log.Warn("config", "warning", w)
	}
	if err != nil {
		return cfg, fmt.Errorf("invalid configuration: %w", err)
	}
	log.Info("config loaded", "config", cfg)
	return cfg, nil
}

func openPool(ctx context.Context, log *slog.Logger) (config.Config, *pgxpool.Pool, error) {
	cfg, err := loadConfig(log)
	if err != nil {
		return cfg, nil, err
	}
	if err := cfg.RequireDatabase(); err != nil {
		return cfg, nil, err
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return cfg, nil, fmt.Errorf("open database pool: %w", err)
	}
	return cfg, pool, nil
}

func runMigrate(ctx context.Context, log *slog.Logger, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: haalchaal migrate up|down|status")
	}
	_, pool, err := openPool(ctx, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	m, err := migrate.New(pool)
	if err != nil {
		return err
	}
	defer m.Close()

	switch args[0] {
	case "up":
		v, err := m.Up(ctx)
		if err != nil {
			return err
		}
		log.Info("migrations applied", "version", v)
	case "down":
		v, err := m.Down(ctx)
		if err != nil {
			return err
		}
		log.Info("rolled back one migration", "version", v)
	case "status":
		statuses, err := m.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			fmt.Printf("%-8s %d %s\n", s.State, s.Source.Version, filepath.Base(s.Source.Path))
		}
	default:
		return fmt.Errorf("unknown migrate command %q (want up, down or status)", args[0])
	}
	return nil
}

// genkey prints "<kid>:<base64 key>" to append to ENCRYPTION_KEYS.
func genkey(args []string) error {
	kid := "1"
	if len(args) > 0 {
		kid = args[0]
	}
	if n, err := strconv.Atoi(kid); err != nil || n < 1 || n > 255 {
		return errors.New("usage: haalchaal genkey [kid 1-255]")
	}
	k, err := crypto.GenerateKey()
	if err != nil {
		return err
	}
	fmt.Printf("%s:%s\n", kid, k)
	return nil
}

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, pool, err := openPool(ctx, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	m, err := migrate.New(pool)
	if err != nil {
		return err
	}
	defer m.Close()

	api := &httpapi.Server{DB: pool, Migrations: m, Log: log}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// runWorker runs the scheduler tick and the job workers until shutdown. Any
// number of worker processes may run against one database.
func runWorker(ctx context.Context, log *slog.Logger) error {
	cfg, pool, err := openPool(ctx, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := requireMigrated(ctx, pool); err != nil {
		return err
	}

	clk := clock.Real{}
	w := &jobs.Worker{
		DB:          pool,
		Clock:       clk,
		Log:         log,
		Concurrency: cfg.WorkerConcurrency,
		OnDeadLetter: func(_ context.Context, kind string, id int64, _ jobs.Payload) {
			log.Error("job dead-lettered", "job_id", id, "kind", kind)
		},
	}
	sched := &scheduler.Scheduler{Pool: pool, Clock: clk, Log: log, Provider: cfg.VoiceProvider}

	errc := make(chan error, 2)
	go func() { errc <- w.Run(ctx) }()
	go func() { errc <- sched.Run(ctx) }()
	err = errors.Join(<-errc, <-errc)
	return err
}

func requireMigrated(ctx context.Context, pool *pgxpool.Pool) error {
	m, err := migrate.New(pool)
	if err != nil {
		return err
	}
	defer m.Close()
	pending, err := m.HasPending(ctx)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("database has pending migrations; run haalchaal migrate up")
	}
	return nil
}

// runDev runs the server and the worker together, for make dev.
func runDev(ctx context.Context, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- serve(ctx, log); cancel() }()
	go func() { errc <- runWorker(ctx, log); cancel() }()
	return errors.Join(<-errc, <-errc)
}
