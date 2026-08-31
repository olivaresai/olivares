// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/license"
)

// licenseService is the engine-side half of the in-place edition system: it
// implements api.LicenseService over the live licenseHolder, the canonical data-dir
// file and the authenticator's usage accessors. It persists, observes and HOT-APPLIES
// a license (reusing the holder's swap) and refuses an install a higher-precedence
// boot override would shadow.
//
// OPEN-CORE (LICENSING.md): it NEVER gates a feature on the license. In the community
// build the install simply persists + displays the attestation (so an in-place swap
// to the enterprise binary already has it). The build edition is f(build tag),
// reported for display and explicitly NOT changeable by a hot-apply (only a binary
// swap does).
//
// B10 (2026-07-27): user accounts are unlimited in every self-hosted tier, so the
// license never carries a seat consequence here — no install, expiry or removal can
// reduce the accounts a deployment may run. The downgrade-acknowledge seam is kept
// wired but inert (checkDowngrade).
type licenseService struct {
	holder       *licenseHolder
	authr        *auth.Authenticator
	dataDir      string
	explicitPath string // the --license flag value, for override detection
	getenv       func(string) string
	edition      string // build edition: "community" | "enterprise"
	log          *slog.Logger

	// reconcileMu serializes the file read-modify-write across install/uninstall/
	// reconcile. The holder has its own lock for the in-memory swap; this guards the
	// on-disk file so two concurrent writers cannot interleave.
	reconcileMu sync.Mutex
}

// seatDisplayBound bounds the active-user scan behind the console's usage display
// (the status read is superadmin-only and infrequent). A larger estate renders "N+".
// It is a QUERY bound, never an entitlement: running more accounts than this is
// perfectly licensed, the panel just stops counting exactly.
const seatDisplayBound = 10000

func newLicenseService(holder *licenseHolder, authr *auth.Authenticator, dataDir, explicitPath string, getenv func(string) string, edition string, log *slog.Logger) *licenseService {
	return &licenseService{
		holder: holder, authr: authr, dataDir: dataDir, explicitPath: explicitPath,
		getenv: getenv, edition: edition, log: log,
	}
}

var _ api.LicenseService = (*licenseService)(nil)

// LicenseDisplay is the cheap, no-DB live view for the unauthenticated server-info:
// the lifecycle status, licensee, and the attested display-only labels (plan, support
// tier). The labels come from the VERIFIED claims only — an invalid/absent license
// leaves them empty (docs/SECURITY-HARDENING.md: unknown is never reported as a concrete value). It
// gates nothing.
func (s *licenseService) LicenseDisplay() api.LicenseDisplayInfo {
	d := s.holder.display()
	info := api.LicenseDisplayInfo{Status: d.status, Licensee: d.licensee}
	if d.verified {
		info.Plan = d.lic.Plan()
		info.SupportTier = d.lic.SupportTier()
	}
	return info
}

// LicenseStatus renders the full live edition/license view, including live seat usage.
func (s *licenseService) LicenseStatus(ctx context.Context) (api.LicenseStatus, error) {
	return s.statusFor(ctx, s.holder.display())
}

