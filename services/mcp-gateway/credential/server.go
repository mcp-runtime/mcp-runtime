package credential

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Run serves the credential gateway described by the config file at path until
// the process is told to stop.
func Run(path string) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	store := &Store{TTL: cfg.FormTTL, Max: cfg.FormMaxEntries}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           New(cfg, store, nil),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("credential gateway listening", "addr", cfg.Listen, "services", len(cfg.Services))
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
