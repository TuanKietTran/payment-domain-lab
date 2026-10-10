package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8081" || cfg.AuthToken != "" || cfg.ReadTimeout != 10*time.Second || cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"LEDGER_ADDR": "127.0.0.1:9", "LEDGER_AUTH_TOKEN": "s3cret", "HTTP_IDLE_TIMEOUT": "2m"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9" || cfg.AuthToken != "s3cret" || cfg.IdleTimeout != 2*time.Minute {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfigRejectsBadDurations(t *testing.T) {
	for _, value := range []string{"abc", "0", "-1s"} {
		if _, err := loadConfig(env(map[string]string{"HTTP_READ_TIMEOUT": value})); err == nil {
			t.Errorf("expected error for %q", value)
		}
	}
}

func start(t *testing.T, cfg config) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), func(a net.Addr) { addr <- a })
	}()
	select {
	case a := <-addr:
		return "http://" + a.String(), cancel, done
	case err := <-done:
		cancel()
		t.Fatalf("run exited early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not start")
	}
	return "", cancel, done
}

func stop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	cfg, _ := loadConfig(env(map[string]string{"LEDGER_ADDR": "127.0.0.1:0"}))
	base, cancel, done := start(t, cfg)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Request-ID") == "" {
		t.Fatalf("status=%d request-id=%q", response.StatusCode, response.Header.Get("X-Request-ID"))
	}
	stop(t, cancel, done)
	shutdownClient := &http.Client{Timeout: 500 * time.Millisecond}
	if response, err := shutdownClient.Get(base + "/healthz"); err == nil {
		response.Body.Close()
		t.Fatal("server still accepted requests after shutdown")
	}
}

func TestRunWiresAuthToken(t *testing.T) {
	cfg, _ := loadConfig(env(map[string]string{"LEDGER_ADDR": "127.0.0.1:0", "LEDGER_AUTH_TOKEN": "tok"}))
	base, cancel, done := start(t, cfg)
	defer stop(t, cancel, done)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	status := func(path, auth string) int {
		request, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if auth != "" {
			request.Header.Set("Authorization", auth)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.StatusCode
	}
	if got := status("/healthz", ""); got != http.StatusOK {
		t.Errorf("healthz = %d", got)
	}
	if got := status("/v1/accounts", ""); got != http.StatusUnauthorized {
		t.Errorf("no token = %d", got)
	}
	if got := status("/v1/accounts", "Bearer wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token = %d", got)
	}
	if got := status("/v1/accounts", "Bearer tok"); got != http.StatusOK {
		t.Errorf("right token = %d", got)
	}
}

func TestRunReportsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg, _ := loadConfig(env(map[string]string{"LEDGER_ADDR": listener.Addr().String()}))
	if err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil); err == nil {
		t.Fatal("expected listen error")
	}
}
