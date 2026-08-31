// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/s3archive"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
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
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.interval = d
		} else {
			log.Warn("audit-archive: "+auditArchiveIntervalEnv+" is not a valid positive duration; using the default (disable archival via "+auditArchiveSinkEnv+"=\"\", not the interval)", "value", raw, "default", defaultAuditArchiveInterval.String())
		}
	}
	if raw := strings.TrimSpace(getenv(auditArchiveSegmentEventsEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.segmentEvents = n
		} else {
			log.Warn("audit-archive: "+auditArchiveSegmentEventsEnv+" is not a positive integer; using the default", "value", raw, "default", audit.DefaultSegmentEvents)
		}
	}
	if raw := strings.TrimSpace(getenv(auditArchiveRetainDaysEnv)); raw != "" {
		// 0 is a legitimate explicit choice: no per-object retain-until, deferring
		// to the bucket's default Object Lock retention (ArchivePutOptions zero).
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= maxAuditArchiveRetainDays {
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
}

// newAuditArchiveLoop builds the loop from the resolved config. nil is valid only
// when no sink is configured (archival OFF — the shipped default, warned once so
// the gap is visible). A selected sink that cannot be built is invalid
// operator intent and aborts startup rather than silently dropping WORM evidence.
func newAuditArchiveLoop(cfg auditArchiveConfig, st store.Store, signer *audit.Signer, priors []ed25519.PublicKey, log *slog.Logger) (*auditArchiveLoop, error) {
	if cfg.sink == "" {
		log.Warn("audit-archive: no sink configured (" + auditArchiveSinkEnv + " is empty); continuous ledger archival is OFF — multi-year retention relies on `olivares audit archive export` out of band")
		return nil, nil
	}
	sink, err := buildAuditArchiveSink(cfg, log)
	if err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, nil
	}
	log.Info("audit-archive: continuous ledger archival wired",
		"sink", cfg.sink, "interval", cfg.interval.String(),
		"segment_events", cfg.segmentEvents, "retain_days", cfg.retainDays)
	return &auditArchiveLoop{
		st: st, sink: sink, signer: signer, priors: priors,
		interval: cfg.interval, segmentEvents: cfg.segmentEvents, retainDays: cfg.retainDays,
		clock: time.Now, log: log,
	}, nil
}

// buildAuditArchiveSink constructs the configured sink. Once a sink is selected,
// unknown kinds and construction failures are returned so boot fails closed;
// connector errors are not logged because they may embed endpoints or credentials.
func buildAuditArchiveSink(cfg auditArchiveConfig, log *slog.Logger) (audit.ArchiveSink, error) {
	switch cfg.sink {
	case "dir":
		if cfg.dir == "" {
			return nil, fmt.Errorf("%s=\"dir\" requires %s; refusing to start instead of silently disabling archival", auditArchiveSinkEnv, auditArchiveDirEnv)
		}
		sink, err := audit.NewDirSink(cfg.dir)
		if err != nil {
			return nil, fmt.Errorf("cannot create audit archive directory sink %q; refusing to start instead of silently disabling archival: %w", cfg.dir, err)
		}
		return sink, nil
	case "s3archive":
		if cfg.configPath == "" {
			log.Warn("audit-archive: sink \"s3archive\" needs " + auditArchiveConfigEnv + " (a JSON file with the connector settings); archival OFF")
			return nil, nil
		}
		var settings map[string]string
		if err := loadOperatorJSONConfig(auditArchiveConfigEnv, cfg.configPath, &settings); err != nil {
			return nil, err
		}
		out := s3archive.New()
		if err := out.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
			return nil, fmt.Errorf("%s=%q contains invalid s3archive settings; refusing to start instead of silently disabling archival: %w", auditArchiveConfigEnv, cfg.configPath, err)
		}
		return s3ArchiveSinkAdapter{out: out}, nil
	default:
		// an enterprise WORM sink kind (e.g. azureblobworm | gcsbucketlock)
		// resolves ONLY under -tags enterprise; the default build's enterpriseArchiveSink
		// returns (nil,false) (wire_noenterprise.go), so an unrecognized kind reaches the
		// same fail-closed startup error as every other unknown operator selection.
		if sink, ok := enterpriseArchiveSink(cfg.sink, cfg, log); ok {
			return sink, nil
		}
		return nil, fmt.Errorf("unknown %s=%q (want \"\"|dir|s3archive); refusing to start instead of silently disabling archival", auditArchiveSinkEnv, cfg.sink)
	}
}

