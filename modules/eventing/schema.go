// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"embed"
	"io/fs"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// migrationsFS carries the module's file migrations (per-engine subdirs), the
// additive-DDL path for schema the descriptor cannot retrofit onto an existing
// table (applyModuleTables tracks created tables by name and never alters
// them). First use: the eventing_event_id_uniq index on upgraded estates.
//
//go:embed migrations/postgres/*.sql migrations/sqlite/*.sql
var migrationsFS embed.FS

// Owned entity kinds and their physical tables. Tables are "eventing_<entity>";
// kinds are "eventing.<entity>". The longest, eventing_subscription (21 chars),
// is within the 40-char module-table cap.
const (
	subscriptionKind  model.Kind = "eventing.subscription"
	subscriptionTable            = "eventing_subscription"
	eventKind         model.Kind = "eventing.event"
	eventTable                   = "eventing_event"
	deliveryKind      model.Kind = "eventing.delivery"
	deliveryTable                = "eventing_delivery"
	cursorKind        model.Kind = "eventing.cursor"
	cursorTable                  = "eventing_cursor"
	// SIEM-sink profile side table (1:1 with a subscription). 25 chars, within
	// the 40-char module-table cap.
	subscriptionSinkKind  model.Kind = "eventing.subscription_sink"
	subscriptionSinkTable            = "eventing_subscription_sink"
	// Unit G compatibility record: the exact destinations a tenant already had
	// when the egress destination control was installed here, and the per-tenant
	// marker proving the recording ran. Both APPEND-ONLY: they are evidence of what
	// was true at a moment, and an actuation is approved against them.
	egressExceptionKind  model.Kind = "eventing.egress_exception"
	egressExceptionTable            = "eventing_egress_exception"
	egressSeedKind       model.Kind = "eventing.egress_seed"
	egressSeedTable                 = "eventing_egress_seed"
	// Unit H: the per-mutation proof that the writer carries the egress gate. MUTABLE by
	// design — the fence's trigger CONSUMES a row, so it cannot be append-only.
	writerAttestKind  model.Kind = "eventing.writer_attest"
	writerAttestTable            = "eventing_writer_attest"
)

// Unit H — the writer-fence columns.
const (
	// colWriterNonce is the per-mutation nonce a writer stamps on a governed row. NULLABLE, so
	// the engine's additive reconcile adds it to an already-created table and a row written
	// before this unit reads as NULL rather than breaking.
	//
	// It lives on the ROW, not only in a side table, and that is the correction an adversarial
	// review of this unit's design produced. A token that merely EXISTS somewhere authorizes
	// whoever finds it: a committed orphan authorized an old write forever on SQLite, and
	// consuming it on use still authorized ONE. Bound to the row, an old binary cannot supply it
	// (Create emits only its own descriptor's fields) and cannot reuse the stored one (Update
	// preserves a value whose token was already consumed).
	colWriterNonce = "writer_nonce"

	colAttestNonce      = "nonce"
	colAttestCapability = "capability"
	colAttestGeneration = "fence_generation"
)

