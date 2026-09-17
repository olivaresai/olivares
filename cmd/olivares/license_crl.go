// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/secure"
)

// license_crl.go persists what this DEPLOYMENT has observed of the license CRL
// (D4=D §5.2). The CRL rides the OTA-signed channel manifest; every
// upgrade-flow verification (CLI, the unattended timer's `upgrade --if-eligible`,
// an offline bundle import) calls recordCRLObservations after VerifyManifest
// succeeds, so the observation clock is fed by exactly the pull channels the
// design documents — there is no phone-home.
//
// Semantics of the store:
//   - The revocation CONTENT always mirrors the LATEST verified manifest (the
//     custodian can publish a corrected CRL; un-revoking is sanctioned because
//     only the OTA ceremony can sign it).
//   - first_observed keeps, PER revoked identity, when THIS deployment first saw
//     it listed. Still-listed identities keep their original timestamp across
//     updates (a consumer of the observation clock must not see it reset on every
//     manifest); identities that disappear from the CRL are dropped (un-revoked
//     clears the clock).
//   - The open binary only RECORDS and DISPLAYS this. Since B10 removed the user
//     cap there is no seat lift to fall back from, so revocation currently has NO
//     behavioral consumer at all — it is display/attestation only. Nothing gates.
type crlObservations struct {
	// Revocation mirrors the revoked set of the NEWEST verified manifest observed
	// (by SetByReleasedAt) — an older, replayed manifest cannot roll it back.
	Serials         []string `json:"serials,omitempty"`
	HolderIDs       []string `json:"holder_ids,omitempty"`
	LicenseKeyEpoch int64    `json:"license_key_epoch,omitempty"`
	// FirstObserved is keyed "serial:<s>" | "holder:<h>" | "epoch:<n>" (RFC3339).
	FirstObserved map[string]string `json:"first_observed,omitempty"`
	// SetByChannel / SetByReleasedAt identify the manifest that last SET the
	// revocation content. They are the anti-freeze anchor for the CRL itself: a
	// manifest older than SetByReleasedAt is refused as a rollback (the replay an
	// attacker uses to drop an observed revocation). A newer/equal one is accepted,
	// which is also how a legitimate corrected CRL (un-revoke) lands.
	SetByChannel    string `json:"set_by_channel,omitempty"`
	SetByReleasedAt string `json:"set_by_released_at,omitempty"`
	// UpdatedAt is when the store was last written.
	UpdatedAt string `json:"updated_at"`
}

const (
	crlFileName = "license-crl.json"
	// crlLockFileName is the permanent transaction lock beside the store (CR1 §3). It carries no
	// bytes of its own and is never unlinked.
	crlLockFileName = "license-crl.lock"
	// crlStagingPattern names each invocation's own staging file. The historical fixed name
	// "license-crl.json.tmp" is deliberately NOT reused: two recorders shared it and published
	// each other's bytes, and a file left at that name is unrelated material this code never touches.
	crlStagingPattern = "license-crl-staging-*"
)

func crlFilePath(dataDir string) string { return filepath.Join(dataDir, crlFileName) }

// The recorder's stable failure categories. Callers and tests match them with errors.Is; the
// surrounding text names the path and the cause.
var (
	errCRLStoreBusy       = errors.New("the license CRL store is locked by another recorder")
	errCRLLockUnsupported = errors.New("the license CRL store lock is not supported on this platform")
	errCRLLockUnavailable = errors.New("the license CRL store lock could not be taken")
	errCRLLockUnsafe      = errors.New("the license CRL store lock path is not a regular file this recorder may use")
	// errCRLNotPublished: the failure happened before the rename; the previous store bytes stand.
	errCRLNotPublished = errors.New("the license CRL observation was NOT published; the previous store is unchanged")
	// errCRLPublishedUnconfirmed: the rename happened, so the new store IS current, but the
	// directory sync or close that makes it durable did not confirm.
	errCRLPublishedUnconfirmed = errors.New("the license CRL observation WAS published, but its durability was not confirmed")
	errCRLLeaseCleanup         = errors.New("releasing the license CRL store lock reported an error")
)

// crlStoreLease is one invocation's held CRL transaction lock. Only the recorder that took it
// closes it, once.
type crlStoreLease struct {
	f    *os.File
	path string
}

