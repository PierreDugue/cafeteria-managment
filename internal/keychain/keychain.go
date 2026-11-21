// Package keychain keeps secrets in the OS keychain (macOS Keychain,
// Windows Credential Manager), whatever their size (SPEC §8). It knows
// nothing about what the secrets are.
package keychain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	// chunkSize keeps every entry far below the smallest OS limit (Windows
	// refuses a secret over 2,560 bytes).
	chunkSize = 1500
	// maxChunks bounds a secret to 48 KB.
	maxChunks = 32
	// headerPrefix versions the header format.
	headerPrefix = "v1"
)

// ErrNotFound means no complete, intact secret is stored under the name.
var ErrNotFound = errors.New("secret not found")

// Backend is the OS keychain (satisfied by OS, which wraps go-keyring).
type Backend interface {
	Get(service, user string) (string, error) // ErrNotFound if absent
	Set(service, user, secret string) error
	Delete(service, user string) error // nil if absent
}

// OS is the real keychain: macOS Keychain, Windows Credential Manager.
type OS struct{}

// Get implements Backend.
func (OS) Get(service, user string) (string, error) {
	v, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

// Set implements Backend.
func (OS) Set(service, user, secret string) error { return keyring.Set(service, user, secret) }

// Delete implements Backend.
func (OS) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Keychain stores secrets of any size (up to 48 KB) under one service,
// split into chunks that every OS accepts. A header entry named like the
// secret holds the chunk count and a hash of the whole secret, so a
// half-written secret is never read as a valid one.
type Keychain struct {
	service string
	backend Backend
}

// New returns a Keychain storing its entries under service in b.
func New(service string, b Backend) *Keychain {
	return &Keychain{service: service, backend: b}
}

func chunkName(name string, i int) string { return name + "." + strconv.Itoa(i) }

func header(count int, sum [sha256.Size]byte) string {
	return fmt.Sprintf("%s:%d:%s", headerPrefix, count, hex.EncodeToString(sum[:]))
}

// Load returns the secret stored under name. ErrNotFound means it is
// missing, incomplete or corrupted; other errors come from the backend.
func (k *Keychain) Load(name string) (string, error) {
	h, err := k.backend.Get(k.service, name)
	if errors.Is(err, ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keychain read %s: %w", name, err)
	}
	parts := strings.Split(h, ":")
	if len(parts) != 3 || parts[0] != headerPrefix {
		return "", ErrNotFound
	}
	count, err := strconv.Atoi(parts[1])
	if err != nil || count < 1 || count > maxChunks {
		return "", ErrNotFound
	}
	want, err := hex.DecodeString(parts[2])
	if err != nil || len(want) != sha256.Size {
		return "", ErrNotFound
	}

	var sb strings.Builder
	for i := 1; i <= count; i++ {
		c, err := k.backend.Get(k.service, chunkName(name, i))
		if errors.Is(err, ErrNotFound) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", fmt.Errorf("keychain read %s: %w", chunkName(name, i), err)
		}
		sb.WriteString(c)
	}
	secret := sb.String()
	got := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return "", ErrNotFound
	}
	return secret, nil
}

// Save stores secret under name, replacing any previous value. Chunks are
// written first and the header last, then the chunks of a longer old
// value are deleted. An empty secret or one over 48 KB is refused.
func (k *Keychain) Save(name, secret string) error {
	if secret == "" {
		return fmt.Errorf("keychain save %s: empty secret", name)
	}
	count := (len(secret) + chunkSize - 1) / chunkSize
	if count > maxChunks {
		return fmt.Errorf("keychain save %s: secret too large", name)
	}
	oldCount := k.storedCount(name)

	for i := 1; i <= count; i++ {
		part := secret[(i-1)*chunkSize : min(i*chunkSize, len(secret))]
		if err := k.backend.Set(k.service, chunkName(name, i), part); err != nil {
			return fmt.Errorf("keychain write %s: %w", chunkName(name, i), err)
		}
	}
	if err := k.backend.Set(k.service, name, header(count, sha256.Sum256([]byte(secret)))); err != nil {
		return fmt.Errorf("keychain write %s: %w", name, err)
	}
	// Leftovers are harmless (the header ignores them): best effort.
	for i := count + 1; i <= oldCount; i++ {
		_ = k.backend.Delete(k.service, chunkName(name, i))
	}
	return nil
}

// Delete removes the header and every chunk of name. Deleting nothing is
// not an error.
func (k *Keychain) Delete(name string) error {
	// The header goes first: from then on the secret reads as missing.
	if err := k.backend.Delete(k.service, name); err != nil {
		return fmt.Errorf("keychain delete %s: %w", name, err)
	}
	for i := 1; i <= maxChunks; i++ {
		c := chunkName(name, i)
		if _, err := k.backend.Get(k.service, c); errors.Is(err, ErrNotFound) {
			break
		}
		if err := k.backend.Delete(k.service, c); err != nil {
			return fmt.Errorf("keychain delete %s: %w", c, err)
		}
	}
	return nil
}

// storedCount returns the chunk count in the current header, 0 if none or
// unreadable (then no leftover is cleaned: they stay unreachable).
func (k *Keychain) storedCount(name string) int {
	h, err := k.backend.Get(k.service, name)
	if err != nil {
		return 0
	}
	parts := strings.Split(h, ":")
	if len(parts) != 3 {
		return 0
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 || n > maxChunks {
		return 0
	}
	return n
}