// register schedules the drain on the runtime's own scheduler (before Start).
func (l *auditArchiveLoop) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(auditArchiveJobName, l.interval, false, l.runOnce)
}

// runOnce drains every tenant's pending chain. A per-tenant failure is logged
// and the remaining tenants still drain; the failed tenant resumes at its last
// ANCHORED segment next tick (last_seq never advanced past a failure).
func (l *auditArchiveLoop) runOnce(ctx context.Context) error {
	if !l.st.Leader().Active() {
		l.log.Debug("audit-archive skipped: this node is a standby, not the active writer")
		return nil
	}
	l.writeKeysOnce(ctx)
	tenants, err := l.tenants(ctx)
	if err != nil {
		l.log.Warn("audit-archive: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := l.drainTenant(ctx, t); err != nil {
			l.log.Warn("audit-archive: tenant drain stopped; will resume at the last anchored segment next tick", "tenant", t.String(), "err", err)
		}
	}
	return nil
}

// tenants enumerates the chains to drain: every org PLUS the reserved system
// tenant (auth/cross-tenant events need archival too), deduplicated — the
// exact CheckpointAll coverage.
func (l *auditArchiveLoop) tenants(ctx context.Context) ([]model.TenantID, error) {
	var tenants []model.TenantID
	if err := l.st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if o.TenantID.IsZero() {
				continue
			}
			tenants = append(tenants, o.TenantID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	tenants = append(tenants, model.SystemTenantID)
	seen := make(map[model.TenantID]bool, len(tenants))
	out := tenants[:0]
	for _, t := range tenants {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out, nil
}

// drainTenant exports everything pending for one tenant in segment_events
// chunks under the pending-boundary protocol: per segment, the boundary is
// persisted BEFORE the first Put (setPending), and AFTER both objects are
// durably written ONE tx anchors the segment in the chain, advances last_seq
// and clears pending (anchorAndAdvance) — atomic, so no crash can leave the
// anchor in the chain (moving the head) with last_seq still behind.
// ExportSegments snapshots the head up front, so the anchors this drain
// appends wait for the NEXT tick instead of being chased; a crash mid-segment
// leaves pending on record and the next tick rebuilds that exact boundary.
func (l *auditArchiveLoop) drainTenant(ctx context.Context, tenant model.TenantID) error {
	bk, err := l.bookkeeping(ctx, tenant)
	if err != nil {
		return err
	}
	opts := audit.ExportOptions{FromSeq: bk.lastSeq + 1, SegmentEvents: l.segmentEvents}
	if bk.hasPending {
		// The protocol clears pending atomically with the advance, so a pending
		// that does not resume exactly at last_seq+1 means the bookkeeping was
		// hand-edited or half-restored. Refuse to guess which value is right —
		// a guessed boundary could overlap already-sealed WORM objects.
		if bk.pendingFrom != bk.lastSeq+1 {
			return fmt.Errorf("audit-archive: pending boundary %d-%d does not resume last_seq %d: %s", bk.pendingFrom, bk.pendingTo, bk.lastSeq, archiveRecoveryHint)
		}
		opts.PendingToSeq = bk.pendingTo
	}
	if l.retainDays > 0 {
		opts.RetainUntil = l.clock().Add(time.Duration(l.retainDays) * 24 * time.Hour)
	}
	opts.BeforePut = func(m audit.SegmentManifest) error {
		return l.setPending(ctx, tenant, m.FromSeq, m.ToSeq)
	}
	rep, err := audit.ExportSegments(ctx, l.st, tenant, l.sink, opts, func(res audit.SegmentResult) error {
		return l.anchorAndAdvance(ctx, tenant, res)
	})
	if rep.Segments > 0 {
		l.log.Info("audit-archive: tenant chain drained",
			"tenant", tenant.String(), "segments", rep.Segments, "events", rep.Events,
			"from_seq", rep.FromSeq, "to_seq", rep.ToSeq)
	}
	return err
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

// bookkeeping reads the tenant's resume state from Org.Settings. A corrupt
// value is a loud error that SKIPS this tenant's drain — never a reset to 0:
// a silent restart-from-genesis would re-export the whole chain with
// boundaries that no longer match the already-sealed WORM objects (permanent
// verify failures) and would bury the corruption instead of surfacing it.
func (l *auditArchiveLoop) bookkeeping(ctx context.Context, tenant model.TenantID) (archiveBookkeeping, error) {
	var bk archiveBookkeeping
	err := l.st.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		var ok bool
		if bk.lastSeq, ok = parseArchiveLastSeq(org.Settings[archiveLastSeqSettingsKey]); !ok {
			return fmt.Errorf("audit-archive: corrupt %s=%v: %s", archiveLastSeqSettingsKey, org.Settings[archiveLastSeqSettingsKey], archiveRecoveryHint)
		}
		if raw, present := org.Settings[archivePendingSettingsKey]; present {
			if bk.pendingFrom, bk.pendingTo, ok = parseArchivePending(raw); !ok {
				return fmt.Errorf("audit-archive: corrupt %s=%v: %s", archivePendingSettingsKey, raw, archiveRecoveryHint)
			}
			bk.hasPending = true
		}
		return nil
	})
	return bk, err
}

// setPending persists the in-flight segment boundary BEFORE its first Put —
// read-modify-write of the FULL settings map inside one Mutate tx
// (SetOrgSettings REPLACES the map, so sibling keys must ride along).
func (l *auditArchiveLoop) setPending(ctx context.Context, tenant model.TenantID, fromSeq, toSeq int64) error {
	return l.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		settings[archivePendingSettingsKey] = strconv.FormatInt(fromSeq, 10) + "-" + strconv.FormatInt(toSeq, 10)
		_, err = sc.SetOrgSettings(ctx, settings)
		return err
	})
}

