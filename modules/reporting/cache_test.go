// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheKeyIsDeterministicHashedAndParameterSensitive(t *testing.T) {
	a := cacheKey("tenant-a", "finops", "html", "from", "to", "en")
	b := cacheKey("tenant-a", "finops", "html", "from", "to", "en")
	c := cacheKey("tenant-a", "finops", "pdf", "from", "to", "en")
	if a != b {
		t.Fatalf("same inputs produced different keys %q/%q", a, b)
	}
	if a == c {
		t.Fatal("format change did not change cache key")
	}
	if len(a) != 32 || strings.Contains(a, "tenant") {
		t.Fatalf("cache key = %q, want 32-char hash without raw tenant", a)
	}
}

func TestCacheHonorsTTLReadFailureEvictionAndClose(t *testing.T) {
	t.Setenv("OLIVARES_REPORT_CACHE_DIR", t.TempDir())
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	c := NewCache(nil)
	c.ttl = time.Minute
	c.now = func() time.Time { return now }

	c.Put("k1", []byte("hello"))
	if got, ok := c.Get("k1"); !ok || string(got) != "hello" {
		t.Fatalf("cache get = %q/%v, want hello/true", got, ok)
	}
	now = now.Add(2 * time.Minute)
	if got, ok := c.Get("k1"); ok || got != nil {
		t.Fatalf("expired get = %q/%v, want nil/false", got, ok)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "k1")); !os.IsNotExist(err) {
		t.Fatalf("expired file still present or stat error = %v", err)
	}

	now = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	c.Put("k2", []byte("bye"))
	if err := os.Remove(filepath.Join(c.dir, "k2")); err != nil {
		t.Fatalf("remove cached file: %v", err)
	}
	if got, ok := c.Get("k2"); ok || got != nil {
		t.Fatalf("missing file get = %q/%v, want nil/false", got, ok)
	}

	c.Put("k3", []byte("cleanup"))
	c.Close()
	if len(c.entries) != 0 {
		t.Fatalf("entries after Close = %d, want 0", len(c.entries))
	}
	if _, err := os.Stat(filepath.Join(c.dir, "k3")); !os.IsNotExist(err) {
		t.Fatalf("closed cache file still present or stat error = %v", err)
	}
}
