package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vtunnel/internal/config"
	"vtunnel/internal/logrotate"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

type Server struct {
	cfg    config.Config
	routes *routes.Store
	logs   *requestlog.Store
	logger *slog.Logger
}

func New(cfg config.Config, routeStore *routes.Store, logStore *requestlog.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, routes: routeStore, logs: logStore, logger: logger}
}

func (s *Server) Run(ctx context.Context) error {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	proxyServer := &http.Server{
		Addr:              s.cfg.Proxy.Listen,
		Handler:           s.proxyHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	apiServer := &http.Server{
		Addr:              s.cfg.API.Listen,
		Handler:           s.apiHandler(stop),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 2)
	go func() {
		s.logger.Info("proxy listening", "addr", s.cfg.Proxy.Listen)
		errs <- listenAndServeLocal(proxyServer)
	}()
	go func() {
		s.logger.Info("api listening", "addr", s.cfg.API.Listen)
		errs <- listenAndServeLocal(apiServer)
	}()
	go s.runLogJanitor(runCtx)

	select {
	case <-runCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxyServer.Shutdown(shutdownCtx)
		_ = apiServer.Shutdown(shutdownCtx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	case err := <-errs:
		_ = proxyServer.Close()
		_ = apiServer.Close()
		return err
	}
}

// logJanitorMaxBytes caps each *.log file the janitor manages.
const logJanitorMaxBytes = 5 << 20 // 5 MiB

// runLogJanitor periodically caps the *.log files in the logs directory so they
// never grow without bound — cloudflared and launchd append to them for the
// lifetime of a long-running tunnel.
func (s *Server) runLogJanitor(ctx context.Context) {
	logsDir, err := config.LogsDir()
	if err != nil {
		return
	}
	sweep := func() {
		if _, err := logrotate.CapDir(logsDir, logJanitorMaxBytes); err != nil {
			s.logger.Warn("log janitor failed", "dir", logsDir, "error", err)
		}
	}
	sweep()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func listenAndServeLocal(server *http.Server) error {
	host, _, err := net.SplitHostPort(server.Addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", server.Addr, err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("refusing to bind local API/proxy to non-local address %q", server.Addr)
	}

	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type proxyTargetKey struct{}

type proxyTarget struct {
	url      *url.URL
	hostname string
}

func (s *Server) proxyHandler() http.Handler {
	// A single reverse proxy is shared across requests; the per-request target is
	// resolved in the handler and passed through the request context. This avoids
	// allocating a proxy per request and uses the modern Rewrite hook (Director
	// is deprecated as of Go 1.26).
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target, ok := pr.In.Context().Value(proxyTargetKey{}).(*proxyTarget)
			if !ok {
				return
			}
			pr.SetURL(target.url)
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Host", target.hostname)
			proto := pr.In.Header.Get("X-Forwarded-Proto")
			if proto == "" {
				proto = "https"
			}
			pr.Out.Header.Set("X-Forwarded-Proto", proto)
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			host, target := "", ""
			if t, ok := req.Context().Value(proxyTargetKey{}).(*proxyTarget); ok {
				host, target = t.hostname, t.url.String()
			}
			s.logger.Warn("proxy request failed", "host", host, "target", target, "error", err)
			http.Error(w, "vtunnel: upstream unavailable", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname := requestHostname(r)
		route, ok := s.routes.Get(hostname)
		if !ok {
			http.Error(w, fmt.Sprintf("vtunnel: no route for %s", hostname), http.StatusNotFound)
			return
		}

		target, err := url.Parse(route.Target)
		if err != nil {
			http.Error(w, "vtunnel: invalid route target", http.StatusBadGateway)
			return
		}

		ctx := context.WithValue(r.Context(), proxyTargetKey{}, &proxyTarget{url: target, hostname: hostname})
		recorder := newResponseRecorder(w)
		started := time.Now()
		proxy.ServeHTTP(recorder, r.WithContext(ctx))
		s.recordRequest(r, route, recorder, time.Since(started))
	})
}

func (s *Server) apiHandler(shutdown func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /routes", s.handleListRoutes)
	mux.HandleFunc("POST /routes", s.handleAddRoute)
	mux.HandleFunc("DELETE /routes/{hostname}", s.handleDeleteRoute)
	mux.HandleFunc("GET /logs", s.handleListLogs)
	mux.HandleFunc("POST /shutdown", s.handleShutdown(shutdown))
	return localOnly(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	logCount := 0
	if s.logs != nil {
		logCount = s.logs.Count()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"routes": len(s.routes.List()),
		"logs":   logCount,
	})
}

func (s *Server) handleListRoutes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.routes.List())
}

func (s *Server) handleAddRoute(w http.ResponseWriter, r *http.Request) {
	var route routes.Route
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := s.routes.Add(route); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, route)
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	deleted, err := s.routes.Delete(hostname)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeJSON(w, http.StatusOK, []requestlog.Entry{})
		return
	}
	query := r.URL.Query()
	afterID, err := parseUintQuery(query.Get("after"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "after must be a positive integer"})
		return
	}
	limit, err := parseIntQuery(query.Get("limit"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be a positive integer"})
		return
	}
	writeJSON(w, http.StatusOK, s.logs.List(requestlog.Filter{
		Hostname: query.Get("hostname"),
		AfterID:  afterID,
		Limit:    limit,
	}))
}

func (s *Server) handleShutdown(shutdown func()) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		go func() {
			time.Sleep(50 * time.Millisecond)
			shutdown()
		}()
	}
}

func (s *Server) recordRequest(r *http.Request, route routes.Route, recorder *responseRecorder, duration time.Duration) {
	if s.logs == nil {
		return
	}
	path := r.URL.RequestURI()
	if path == "" {
		path = r.URL.Path
	}
	_, err := s.logs.Add(requestlog.Entry{
		Time:       time.Now(),
		Hostname:   route.Hostname,
		Method:     r.Method,
		Path:       path,
		Status:     recorder.statusCode(),
		Bytes:      recorder.bytesWritten,
		Duration:   duration,
		Target:     route.Target,
		RemoteAddr: r.RemoteAddr,
	})
	if err != nil {
		s.logger.Warn("record request log failed", "host", route.Hostname, "error", err)
	}
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "vtunnel API only accepts loopback clients", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestHostname(r *http.Request) string {
	host := r.Host
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	return routes.NormalizeHostname(host)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func parseUintQuery(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

func parseIntQuery(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New("invalid integer")
	}
	return parsed, nil
}

type responseRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w}
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(data)
	r.bytesWritten += int64(n)
	return n, err
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *responseRecorder) ReadFrom(reader io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err := readerFrom.ReadFrom(reader)
		r.bytesWritten += n
		return n, err
	}
	return io.Copy(r.ResponseWriter, reader)
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *responseRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}