// crlPersistOps is the narrow private seam over the calls whose failure a real filesystem
// cannot produce on demand. Production uses defaultCRLPersistOps; a test passes its own copy
// to recordCRLObservationsWith, so nothing global is ever swapped.
type crlPersistOps struct {
	write     func(f *os.File, b []byte) (int, error)
	syncFile  func(f *os.File) error
	closeFile func(f *os.File) error
	rename    func(oldpath, newpath string) error
	syncDir   func(d *os.File) error
	closeDir  func(d *os.File) error
	closeLock func(f *os.File) error
}

func defaultCRLPersistOps() crlPersistOps {
	return crlPersistOps{
		write:     (*os.File).Write,
		syncFile:  (*os.File).Sync,
		closeFile: (*os.File).Close,
		rename:    os.Rename,
		syncDir:   (*os.File).Sync,
		closeDir:  (*os.File).Close,
		closeLock: (*os.File).Close,
	}
}

// recordCRLObservations merges a VERIFIED manifest's CRL into the data-dir store.
// Callers pass a manifest whose signature already verified — this function must
// never be reachable from unverified bytes. A manifest with no CRL clears the
// store content (nothing is revoked on this channel anymore) but the write still
// happens, so the file's updated_at honestly reflects the latest observation.
//
// The whole read → compare → merge → publish → directory sync runs under the data
// directory's CRL lease (CR1). Without it two recorders read the same prior state and
// the later rename silently discarded the other's observation — including an older
// manifest landing after a newer one. A busy lease is an error, never a wait.
func recordCRLObservations(dataDir string, m release.Manifest, now time.Time) error {
	return recordCRLObservationsWith(dataDir, m, now, defaultCRLPersistOps())
}

func recordCRLObservationsWith(dataDir string, m release.Manifest, now time.Time, ops crlPersistOps) (err error) {
	if dataDir == "" {
		return nil // no data dir in play (e.g. bare verify runs) — nothing to record
	}
	// EnsureDataDir: the CRL observation store lives beside the signing keys, so
	// the directory carries its own VCS exclusion. It runs before the lease
	// because the lock file lives in that directory.
	if err := secure.EnsureDataDir(dataDir); err != nil {
		return err
	}
	lease, err := acquireCRLStoreLock(dataDir, ops)
	if err != nil {
		return err
	}
	published := false
	defer func() { err = releaseCRLStoreLock(lease, ops, err, published) }()

	prev, _, err := loadCRLObservations(dataDir)
	if err != nil {
		// A corrupt store must NOT be silently reset: starting fresh would restart
		// every 14-day grace clock (and could be an attacker's tampering). Refuse and
		// keep the existing file for investigation — the caller warns, never blocks.
		return fmt.Errorf("license CRL store is unreadable, not overwriting it: %w", err)
	}
	// Monotonicity (anti-freeze for the CRL itself): a manifest OLDER than the one
	// that last SET the store must not clear or rewrite the revocation content — that
	// is the replay an attacker serves to drop an observed revocation. A manifest with
	// no released_at cannot displace a dated one either. Newer/equal is accepted (also
	// the legitimate corrected-CRL / un-revoke path).
	if prev != nil && prev.SetByReleasedAt != "" {
		if prevSet, perr := time.Parse(time.RFC3339, prev.SetByReleasedAt); perr == nil {
			if m.ReleasedAt.IsZero() || m.ReleasedAt.Before(prevSet) {
				return nil // older/undated/replayed manifest — keep the newer CRL we hold
			}
		}
	}
	prevFirst := map[string]string{}
	if prev != nil {
		prevFirst = prev.FirstObserved
	}

	next := crlObservations{
		FirstObserved: map[string]string{},
		SetByChannel:  m.Channel,
		UpdatedAt:     now.UTC().Format(time.RFC3339),
	}
	if !m.ReleasedAt.IsZero() {
		next.SetByReleasedAt = m.ReleasedAt.UTC().Format(time.RFC3339)
	}
	if !m.Revoked.Empty() {
		next.Serials = append([]string(nil), m.Revoked.Serials...)
		next.HolderIDs = append([]string(nil), m.Revoked.HolderIDs...)
		next.LicenseKeyEpoch = m.Revoked.LicenseKeyEpoch
	}
	keep := func(key string) {
		if ts, ok := prevFirst[key]; ok {
			next.FirstObserved[key] = ts // the clock never resets while still listed
			return
		}
		next.FirstObserved[key] = now.UTC().Format(time.RFC3339)
	}
	for _, s := range next.Serials {
		keep("serial:" + s)
	}
	for _, h := range next.HolderIDs {
		keep("holder:" + h)
	}
	if next.LicenseKeyEpoch > 0 {
		keep(fmt.Sprintf("epoch:%d", next.LicenseKeyEpoch))
	}

	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	published, err = publishCRLObservations(dataDir, append(b, '\n'), ops)
	return err
}

