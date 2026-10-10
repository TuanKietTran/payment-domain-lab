package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"payment-sim/internal/ledger"
)

type config struct {
	Addr              string
	AuthToken         string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func loadConfig(getenv func(string) string) (config, error) {
	value := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	var firstErr error
	duration := func(name string, fallback time.Duration) time.Duration {
		raw := getenv(name)
		if raw == "" {
			return fallback
		}
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s must be a positive duration, got %q", name, raw)
			}
			return fallback
		}
		return parsed
	}
	cfg := config{
		Addr:              value("LEDGER_ADDR", ":8081"),
		AuthToken:         getenv("LEDGER_AUTH_TOKEN"),
		ReadHeaderTimeout: duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       duration("HTTP_READ_TIMEOUT", 10*time.Second),
		WriteTimeout:      duration("HTTP_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:       duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout:   duration("SHUTDOWN_TIMEOUT", 5*time.Second),
	}
	return cfg, firstErr
}

func newServer(cfg config, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           ledger.NewHandler(ledger.NewStore(), logger, cfg.AuthToken),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

// run serves until ctx is canceled or the listener fails. ready, if set, is
// called with the bound address once the server is accepting connections.
func run(ctx context.Context, cfg config, logger *slog.Logger, ready func(net.Addr)) error {
	server := newServer(cfg, logger)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	logger.Info("starting", "service", "ledger-service", "addr", listener.Addr().String(), "auth_enabled", cfg.AuthToken != "")
	if ready != nil {
		ready(listener.Addr())
	}
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdown)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = run(ctx, cfg, logger, nil)
	stop()
	if err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
