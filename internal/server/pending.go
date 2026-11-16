package server

import (
	"crypto/rand"
	"sync"
	"time"

	"cafeteria-management/internal/store"
)

// pendingImport is a checked file waiting for the user's confirmation.
// Nothing is written to the database before that (SPEC §4.1).
type pendingImport struct {
	fileName string
	rowsRead int
	rows     []store.ImportRow
	base     int64 // ImportSummary.Base of the preview
	expires  time.Time
}

// pendingImports keeps checked files in memory between the preview and the
// confirmation, under a random token.
type pendingImports struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	items map[string]pendingImport
}

func newPendingImports(ttl time.Duration) *pendingImports {
	return &pendingImports{ttl: ttl, now: time.Now, items: map[string]pendingImport{}}
}

// put stores p and returns its token. Expired entries are dropped.
func (p *pendingImports) put(item pendingImport) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	for k, v := range p.items {
		if now.After(v.expires) {
			delete(p.items, k)
		}
	}
	token := rand.Text()
	item.expires = now.Add(p.ttl)
	p.items[token] = item
	return token
}

// get returns the entry for token, if it exists and has not expired.
func (p *pendingImports) get(token string) (pendingImport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	item, ok := p.items[token]
	if !ok || p.now().After(item.expires) {
		return pendingImport{}, false
	}
	return item, true
}

// remove deletes the entry for token, once it has been used.
func (p *pendingImports) remove(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.items, token)
}
