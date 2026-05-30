package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Route struct {
	Hostname  string    `json:"hostname"`
	Target    string    `json:"target"`
	CreatedAt time.Time `json:"created_at"`
	// Orbstack is set when the route exposes an OrbStack container. It is
	// optional metadata for display (badge, detail pane) and does not affect
	// routing; nil for ordinary routes. omitempty keeps existing routes.json
	// files unchanged.
	Orbstack *OrbstackInfo `json:"orbstack,omitempty"`
	// Access is set when the route is protected by a Cloudflare Access app.
	// nil means the route is public. It records the IDs needed for clean
	// teardown plus display metadata.
	Access *AccessInfo `json:"access,omitempty"`
}

// AccessInfo records the Cloudflare Access protection on a route.
type AccessInfo struct {
	AppID     string   `json:"app_id,omitempty"`
	PolicyIDs []string `json:"policy_ids,omitempty"`
	Mode      string   `json:"mode"`            // otp | email | sso
	IdP       string   `json:"idp,omitempty"`   // identity provider name (sso)
	Allow     []string `json:"allow,omitempty"` // emails / @domains / everyone
	// Paused is true when protection is temporarily bypassed (route public)
	// while keeping the app and allow-list so it can be resumed.
	Paused bool `json:"paused,omitempty"`
}

// OrbstackInfo describes the OrbStack container behind a route.
type OrbstackInfo struct {
	Container     string   `json:"container"`
	Image         string   `json:"image,omitempty"`
	OrbDomain     string   `json:"orb_domain,omitempty"`
	CustomDomains []string `json:"custom_domains,omitempty"`
	// Managed marks routes created by `vtunnel orbstack watch`, so the watcher
	// only ever removes its own auto-created routes — never manual ones.
	Managed bool `json:"managed,omitempty"`
}

type Store struct {
	path   string
	mu     sync.RWMutex
	routes map[string]Route
}

func NewStore(path string) (*Store, error) {
	store := &Store{
		path:   path,
		routes: map[string]Route{},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Get(hostname string) (Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	route, ok := s.routes[NormalizeHostname(hostname)]
	return route, ok
}

func (s *Store) List() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Route, 0, len(s.routes))
	for _, route := range s.routes {
		out = append(out, route)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Hostname < out[j].Hostname
	})
	return out
}

func (s *Store) Add(route Route) error {
	if err := Validate(route); err != nil {
		return err
	}
	route.Hostname = NormalizeHostname(route.Hostname)
	if route.CreatedAt.IsZero() {
		route.CreatedAt = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route.Hostname] = route
	return s.saveLocked()
}

func (s *Store) Delete(hostname string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	hostname = NormalizeHostname(hostname)
	if _, ok := s.routes[hostname]; !ok {
		return false, nil
	}
	delete(s.routes, hostname)
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read routes %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil
	}

	var stored []Route
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("parse routes %s: %w", s.path, err)
	}
	for _, route := range stored {
		if err := Validate(route); err != nil {
			return err
		}
		s.routes[NormalizeHostname(route.Hostname)] = route
	}
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create routes directory: %w", err)
	}

	stored := make([]Route, 0, len(s.routes))
	for _, route := range s.routes {
		stored = append(stored, route)
	}
	sort.Slice(stored, func(i, j int) bool {
		return stored[i].Hostname < stored[j].Hostname
	})

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode routes: %w", err)
	}
	if err := os.WriteFile(s.path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write routes %s: %w", s.path, err)
	}
	return nil
}

func Validate(route Route) error {
	if NormalizeHostname(route.Hostname) == "" {
		return errors.New("hostname is required")
	}
	target, err := url.Parse(route.Target)
	if err != nil {
		return fmt.Errorf("parse target: %w", err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("target must use http or https, got %q", target.Scheme)
	}
	if target.Host == "" {
		return errors.New("target host is required")
	}
	return nil
}

func NormalizeHostname(hostname string) string {
	hostname = strings.TrimSpace(hostname)
	hostname = strings.TrimSuffix(hostname, ".")
	return strings.ToLower(hostname)
}