// anchorAndAdvance runs AFTER a segment's objects are durably written: ONE
// Mutate tx appends the in-chain anchor event, advances last_seq and clears
// the pending boundary. The atomicity is the point — when anchor and advance
// were separate txs, a crash between them left the anchor in the chain (the
// head moved) with last_seq still behind, so the retried tail segment
// recomputed a different boundary and orphaned the sealed objects. last_seq
// never regresses, and a stale toSeq is a full no-op (no duplicate anchor):
// that segment was already anchored and advanced past.
func (l *auditArchiveLoop) anchorAndAdvance(ctx context.Context, tenant model.TenantID, res audit.SegmentResult) error {
	return l.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		last, ok := parseArchiveLastSeq(org.Settings[archiveLastSeqSettingsKey])
		if !ok {
			return fmt.Errorf("audit-archive: corrupt %s=%v: %s", archiveLastSeqSettingsKey, org.Settings[archiveLastSeqSettingsKey], archiveRecoveryHint)
		}
		if last >= res.Manifest.ToSeq {
			return nil
		}
		if _, err := sc.Audit().Append(ctx, audit.SegmentAnchorDraft(res)); err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		settings[archiveLastSeqSettingsKey] = strconv.FormatInt(res.Manifest.ToSeq, 10)
		delete(settings, archivePendingSettingsKey)
		_, err = sc.SetOrgSettings(ctx, settings)
		return err
	})
}

// parseArchiveLastSeq decodes the stored resume point: ok=false when the value
// is present but unparseable or negative — the caller must treat that as
// corrupt bookkeeping and skip the drain (deny-closed), NEVER reset to 0.
// Absent (nil) is the only state that means "start at seq 1". Written as a
// decimal string (exact at any magnitude); a non-negative integral float64 or
// int is tolerated because Settings is a JSON round-tripped map.
func parseArchiveLastSeq(v any) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, true
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	case float64:
		if x < 0 || x != math.Trunc(x) || x >= math.MaxInt64 {
			return 0, false
		}
		return int64(x), true
	case int64:
		if x < 0 {
			return 0, false
		}
		return x, true
	case int:
		if x < 0 {
			return 0, false
		}
		return int64(x), true
	}
	return 0, false
}

