// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// VerifiedCache remembers, in process memory only, the releases a full check found
// installed, so a read that asks which program a tool runs (the AI tools status, the
// resolve rule, a launch) does not re-hash the installed release every time
// (EU-CB07 on 09b: about 1.0 s for Claude Code and 0.5 s for Codex per read). Root,
// 2026-10-02:
//   - the key is the receipt's digest and the identity (device, inode, size, mtime,
//     ctime, mode) of every entry under the release directory — the executable, the
//     retained manifest, signature and key, every payload member — so a replaced file,
//     an in-place write or an added or removed entry changes it and the release is
//     fully checked again (hash and, for Claude, the pinned-key manifest) before use;
//   - nothing is written to disk; an engine restart starts empty;
//   - an install, an update and a sign-in clear that tool's entries;
//   - only an installed result is kept: a damaged or unverified release is checked
//     again at every read, and a refusal stays a refusal;
//   - an entry holds only for the verification posture that established it (for
//     Claude Code, the signature verifier and the pinned key): the host observer's
//     unavailable verifier never reads the installing engine's verdict (SR-FH-CACHE-01);
//   - a symbolic link in the release counts with what it points at: a link to a
//     file adds that file's identity, and a link to a directory makes the release
//     uncacheable, because a check reads through it and the walk would not.
//
// An engine built without a cache (EngineOptions.Verified nil) checks every read,
// as before. Off Linux the identity is not read and nothing is cached.
type VerifiedCache struct {
	mu sync.Mutex
	m  map[string]verifiedRelease // release directory → what was verified
}

type verifiedRelease struct {
	key string
	in  Installed
}

// NewVerifiedCache returns an empty cache; the composition makes one per process.
func NewVerifiedCache() *VerifiedCache {
	return &VerifiedCache{m: map[string]verifiedRelease{}}
}

// Forget drops every entry of driver.
func (c *VerifiedCache) Forget(driver string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for dir, v := range c.m {
		if v.in.Driver == driver {
			delete(c.m, dir)
		}
	}
}

func (c *VerifiedCache) lookup(dir, key string) (Installed, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[dir]
	if !ok || v.key != key {
		return Installed{}, false
	}
	return v.in, true
}

func (c *VerifiedCache) store(dir, key string, in Installed) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[dir] = verifiedRelease{key: key, in: in}
}

// ForgetVerified drops the cached verification of driver's releases (a sign-in
// changed what the tool uses). A no-op for an engine without a cache.
func (e *Engine) ForgetVerified(driver string) { e.verified.Forget(driver) }

// inspectReleaseCached is inspectRelease behind the cache. The release's identity is
// read before the full check and again after it: a result is kept only when nothing
// changed while it was being checked.
func (e *Engine) inspectReleaseCached(ctx context.Context, root *os.Root, rel, abs, driver string) Installed {
	if e.verified == nil {
		return e.inspectRelease(ctx, root, rel, abs, driver)
	}
	posture := e.verificationPosture(driver)
	key, ok := releaseIdentity(root, rel, posture)
	if ok {
		if in, hit := e.verified.lookup(abs, key); hit {
			return in
		}
	}
	in := e.inspectRelease(ctx, root, rel, abs, driver)
	if ok && in.State == StateInstalled {
		if after, still := releaseIdentity(root, rel, posture); still && after == key {
			e.verified.store(abs, key, in)
		}
	}
	return in
}

// verificationPosture names what this engine's full check of driver trusts: for
// a signed release (Claude Code), its signature verifier and pinned key; for the
// release archives, nothing beyond the receipt's official checksum (empty).
func (e *Engine) verificationPosture(driver string) string {
	p, err := e.v1Provider(driver)
	if err != nil {
		return ""
	}
	if v, ok := p.(interface{ verificationPosture() string }); ok {
		return v.verificationPosture()
	}
	return "v1 provider without a named posture"
}

// releaseIdentity is the digest of the verification posture, the release's
// receipt and every entry's identity under the release directory; a symbolic link
// also carries the identity of the file it points at. ok is false when any of it
// cannot be read or a link points at a directory (the release is then checked in
// full and not cached).
func releaseIdentity(root *os.Root, rel, posture string) (string, bool) {
	receipt, err := readSmallFile(root, filepath.Join(rel, ReceiptFile), maxReceiptBytes)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(receipt)
	lines := []string{"receipt " + hex.EncodeToString(sum[:]), "posture " + posture}
	walkErr := fs.WalkDir(root.FS(), rel, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := root.Lstat(path)
		if err != nil {
			return err
		}
		id, ok := fileIdentity(fi)
		if !ok {
			return fmt.Errorf("no identity for %s", path)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			target, err := root.Stat(path)
			if err != nil || !target.Mode().IsRegular() {
				return fmt.Errorf("%s does not point at a regular file", path)
			}
			tid, ok := fileIdentity(target)
			if !ok {
				return fmt.Errorf("no identity for the target of %s", path)
			}
			id += " -> " + tid
		}
		lines = append(lines, path+" "+id)
		return nil
	})
	if walkErr != nil {
		return "", false
	}
	sort.Strings(lines)
	digest := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(digest[:]), true
}

// fileHashes counts the files hashed by fileSHA256Root and hashReader (tests).
var fileHashes atomic.Int64
