package requestlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"vtunnel/internal/routes"
)

const DefaultMaxEntries = 1000

type Entry struct {
	ID         uint64        `json:"id"`
	Time       time.Time     `json:"time"`
	Hostname   string        `json:"hostname"`
	Method     string        `json:"method"`
	Path       string        `json:"path"`
	Status     int           `json:"status"`
	Bytes      int64         `json:"bytes"`
	Duration   time.Duration `json:"duration"`
	Target     string        `json:"target"`
	RemoteAddr string        `json:"remote_addr"`
}

type Filter struct {
	Hostname string
	AfterID  uint64
	Limit    int
}

type Store struct {
	path string
	max  int

	mu      sync.RWMutex
	nextID  uint64
	entries []Entry
}

func NewStore(path string, maxEntries int) (*Store, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	store := &Store{
		path:   path,
		max:    maxEntries,
		nextID: 1,
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Add(entry Entry) (Entry, error) {
	entry.Hostname = routes.NormalizeHostname(entry.Hostname)
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry.ID = s.nextID
	s.nextID++
	s.entries = append(s.entries, entry)
	if len(s.entries) > s.max {
		s.entries = append([]Entry(nil), s.entries[len(s.entries)-s.max:]...)
	}
	if err := s.appendLocked(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s *Store) List(filter Filter) []Entry {
	hostname := routes.NormalizeHostname(filter.Hostname)
	limit := filter.Limit
	if limit <= 0 || limit > s.max {
		limit = s.max
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Entry, 0, min(limit, len(s.entries)))
	for _, entry := range s.entries {
		if hostname != "" && entry.Hostname != hostname {
			continue
		}
		if filter.AfterID > 0 && entry.ID <= filter.AfterID {
			continue
		}
		out = append(out, entry)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

func (s *Store) load() error {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read request logs %s: %w", s.path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("parse request logs %s: %w", s.path, err)
		}
		s.entries = append(s.entries, entry)
		if entry.ID >= s.nextID {
			s.nextID = entry.ID + 1
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan request logs %s: %w", s.path, err)
	}
	sort.Slice(s.entries, func(i, j int) bool {
		return s.entries[i].ID < s.entries[j].ID
	})
	if len(s.entries) > s.max {
		s.entries = append([]Entry(nil), s.entries[len(s.entries)-s.max:]...)
	}
	return nil
}

func (s *Store) appendLocked(entry Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create request log directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open request log %s: %w", s.path, err)
	}
	defer file.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode request log: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write request log %s: %w", s.path, err)
	}
	return nil
}
