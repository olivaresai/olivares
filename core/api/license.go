// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"time"
)

// LicenseService is the LIVE edition/license surface: install, observe and
// HOT-APPLY a commercial license without a restart. The engine implements it; the
// console/CLI drive it through the superadmin-gated /v1/console/license endpoints.
//
// OPEN-CORE INVARIANT (LICENSING.md). This is pure EDITION plumbing. The open binary
// wires it to persist, display and hot-apply the license artifact, but it NEVER
// gates a feature, degrades a request or blocks a boot on a license check — the
// consumer of an attested claim is the closed enterprise build's add-on entitlement.
// Installing a license in the community build verifies and stores it (so an in-place
// swap to the enterprise binary already has it), but does NOT change this build's
// behavior. Attestation-only is preserved end to end.
//
// B10 (2026-07-27): user accounts are never part of that entitlement. Self-hosted
// users are unlimited in every tier, so no install, expiry or removal on this
// surface can reduce the accounts a deployment may run (core/auth/seatcap.go).
type LicenseService interface {
	// LicenseStatus reports the live edition/license for display, including the live
	// ACTIVE-USER usage so the console can render entitlements / usage / expiry.
	LicenseStatus(ctx context.Context) (LicenseStatus, error)
	// InstallLicense verifies, persists to the canonical data-dir path and HOT-APPLIES
	// a license (no restart). It refuses a blob that does not verify
	// (ErrLicenseInvalid) and refuses entirely when a higher-precedence boot override
	// is active (ErrLicenseManagedExternally). acknowledge is accepted and INERT since
	// B10: no license entitles fewer user accounts, so there is no seat downgrade to
	// acknowledge (ErrLicenseDowngrade is retained but no longer returned).
	InstallLicense(ctx context.Context, blob string, acknowledge bool) (LicenseStatus, error)
	// UninstallLicense removes the persisted license and hot-reverts to the community
	// edition, refusing under an external override. Removal costs no user account
	// (B10), so acknowledge is accepted and inert here too.
	UninstallLicense(ctx context.Context, acknowledge bool) (LicenseStatus, error)
	// LicenseDisplay is the CHEAP (no DB) live status + attested display labels for the
	// unauthenticated server-info surface — it must not count users or touch user tables.
	// The plan / support-tier labels are display-only attestations (never gates).
	LicenseDisplay() LicenseDisplayInfo
	// Reconcile re-resolves the license by precedence and hot-applies it, with no
	// restart. It is the license half of the runtime reload (folded into POST
	// /v1/console/runtime/reload and SIGHUP alongside the source reconcile), so a
	// file-based `license install` + reload applies live. It never fails the caller.
	Reconcile(ctx context.Context)
}

// LicenseDisplayInfo is the cheap, no-DB license view for the unauthenticated
// server-info surface: the lifecycle status, the licensee, and the attested
// display-only commercial labels (plan and support tier). Every field is
// informational — none gates, degrades or blocks anything (LICENSING.md). Plan and
// SupportTier are populated only when the signature verified (an invalid/absent
// license leaves them empty, never a fabricated value).
type LicenseDisplayInfo struct {
	Status      string
	Licensee    string
	Plan        string
	SupportTier string
}

// LicenseStatus is the rendered edition/license view returned to the console/CLI.
type LicenseStatus struct {
	// Edition is the BUILD edition — f(build tag), independent of the license:
	// "community" (the default AGPL binary) or "enterprise" (the -tags enterprise
	// superset). It is precisely what a restart-free hot-apply CANNOT change; only a
	// binary swap does (the one reboot in the Grafana in-place model).
	Edition string `json:"edition"`
	// HotApply is true when this engine applies a license change without a restart
	// (always true here — the holder is live). Display honesty for the upgrade UX.
	HotApply bool `json:"hot_apply"`
	// Status is the license lifecycle: none | invalid | valid | expired | perpetual.
	Status string `json:"status"`
	// Source is the provenance: none | flag | env-path | env-inline | data-dir.
	Source string `json:"source"`
	// SourcePath is the file path when the source is a file (flag/env-path/data-dir).
	SourcePath string `json:"source_path,omitempty"`
	// ManagedExternally is true when a boot override (--license / OLIVARES_LICENSE
	// [_PATH]) outranks the data-dir file, so the console/CLI install is refused (the
	// license is managed out-of-band — e.g. a Kubernetes secret mount).
	ManagedExternally bool `json:"managed_externally"`

	// The attested, DISPLAY-ONLY claims (present when verified — valid/expired/perpetual).
	Licensee    string     `json:"licensee,omitempty"`
	Plan        string     `json:"plan,omitempty"`
	SupportTier string     `json:"support_tier,omitempty"` // attested support relationship (display-only; never gates — SUPPORT.md)
	Features    []string   `json:"features,omitempty"`
	MaxUsers    int        `json:"max_users"` // Deprecated: attested seat figure (0 = unlimited); IGNORED by every build since B10
	IssuedAt    *time.Time `json:"issued_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`

	// Live ACTIVE-USER usage. ActiveUsers is a usage figure, never a quota
	// numerator: self-hosted accounts are unlimited in every tier (B10), which is
	// why the console renders usage and no quota bar.
	//
	// Deprecated: SeatLimit/SeatLimited are retained on the wire for compatibility with
	// clients that still read them; this build reports unlimited (0/false) because
	// nothing enforces a seat limit any more. Authenticator.SeatLimit normalizes any
	// non-positive figure to (0,false), so "limited with a limit of zero" — which a
	// client would read as "no account may exist" — can never reach the wire
	// (core/auth/seatcap.go).
	SeatLimit         int  `json:"seat_limit"` // with SeatLimited=false means unlimited
	SeatLimited       bool `json:"seat_limited"`
	ActiveUsers       int  `json:"active_users"`
	ActiveUsersCapped bool `json:"active_users_capped,omitempty"` // count hit the display bound (render "N+")
}

// License service errors. NONE is an authorization control — they are edition/UX
// signals mapped to honest HTTP codes in statusFor (errors.go).
var (
	// ErrLicenseUnavailable is returned when no license service is wired (an embedder/
	// test that did not opt in). 501, like the source roster.
	ErrLicenseUnavailable = errors.New("api: license service unavailable")
	// ErrLicenseDowngrade WAS returned when the license being installed (or the
	// removal) entitled fewer seats than were currently ACTIVE and acknowledge was not
	// set. 409. Since B10 no license entitles fewer user accounts, so nothing returns
	// it; the error and its wire code are retained (never deleted) so clients that
	// still handle the acknowledge round-trip keep compiling and behaving.
	ErrLicenseDowngrade = errors.New("api: license_downgrade_requires_acknowledge")
	// ErrLicenseManagedExternally is returned when a higher-precedence boot override
	// (--license / OLIVARES_LICENSE[_PATH]) is active, so a console/CLI install to the
	// data-dir would be shadowed. 409, with a message naming the override.
	ErrLicenseManagedExternally = errors.New("api: license_managed_externally")
	// ErrLicenseInvalid is returned when a blob to install does not verify against the
	// build's embedded key. 400 — a paste/format error, refused before it is persisted.
	ErrLicenseInvalid = errors.New("api: license blob does not verify against this build's key")
)
