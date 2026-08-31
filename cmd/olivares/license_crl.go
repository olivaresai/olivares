// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
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

const crlFileName = "license-crl.json"

func crlFilePath(dataDir string) string { return filepath.Join(dataDir, crlFileName) }

// recordCRLObservations merges a VERIFIED manifest's CRL into the data-dir store.
// Callers pass a manifest whose signature already verified — this function must
// never be reachable from unverified bytes. A manifest with no CRL clears the
// store content (nothing is revoked on this channel anymore) but the write still
// happens, so the file's updated_at honestly reflects the latest observation.
func recordCRLObservations(dataDir string, m release.Manifest, now time.Time) error {
	if dataDir == "" {
		return nil // no data dir in play (e.g. bare verify runs) — nothing to record
	}
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

	// EnsureDataDir: the CRL observation store lives beside the signing keys, so
	// the directory carries its own VCS exclusion.
	if err := secure.EnsureDataDir(dataDir); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write (temp + rename in the same dir): serve reads this file per-call
	// (crlViewFromDataDir) concurrently with an `upgrade` process writing it, so a
	// truncating in-place write could hand the reader a torn file. os.Rename is
	// atomic on one filesystem; a torn read is thus impossible, and a crash mid-write
	// leaves the previous good file intact (M3).
	final := crlFilePath(dataDir)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		return lines
	}
	lic, err := license.VerifyEnvelope(src.Blob, pub)
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