// writerAttestDescriptor is the per-mutation proof.
//
// It is tenant-scoped like every module entity, and that is load-bearing rather than incidental:
// the fence's trigger runs in the writer's own tenant scope, so row-level security is what stops
// one tenant's proof from authorizing another's mutation. Measured on PostgreSQL with FORCE RLS and
// a NOBYPASSRLS application role: tenant B cannot see tenant A's attestation.
//
// It is NOT append-only. The fence consumes a row when it accepts a mutation, so the proof is spent
// rather than accumulated.
//
// Unconsumed rows are swept by the retention pass (maintenance.go) with a cutoff of their own. That
// is a SWEEP, not an expiry, and the difference is worth stating because an earlier version of this
// comment called it a bound: the trigger does not check a proof's age, so a proof stays VALID until
// a sweep actually runs — and the sweep runs from the maintenance pump, which a deployment can
// disable. What genuinely bounds a proof's usefulness is the fence GENERATION, which every arming
// moves.
func writerAttestDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:  writerAttestKind,
		Table: writerAttestTable,
		Fields: []model.FieldSpec{
			{Name: colAttestNonce, Kind: model.KindText},
			{Name: colAttestCapability, Kind: model.KindInt},
			// The fence generation the writer OBSERVED. Carried so the proof cannot be made
			// against a disposition read before the last decision: a node whose cached read is
			// stale attests an old generation and is refused, which is the correct outcome —
			// it retries. Without it the proof would only mean "code able to write this ran",
			// which is weaker than this unit claimed before the contrast.
			{Name: colAttestGeneration, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// One proof per nonce per tenant. A repeated nonce is either a bug or a replay, and
			// either way it must not silently authorize a second mutation.
			Name:    "eventing_writer_attest_uniq",
			Columns: []string{model.ColTenantID, colAttestNonce},
			Unique:  true,
		}},
	}
}

// Unit G compatibility-record columns.
const (
	colExcSubRef = "subscription_ref" // the subscription whose destination this was
	colExcKind   = "authority_kind"   // canonical_v1 | legacy_raw_v1 (see authorityKind)
	colExcDigest = "authority_digest" // domain-separated sha256 of the authority key — the match key
	colExcScheme = "scheme"
	colExcHost   = "canonical_host"
	colExcPort   = "effective_port"
	colExcBatch  = "seed_batch" // the seeding pass that wrote it

	colSeedBatch    = "seed_batch"
	colSeedSubs     = "subscription_count"
	colSeedExcs     = "exception_count"
	colSeedUnparsed = "unparsed_count" // endpoints the strict parser cannot canonicalize
	colSeedDigest   = "seed_digest"    // fingerprint of the exact recorded set
)

// egressExceptionDescriptor and egressSeedDescriptor are the compatibility record.
//
// The exception row stores the AUTHORITY and the subscription identity, and nothing
// else. Not the URL: a path or a query can carry tenant data, and an exception must
// record where a tenant sent events rather than what it sent. Not the resolved
// addresses: a collector's address legitimately changes, and pinning one would turn a
// compatibility record into an outage the first time DNS moved.
//
// RETENTION, stated rather than left implicit. These rows are append-only, and tenant
// deletion deliberately skips append-only module tables (sqlstore/system.go: a DELETE
// would hit the immutability trigger and abort the whole drop) — so a collector
// hostname recorded here outlives an ordinary tenant deletion. There is no
// engine-level retention job over append-only module tables; the "separate retention
// path" that comment refers to is per-module. VERIFIED, not assumed.
//
// It is the same property the subscription revision ledger already has, whose
// snapshots carry the endpoint verbatim, so this adds a class of data the estate
// already retained rather than a new one. And it is the price of these rows being the
// evidence a durable decision was approved against: a record that could be erased is
// not evidence that anything was true.
//
// The MATCH is by digest alone, which is the property that keeps the choice open: a
// deployment that wants the plaintext gone can stop populating scheme/host/port
// without breaking a single decision — only the operator's diff report reads them, and
// it is admin-tier. What is NOT claimed is that these hostnames are covered by a
// right-to-erasure flow; they are infrastructure metadata, and nothing here enumerates
// them for erasure.
func egressExceptionDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:       egressExceptionKind,
		Table:      egressExceptionTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colExcSubRef, Kind: model.KindText, Indexed: true},
			{Name: colExcKind, Kind: model.KindText},
			{Name: colExcDigest, Kind: model.KindText},
			{Name: colExcScheme, Kind: model.KindText},
			{Name: colExcHost, Kind: model.KindText},
			{Name: colExcPort, Kind: model.KindInt},
			{Name: colExcBatch, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One row per (subscription, authority). The uniqueness is what makes a
			// concurrent second seeding pass collide and roll back instead of doubling the
			// record an actuation is counted from.
			Name:    "eventing_egress_exception_uniq",
			Columns: []string{model.ColTenantID, colExcSubRef, colExcDigest},
			Unique:  true,
		}},
	}
}

