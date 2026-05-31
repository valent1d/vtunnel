// Package secrets stores small secrets (the Cloudflare API token) per platform:
// the macOS Keychain on darwin, and a 0600 file elsewhere. Callers use
// DefaultStore, which returns the right backend for the OS.
package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	KeychainService = "vtunnel"
	CloudflareToken = "cloudflare-api-token"
)

// ErrNotFound is returned by Get/Delete when the account has no stored secret.
var ErrNotFound = errors.New("secret not found")

// Store reads and writes secrets keyed by account name.
type Store interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// fileStore keeps secrets as a JSON object in a single 0600 file. It is the
// backend on platforms without a system keychain integration (Linux, etc.).
type fileStore struct {
	path string
}

// NewFileStore returns a file-backed store at path.
func NewFileStore(path string) Store { return fileStore{path: path} }

func (s fileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]string{}, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return m, nil
}

func (s fileStore) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file then rename so the final file is always 0600 and the
	// replacement is atomic.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s fileStore) Get(account string) (string, error) {
	m, err := s.load()
	if err != nil {
		return "", err
	}
	secret, ok := m[account]
	if !ok || secret == "" {
		return "", ErrNotFound
	}
	return secret, nil
}

func (s fileStore) Set(account, secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errors.New("secret is empty")
	}
	m, err := s.load()
	if err != nil {
		return err
	}
	m[account] = secret
	return s.save(m)
}

// Delete removes the account's secret. It is idempotent.
func (s fileStore) Delete(account string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[account]; !ok {
		return nil
	}
	delete(m, account)
	return s.save(m)
}
