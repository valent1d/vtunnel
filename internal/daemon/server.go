package daemon

import (
	"bufio"
	"bytes"
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
	cfg           config.Config
	routes        *routes.Store
	logs          *requestlog.Store
	exchanges     *requestlog.ExchangeStore
	bodyLimit     int
	logger        *slog.Logger
	proxyListener net.Listener
	apiListener   net.Listener
}

// Option configures a Server.
type Option func(*Server)

// WithListeners makes Run serve on pre-bound listeners instead of binding the
// configured addresses. Tests use it to avoid the bind-after-close port race;
// both listeners must be loopback.
func WithListeners(proxy, api net.Listener) Option {
	return func(s *Server) {
		s.proxyListener = proxy
		s.apiListener = api
	}
}

func New(cfg config.Config, routeStore *routes.Store, logStore *requestlog.Store, logger *slog.Logger, opts ...Option) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{
		cfg:       cfg,
		routes:    routeStore,
		logs:      logStore,
		exchanges: requestlog.NewExchangeStore(requestlog.DefaultMaxExchanges),
		bodyLimit: requestlog.DefaultBodyCaptureLimit,
		logger:    logger,
	}
	for _, opt := range opts {
		opt(server)
	}
	return server
}

func (s *Server) Run(ctx context.Context) error {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	proxyServer := &http.Server{
		Handler:           s.proxyHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	apiServer := &http.Server{
		Handler:           s.apiHandler(stop),
		ReadHeaderTimeout: 10 * time.Second,
	}

	proxyListener, err := loopbackListener(s.proxyListener, s.cfg.Proxy.Listen)
	if err != nil {
		return err
	}
	apiListener, err := loopbackListener(s.apiListener, s.cfg.API.Listen)
	if err != nil {
		_ = proxyListener.Close()
		return err
	}

	errs := make(chan error, 2)
	go func() {
		s.logger.Info("proxy listening", "addr", proxyListener.Addr().String())
		errs <- serveUntilClosed(proxyServer, proxyListener)
	}()
	go func() {
		s.logger.Info("api listening", "addr", apiListener.Addr().String())
		errs <- serveUntilClosed(apiServer, apiListener)
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

// loopbackListener returns the injected listener when present (validated as
// loopback), otherwise binds the configured address. Binding here — rather than
// via http.Server.ListenAndServe — lets tests hand in a pre-bound listener and
// avoid the bind-after-close port race.
func loopbackListener(injected net.Listener, addr string) (net.Listener, error) {
	if injected != nil {
		if err := requireLoopback(injected.Addr().String()); err != nil {
			return nil, err
		}
		return injected, nil
	}
	if err := requireLoopback(addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", addr)
}

func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("refusing to bind local API/proxy to non-local address %q", addr)
	}
	return nil
}

func serveUntilClosed(server *http.Server, listener net.Listener) error {
	err := server.Serve(listener)
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

		// Capture the request headers and a capped prefix of the body without
		// buffering the whole upload: the proxy still streams the full body
		// upstream through the tee.
		requestHeaders := r.Header.Clone()
		requestBody := newCappedBuffer(s.bodyLimit)
		if r.Body != nil {
			r.Body = &teeReadCloser{reader: io.TeeReader(r.Body, requestBody), closer: r.Body}
		}

		ctx := context.WithValue(r.Context(), proxyTargetKey{}, &proxyTarget{url: target, hostname: hostname})
		recorder := newResponseRecorder(w, s.bodyLimit)
		started := time.Now()
		proxy.ServeHTTP(recorder, r.WithContext(ctx))
		entry := s.recordRequest(r, route, recorder, time.Since(started))
		s.recordExchange(entry, route, hostname, requestHeaders, requestBody, recorder)
	})
}

func (s *Server) apiHandler(shutdown func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /routes", s.handleListRoutes)
	mux.HandleFunc("POST /routes", s.handleAddRoute)
	mux.HandleFunc("DELETE /routes/{hostname}", s.handleDeleteRoute)
	mux.HandleFunc("GET /logs", s.handleListLogs)
	mux.HandleFunc("GET /logs/{id}", s.handleGetExchange)
	mux.HandleFunc("POST /logs/{id}/replay", s.handleReplay)
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

func (s *Server) handleGetExchange(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id must be a positive integer"})
		return
	}
	if s.exchanges == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request capture is disabled"})
		return
	}
	exchange, ok := s.exchanges.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request no longer captured"})
		return
	}
	writeJSON(w, http.StatusOK, exchange)
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id must be a positive integer"})
		return
	}
	if s.exchanges == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request capture is disabled"})
		return
	}
	exchange, ok := s.exchanges.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request no longer captured"})
		return
	}
	if err := s.replay(r.Context(), exchange); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// replay re-issues a captured request back through the local proxy, so it gets