// egressSeedDescriptor is the per-tenant proof that the recording ran.
//
// It is REQUIRED even for a tenant with no subscriptions, because the absence of
// exception rows cannot distinguish "nothing to grandfather" from "never recorded" —
// and those two demand opposite answers when a policy is authored. Unique on the
// tenant alone, so the line is drawn exactly once and can never be redrawn: a second
// pass would move it, and the whole value of the record is that it did not move.
func egressSeedDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:       egressSeedKind,
		Table:      egressSeedTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colSeedBatch, Kind: model.KindText},
			{Name: colSeedSubs, Kind: model.KindInt},
			{Name: colSeedExcs, Kind: model.KindInt},
			{Name: colSeedUnparsed, Kind: model.KindInt},
			{Name: colSeedDigest, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "eventing_egress_seed_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	}
}

// subscription columns — a tenant's external event subscription. MUTABLE
// lifecycle. The signing secret is persisted ONLY through the SecretSealer seam
// (col_secret_sealed holds the sealed form); the cleartext is generated
// server-side, returned exactly once, and never stored or logged (docs/SECURITY-HARDENING.md).
// col_secret_hint is a non-secret SHA-256 fingerprint prefix for display.
const (
	colSubName        = "name"
	colSubEnabled     = "enabled"
	colSubTypes       = "event_types"   // csv of cataloged event.Type values (validated, never empty)
	colSubSources     = "match_sources" // csv of emitter source names; "" = any
	colSubEndpoint    = "endpoint"      // consumer URL (validated; never a credential carrier)
	colSubSecret      = "secret_sealed" // sealed HMAC signing secret (SecretSealer output)
	colSubSecretHint  = "secret_hint"   // non-secret fingerprint prefix for display
	colSubRole        = "role"          // authorization role for the per-event RBAC filter
	colSubDescription = "description"
	colSubOwnerActor  = "owner_actor"
	colSubOwnerActorK = "owner_actor_kind"
	// per-subscription authentication and retry policy. HMAC signing
	// (colSubSecret) is always applied (integrity). These fields add an OPTIONAL
	// additional auth header (bearer/basic/custom) and per-subscription retry
	// tuning. auth_type "none" = HMAC only (the default, unchanged behavior).
	colSubAuthType       = "auth_type"                // none | bearer | basic | header
	colSubAuthValSealed  = "auth_value_sealed"        // sealed credential (SecretSealer output)
	colSubAuthValHint    = "auth_value_hint"          // non-secret fingerprint prefix for display
	colSubAuthHeaderName = "auth_header_name"         // custom header name (for auth_type "header")
	colSubMaxAttempts    = "max_attempts"             // 0 = module default (len(retrySchedule)+1)
	colSubInitInterval   = "initial_interval_seconds" // 0 = module default (30s)
)

// subscription_sink columns — the SIEM-sink profile, a 1:1 OPTIONAL side
// table keyed by the subscription. It is a SEPARATE table (not new columns on
// eventing_subscription) because a module's already-created table is frozen — the
// engine never ALTERs it (applyModuleTables creates by name and never alters), and
// SQLite cannot ADD COLUMN idempotently — whereas a wholly new table is created
// cleanly on both fresh and upgraded estates. A subscription with NO sink row is
// delivered exactly as before (the generic, HMAC-signed wireEvent webhook); a sink
// row re-shapes the SAME captured event into a control tower's native dialect +
// envelope over the SAME delivery engine. The sealed credential is the SIEM token/
// key/bearer (distinct from the HMAC secret), sealed under its own AAD purpose.
const (
	colSinkSubRef = "subscription_ref" // the owning eventing_subscription id (unique 1:1)
	colSinkKind   = "sink_kind"        // https | splunk_hec | sentinel_dcr | datadog | newrelic
	colSinkFormat = "sink_format"      // "" (per-sink default) or a siemwire.EventingSinkFormats token (siem kinds only)
	colSinkCred   = "sink_cred_sealed" // sealed sink credential (token/key/bearer); own AAD purpose
	colSinkOpts   = "sink_opts"        // JSON of non-secret routing (index, sourcetype, dce, dcr_immutable_id, stream, ...)
	colSinkHint   = "sink_cred_hint"   // non-secret fingerprint prefix of the sink credential, for display
)