// statusFor builds the API DTO from a holder display plus the live active-user usage
// and boot-override detection. The usage figure is read fresh each call. SeatLimit/
// SeatLimited come from the (display-only) wired policy and, since B10, report
// UNLIMITED in this build — they are kept on the wire for compatibility, not as a
// quota the console should render.
func (s *licenseService) statusFor(ctx context.Context, d licenseDisplay) (api.LicenseStatus, error) {
	out := api.LicenseStatus{
		Edition:    s.edition,
		HotApply:   true,
		Status:     d.status,
		Source:     d.source.Kind,
		SourcePath: d.source.Path,
	}
	if _, _, present := licenseOverridePresent(s.explicitPath, s.getenv); present {
		out.ManagedExternally = true
	}
	if d.verified {
		// MaxUsers et al. are populated ONLY when the signature verified — an
		// unverifiable (invalid) blob has UNKNOWN entitlements, never a concrete 0 the
		// UI would render as "unlimited" (docs/SECURITY-HARDENING.md: unknown is never reported as off).
		out.MaxUsers = d.lic.MaxUsers()
		out.Licensee = d.lic.Licensee()
		out.Plan = d.lic.Plan()
		out.SupportTier = d.lic.SupportTier()
		out.Features = d.lic.Features()
		if t := d.lic.IssuedAt(); !t.IsZero() {
			out.IssuedAt = &t
		}
		// ExpiresAt is when the right ends. For a v3 credential that is the BASE line's EFFECTIVE
		// boundary — its lease while provisional, its paid_through once promoted — and NOT a
		// summary of the purchase: the add-on lines have their own terms and their own phases,
		// and `olivares license status` prints every one of them. This DTO carries no per-line
		// field yet, and adding one is a console change, named in the PR.
		if t := d.lic.Term(); !t.IsZero() {
			out.ExpiresAt = &t
		}
	}
	limit, limited := s.authr.SeatLimit()
	// The deprecation is deliberate and the field's own note says so: SeatLimit/SeatLimited stay
	// on the wire for clients that still read them, and this build reports unlimited. Not
	// writing them would make an old client read "limited with a limit of zero", i.e. no account
	// may exist — the exact failure core/auth/seatcap.go normalizes against.
	out.SeatLimit, out.SeatLimited = limit, limited //nolint:staticcheck // deprecated ON PURPOSE, see above
	active, capped, err := s.authr.ActiveUserCount(ctx, seatDisplayBound)
	if err != nil {
		return api.LicenseStatus{}, err
	}
	out.ActiveUsers, out.ActiveUsersCapped = active, capped
	return out, nil
}

// InstallLicense verifies, persists to the canonical data-dir file and hot-applies a
// license. It refuses an unverifiable blob (before persisting) and any install while a
// boot override is active. The acknowledge flag is accepted and inert (checkDowngrade):
// no license can reduce the accounts a deployment may run.
func (s *licenseService) InstallLicense(ctx context.Context, blob string, acknowledge bool) (api.LicenseStatus, error) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	blob = strings.TrimSpace(blob)
	if kind, detail, present := licenseOverridePresent(s.explicitPath, s.getenv); present {
		return api.LicenseStatus{}, fmt.Errorf("%w: a %s license override (%s) is active; edit that source instead of installing to the data dir", api.ErrLicenseManagedExternally, kind, detail)
	}
	// Verify against the build's embedded key BEFORE persisting — refuse a malformed or
	// wrong-key blob so we never store garbage. This is NOT a gate: a VALID-but-expired
	// blob installs fine (it just reports "expired" and lifts nothing). It only rejects a
	// structurally/cryptographically bad blob (a paste/format error).
	if len(s.holder.pub) == 0 {
		return api.LicenseStatus{}, fmt.Errorf("%w (this build embeds no verification key: %s)", api.ErrLicenseInvalid, license.KeyOrigin())
	}
	v, err := license.VerifyEnvelope(blob, s.holder.pub)
	if err != nil {
		return api.LicenseStatus{}, fmt.Errorf("%w: %v", api.ErrLicenseInvalid, err)
	}
	newOK := v.Status(s.holder.clock()) != license.StatusExpired
	// checkDowngrade takes the flat claim set and is INERT in every build since B10 (no license
	// can reduce the accounts a deployment may run). A v3 credential therefore passes its own
	// zero value: there is nothing to compare and nothing that could refuse.
	if err := s.checkDowngrade(ctx, v.Claims, newOK, acknowledge); err != nil {
		return api.LicenseStatus{}, err
	}
	if err := s.writeLicenseFile(blob); err != nil {
		return api.LicenseStatus{}, err
	}
	return s.reconcileLocked(ctx)
}

