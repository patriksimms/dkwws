// Command dkwws-viewer serves share links from a private S3-compatible bucket.
//
// It needs read-only credentials: it resolves a link token to a link record,
// checks the expiry and streams the referenced object.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/patriksimms/dkwws/internal/config"
	"github.com/patriksimms/dkwws/internal/store"
	"github.com/patriksimms/dkwws/internal/viewer"
)

// version is set at build time with -ldflags.
var version = "dev"

const (
	// defaultAddr matches the port Coolify's reverse proxy expects by default.
	defaultAddr = ":8080"
	// shutdownGrace is how long in-flight responses get to finish.
	shutdownGrace = 15 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("viewer stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, _, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	s, err := store.New(cfg)
	if err != nil {
		return err
	}

	addr := strings.TrimSpace(os.Getenv("DKWWS_LISTEN_ADDR"))
	if addr == "" {
		addr = defaultAddr
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           viewer.New(s, viewer.Options{Logger: logger}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		logger.Info("viewer listening", "addr", addr, "bucket", cfg.Bucket, "version", version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
