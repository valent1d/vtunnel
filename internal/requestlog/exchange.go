package requestlog

import (
	"net/http"
	"sync"
)

// DefaultMaxExchanges bounds how many full captured exchanges are kept in
// memory for inspection and replay. Exchanges hold headers and (capped) bodies,
// so unlike the lean persisted log they are intentionally short-lived.
const DefaultMaxExchanges = 256

// DefaultBodyCaptureLimit caps how many bytes of each request/response body are
// captured. Bodies larger than this are truncated and flagged.
const DefaultBodyCaptureLimit = 64 << 10 // 64 KiB

// Exchange is the full capture of one proxied request/response, keyed by the
// matching log Entry ID. Bodies are capped (see DefaultBodyCaptureLimit).
type Exchange struct {
	ID                uint64      `json:"id"`
	Hostname          string      `json:"hostname"`
	Method            string      `json:"method"`
	Path              string      `json:"path"`
	Target            string      `json:"target"`
	Status            int         `json:"status"`
	RequestHeaders    http.Header `json:"request_headers,omitempty"`
	RequestBody       []byte      `json:"request_body,omitempty"`
	RequestTruncated  bool        `json:"request_truncated,omitempty"`
	ResponseHeaders   http.Header `json:"response_headers,omitempty"`
	ResponseBody      []byte      `json:"response_body,omitempty"`
	ResponseTruncated bool        `json:"response_truncated,omitempty"`
}

// ExchangeStore is a bounded, in-memory ring of exchanges keyed by ID. The
// daemon is the only writer; readers are the detail and replay endpoints.
type ExchangeStore struct {
	mu    sync.RWMutex
	max   int
	order []uint64
	byID  map[uint64]Exchange
}

// NewExchangeStore returns a store keeping at most max exchanges.
func NewExchangeStore(max int) *ExchangeStore {
	if max <= 0 {
		max = DefaultMaxExchanges
	}
	return &ExchangeStore{max: max, byID: make(map[uint64]Exchange, max)}
}

// Put stores an exchange, evicting the oldest once the store is full.
func (s *ExchangeStore) Put(exchange Exchange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[exchange.ID]; !exists {
		s.order = append(s.order, exchange.ID)
		for len(s.order) > s.max {
			evicted := s.order[0]
			s.order = s.order[1:]
			delete(s.byID, evicted)
		}
	}
	s.byID[exchange.ID] = exchange
}

// Get returns the exchange for id, if it is still retained.
func (s *ExchangeStore) Get(id uint64) (Exchange, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exchange, ok := s.byID[id]
	return exchange, ok
}

// Len reports how many exchanges are currently retained.
func (s *ExchangeStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}