// parseArchivePending decodes the "<from>-<to>" pending boundary: ok=false on
// any malformation (non-string, non-positive bounds, to < from) — the caller
// skips the drain loudly rather than guess a boundary that could overlap
// already-sealed WORM objects.
func parseArchivePending(v any) (fromSeq, toSeq int64, ok bool) {
	s, isStr := v.(string)
	if !isStr {
		return 0, 0, false
	}
	dash := strings.IndexByte(s, '-')
	if dash <= 0 {
		return 0, 0, false
	}
	from, ferr := strconv.ParseInt(s[:dash], 10, 64)
	to, terr := strconv.ParseInt(s[dash+1:], 10, 64)
	if ferr != nil || terr != nil || from < 1 || to < from {
		return 0, 0, false
	}
	return from, to, true
}

// writeKeysOnce writes the ADVISORY keys.json next to the segments, once per
// process (the sink is fresh exactly once). Advisory by definition — verifier
// pins REPLACE it (cmd_audit.go) — so a failure warns and archival continues;
// it is not retried (the keys embed created_at, and a WORM dir sink refuses a
// re-put with different bytes — one honest warning beats a daily one).
func (l *auditArchiveLoop) writeKeysOnce(ctx context.Context) {
	if l.keysAttempted {
		return
	}
	l.keysAttempted = true
	keys, err := l.archiveKeys(ctx)
	if err == nil {
		_, err = audit.WriteArchiveKeys(ctx, l.sink, keys)
	}
	if err != nil {
		l.log.Warn("audit-archive: could not write the advisory keys.json (archival continues; verifier pins replace it anyway)", "err", err)
		return
	}
	l.log.Info("audit-archive: advisory keys.json written", "key", audit.ArchiveKeysName)
}

// archiveKeys assembles the engine's own public keys (the cmd_audit.go export
// shape): per-event Ed25519 current + prior generations; checkpoints the same
// set plus the off-box KMS/HSM key when one is wired (its spec form).
func (l *auditArchiveLoop) archiveKeys(ctx context.Context) (audit.ArchiveKeys, error) {
	eventKeys := []string{base64.StdEncoding.EncodeToString(l.signer.PublicKey())}
	for _, p := range l.priors {
		eventKeys = append(eventKeys, base64.StdEncoding.EncodeToString(p))
	}
	checkpointKeys := append([]string(nil), eventKeys...) // on-box signs checkpoints too
	if ck := l.signer.CheckpointKey(); ck != nil {
		raw, err := ck.PublicKey(ctx)
		if err != nil {
			return audit.ArchiveKeys{}, err
		}
		spec := base64.StdEncoding.EncodeToString(raw)
		if ck.Algorithm() != audit.AlgEd25519 {
			spec = string(ck.Algorithm()) + ":" + spec
		}
		checkpointKeys = append(checkpointKeys, spec)
	}
	return audit.ArchiveKeys{
		EventPubKeys: eventKeys, CheckpointKeys: checkpointKeys,
		CreatedAt: model.NewTimestamp(l.clock()).String(),
	}, nil
}

// --- the s3archive → core/audit sink adapter ---------------------------------

// s3archivePutter is the archival face of connectors/s3archive.Output this
// adapter consumes (an interface so tests inject a fake; *s3archive.Output
// satisfies it).
type s3archivePutter interface {
	Put(ctx context.Context, key string, body []byte, opts s3archive.PutOptions) (s3archive.Receipt, error)
}

// s3ArchiveSinkAdapter maps core/audit's ArchiveSink onto the connector's Put
// face, field-for-field. The connector's Receipt carries no Location, so the
// adapter derives "bucket/key"; LockVerified passes through honestly (true
// only when the connector's verify-after-write HEAD confirmed the lock).
type s3ArchiveSinkAdapter struct{ out s3archivePutter }

