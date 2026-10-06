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
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/httpapi"
	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
)

const usage = `usage: haalchaal <command>

commands:
  serve     run the HTTP server (webhooks and admin pages)
  worker    run the scheduler and job workers (Milestone 2)
  migrate   apply database migrations (Milestone 1)
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
	case "worker", "migrate", "simcall":
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

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := loadConfig(log)
	if err != nil {
		return err
	}
	if err := cfg.RequireDatabase(); err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer pool.Close()

	api := &httpapi.Server{DB: pool, Log: log}
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
