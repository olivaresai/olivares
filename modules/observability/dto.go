// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

// The DTOs in this file are the wire contract the admin view consumes
// (web/src/features/observability/types.ts — IngestionHealthResponse,
// TraceListItem, TraceDetail/TraceSpan — plus the sources/since
// and attestation shapes). Field names are part of the cross-agent contract;
// do not rename without flipping the web types in the same change.

// --- GET /ingestion-health ----------------------------------------------------

// ingestionStandardDTO is one interop standard's row: a verified upstream
// version pin plus the standard's TRUE operational status. UpstreamRepo/Ref are
// ADDITIVE and only present for standards whose authority moved off a versioned
// release (OTel GenAI: v1.41.1 = wire vocabulary label, semconv-genai main ref =
// verified live shape). Pointer fields keep "unknowable" distinguishable from a
// zero/false claim: a gated profile whose state the engine cannot
// observe carries opt_in_active=nil, and records_total appears ONLY when a count
// is soundly attributable to the standard.
type ingestionStandardDTO struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Direction string `json:"direction"` // "in" | "out"
	Maturity  string `json:"maturity"`  // development | ga | pre_1_0 | stable
	Version   string `json:"version"`
	// UpstreamRepo/UpstreamRef name the unversioned authority and commit/date
	// checked for standards whose version is a stable wire/table label, not the
	// live source of current mapping truth. Omitted for ordinary versioned specs.
	UpstreamRepo string `json:"upstream_repo,omitempty"`
	UpstreamRef  string `json:"upstream_ref,omitempty"`
	OptInGate    string `json:"opt_in_gate,omitempty"`
	// OptInActive is nil when the gate state is unknowable from inside the
	// engine (it is per-source connector config); true only on bus evidence.
	OptInActive *bool `json:"opt_in_active,omitempty"`
	// RecordsTotal is emitted ONLY when records are soundly attributable to
	// this standard on the bus — never an engine-wide proxy figure.
	RecordsTotal *int64 `json:"records_total,omitempty"`
	LastSeen     string `json:"last_seen,omitempty"` // RFC3339
	Status       string `json:"status"`              // active | available | opt_in_off | blocked
}

// ingestionSourceDTO is one live per-source counter row (ADDITIVE): the
// "por fuente" half of ingestion health, keyed by the bus Event.Source — the
// connector/module name the engine stamps on every published observation
// (core/runtime/host.go). Counts only, never payloads.
type ingestionSourceDTO struct {
	Name         string           `json:"name"`
	RecordsTotal int64            `json:"records_total"`
	FirstSeen    string           `json:"first_seen"` // RFC3339
	LastSeen     string           `json:"last_seen"`  // RFC3339
	Kinds        map[string]int64 `json:"kinds"`      // edge | cost | finding
	// Signals breaks the source's EDGE observations down by their
	// EdgeObservation.Source signal (otel | pg_audit | …); other kinds carry
	// no signal dimension, so the map covers edges only.
	Signals map[string]int64 `json:"signals,omitempty"`
}

// ingestionHealthDTO is the GET /ingestion-health response.
type ingestionHealthDTO struct {
	Standards []ingestionStandardDTO `json:"standards"`
	// EngineScope is ALWAYS true: the counters are process-global (the bus has
	// no per-tenant partition and /metrics is engine-wide by construction,
	// OBS-06) — the flag keeps the UI from presenting them as per-tenant.
	EngineScope bool `json:"engine_scope"`
	// Sources is sorted by name; [] (never null) when nothing was observed.
	Sources []ingestionSourceDTO `json:"sources"`
	// Since is the RFC3339 module-start instant: counters accumulate from here
	// and reset on restart, exactly like /metrics.
	Since string `json:"since"`
}

// --- GET /traces, GET /traces/{id} --------------------------------------------

