// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

// licenseHolder is the engine's LIVE, swappable commercial license — the heart of
// the in-place edition upgrade (§3 point 3, the Grafana/Elastic hot-apply model).
//
// Before the boot blob was captured IMMUTABLY in the seat-policy closure, so
// changing the license meant a restart. Now the closure reads THIS holder (claims),
// and `olivares license install` / the console / a reload swap the holder under a
// lock — a renewed license takes effect with ZERO downtime and an expired one
// degrades back to the community edition, both at the next per-call evaluation. The
// ONLY restart left is the binary swap open→enterprise.
//
// OPEN-CORE INVARIANT (LICENSING.md): the holder is pure edition plumbing. The open
// binary wires it and DISPLAYS its status; it never reads a license to change
// behavior. Since B10 that is true of USER ACCOUNTS in every build: accounts are
// unlimited in every self-hosted tier, so an expiry can never cap or degrade them
// (the seat seam is a no-op — core/auth/seatcap.go). Nothing the holder does gates a
// feature, degrades a request, or blocks a boot on a license check; attestation-only
// is preserved.
type licenseHolder struct {
	pub   ed25519.PublicKey
	clock func() time.Time
	log   *slog.Logger

	mu         sync.RWMutex
	src        licenseSource
	lastStatus string // last OBSERVED display status, for one-shot transition logging
}

// licenseDisplay is a point-in-time, verify-but-never-gate view of the live
// license, for status reporting (server-info, `license status`, the console panel).
type licenseDisplay struct {
	status   string // none | invalid | valid | expired | perpetual
	licensee string
	// lic is the verified license in WHICHEVER signed container it arrived in — the flat v1/v2
	// claim set or the v3 aggregate credential. It used to be a license.Claims, which is why
	// every display surface below rejected a v3 credential outright: the flat type cannot hold
	// one, so the whole holder was blind to the container the license Worker now issues.
	lic      license.Verified
	verified bool  // signature verified (the display facts are trustworthy)
	reason   error // why an "invalid" license could not be read; nil otherwise
	source   licenseSource
}

// newLicenseHolder seeds the holder with the boot-resolved license. clock may be nil
// (system clock). It records the initial status WITHOUT logging a transition (boot
// logs the initial posture separately) so the first reEvaluate/set only fires on a
// real change.
func newLicenseHolder(pub ed25519.PublicKey, src licenseSource, clock func() time.Time, log *slog.Logger) *licenseHolder {
	if clock == nil {
		clock = func() time.Time { return time.Now() }
	}
	h := &licenseHolder{pub: pub, clock: clock, log: log, src: src}
	h.lastStatus = h.displayFor(src).status
	return h
}

// claims is the licenseClaimsFunc handed to newSeatPolicy. It returns the VERIFIED,
// UNEXPIRED claims (ok=false otherwise), evaluated PER CALL so an expiry or a hot-
// applied renewal is reflected with no restart. The open binary's community seat
// policy never calls through to a license; and since B10 no policy in ANY build may
// turn Claims.MaxUsers into a runtime refusal — it is an attested, display-only
// figure (0 = unlimited, which is what every self-hosted tier now gets).
//
// ⛔ THIS SEAM IS NOT A DISPLAY HOOK, AND THAT WAS MEASURED THE HARD WAY. An earlier cut of this
// change returned false for every v3 credential and called it free, reasoning that since B10 no
// build turns MaxUsers into a refusal and this build's policy ignores the seam entirely
// (wire_noenterprise.go). That checked ONE consumer and generalised. The enterprise overlay
// publishes this SAME provider as the process-wide add-on license source
// (cmd-overlay/olivares/wire_enterprise.go: newSeatPolicy → installAddonLicenseSources →
// addonClaims → addonGate), where ok=false is StateUnentitled and Authorize refuses the
// operation. A paying customer holding a v3 credential would have lost EVERY add-on, silently,
// while this same holder's display path reported the license valid. Found by the 2026-08-11 Codex
// contrast (F-1).
//
// So a credential is projected onto the flat claim set instead — see license.LegacySeamClaims for
// exactly what it keeps (holder, serial, term) and what it deliberately drops (everything that
// says what was bought). The seam cannot carry a grant list, so a consumer reading through it
// cannot honor which lines were purchased; making that possible needs a container-aware source,
// which is the overlay's own change and is reported as such.
func (h *licenseHolder) claims() (license.Claims, bool) {
	h.mu.RLock()
	blob := h.src.Blob
	h.mu.RUnlock()
	if blob == "" || len(h.pub) != ed25519.PublicKeySize {
		return license.Claims{}, false
	}
	v, err := license.VerifyEnvelope(blob, h.pub)
	if err != nil {
		return license.Claims{}, false
	}
	c, ok := v.LegacySeamClaims()
	if !ok {
		return license.Claims{}, false
	}
	if c.Status(h.clock()) == license.StatusExpired {
		return license.Claims{}, false
	}
	return c, true
}

// grants is the container-aware entitlement source for addongate.
// ok=false matches claims(): no blob, unverifiable, or expired container.
// A live FLAT license returns (nil, true) — there is no grant list.
// A live v3 returns a COPY of the signed lines, including lines whose
// own Active(now) is false. Filtering here would hide a purchased add-on
// from the overlay and collapse every line onto the base term.
func (h *licenseHolder) grants() ([]license.Grant, bool) {
	h.mu.RLock()
	blob := h.src.Blob
	h.mu.RUnlock()
	if blob == "" || len(h.pub) != ed25519.PublicKeySize {
		return nil, false
	}
	v, err := license.VerifyEnvelope(blob, h.pub)
	if err != nil {
		return nil, false
	}
	c, ok := v.LegacySeamClaims()
	if !ok {
		return nil, false
	}
	if c.Status(h.clock()) == license.StatusExpired {
		return nil, false
	}
	if !v.IsCredentialV3() {
		return nil, true
	}
	src := v.Grants()
	out := make([]license.Grant, len(src))
	copy(out, src)
	return out, true
}

