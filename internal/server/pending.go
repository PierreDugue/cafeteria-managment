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
	// The target (SPEC §4.0): exactly one of listID (replace that list) and
	// newListName (create it) is set. It is fixed at preview time, so the
	// confirmation cannot change it.
	listID      int64
	newListName string
	base        int64 // ImportSummary.Base of the preview (existing list only)
	// preview is the page the user saw, shown again if the replacement is
	// not confirmed. Its Token is empty here.
	preview importPreviewData
	expires time.Time
}

// maxPendingImports bounds the memory used by checked files waiting for
// confirmation: the oldest one is dropped to make room.
const maxPendingImports = 8

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

// put stores p and returns its token. Expired entries are dropped, then the oldest
// ones beyond maxPendingImports.
func (p *pendingImports) put(item pendingImport) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	for k, v := range p.items {
		if now.After(v.expires) {
			delete(p.items, k)
		}
	}
	for len(p.items) >= maxPendingImports {
		var oldest string
		var oldestExp time.Time
		for k, v := range p.items {
			if oldest == "" || v.expires.Before(oldestExp) {
				oldest, oldestExp = k, v.expires
			}
		}
		delete(p.items, oldest)
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