// UninstallLicense removes the persisted license and hot-reverts to the community
// edition, and refuses under a boot override. Removal costs the deployment NO user
// accounts (B10 removed the drop to the community cap), so the acknowledge flag is
// accepted and inert; existing accounts and data are untouched either way.
func (s *licenseService) UninstallLicense(ctx context.Context, acknowledge bool) (api.LicenseStatus, error) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	if kind, detail, present := licenseOverridePresent(s.explicitPath, s.getenv); present {
		return api.LicenseStatus{}, fmt.Errorf("%w: a %s license override (%s) is active; edit that source instead", api.ErrLicenseManagedExternally, kind, detail)
	}
	// The retained (inert) downgrade seam — removal never costs a seat since B10.
	if err := s.checkDowngrade(ctx, license.Claims{}, false, acknowledge); err != nil {
		return api.LicenseStatus{}, err
	}
	path := licenseDataDirPath(s.dataDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return api.LicenseStatus{}, fmt.Errorf("remove license %q: %w", path, err)
	}
	return s.reconcileLocked(ctx)
}

// checkDowngrade is the retained downgrade-acknowledge seam, and since B10 it is an
// unconditional no-op that returns nil.
//
// It used to compare the entitlement a change left in force against the LIVE active
// estate and refuse (api.ErrLicenseDowngrade) unless acknowledge was set — where the
// entitlement of an expired/absent license was auth.CommunitySeatLimit, i.e. the
// downgrade-to-3 that B10 removes (an internal design note (not shipped) §B10:
// "retirar el downgrade a 3"). Self-hosted accounts are unlimited in every tier and
// under every commercial state, so installing, replacing, letting lapse or REMOVING a
// license can no longer reduce the number of accounts a deployment may run. There is
// therefore nothing to warn about and nothing to acknowledge: the guard would have to
// invent a limit to fire, and a warning that "new active accounts will be capped"
// would be false.
//
// The seam (this function, the acknowledge parameters on Install/Uninstall, the
// api.ErrLicenseDowngrade code and the console's acknowledge step) is kept wired and
// inert on purpose: only the limit was removed, not the mechanism, and a future
// NON-seat downgrade signal (e.g. add-on lines a shorter license would not carry)
// would land here instead of being re-invented. Seats will never be that signal.
func (s *licenseService) checkDowngrade(_ context.Context, _ license.Claims, _, _ bool) error {
	return nil
}

// Reconcile re-resolves the license by precedence (flag > env > data-dir) and hot-
// applies it. It is what the SIGHUP handler and POST /v1/console/runtime/reload call
// (alongside the source reload), so a file-based `license install` + reload applies
// WITHOUT a restart and an externally-rotated override file is picked up. It never
// fails the caller: an unreadable configured source logs and keeps the live license.
func (s *licenseService) Reconcile(ctx context.Context) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	_, _ = s.reconcileLocked(ctx)
}

// reEvaluate re-derives the status of the unchanged live license (the expiry monitor)
// and logs a valid→expired transition once. It changes no file and takes no DB read.
func (s *licenseService) reEvaluate() licenseDisplay { return s.holder.reEvaluate() }

func (s *licenseService) reconcileLocked(ctx context.Context) (api.LicenseStatus, error) {
	src, err := resolveLicense(s.explicitPath, s.dataDir, s.getenv)
	if err != nil {
		s.log.Warn("license: reconcile could not read the configured source; keeping the current live license", "err", err)
		return s.statusFor(ctx, s.holder.display())
	}
	d := s.holder.set(src)
	return s.statusFor(ctx, d)
}

// writeLicenseFile persists the blob to the canonical data-dir path with 0600 (a
// license is public, but a tight mode matches the other data-dir artifacts and keeps
// the file owner-only). A trailing newline keeps the file shell-friendly.
func (s *licenseService) writeLicenseFile(blob string) error {
	path := licenseDataDirPath(s.dataDir)
	if err := os.WriteFile(path, []byte(blob+"\n"), 0o600); err != nil {
		return fmt.Errorf("write license %q: %w", path, err)
	}
	return nil
}