// traceListItemDTO is one row of the trace list (web TraceListItem,
// types.ts:124-134). All figures are derived from the audit ledger's
// trace_id/span_id stamps — duration_ms is the LEDGER-EVENT WINDOW
// (max−min OccurredAt of the trace's ledger events), NOT an OTel span
// duration, and status is always "unset" because the ledger stores no OTel
// span status (never fabricate).
type traceListItemDTO struct {
	TraceID    string   `json:"trace_id"`
	RootName   string   `json:"root_name"`  // action of the earliest (lowest-seq) event
	StartedAt  string   `json:"started_at"` // RFC3339, min OccurredAt
	DurationMS int64    `json:"duration_ms"`
	SpanCount  int      `json:"span_count"`  // DISTINCT span_ids in the window
	AgentCount int      `json:"agent_count"` // DISTINCT actors in the window
	Status     string   `json:"status"`      // always "unset"
	Services   []string `json:"services"`    // always ["olivares"]
}

// traceSpanDTO is one span of the trace detail (web TraceSpan,
// types.ts:94-111): ONE row per distinct span_id, grouping every ledger event
// that engine span produced (presenting each event as its own "span" would
// duplicate span_ids and over-claim). parent_span_id is never emitted (the
// ledger does not store it), kind is the honest non-OTel label "ledger", and
// attributes carry ONLY synthesized ledger.* keys — never raw meta passthrough.
type traceSpanDTO struct {
	SpanID     string            `json:"span_id"`
	Name       string            `json:"name"`
	Service    string            `json:"service"`
	Kind       string            `json:"kind"`     // always "ledger"
	StartMS    int64             `json:"start_ms"` // offset from trace start
	DurationMS int64             `json:"duration_ms"`
	Status     string            `json:"status"`               // always "unset"
	Actor      string            `json:"actor,omitempty"`      // actor identity from the earliest event (e.g. "user:admin")
	ActorKind  string            `json:"actor_kind,omitempty"` // user | agent | connector | system
	EntityRef  string            `json:"entity_ref,omitempty"` // "<target_kind>:<target_id>" of the earliest event
	Attributes map[string]string `json:"attributes,omitempty"`
}

