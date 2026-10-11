// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/store"
)

// auditarchive.go is the engine's continuous ledger-archival loop:
// per tick it drains every tenant's audit chain — business orgs PLUS the
// reserved system tenant, the same coverage as CheckpointAll — into verifiable
// JSONL segments (core/audit.ExportSegments) on a WORM sink, anchors each
// durably-written segment back INSIDE the chain it archives (AnchorSegment:
// the next segment contains the anchor), and only then advances the per-tenant
// resume bookkeeping.
//
// Bookkeeping vs evidence: the resume point lives in Org.Settings under
// "audit.archive.last_seq" (read-modify-write inside the Mutate tx, the SCIM
// settings-key precedent) and is RECOVERABLE state, never evidence — the
// evidence is the manifest chain plus the in-chain anchor events. Delivery is
// at-least-once and CONVERGENT via the pending-boundary protocol: before a
// segment's first Put its "<from>-<to>" boundary is persisted under
// "audit.archive.pending" (own tx); after both Puts, ONE tx appends the anchor
// event, advances last_seq and clears pending — atomically, so a crash can
// never separate "anchored" from "advanced". A crash mid-segment leaves
// pending on record and the next tick reuses that boundary VERBATIM instead of
// recomputing it from the moved head, so the retry re-puts byte-identical
// content to the same keys — which both sinks absorb (DirSink accepts an
// identical re-put; S3 versioning+lock adds another harmless locked version,
// the documented idempotent recovery). Corrupt bookkeeping (an unparseable
// last_seq or pending, or a pending that does not resume last_seq+1) SKIPS the
// tenant's drain with a loud error carrying the recovery instruction — never a
// silent reset to 0, which would mass re-export with boundaries that no longer
// match the already-sealed WORM objects.
//
// HA: only the ACTIVE writer archives — anchoring is a chain write, so
// a standby gates out per tick exactly like the checkpointer. Postgres (R2,
// inherited): without an --admin-dsn BYPASSRLS pool, ListOrgs may return empty
// and only the system tenant is covered; boot already warned loudly.
//
// The sink configuration (OLIVARES_AUDIT_ARCHIVE_CONFIG) is operator-provided
// and SECRET-BEARING (S3 credentials): it is read via readOperatorConfig
// (sealed-envelope aware), lives out of the store, and is never logged. A supplied
// unreadable/invalid file fails startup rather than silently disabling archival.

const (
	// auditArchiveJobName is the runtime scheduler's job name (contract §8.5).
	auditArchiveJobName = "audit-archive"

	// Environment (names fixed by contract §8.5).
	auditArchiveSinkEnv          = "OLIVARES_AUDIT_ARCHIVE_SINK"     // "" off | dir | s3archive
	auditArchiveDirEnv           = "OLIVARES_AUDIT_ARCHIVE_DIR"      // dir sink root
	auditArchiveConfigEnv        = "OLIVARES_AUDIT_ARCHIVE_CONFIG"   // s3archive settings JSON (secret-bearing)
	auditArchiveIntervalEnv      = "OLIVARES_AUDIT_ARCHIVE_INTERVAL" // Go duration, default 24h
	auditArchiveSegmentEventsEnv = "OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS"
	auditArchiveRetainDaysEnv    = "OLIVARES_AUDIT_ARCHIVE_RETAIN_DAYS"

	defaultAuditArchiveInterval   = 24 * time.Hour
	defaultAuditArchiveRetainDays = 2555 // 7 years, the audit.ledger recommendation (§2)
	// maxAuditArchiveRetainDays mirrors the s3archive/retention-policy ceiling.
	maxAuditArchiveRetainDays = 36500

	// archiveLastSeqSettingsKey is the per-tenant resume point in Org.Settings.
	archiveLastSeqSettingsKey = "audit.archive.last_seq"

	// archivePendingSettingsKey is the per-tenant IN-FLIGHT segment boundary
	// ("<from>-<to>") in Org.Settings: persisted before the segment's first Put,
	// cleared in the same tx that anchors the segment and advances last_seq.
	// After a crash mid-segment the next tick reuses this boundary verbatim, so
	// the retry rebuilds the byte-identical segment for the same keys instead of
	// recomputing a shifted boundary from the moved head (which would orphan the
	// sealed WORM objects as a permanent "segment-gap" overlap).
	archivePendingSettingsKey = "audit.archive.pending"
)

