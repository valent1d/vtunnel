package secrets

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	KeychainService = "vtunnel"
	CloudflareToken = "cloudflare-api-token"
)

var ErrNotFound = errors.New("secret not found")

type Store struct {
	Service string
}

func DefaultStore() Store {
	return Store{Service: KeychainService}
}

func (store Store) Get(account string) (string, error) {
	service := store.service()
	output, err := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w").CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "could not be found") || strings.Contains(string(output), "The specified item could not be found") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("read %s from macOS Keychain: %s", account, strings.TrimSpace(string(output)))
	}
	return strings.TrimRight(string(output), "\r\n"), nil
}

func (store Store) Set(account string, secret string) error {
	service := store.service()
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errors.New("secret is empty")
	}

	if err := deleteAll(service, account); err != nil {
		return err
	}

	output, err := exec.Command("security", "add-generic-password", "-s", service, "-a", account, "-w", secret).CombinedOutput()
	if err != nil {
		return fmt.Errorf("store %s in macOS Keychain: %s", account, strings.TrimSpace(string(output)))
	}
	return nil
}

func (store Store) Delete(account string) error {
	err := deleteOne(store.service(), account)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func deleteAll(service string, account string) error {
	for {
		err := deleteOne(service, account)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func deleteOne(service string, account string) error {
	output, err := exec.Command("security", "delete-generic-password", "-s", service, "-a", account).CombinedOutput()
	if err != nil {
		if isNotFoundOutput(output) {
			return ErrNotFound
		}
		return fmt.Errorf("delete %s from macOS Keychain: %s", account, strings.TrimSpace(string(output)))
	}
	return nil
}

func isNotFoundOutput(output []byte) bool {
	text := string(output)
	return strings.Contains(text, "could not be found") || strings.Contains(text, "The specified item could not be found")
}

func (store Store) service() string {
	if strings.TrimSpace(store.Service) == "" {
		return KeychainService
	}
	return store.Service
}
