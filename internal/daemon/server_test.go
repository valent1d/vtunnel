package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vtunnel/internal/api"
	"vtunnel/internal/config"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

func TestRequestHostnameUsesForwardedHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://internal.local", nil)
	req.Header.Set("X-Forwarded-Host", "Dev.Example.Test:443")

	got := requestHostname(req)
	want := "dev.example.test"
	if got != want {
		t.Fatalf("hostname = %q, want %q", got, want)
	}
}

func TestStoreValidationRejectsMissingTarget(t *testing.T) {
	err := routes.Validate(routes.Route{Hostname: "dev.example.test"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestServerRoutesProxyRequestsByHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("X-Forwarded-Host"), "dev.example.test"; got != want {
			t.Errorf("X-Forwarded-Host = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("X-Forwarded-Proto"), "https"; got != want {
			t.Errorf("X-Forwarded-Proto = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, "proxied:"+r.URL.Path)
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Proxy.Listen = freeLoopbackAddr(t)
	cfg.API.Listen = freeLoopbackAddr(t)

	store, err := routes.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	logStore, err := requestlog.NewStore(filepath.Join(t.TempDir(), "requests.jsonl"), requestlog.DefaultMaxEntries)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		errs <- New(cfg, store, logStore, logger).Run(ctx)
	}()

	client := api.New(cfg)
	waitForHealth(t, client)

	if err := client.AddRoute(ctx, routes.Route{
		Hostname: "dev.example.test",
		Target:   upstream.URL,
	}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cfg.Proxy.Listen+"/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "dev.example.test"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), "proxied:/hello"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	logs, err := client.ListLogs(ctx, requestlog.Filter{Hostname: "dev.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs length = %d, want 1", len(logs))
	}
	if got, want := logs[0].Status, http.StatusOK; got != want {
		t.Fatalf("log status = %d, want %d", got, want)
	}
	if got, want := logs[0].Path, "/hello"; got != want {
		t.Fatalf("log path = %q, want %q", got, want)
	}

	cancel()
	select {
	case err := <-errs:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("server returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func waitForHealth(t *testing.T, client *api.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for {
		_, err := client.Health(ctx)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("daemon did not become healthy: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
