//go:build !darwin

package secrets

import (
	"path/filepath"

	"vtunnel/internal/config"
)

// DefaultStore returns the file-backed store used on platforms without a
// keychain integration (Linux, etc.): a 0600 JSON file in the config directory.
func DefaultStore() Store {
	dir, err := config.ConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return NewFileStore(filepath.Join(dir, "credentials.json"))
}
