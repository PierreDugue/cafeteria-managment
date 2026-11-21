package keychain

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// mapBackend is an in-memory Backend that records the largest value set.
type mapBackend struct {
	mu        sync.Mutex
	m         map[string]string
	maxLen    int
	sets      int
	setErr    error
	getErr    error
	deleteErr error
}

func newMap() *mapBackend { return &mapBackend{m: map[string]string{}} }

func (b *mapBackend) Get(service, user string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.getErr != nil {
		return "", b.getErr
	}
	v, ok := b.m[service+"/"+user]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (b *mapBackend) Set(service, user, secret string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.setErr != nil {
		return b.setErr
	}
	b.sets++
	b.maxLen = max(b.maxLen, len(secret))
	b.m[service+"/"+user] = secret
	return nil
}

func (b *mapBackend) Delete(service, user string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deleteErr != nil {
		return b.deleteErr
	}
	delete(b.m, service+"/"+user)
	return nil
}

func TestSaveLoadRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		secret string
	}{
		{"one byte", "a"},
		{"exactly one chunk", strings.Repeat("x", 1500)},
		{"one byte over", strings.Repeat("y", 1501)},
		{"ten thousand", strings.Repeat("z", 10000)},
		{"accents", strings.Repeat("é à ç 日本 ", 400)},
		{"max size", strings.Repeat("m", 32*1500)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newMap()
			k := New("svc", b)
			if err := k.Save("s", tt.secret); err != nil {
				t.Fatal(err)
			}
			if b.maxLen > 1500 && !strings.HasPrefix(tt.name, "never") {
				// header is short; chunks must never exceed 1500
				t.Errorf("a value of %d bytes was handed to the backend", b.maxLen)
			}
			got, err := k.Load("s")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.secret {
				t.Error("round trip changed the secret")
			}
		})
	}
}

func TestSaveRefusesEmptyAndTooLarge(t *testing.T) {
	for name, secret := range map[string]string{
		"empty":     "",
		"too large": strings.Repeat("a", 32*1500+1),
	} {
		t.Run(name, func(t *testing.T) {
			b := newMap()
			if err := New("svc", b).Save("s", secret); err == nil {
				t.Fatal("expected an error")
			}
			if len(b.m) != 0 {
				t.Errorf("nothing must be written, got %d entries", len(b.m))
			}
		})
	}
}

func TestSaveOverwriteDeletesLeftoverChunks(t *testing.T) {
	b := newMap()
	k := New("svc", b)
	if err := k.Save("s", strings.Repeat("a", 5*1500)); err != nil {
		t.Fatal(err)
	}
	if err := k.Save("s", "short"); err != nil {
		t.Fatal(err)
	}
	got, err := k.Load("s")
	if err != nil || got != "short" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	for i := 2; i <= 5; i++ {
		if _, err := b.Get("svc", chunkName("s", i)); !errors.Is(err, ErrNotFound) {
			t.Errorf("chunk %d should be deleted", i)
		}
	}
}

func TestLoadCorruptedIsNotFound(t *testing.T) {
	secret := strings.Repeat("q", 4000) // 3 chunks
	tests := []struct {
		name   string
		mutate func(b *mapBackend)
	}{
		{"missing chunk", func(b *mapBackend) { delete(b.m, "svc/s.2") }},
		{"changed chunk", func(b *mapBackend) { b.m["svc/s.2"] = "tampered" }},
		{"bad header", func(b *mapBackend) { b.m["svc/s"] = "garbage" }},
		{"unknown version", func(b *mapBackend) { b.m["svc/s"] = strings.Replace(b.m["svc/s"], "v1", "v9", 1) }},
		{"zero count", func(b *mapBackend) { b.m["svc/s"] = "v1:0:" + strings.Repeat("0", 64) }},
		{"huge count", func(b *mapBackend) { b.m["svc/s"] = "v1:99:" + strings.Repeat("0", 64) }},
		{"non numeric count", func(b *mapBackend) { b.m["svc/s"] = "v1:x:" + strings.Repeat("0", 64) }},
		{"bad hash hex", func(b *mapBackend) { b.m["svc/s"] = "v1:3:zz" }},
		{"short hash", func(b *mapBackend) { b.m["svc/s"] = "v1:3:abcd" }},
		{"header missing", func(b *mapBackend) { delete(b.m, "svc/s") }},
		{"extra garbage chunk", func(b *mapBackend) { b.m["svc/s.3"] = b.m["svc/s.3"] + "junk" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newMap()
			k := New("svc", b)
			if err := k.Save("s", secret); err != nil {
				t.Fatal(err)
			}
			tt.mutate(b)
			if _, err := k.Load("s"); !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestLoadIgnoresExtraChunkBeyondCount(t *testing.T) {
	b := newMap()
	k := New("svc", b)
	if err := k.Save("s", "hello"); err != nil {
		t.Fatal(err)
	}
	b.m["svc/s.2"] = "garbage"
	got, err := k.Load("s")
	if err != nil || got != "hello" {
		t.Fatalf("Load = %q, %v", got, err)
	}
}

func TestDelete(t *testing.T) {
	b := newMap()
	k := New("svc", b)
	if err := k.Save("s", strings.Repeat("a", 4000)); err != nil {
		t.Fatal(err)
	}
	if err := k.Save("other", "keep"); err != nil {
		t.Fatal(err)
	}
	if err := k.Delete("s"); err != nil {
		t.Fatal(err)
	}
	for key := range b.m {
		if strings.HasPrefix(key, "svc/s") && !strings.HasPrefix(key, "svc/other") {
			t.Errorf("entry %s left behind", key)
		}
	}
	if _, err := k.Load("s"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load after delete = %v", err)
	}
	if got, err := k.Load("other"); err != nil || got != "keep" {
		t.Errorf("other secret affected: %q, %v", got, err)
	}
}

func TestDeleteNothing(t *testing.T) {
	if err := New("svc", newMap()).Delete("nope"); err != nil {
		t.Fatal(err)
	}
}

func TestBackendErrorsWrappedWithoutSecret(t *testing.T) {
	const secret = "TOP-SECRET-VALUE"
	boom := errors.New("backend down")

	t.Run("set", func(t *testing.T) {
		b := newMap()
		b.setErr = boom
		err := New("svc", b).Save("s", secret)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Error("error leaks the secret")
		}
	})
	t.Run("get", func(t *testing.T) {
		b := newMap()
		k := New("svc", b)
		if err := k.Save("s", secret); err != nil {
			t.Fatal(err)
		}
		b.getErr = boom
		_, err := k.Load("s")
		if !errors.Is(err, boom) || errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Error("error leaks the secret")
		}
	})
	t.Run("delete", func(t *testing.T) {
		b := newMap()
		b.deleteErr = boom
		if err := New("svc", b).Delete("s"); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("leftover delete failure is ignored", func(t *testing.T) {
		b := newMap()
		k := New("svc", b)
		if err := k.Save("s", strings.Repeat("a", 3000)); err != nil {
			t.Fatal(err)
		}
		b.deleteErr = boom
		if err := k.Save("s", "x"); err != nil {
			t.Fatalf("Save = %v", err)
		}
		if got, _ := k.Load("s"); got != "x" {
			t.Errorf("Load = %q", got)
		}
	})
}
