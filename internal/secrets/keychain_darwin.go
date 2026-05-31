//go:build darwin

package secrets

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultStore returns the macOS Keychain backend.
func DefaultStore() Store {
	return keychainStore{service: KeychainService}
}

// keychainStore stores secrets in the macOS login Keychain via the `security`
// CLI.
type keychainStore struct {
	service string
}

func (store keychainStore) Get(account string) (string, error) {
	output, err := exec.Command("security", "find-generic-password", "-s", store.svc(), "-a", account, "-w").CombinedOutput()
	if err != nil {
		if isNotFoundOutput(output) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("read %s from macOS Keychain: %s", account, strings.TrimSpace(string(output)))
	}
	return strings.TrimRight(string(output), "\r\n"), nil
}

func (store keychainStore) Set(account, secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errors.New("secret is empty")
	}
	if err := store.deleteAll(account); err != nil {
		return err
	}
	output, err := exec.Command("security", "add-generic-password", "-s", store.svc(), "-a", account, "-w", secret).CombinedOutput()
	if err != nil {
		return fmt.Errorf("store %s in macOS Keychain: %s", account, strings.TrimSpace(string(output)))
	}
	return nil
}

// Delete removes every Keychain entry for the account. It is idempotent.
func (store keychainStore) Delete(account string) error {
	return store.deleteAll(account)
}

func (store keychainStore) deleteAll(account string) error {
	for {
		err := store.deleteOne(account)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (store keychainStore) deleteOne(account string) error {
	output, err := exec.Command("security", "delete-generic-password", "-s", store.svc(), "-a", account).CombinedOutput()
	if err != nil {
		if isNotFoundOutput(output) {
			return ErrNotFound
		}
		return fmt.Errorf("delete %s from macOS Keychain: %s", account, strings.TrimSpace(string(output)))
	}
	return nil
}

func (store keychainStore) svc() string {
	if strings.TrimSpace(store.service) == "" {
		return KeychainService
	}
	return store.service
}

func isNotFoundOutput(output []byte) bool {
	text := string(output)
	return strings.Contains(text, "could not be found") || strings.Contains(text, "The specified item could not be found")
}
