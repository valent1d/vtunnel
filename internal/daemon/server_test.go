package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
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

	proxyAddr, proxyLn := reserveLoopback(t)
	apiAddr, apiLn := reserveLoopback(t)
	cfg := config.Default()
	cfg.Proxy.Listen = proxyAddr
	cfg.API.Listen = apiAddr

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
		errs <- New(cfg, store, logStore, logger, WithListeners(proxyLn, apiLn)).Run(ctx)
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

func TestServerCapturesAndReplaysRequest(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != "ping" {
			t.Errorf("upstream body = %q, want ping", body)
		}
		w.Header().Set("X-Upstream", "yes")
		_, _ = io.WriteString(w, "pong")
	}))
	defer upstream.Close()

	proxyAddr, proxyLn := reserveLoopback(t)
	apiAddr, apiLn := reserveLoopback(t)
	cfg := config.Default()
	cfg.Proxy.Listen = proxyAddr
	cfg.API.Listen = apiAddr

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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { _ = New(cfg, store, logStore, logger, WithListeners(proxyLn, apiLn)).Run(ctx) }()

	client := api.New(cfg)
	waitForHealth(t, client)
	if err := client.AddRoute(ctx, routes.Route{Hostname: "dev.example.test", Target: upstream.URL}); err != nil {
		t.Fatal(err)
	}

	// Send a request with a body through the proxy.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+cfg.Proxy.Listen+"/submit", strings.NewReader("ping"))
	req.Host = "dev.example.test"
	req.Header.Set("X-Custom", "abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	logs, err := client.ListLogs(ctx, requestlog.Filter{Hostname: "dev.example.test"})
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %v, err = %v", logs, err)
	}
	id := logs[0].ID

	// The captured exchange has request headers/body and response body.
	exchange, err := client.GetExchange(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if exchange.Method != http.MethodPost || exchange.Path != "/submit" {
		t.Fatalf("exchange method/path = %s %s", exchange.Method, exchange.Path)
	}
	if string(exchange.RequestBody) != "ping" {
		t.Fatalf("captured request body = %q", exchange.RequestBody)
	}
	if exchange.RequestHeaders.Get("X-Custom") != "abc" {
		t.Fatalf("captured request header missing: %v", exchange.RequestHeaders)
	}
	if string(exchange.ResponseBody) != "pong" {
		t.Fatalf("captured response body = %q", exchange.ResponseBody)
	}
	if exchange.ResponseHeaders.Get("X-Upstream") != "yes" {
		t.Fatalf("captured response header missing: %v", exchange.ResponseHeaders)
	}

	// Replay re-hits the upstream with the captured body.
	if err := client.ReplayRequest(ctx, id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&hits) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&hits); got < 2 {
		t.Fatalf("upstream hits = %d, want >= 2 after replay", got)
	}
}

func TestProxyHandlerRewritesHostAndForwardsHeaders(t *testing.T) {
	var gotHost, gotXFH, gotXFP, gotXFF string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFH = r.Header.Get("X-Forwarded-Host")
		gotXFP = r.Header.Get("X-Forwarded-Proto")
		gotXFF = r.Header.Get("X-Forwarded-For")
		_, _ = io.WriteString(w, "ok:"+r.URL.Path)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	store, err := routes.NewStore(filepath.Join(t.TempDir(), "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(routes.Route{Hostname: "dev.example.test", Target: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	logStore, err := requestlog.NewStore(filepath.Join(t.TempDir(), "requests.jsonl"), requestlog.DefaultMaxEntries)
	if err != nil {
		t.Fatal(err)
	}

	handler := New(config.Default(), store, logStore, slog.New(slog.NewTextHandler(io.Discard, nil))).proxyHandler()

	// Known host: request is proxied, Host is rewritten to the upstream and the
	// forwarded headers reflect the public hostname over https.
	req := httptest.NewRequest(http.MethodGet, "http://proxy.local/hello", nil)
	req.Host = "dev.example.test"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "ok:/hello"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if gotHost != upstreamURL.Host {
		t.Errorf("upstream Host = %q, want %q", gotHost, upstreamURL.Host)
	}
	if gotXFH != "dev.example.test" {
		t.Errorf("X-Forwarded-Host = %q, want %q", gotXFH, "dev.example.test")
	}
	if gotXFP != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want %q", gotXFP, "https")
	}
	if gotXFF == "" {
		t.Error("X-Forwarded-For was not set on the proxied request")
	}

	// Unknown host: no route, so the proxy must answer 404 without dialing upstream.
	unknown := httptest.NewRequest(http.MethodGet, "http://proxy.local/", nil)
	unknown.Host = "missing.example.test"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, unknown)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown-host status = %d, want %d", rec.Code, http.StatusNotFound)
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

// reserveLoopback binds a loopback listener and keeps it open (closed on
// cleanup), returning its address and the listener. Passing the listener to the
// server via WithListeners avoids the bind-after-close race that flakes under
// parallel test execution.
func reserveLoopback(t *testing.T) (string, net.Listener) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String(), listener
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