// event columns — the durable per-tenant event log (the replay buffer). Rows
// are written once at capture and NEVER updated by code; the table is not
// AppendOnly only so the retention sweep can prune it (PruneExpired), which a
// DB-level immutability guard would forbid. The base created_at column is the
// pruning predicate.
const (
	colEvSeq        = "seq"         // per-tenant monotonic sequence (the replay cursor)
	colEvEventID    = "event_id"    // the bus event id — the consumer idempotency key
	colEvType       = "event_type"  // the event.Type discriminator
	colEvSource     = "source"      // emitting component name
	colEvOccurredAt = "occurred_at" // event.Time (the fact's clock)
	colEvPayload    = "payload"     // JSON of the typed payload (minimal-data by bus contract)
)

// cursor column — the per-tenant seq allocator: ONE row per tenant (unique
// (tenant_id) index) whose last_seq only ever increases. Separate from the log
// so pruning the whole log can never regress the sequence; concurrent captures
// collide on this row's optimistic version (the same ErrConflict retry path
// the unique (tenant_id, seq) backstop covers).
const colCurLastSeq = "last_seq"

// delivery columns — one MUTABLE row per (event, subscription): the durable
// delivery state machine. queued → delivering → delivered | dead | denied.
// Claiming is by optimistic version (store.ErrConflict loses the race); a
// delivering row whose last_attempt_at is older than the stale-claim window is
// reclaimable (crash recovery — the at-least-once half). last_status is a short
// NON-SENSITIVE outcome class (e.g. "http_503", "timeout"), never response
// content or an error string that could embed the endpoint URL.
const (
	colDelSubRef     = "subscription_ref"
	colDelEventRef   = "event_ref"  // eventing_event row id
	colDelEventID    = "event_id"   // denormalized bus event id (headers without a join)
	colDelEventSeq   = "event_seq"  // denormalized seq (cursor queries without a join)
	colDelEventType  = "event_type" // denormalized type (filtering without a join)
	colDelStatus     = "status"     // queued|delivering|delivered|dead|denied
	colDelOrigin     = "origin"     // live|replay
	colDelAttempts   = "attempts"
	colDelNextAt     = "next_attempt_at"
	colDelLastAt     = "last_attempt_at" // zero until the first claim
	colDelLastStatus = "last_status"     // short outcome class
)

// Delivery statuses. queued covers both "never attempted" and "awaiting retry"
// (attempts distinguishes them) so the due scan is a single ANDed filter — the
// store's closed query language has no OR.
const (
	statusQueued = "queued"
	// statusParked is NOT a stored status. It is the sender's way of saying "the control
	// plane could not decide, so give the attempt back" — processOne turns it into a queued
	// row with the attempt restored (parkOwned). Keeping it out of the column vocabulary is
	// deliberate: a reader of a delivery row should see the same three states it always saw,
	// and learn WHY from the outcome token.
	statusParked     = "parked"
	statusDelivering = "delivering"
	statusDelivered  = "delivered"
	statusDead       = "dead"   // attempts exhausted or terminal response — the DLQ
	statusDenied     = "denied" // the per-event RBAC filter refused (terminal)
)

// Delivery origins.
const (
	originLive   = "live"
	originReplay = "replay"
)