var _ audit.ArchiveSink = s3ArchiveSinkAdapter{}

func (a s3ArchiveSinkAdapter) Put(ctx context.Context, key string, body []byte, opts audit.ArchivePutOptions) (audit.ArchiveReceipt, error) {
	rec, err := a.out.Put(ctx, key, body, s3archive.PutOptions{
		ContentSHA256: opts.ContentSHA256,
		RetainUntil:   opts.RetainUntil,
		LegalHold:     opts.LegalHold,
	})
	if err != nil {
		return audit.ArchiveReceipt{}, err
	}
	return audit.ArchiveReceipt{
		Location:     rec.Bucket + "/" + rec.Key,
		ETag:         rec.ETag,
		VersionID:    rec.VersionID,
		LockMode:     rec.LockMode,
		RetainUntil:  rec.RetainUntil,
		LockVerified: rec.LockVerified,
	}, nil
}

// s3archiveCapable is the connector's OPTIONAL post-hoc capability set: listing
// object versions and setting a legal hold on an already-written version. *s3archive.Output
// satisfies it. The adapter forwards to it when present, so the archival Put-only fake in
// the tests stays valid (the capability is type-asserted at call time, not required of
// every s3archivePutter).
type s3archiveCapable interface {
	ListObjectVersions(ctx context.Context, prefix string) ([]s3archive.ObjectVersion, error)
	SetObjectLegalHold(ctx context.Context, key, versionID string, on bool) (s3archive.Receipt, error)
}

// ListSegments implements audit.ArchiveLister: it lists the tenant's object versions and
// filters them to the segment-body grammar (audit.ParseSegmentKey skips manifests/keys.json),
// so the long-horizon orchestrator can address each segment version for a legal hold.
func (a s3ArchiveSinkAdapter) ListSegments(ctx context.Context, tenant string) ([]audit.SegmentRef, error) {
	capable, ok := a.out.(s3archiveCapable)
	if !ok {
		return nil, fmt.Errorf("audit-archive: s3archive sink does not support version listing")
	}
	vers, err := capable.ListObjectVersions(ctx, tenant+"/")
	if err != nil {
		return nil, err
	}
	var refs []audit.SegmentRef
	for _, v := range vers {
		t, from, to, ok := audit.ParseSegmentKey(v.Key)
		if !ok || t != tenant {
			continue // not a segment body for this tenant (manifest, keys.json, other tenant)
		}
		refs = append(refs, audit.SegmentRef{Key: v.Key, VersionID: v.VersionID, FromSeq: from, ToSeq: to, IsLatest: v.IsLatest})
	}
	return refs, nil
}

// SetObjectLegalHold implements audit.LegalHoldSetter: it places/lifts an S3 Object Lock
// legal hold on one segment version (fail-closed verify is the connector's).
func (a s3ArchiveSinkAdapter) SetObjectLegalHold(ctx context.Context, key, versionID string, on bool) (audit.ArchiveReceipt, error) {
	capable, ok := a.out.(s3archiveCapable)
	if !ok {
		return audit.ArchiveReceipt{}, fmt.Errorf("audit-archive: s3archive sink does not support legal-hold")
	}
	rec, err := capable.SetObjectLegalHold(ctx, key, versionID, on)
	if err != nil {
		return audit.ArchiveReceipt{}, err
	}
	return audit.ArchiveReceipt{
		Location:     rec.Bucket + "/" + rec.Key,
		ETag:         rec.ETag,
		VersionID:    rec.VersionID,
		LockMode:     rec.LockMode,
		RetainUntil:  rec.RetainUntil,
		LockVerified: rec.LockVerified,
	}, nil
}

// Compile-time proof the adapter exposes the post-hoc capabilities (the orchestrator
// type-asserts these on the live sink; DirSink does not satisfy them and degrades honestly).
var (
	_ audit.ArchiveLister   = s3ArchiveSinkAdapter{}
	_ audit.LegalHoldSetter = s3ArchiveSinkAdapter{}
)