// display returns the current verify-but-never-gate view of the live license.
func (h *licenseHolder) display() licenseDisplay {
	h.mu.RLock()
	src := h.src
	h.mu.RUnlock()
	return h.displayFor(src)
}

// displayFor derives the status of an arbitrary source against the embedded key and
// the local clock. It NEVER gates — any verification error is reported as a status,
// not an enforcement signal (LICENSING.md). It holds no lock (pure over src + immutable
// pub + clock), so callers may invoke it inside or outside the mutex.
func (h *licenseHolder) displayFor(src licenseSource) licenseDisplay {
	if src.Blob == "" || len(h.pub) != ed25519.PublicKeySize {
		return licenseDisplay{status: "none", source: src}
	}
	v, err := license.VerifyEnvelope(src.Blob, h.pub)
	if err != nil {
		// The REASON is kept, because collapsing every failure to "invalid" made this surface
		// blame the wrong thing: the boot warning and the transition log both say the license
		// "no longer verifies against this build's key", and for a container this build cannot
		// READ the key verified perfectly well. An operator sent to check their key over a
		// binary that needs upgrading loses the afternoon (measured by the 2026-08-11 scope
		// audit against boot.go and logTransition).
		return licenseDisplay{status: "invalid", reason: err, source: src}
	}
	return licenseDisplay{
		status:   string(v.Status(h.clock())),
		licensee: v.Licensee(),
		lic:      v,
		verified: true,
		source:   src,
	}
}

// set swaps the live license to src (the hot-apply). It is the SINGLE mutation point
// — Install (after persisting) and the reload/SIGHUP reconcile both call it — so
// every path converges on one swap and one transition log. Returns the new display.
func (h *licenseHolder) set(src licenseSource) licenseDisplay { return h.update(src) }

// reEvaluate re-derives the status of the UNCHANGED license and logs a transition if
// it crossed (e.g. valid→expired as the clock passes ExpiresAt). It is what the
// serve-time expiry monitor ticks so the operator gets a WARN at the moment of
// expiry, not only at the next reload. The seat policy already honors the crossing
// per call; this is the observability half.
//
// It re-derives from the CURRENT h.src inside ONE critical section and NEVER writes
// h.src — only set()/reconcile mutate the source. (An earlier cut snapshot-read h.src
// under RLock then wrote it back under a fresh Lock; that read-then-write split let a
// concurrent set() — install/reload/SIGHUP coinciding with the hourly tick — be lost,
// silently reverting a just-applied license. reEvaluate is read-and-reclassify only.)
func (h *licenseHolder) reEvaluate() licenseDisplay {
	h.mu.Lock()
	prev := h.lastStatus
	d := h.displayFor(h.src)
	h.lastStatus = d.status
	h.mu.Unlock()
	h.logTransition(prev, d)
	return d
}

func (h *licenseHolder) update(src licenseSource) licenseDisplay {
	h.mu.Lock()
	prev := h.lastStatus
	h.src = src
	d := h.displayFor(src)
	h.lastStatus = d.status
	h.mu.Unlock()
	h.logTransition(prev, d)
	return d
}

// errText renders a display reason for a log/report field, and never an empty string: a key
// whose value is "" reads as "there was no reason", which is the opposite of what an absent
// reason means here.
func errText(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

// logTransition emits a one-shot, honest log line when the status changes. Expiry
// and removal are WARN (an operator must notice the edition dropping); a fresh valid
// license is INFO. It never crashes or fails — degradation is always graceful
// (docs/SECURITY-HARDENING.md, §3 point 4).
func (h *licenseHolder) logTransition(prev string, d licenseDisplay) {
	if h.log == nil || prev == d.status {
		return
	}
	switch d.status {
	case "grace":
		// The profile field is OMITTED for a credential rather than logged empty: a v3 carries no
		// issuance profile, and `profile=""` reads as a fact that was measured and came back
		// blank. (2026-08-11 Codex contrast, F-7.)
		args := []any{"previous", prev, "licensee", d.licensee}
		if p := d.lic.Profile(); p != "" {
			args = append(args, "profile", p)
		}
		args = append(args, "grace_ends", d.lic.RightEnds().UTC().Format(time.RFC3339), "source", d.source.Kind)
		h.log.Warn("license: the installed commercial license is past its expiry and inside its profile's GRACE window — enterprise entitlements are MAINTAINED for now (in an enterprise build); renew before the grace ends or they drop to the community edition (user accounts are never capped)",
			args...)
	case "expired":
		h.log.Warn("license: the installed commercial license has EXPIRED — enterprise entitlements drop to the community edition (data intact, no restart, no crash); install a renewed license to restore them",
			"previous", prev, "licensee", d.licensee, "source", d.source.Kind)
	case "invalid":
		// The reason rides along: "does not verify against this build's key" is TRUE for a bad
		// signature and FALSE — and misleading — for a signed container this build cannot read,
		// where the next step is upgrading the binary, not hunting for a key.
		h.log.Warn("license: the installed license could not be read — treated as no commercial license (community edition)",
			"previous", prev, "reason", errText(d.reason), "source", d.source.Kind)
	case "valid", "perpetual":
		h.log.Info("license: commercial license applied",
			"status", d.status, "licensee", d.licensee, "source", d.source.Kind)
	case "none":
		if prev == "valid" || prev == "perpetual" {
			h.log.Warn("license: the commercial license was removed — enterprise entitlements drop to the community edition (data intact)",
				"previous", prev)
		}
	}
}