// RegisterSchema declares the module's three owned entities. The engine creates
// the tables, injects the base columns and attaches the tenant guards (S02 §7); a module cannot opt out of isolation. Unique indexes lead with
// model.ColTenantID so they neither couple tenants nor leak existence.
//
// Minimal data (docs/SECURITY-HARDENING.md): the event log stores the bus payload, which is
// minimal-data BY the bus contract (facts, refs and hashes — never raw
// payloads/secrets/PII); the subscription stores the signing secret only in its
// sealed form; a delivery row stores routing metadata and outcome classes only.
// Subscriptions are not descriptor-Audited: their privileged mutations append a
// SEMANTIC self-audit attributed to the real principal in the same transaction
// (helpers.go auditEvent, docs/SECURITY-HARDENING.md).
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	// the append-only subscription revision ledger (change history +
	// restore). Snapshot = the redacted DTO projection, never a credential.
	if err := reg.Register(revisionDescriptor()); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  subscriptionKind,
		Table: subscriptionTable,
		Fields: []model.FieldSpec{
			{Name: colSubName, Kind: model.KindText, Indexed: true},
			{Name: colSubEnabled, Kind: model.KindBool, Indexed: true},
			{Name: colSubTypes, Kind: model.KindText},
			{Name: colSubSources, Kind: model.KindText, Nullable: true},
			{Name: colSubEndpoint, Kind: model.KindText},
			{Name: colSubSecret, Kind: model.KindText},
			{Name: colSubSecretHint, Kind: model.KindText},
			{Name: colSubRole, Kind: model.KindText},
			{Name: colSubDescription, Kind: model.KindText, Nullable: true},
			{Name: colSubOwnerActor, Kind: model.KindText},
			{Name: colSubOwnerActorK, Kind: model.KindText},
			// Unit H. Nullable: the engine's additive reconcile adds it to an existing
			// table, and a row written before this unit reads as NULL.
			{Name: colWriterNonce, Kind: model.KindText, Nullable: true},
			// auth headers + retry policy (fresh installs; upgraded estates
			// get these via migration 0002).
			{Name: colSubAuthType, Kind: model.KindText, Nullable: true},
			{Name: colSubAuthValSealed, Kind: model.KindText, Nullable: true},
			{Name: colSubAuthValHint, Kind: model.KindText, Nullable: true},
			{Name: colSubAuthHeaderName, Kind: model.KindText, Nullable: true},
			{Name: colSubMaxAttempts, Kind: model.KindInt, Nullable: true},
			{Name: colSubInitInterval, Kind: model.KindInt, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "eventing_subscription_uniq",
			Columns: []string{model.ColTenantID, colSubName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  eventKind,
		Table: eventTable,
		Fields: []model.FieldSpec{
			// seq is NOT also field-Indexed: the unique index below covers it.
			{Name: colEvSeq, Kind: model.KindInt},
			{Name: colEvEventID, Kind: model.KindText},
			{Name: colEvType, Kind: model.KindText, Indexed: true},
			{Name: colEvSource, Kind: model.KindText},
			{Name: colEvOccurredAt, Kind: model.KindTimestamp},
			{Name: colEvPayload, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{
			{
				// The allocator's belt: even if two captures somehow carried the
				// same seq past the cursor row, the insert collides here as
				// store.ErrConflict and the loser re-reads.
				Name:    "eventing_event_seq_uniq",
				Columns: []string{model.ColTenantID, colEvSeq},
				Unique:  true,
			},
			{
				// Exactly-once CAPTURE per bus event: with the NATS bridge,
				// the same event can reach two nodes' captures in the ≤2s leader
				// failover overlap (the write gate is an advisory tick, not a
				// fence). The loser's insert collides here; captureOnce treats the
				// existing row as already-captured. Existing deployments get this
				// index through the module file migration (migrations/), the first
				// real use of that seam.
				Name:    "eventing_event_id_uniq",
				Columns: []string{model.ColTenantID, colEvEventID},
				Unique:  true,
			},
			{
				// The retention sweep's pruning predicate.
				Name:    "eventing_event_prune",
				Columns: []string{model.ColTenantID, model.ColCreatedAt},
			},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  deliveryKind,
		Table: deliveryTable,
		Fields: []model.FieldSpec{
			{Name: colDelSubRef, Kind: model.KindUUID, Indexed: true},
			{Name: colDelEventRef, Kind: model.KindUUID},
			{Name: colDelEventID, Kind: model.KindText},
			{Name: colDelEventSeq, Kind: model.KindInt},
			{Name: colDelEventType, Kind: model.KindText, Indexed: true},
			{Name: colDelStatus, Kind: model.KindText, Indexed: true},
			{Name: colDelOrigin, Kind: model.KindText},
			{Name: colDelAttempts, Kind: model.KindInt},
			{Name: colDelNextAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colDelLastAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colDelLastStatus, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// The retention sweep's pruning predicate (status is separately
			// field-Indexed for the due/DLQ scans).
			Name:    "eventing_delivery_prune",
			Columns: []string{model.ColTenantID, model.ColCreatedAt},
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  cursorKind,
		Table: cursorTable,
		Fields: []model.FieldSpec{
			{Name: colCurLastSeq, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// One allocator row per tenant.
			Name:    "eventing_cursor_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// SIEM-sink profile (1:1 with a subscription). A NEW table — created from
	// this descriptor on both fresh AND upgraded estates (applyModuleTables creates
	// any not-yet-existing module table) — so the sink feature needs no column
	// ALTER of the frozen eventing_subscription table. The sealed credential and a
	// non-secret hint are stored exactly like the subscription's HMAC secret
	// (docs/SECURITY-HARDENING.md): the cleartext is never persisted or logged.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  subscriptionSinkKind,
		Table: subscriptionSinkTable,
		Fields: []model.FieldSpec{
			{Name: colSinkSubRef, Kind: model.KindUUID},
			{Name: colSinkKind, Kind: model.KindText},
			{Name: colSinkFormat, Kind: model.KindText, Nullable: true},
			{Name: colSinkCred, Kind: model.KindText, Nullable: true},
			{Name: colSinkOpts, Kind: model.KindText, Nullable: true},
			{Name: colSinkHint, Kind: model.KindText, Nullable: true},
			// Unit H — the sink profile decides the rendered URL, so it is a destination
			// surface and carries the same proof as the endpoint.
			{Name: colWriterNonce, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One sink profile per subscription (the 1:1 anchor; leads with the
			// tenant id so it neither couples tenants nor leaks existence).
			Name:    "eventing_subscription_sink_uniq",
			Columns: []string{model.ColTenantID, colSinkSubRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// File migrations: secondary DDL for already-created tables (see
	// migrationsFS). Fresh installs create the same index from the descriptor;
	// IF NOT EXISTS makes the two paths converge. The loader reads per-engine
	// dirs at the FS ROOT ("postgres/", "sqlite/") and treats an absent dir as
	// "nothing to apply" — so the embed prefix MUST be stripped here or the
	// migration silently never runs (pinned by TestFileMigrationApplies).
	// Unit G: the compatibility record, and the staged control whose disposition
	// the engine classifies from whether eventing_subscription already existed. The
	// control is declared LAST on purpose — nothing here depends on the order, and
	// reading it after the tables makes the witness obvious.
	if err := reg.Register(egressExceptionDescriptor()); err != nil {
		return err
	}
	if err := reg.Register(egressSeedDescriptor()); err != nil {
		return err
	}
	if err := reg.Register(writerAttestDescriptor()); err != nil {
		return err
	}
	if err := reg.RolloutControl(egressRolloutControl()); err != nil {
		return err
	}
	// Unit H: the writer fence's own control. A SEPARATE key from the destination
	// control's, because it asks a different question about a different epoch — see
	// EgressWriterFenceControlKey. Registering a second control needs no engine change: the
	// seam unit G added takes as many as a module declares, and classifies each against its
	// own witness observation under the same migration lock.
	if err := reg.RolloutControl(egressWriterFenceControl()); err != nil {
		return err
	}

	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	return reg.Migrations(Namespace, sub)
}