// publishCRLObservations writes content to this invocation's own staging file and renames it
// over the store. serve reads the store per call (crlViewFromDataDir) without the lease, so
// the store is only ever replaced whole: a reader sees the previous or the new snapshot.
//
// published reports whether the rename happened, and the error says which side of it failed:
//   - before the rename (errCRLNotPublished): the previous store bytes stand and only THIS
//     invocation's staging name is removed;
//   - after it (errCRLPublishedUnconfirmed): the new store is current and is left in place —
//     nothing is restored or deleted — but the directory sync that makes the rename durable
//     did not confirm.
//
// Each descriptor is closed exactly once, and a cleanup failure is joined after the primary
// cause. An injected or real fsync success here is not a power-loss qualification.
func publishCRLObservations(dataDir string, content []byte, ops crlPersistOps) (published bool, err error) {
	final := crlFilePath(dataDir)
	f, err := os.CreateTemp(dataDir, crlStagingPattern)
	if err != nil {
		return false, fmt.Errorf("%w: create a staging file in %s: %w", errCRLNotPublished, dataDir, err)
	}
	staging := f.Name()
	closeAttempted := false
	fail := func(step string, cause error) (bool, error) {
		errs := []error{fmt.Errorf("%w: %s %s: %w", errCRLNotPublished, step, staging, cause)}
		if !closeAttempted {
			closeAttempted = true
			if cerr := ops.closeFile(f); cerr != nil {
				errs = append(errs, fmt.Errorf("close staging file %s: %w", staging, cerr))
			}
		}
		if rerr := os.Remove(staging); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove staging file %s: %w", staging, rerr))
		}
		return false, errors.Join(errs...)
	}

	if err := f.Chmod(0o600); err != nil {
		return fail("set mode 0600 on", err)
	}
	n, err := ops.write(f, content)
	if err == nil && n != len(content) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fail("write", err)
	}
	if err := ops.syncFile(f); err != nil {
		return fail("sync", err)
	}
	closeAttempted = true
	if err := ops.closeFile(f); err != nil {
		return fail("close", err)
	}
	if err := ops.rename(staging, final); err != nil {
		return fail("rename", err)
	}

	d, err := os.Open(dataDir)
	if err != nil {
		return true, fmt.Errorf("%w: %s now holds this observation, but opening %s to sync the rename failed: %w",
			errCRLPublishedUnconfirmed, final, dataDir, err)
	}
	serr := ops.syncDir(d)
	cerr := ops.closeDir(d)
	switch {
	case serr != nil:
		primary := fmt.Errorf("%w: %s now holds this observation, but syncing directory %s failed: %w",
			errCRLPublishedUnconfirmed, final, dataDir, serr)
		if cerr != nil {
			return true, errors.Join(primary, fmt.Errorf("close directory %s: %w", dataDir, cerr))
		}
		return true, primary
	case cerr != nil:
		return true, fmt.Errorf("%w: %s now holds this observation and directory %s synced, but closing it failed: %w",
			errCRLPublishedUnconfirmed, final, dataDir, cerr)
	}
	return true, nil
}

// releaseCRLStoreLock closes the lease once. A close failure never replaces the transaction's
// own answer: it is joined after a primary error, and after a confirmed publication it is a
// lease-cleanup diagnostic that still says the observation was published.
func releaseCRLStoreLock(lease *crlStoreLease, ops crlPersistOps, primary error, published bool) error {
	cerr := ops.closeLock(lease.f)
	switch {
	case cerr == nil:
		return primary
	case primary != nil:
		return errors.Join(primary, fmt.Errorf("%w %s: %w", errCRLLeaseCleanup, lease.path, cerr))
	case published:
		return fmt.Errorf("the license CRL observation was published and its durability confirmed, but %w %s: %w",
			errCRLLeaseCleanup, lease.path, cerr)
	default:
		return fmt.Errorf("no license CRL change was needed, but %w %s: %w", errCRLLeaseCleanup, lease.path, cerr)
	}
}