// routed to the same upstream and logged/captured as a fresh request.
func (s *Server) replay(ctx context.Context, exchange requestlog.Exchange) error {
	req, err := http.NewRequestWithContext(ctx, exchange.Method, "http://"+s.cfg.Proxy.Listen+exchange.Path, bytes.NewReader(exchange.RequestBody))
	if err != nil {
		return err
	}
	// Route by Host, exactly as the original request was routed.
	req.Host = exchange.Hostname
	for key, values := range exchange.RequestHeaders {
		if skipReplayHeader[http.CanonicalHeaderKey(key)] {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// skipReplayHeader lists headers that must not be copied verbatim on replay:
// hop-by-hop headers, the Host (set via req.Host), the length (set from the
// body), and the X-Forwarded-* headers the proxy re-derives.
var skipReplayHeader = map[string]bool{
	"Host":              true,
	"Content-Length":    true,
	"Connection":        true,
	"Proxy-Connection":  true,
	"Keep-Alive":        true,
	"Transfer-Encoding": true,
	"Upgrade":           true,
	"Te":                true,
	"Trailer":           true,
	"X-Forwarded-For":   true,
	"X-Forwarded-Host":  true,
	"X-Forwarded-Proto": true,
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

func (s *Server) recordRequest(r *http.Request, route routes.Route, recorder *responseRecorder, duration time.Duration) requestlog.Entry {
	if s.logs == nil {
		return requestlog.Entry{}
	}
	path := r.URL.RequestURI()
	if path == "" {
		path = r.URL.Path
	}
	entry, err := s.logs.Add(requestlog.Entry{
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
	return entry
}

// recordExchange stores the full captured request/response keyed by the log
// entry ID, so the dashboard can inspect headers/bodies and replay it.
func (s *Server) recordExchange(entry requestlog.Entry, route routes.Route, hostname string, requestHeaders http.Header, requestBody *cappedBuffer, recorder *responseRecorder) {
	if s.exchanges == nil || entry.ID == 0 {
		return
	}
	s.exchanges.Put(requestlog.Exchange{
		ID:                entry.ID,
		Hostname:          hostname,
		Method:            entry.Method,
		Path:              entry.Path,
		Target:            route.Target,
		Status:            recorder.statusCode(),
		RequestHeaders:    requestHeaders,
		RequestBody:       requestBody.bytes(),
		RequestTruncated:  requestBody.truncated,
		ResponseHeaders:   recorder.capturedHeader(),
		ResponseBody:      recorder.body.bytes(),
		ResponseTruncated: recorder.body.truncated,
	})
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
	header       http.Header // snapshot of response headers at WriteHeader time
	body         *cappedBuffer
}

func newResponseRecorder(w http.ResponseWriter, bodyLimit int) *responseRecorder {
	return &responseRecorder{ResponseWriter: w, body: newCappedBuffer(bodyLimit)}
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	if r.header == nil {
		r.header = r.ResponseWriter.Header().Clone()
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(data)
	if r.body != nil && n > 0 {
		_, _ = r.body.Write(data[:n])
	}
	r.bytesWritten += int64(n)
	return n, err
}

// capturedHeader returns the response headers snapshotted at WriteHeader, or the
// live header map if the handler never called WriteHeader explicitly.
func (r *responseRecorder) capturedHeader() http.Header {
	if r.header != nil {
		return r.header
	}
	return r.ResponseWriter.Header().Clone()
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
	// Tee into the capture buffer so bodies streamed via ReadFrom (the proxy's
	// fast path) are captured too, not just those written through Write.
	if r.body != nil {
		reader = io.TeeReader(reader, r.body)
	}
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err := readerFrom.ReadFrom(reader)
		r.bytesWritten += n
		return n, err
	}
	n, err := io.Copy(r.ResponseWriter, reader)
	r.bytesWritten += n
	return n, err
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

// cappedBuffer captures up to limit bytes written to it and flags whether more
// arrived. It is a tee sink: it always reports the full length as consumed so
// it never disturbs the real request/response stream.
type cappedBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	if limit < 0 {
		limit = 0
	}
	return &cappedBuffer{limit: limit}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - len(c.buf); remaining > 0 {
		if len(p) <= remaining {
			c.buf = append(c.buf, p...)
		} else {
			c.buf = append(c.buf, p[:remaining]...)
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) bytes() []byte {
	if len(c.buf) == 0 {
		return nil
	}
	return c.buf
}

// teeReadCloser reads from reader (a TeeReader over the real body) while closing
// the original body, so the proxy still streams the full body upstream while a
// capped prefix is captured.
type teeReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (t *teeReadCloser) Read(p []byte) (int, error) { return t.reader.Read(p) }
func (t *teeReadCloser) Close() error               { return t.closer.Close() }