// traceDetailDTO is the GET /traces/{id} response (web TraceDetail,
// types.ts:114-121). duration_ms is the ledger-event window of the whole trace.
type traceDetailDTO struct {
	TraceID    string         `json:"trace_id"`
	StartedAt  string         `json:"started_at,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	Spans      []traceSpanDTO `json:"spans"`
}

// --- GET /attestation -----------------------------------------------------------

// attestationDTO is the GET /attestation response: the MEASURED truth about the
// running binary, the measured ABSENCE of any release/signature, and the
// DECLARED-only pipeline. The three blocks never blur (ARCHITECTURE.md).
type attestationDTO struct {
	Binary     binaryDTO   `json:"binary"`
	Release    releaseDTO  `json:"release"`
	Pipeline   pipelineDTO `json:"pipeline"`
	CapturedAt string      `json:"captured_at"` // RFC3339 (module clock)
}

// fipsDTO is the FIPS 140-3 MODE of this process, self-verified in-process
// (SCP-09 wording: mode, never a validation claim — cmd/olivares/main.go:59-70).
type fipsDTO struct {
	Enabled bool   `json:"enabled"`
	Version string `json:"version,omitempty"`
}

// mainModDTO identifies the main module from debug.ReadBuildInfo. Under a
// go.work workspace build the version is "(devel)" with no sum — reported
// verbatim, never upgraded to a release claim.
type mainModDTO struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// sumsDTO summarizes the dependency integrity data baked into the binary:
// external deps carry h1: module sums; workspace modules do not (go.work).
type sumsDTO struct {
	ExternalDeps int    `json:"external_deps"`
	SumsPresent  bool   `json:"sums_present"`
	Note         string `json:"note"`
}

// vcsDTO reports whether the Go toolchain stamped vcs.* build settings.
// Empirically verified for this repo: it does NOT under go.work workspace
// mode, even with -buildvcs=true — the only revision signal is the ldflags
// commit and the "-dirty" suffix git describe bakes into version (Taskfile.yml:88).
type vcsDTO struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// binaryDTO is the measured block. Version is reported verbatim: a "-dirty"
// suffix (from git describe) is the only dirty signal that exists — it is
// never converted into a boolean claim.
type binaryDTO struct {
	Version      string     `json:"version"`    // ldflags; "dev" default
	Commit       string     `json:"commit"`     // ldflags; "none" default
	BuildDate    string     `json:"build_date"` // ldflags; "unknown" default
	GoVersion    string     `json:"go_version"`
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	FIPS140      fipsDTO    `json:"fips140"`
	SelfSHA256   string     `json:"self_sha256,omitempty"` // stream-SHA256 of os.Executable(); omitted on error
	SelfHashNote string     `json:"self_hash_note,omitempty"`
	MainModule   mainModDTO `json:"main_module"`
	ModuleSums   sumsDTO    `json:"module_sums"`
	VCSStamp     vcsDTO     `json:"vcs_stamp"`
	Status       string     `json:"status"` // always "measured"
}

// tlogDTO is the transparency-log posture: the native verifier never claims
// Rekor inclusion (core/secure/modelsign), so verified is constant false.
type tlogDTO struct {
	Verified bool   `json:"verified"`
	Note     string `json:"note"`
}

// provenanceDTO is the EPISTEMIC CLASS of the release verdict beside it: what kind
// of evidence the verdict rests on, and whether anything authenticated it.
//
// kind is the constant "self_declared" and attested is constant false, and that is
// NOT the defect PR #730 fixed — it is the opposite of it. published/status were
// constant because a fact the binary CAN read (its own link-time stamps) was never
// read; these two are constant because the fact they describe is not reachable from
// a running process AT ALL: every anchor it holds was chosen by whoever linked it,
// and the detached signature over checksums.txt is not carried inside the binary.
// The shape is the one this DTO already uses for exactly this situation —
// tlogDTO.Verified is a constant false with a note saying why (no Rekor claim is
// ever fabricated). If an out-of-band pin ever gives a running binary something it
// did not choose, this is the field that stops being constant.
type provenanceDTO struct {
	Kind     string `json:"kind"`     // "self_declared"
	Attested bool   `json:"attested"` // false: nothing outside the linker vouched for the facts below
	Note     string `json:"note"`
}

// releaseDTO is the MEASURED release block: whether the binary answering this
// request carries the release build's link-time identity — an orderable main.version
// stamp and an embedded OTA anchor (see the block comment above measuredRelease). It
// used to be a measured ABSENCE with no inputs at all, which meant a real release
// reported itself unpublished. verifier_available is true BY CONSTRUCTION (the
// binary links core/secure/modelsign.VerifyAttestation — see the compile-time
// reference in attestation.go), so the capability claim is compile-time-proven,
// not asserted.
//
// SEALED vs ATTESTED. published/status/reason are the SEAL: what the link-time facts
// say. provenance is the ATTESTATION dimension, and it says nothing authenticated
// them. Read together they claim exactly what a running process can support and no
// more — a binary forged with two -ldflags values reaches published:true, and says so
// about itself in provenance.note on the same response.
type releaseDTO struct {
	Published bool `json:"published"`
	// Status is "not_published" for every build that is not a release — which is
	// every source build, and was every build in existence when this block was
	// written — and "published" once both link-time facts are present.
	Status string `json:"status"`
	// Reason names WHICH fact decided the verdict; it is not boilerplate.
	Reason string `json:"reason"`
	// Provenance travels in BOTH polarities: the class of evidence does not change
	// with the verdict, and a consumer that reads published without it is reading
	// half the sentence.
	Provenance provenanceDTO `json:"provenance"`
	// SignatureStatus is "not_verified" in BOTH states and never flips with
	// Published: the detached signature over checksums.txt is not inside the
	// binary, so a running process cannot check its own. Only SignatureReason
	// differs, because "no release artifacts exist" is false on a published one.
	SignatureStatus   string  `json:"signature_status"`
	SignatureReason   string  `json:"signature_reason"`
	VerifierAvailable bool    `json:"verifier_available"`
	TransparencyLog   tlogDTO `json:"transparency_log"`
}

// pipelineDTO is the DECLARED block: the release workflows exist in the source
// tree the binary was built from; the running process cannot observe
// repository or CI state, so this never claims more than "declared".
type pipelineDTO struct {
	Workflows []string `json:"workflows"`
	Status    string   `json:"status"` // "declared"
	Note      string   `json:"note"`
}
