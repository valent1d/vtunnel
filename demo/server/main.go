// Command demo-server is a tiny HTTP server used only by demo/demo.tape: it
// answers every request with a friendly JSON payload so the vtunnel dashboard
// (and its request inspector) has something fun to show. It is never shipped in
// the vtunnel binary.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("DEMO_ADDR")
	if addr == "" {
		addr = "127.0.0.1:3000"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Vtunnel-Demo", "pong")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]string{
			"message":   "PONG — vtunnel is working like a charm ✨",
			"method":    r.Method,
			"path":      r.URL.Path,
			"served_by": "vtunnel demo server",
		})
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("vtunnel demo server listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
