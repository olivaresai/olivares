// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultCacheTTL = 1 * time.Hour

// Cache stores generated reports on the filesystem with a TTL.
type Cache struct {
	log *slog.Logger
	dir string
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	path    string
	created time.Time
}

// NewCache creates a filesystem-backed report cache.
func NewCache(log *slog.Logger) *Cache {
	dir := os.Getenv("OLIVARES_REPORT_CACHE_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "olivares-report-cache")
	}
	_ = os.MkdirAll(dir, 0o700)
	return &Cache{
		log:     log,
		dir:     dir,
		ttl:     defaultCacheTTL,
		now:     time.Now,
		entries: make(map[string]cacheEntry),
	}
}

// cacheKey builds a deterministic key from report parameters.
func cacheKey(tenant, reportType, format, from, to, extra string) string {
	h := sha256.New()
	for _, s := range []string{tenant, reportType, format, from, to, extra} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}

// Get returns cached content if it exists and is not expired.
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	if c.now().Sub(entry.created) > c.ttl {
		c.evict(key)
		return nil, false
	}
	data, err := os.ReadFile(entry.path)
	if err != nil {
		c.evict(key)
		return nil, false
	}
	return data, true
}

// Put stores content in the cache.
func (c *Cache) Put(key string, data []byte) {
	path := filepath.Join(c.dir, key)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		c.log.Warn("report cache: write failed", "key", key, "err", err)
		return
	}
	c.mu.Lock()
	c.entries[key] = cacheEntry{path: path, created: c.now()}
	c.mu.Unlock()
}

func (c *Cache) evict(key string) {
	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok {
		delete(c.entries, key)
	}
	c.mu.Unlock()
	if ok {
		os.Remove(entry.path)
	}
}

// Close removes the cache directory.
func (c *Cache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		os.Remove(entry.path)
	}
	c.entries = make(map[string]cacheEntry)
}