// loadCRLObservations reads the store. ok=false (with nil error) when this
// deployment has never observed a verified CRL — the honest "unavailable" state.
// A corrupt file returns ok=false with the error: the caller decides whether to
// surface it; it must NEVER be treated as "revoked" or "clear".
func loadCRLObservations(dataDir string) (*crlObservations, bool, error) {
	if dataDir == "" {
		return nil, false, nil
	}
	b, err := os.ReadFile(crlFilePath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var obs crlObservations
	if err := json.Unmarshal(b, &obs); err != nil {
		return nil, false, fmt.Errorf("%s is corrupt: %w", crlFileName, err)
	}
	return &obs, true, nil
}

// crlViewFunc is the seam the seat-policy wiring consumes (types.go pattern):
// the latest observed revocation, the first-observed clock per identity key, and
// ok=false when no verified CRL was ever observed. The community build ignores
// it entirely; enterprise/seats applies its observation grace on top.
type crlViewFunc func() (rev license.Revocation, firstObserved map[string]time.Time, ok bool)

// crlViewFromDataDir returns a crlViewFunc reading the store PER CALL, so a CRL
// recorded by a concurrently running `olivares upgrade` (separate process) is
// honored at the next seat evaluation without any restart or cache invalidation.
func crlViewFromDataDir(dataDir string) crlViewFunc {
	return func() (license.Revocation, map[string]time.Time, bool) {
		obs, ok, err := loadCRLObservations(dataDir)
		if err != nil || !ok {
			return license.Revocation{}, nil, false
		}
		first := make(map[string]time.Time, len(obs.FirstObserved))
		for k, v := range obs.FirstObserved {
			if t, perr := time.Parse(time.RFC3339, v); perr == nil {
				first[k] = t
			}
		}
		return license.Revocation{
			Serials:         obs.Serials,
			HolderIDs:       obs.HolderIDs,
			LicenseKeyEpoch: obs.LicenseKeyEpoch,
		}, first, true
	}
}

// describeCRLForLicense renders the operator-facing WARN lines an upgrade run
// prints when the verified channel CRL affects the CURRENTLY INSTALLED license.
// Returns nil when there is nothing to warn about.
func describeCRLForLicense(dataDir string, m release.Manifest, now time.Time) []string {
	if m.Revoked.Empty() {
		return nil
	}
	lines := []string{fmt.Sprintf(
		"this channel's manifest revokes %d serial(s), %d holder(s)%s",
		len(m.Revoked.Serials), len(m.Revoked.HolderIDs),
		cond(m.Revoked.LicenseKeyEpoch > 0, fmt.Sprintf(", and fences licenses issued before %s",
			time.Unix(m.Revoked.LicenseKeyEpoch, 0).UTC().Format(time.RFC3339)), ""),
	)}
	src, err := resolveLicense("", dataDir, osGetenv)
	if err != nil || src.Blob == "" {
		return lines
	}
	kr, err := licenseKeyringForDataDir(dataDir)
	if err != nil {
		return append(lines, fmt.Sprintf(
			"the license trust could NOT be established, so this CRL was not evaluated against the installed license (%v)", err))
	}
	if kr.Len() == 0 {
		return lines
	}
	lic, err := kr.Verify(src.Blob, now)
	if err != nil {
		// THREE answers, never two. Returning the CRL summary unchanged here made "the installed
		// license is not revoked" and "I could not read the installed license" look identical to
		// the reader — the failure this repository pays for most often. Say which one it is.
		return append(lines, fmt.Sprintf(
			"the installed license could NOT be read, so this CRL was not evaluated against it (%v)", err))
	}
	rev := license.Revocation{
		Serials:         m.Revoked.Serials,
		HolderIDs:       m.Revoked.HolderIDs,
		LicenseKeyEpoch: m.Revoked.LicenseKeyEpoch,
	}
	// A v3 credential matches on its serial and on the signing-key epoch; the holder axis does
	// not apply to it yet (the reason is written next to Credential.RevokedBy). Until this line
	// read both containers, a revoked v3 credential matched NOTHING at all — it never got past
	// the verifier.
	if lic.RevokedBy(rev) {
		lines = append(lines,
			"the INSTALLED license is REVOKED by this CRL: this is recorded and displayed, and it gates "+
				"nothing — no edition caps users or disables anything on a revoked license (docs/07)")
	}
	return lines
}

// sortedCRLKeys is a small test/diagnostic helper: the observation keys in
// deterministic order.
func sortedCRLKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