// archiveRecoveryHint rides on every corrupt-bookkeeping error: the operator
// recovers from the EVIDENCE (the archive itself); the engine never guesses a
// resume point (deny-closed for evidence).
const archiveRecoveryHint = "archival for this tenant is paused (deny-closed); to recover, run `olivares audit archive verify` against the sink's copy, set Org.Settings[\"audit.archive.last_seq\"] to the last verified to_seq, and remove \"audit.archive.pending\""

// auditArchiveConfig is the loop's resolved environment.
type auditArchiveConfig struct {
	sink          string // "" (off) | "dir" | "s3archive"
	dir           string
	configPath    string
	interval      time.Duration
	segmentEvents int
	retainDays    int
}

// loadAuditArchiveConfig resolves the archival environment. Numeric/duration
// typos keep their defaults with a warning (a typo must not silently change
// retention behavior); the sink selection itself is validated in
// newAuditArchiveLoop, where an invalid selected sink aborts startup.
func loadAuditArchiveConfig(getenv func(string) string, log *slog.Logger) auditArchiveConfig {
	cfg := auditArchiveConfig{
		sink:          strings.TrimSpace(getenv(auditArchiveSinkEnv)),
		dir:           strings.TrimSpace(getenv(auditArchiveDirEnv)),
		configPath:    strings.TrimSpace(getenv(auditArchiveConfigEnv)),
		interval:      defaultAuditArchiveInterval,
		segmentEvents: audit.DefaultSegmentEvents,
		retainDays:    defaultAuditArchiveRetainDays,
	}
	if raw := strings.TrimSpace(getenv(auditArchiveIntervalEnv)); raw != "" {
		if d, err := (envconfig.Reader{Source: getenv}).Duration(auditArchiveIntervalEnv, defaultAuditArchiveInterval); err == nil && d > 0 {
			cfg.interval = d
		} else {
			log.Warn("audit-archive: "+auditArchiveIntervalEnv+" is not a valid positive duration; using the default (disable archival via "+auditArchiveSinkEnv+"=\"\", not the interval)", "value", raw, "default", defaultAuditArchiveInterval.String())
		}
	}
	if raw := strings.TrimSpace(getenv(auditArchiveSegmentEventsEnv)); raw != "" {
		if n, err := (envconfig.Reader{Source: getenv}).Int(auditArchiveSegmentEventsEnv, audit.DefaultSegmentEvents); err == nil && n > 0 {
			cfg.segmentEvents = n
		} else {
			log.Warn("audit-archive: "+auditArchiveSegmentEventsEnv+" is not a positive integer; using the default", "value", raw, "default", audit.DefaultSegmentEvents)
		}
	}
	if raw := strings.TrimSpace(getenv(auditArchiveRetainDaysEnv)); raw != "" {
		// 0 is a legitimate explicit choice: no per-object retain-until, deferring
		// to the bucket's default Object Lock retention (ArchivePutOptions zero).
		if n, err := (envconfig.Reader{Source: getenv}).Int(auditArchiveRetainDaysEnv, defaultAuditArchiveRetainDays); err == nil && n >= 0 && n <= maxAuditArchiveRetainDays {
			cfg.retainDays = n
		} else {
			log.Warn("audit-archive: "+auditArchiveRetainDaysEnv+" is not an integer in [0, 36500]; using the default", "value", raw, "default", defaultAuditArchiveRetainDays)
		}
	}
	return cfg
}

// auditArchiveLoop drives the periodic per-tenant archive drain.
type auditArchiveLoop struct {
	st     store.Store
	sink   audit.ArchiveSink
	signer *audit.Signer
	// priors are the per-event key's prior rotation generations, exported
	// into the advisory keys.json so a rotated chain self-verifies.
	priors        []ed25519.PublicKey
	interval      time.Duration
	segmentEvents int
	retainDays    int
	clock         func() time.Time
	log           *slog.Logger
	// keysAttempted: the advisory keys.json is written ONCE per process (the
	// sink is fresh exactly once — at construction). No mutex: the runtime runs
	// a job's passes sequentially on one goroutine (runtime.jobLoop).
	keysAttempted bool
	skips         enumerationSkips
}

// archiveBookkeeping is one tenant's parsed resume state.
type archiveBookkeeping struct {
	// lastSeq is the resume point: the highest anchored to_seq (0 = never
	// archived, start at seq 1).
	lastSeq int64
	// pendingFrom/pendingTo is the in-flight segment boundary when hasPending.
	pendingFrom, pendingTo int64
	hasPending             bool
}
