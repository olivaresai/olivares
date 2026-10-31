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
//   - only an installed result is kept as a verdict: a damaged or unverified release
//     is checked again at every read, and a refusal stays a refusal (the last bullet
//     says what else is kept);
//   - an entry holds only for the verification posture that established it (for
//     Claude Code, the signature verifier and the pinned key): the host observer's
//     unavailable verifier never reads the installing engine's verdict (SR-FH-CACHE-01);
//   - a symbolic link in the release counts with what it points at: a link to a
//     file adds that file's identity, and a link to a directory makes the release
//     uncacheable, because a check reads through it and the walk would not;
//   - what is kept for a release that is not installed is only the digest of its
//     executable, under the same identity but for every posture (it is a fact about
//     the bytes, not a verdict): the verdict (damaged, unverified) is decided again
//     at every read, but an executable that has not changed is not read again. The
//     host observer's Claude Code release is always unverified to it, so without
//     this every observation hashed the whole executable.
//
// An engine built without a cache (EngineOptions.Verified nil) checks every read,
// as before. Off Linux the identity is not read and nothing is cached.
type VerifiedCache struct {
	mu sync.Mutex
	m  map[string]*verifiedRelease // release directory → what was verified
}

type verifiedRelease struct {
	driver string
	admit  chan struct{}
	key    string
	in     Installed
	// digestKey is the release identity under which exe was read.
	digestKey string
	exe       exeDigest
}

// exeDigest is the sha256 and size of a release's executable as one read found them.
// set is false until a read has filled it.
type exeDigest struct {
	sum  string
	size int64
	set  bool
}

// read returns the digest d holds, or reads the executable and keeps its digest in d.
// A nil d reads every time.
func (d *exeDigest) read(root *os.Root, rel string) (string, int64, error) {
	if d != nil && d.set {
		return d.sum, d.size, nil
	}
	sum, size, err := fileSHA256Root(root, rel)
	if err == nil && d != nil {
		d.sum, d.size, d.set = sum, size, true
	}
	return sum, size, err
}

// NewVerifiedCache returns an empty cache; the composition makes one per process.
func NewVerifiedCache() *VerifiedCache {
	return &VerifiedCache{m: map[string]*verifiedRelease{}}
}

// Forget drops every entry of driver.
func (c *VerifiedCache) Forget(driver string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for dir, v := range c.m {
		if v.driver == driver {
			delete(c.m, dir)
		}
	}
}

func (c *VerifiedCache) entry(dir, driver string) *verifiedRelease {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.m[dir]
	if v == nil {
		v = &verifiedRelease{driver: driver, admit: make(chan struct{}, 1)}
		c.m[dir] = v
	}
	return v
}

// ForgetVerified drops the cached verification of driver's releases (a sign-in
// changed what the tool uses). A no-op for an engine without a cache.
func (e *Engine) ForgetVerified(driver string) { e.verified.Forget(driver) }

// inspectReleaseCached is inspectRelease behind the cache. The release's identity is
// read before the full check and again after it: a result is kept only when nothing
// changed while it was being checked.
func (e *Engine) inspectReleaseCached(ctx context.Context, root *os.Root, rel, abs, driver string) Installed {
	if e.verified == nil {
		return e.inspectRelease(ctx, root, rel, abs, driver, nil)
	}
	// Share the first full check as well as later hits. Forget removes this
	// entry, so an in-flight check cannot restore an invalidated verdict.
	v := e.verified.entry(abs, driver)
	select {
	case v.admit <- struct{}{}:
		defer func() { <-v.admit }()
	case <-ctx.Done():
		return Installed{Driver: driver, ReleaseDir: abs, State: StateUnverified, Reason: ctx.Err().Error()}
	}
	// The verdict holds for the posture that made it; the executable's digest is a fact
	// about its bytes and holds for whoever asks.
	identity, ok := releaseIdentity(root, rel)
	key := e.verificationPosture(driver) + "\x00" + identity
	if ok {
		if v.key == key {
			return v.in
		}
	}
	// Only the v1 check (Claude Code) fills or reads the digest; v2 receipts check their
	// payload members themselves.
	var digest *exeDigest
	if ok {
		digest = &exeDigest{}
		if v.digestKey == identity {
			*digest = v.exe
		}
	}
	in := e.inspectRelease(ctx, root, rel, abs, driver, digest)
	if ok && (in.State == StateInstalled || digest.set) {
		if after, still := releaseIdentity(root, rel); still && after == identity {
			if in.State == StateInstalled {
				v.key, v.in = key, in
			}
			if digest.set {
				v.digestKey, v.exe = identity, *digest
			}
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

// releaseIdentity is the digest of the release's receipt and every entry's identity
// under the release directory; a symbolic link also carries the identity of the file
// it points at. ok is false when any of it cannot be read or a link points at a
// directory (the release is then checked in full and not cached).
func releaseIdentity(root *os.Root, rel string) (string, bool) {
	receipt, err := readSmallFile(root, filepath.Join(rel, ReceiptFile), maxReceiptBytes)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(receipt)
	lines := []string{"receipt " + hex.EncodeToString(sum[:])}
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
