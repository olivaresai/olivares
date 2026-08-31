// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file defines the three integration SEAMS the knowledge module depends on
// but does not own, each expressed in the module's own terms so the module stays
// decoupled from its neighbors' packages (the same way deploy defined ApprovalGate
// and governance defined a roster port). The composition root injects real
// adapters; until it exists (the honest Fase C caveat), each port defaults to a
// SAFE behavior so an un-wired knowledge plane governs conservatively and never
// exfiltrates: the guard denies restricted content, the embedder runs LOCALLY
// (zero egress), and there is no external vector backend.

// ----------------------------------------------------------------------------
// Embedder — the model-backed embeddings seam.
// ----------------------------------------------------------------------------

// Embedder turns text into vectors. The module ORCHESTRATES embedding (chunk →
// embed → index); it never calls a model provider directly — that governance and
// cost is (the composition root wires a model-backed adapter). The
// default is a LOCAL, zero-egress embedder (vector.go).
//
// AllowsEgress is the RED LINE gate: it reports whether Embed sends the text out
// of the customer's perimeter (a hosted provider does; a local model does not).
// The module refuses to ingest a local_only / residency-locked KB with an
// egressing embedder (kb.go, ingest.go) — chunk text never leaves when the policy
// forbids it (docs/SECURITY-HARDENING.md).
type Embedder interface {
	// Embed returns one vector per input text and the model reference used. All
	// returned vectors have length Dim(). It honors ctx. An error fails the embed
	// step (the chunks stay pending) — never a silent empty vector.
	Embed(ctx context.Context, tenant model.TenantID, texts []string) (vectors [][]float32, modelRef string, err error)
	// Dim is the embedding dimension this embedder produces (stable per embedder).
	Dim() int
	// AllowsEgress reports whether Embed transmits the text outside the perimeter.
	// A local model returns false (the data does not leave); a hosted provider
	// returns true. The module gates ingest on this against the KB's embed policy.
	AllowsEgress() bool
	// ModelRef is the stable model reference recorded on the KB / chunks so a
	// reader always knows which embedder produced the vectors (e.g. "local-hash").
	ModelRef() string
}

// ----------------------------------------------------------------------------
// RetrievalGuard — the governance seam for retrieval.
// ----------------------------------------------------------------------------

// Grants is the requesting principal/agent's resolved retrieval authority for one
// knowledge base: which groups it belongs to (for chunk ACL), its classification
// clearance, its data-residency region, and whether it may read the KB at all.
// It is resolved ONCE per query, OUTSIDE the store transaction, and applied as a
// chunk-level filter BEFORE ranking (retrieval.go) — never post-hoc in the UI.
type Grants struct {
	// Allowed is the KB-level decision: false denies the whole retrieval.
	Allowed bool
	// Groups are the identity's group/role references, intersected with each
	// chunk's ACL. A chunk with a non-empty ACL is visible only if it shares a
	// group; an unrestricted chunk (empty ACL or "anyone") is always visible.
	Groups []string
	// Clearance is the identity's classification clearance label ("public",
	// "internal", "confidential", "secret"). A chunk is visible only if its
	// classification rank is <= this clearance's rank (unknown labels fail closed).
	Clearance string
	// Region is the identity's data-residency region. If the KB declares a region
	// and it differs, retrieval is denied (no cross-border read).
	Region string
	// Reason is a short, non-sensitive explanation surfaced on a denial.
	Reason string
	// LabelClearances are the namespaced external labels the identity is cleared
	// for (e.g. "purview:confidential", "purview:*"). A chunk whose document has
	// external labels is visible only if at least one label matches a clearance.
	// Empty = no external label clearance declared; if the document has labels, deny.
	LabelClearances []string
}

// RetrievalGuard resolves a requesting principal/agent's retrieval grants for a
// knowledge base. The real adapter bridges to the permissions/identity and
// (the identity roster/region); this module APPLIES the grants, it does not decide
// policy. An error is treated as DENY (fail closed, like ABAC) — never a
// degraded allow.
type RetrievalGuard interface {
	// Resolve returns the grants for (principal acting as agentRef) over the named
	// KB. An error means deny. It is called outside the store transaction (it may
	// make an external call).
	Resolve(ctx context.Context, tenant model.TenantID, principalActor, agentRef, kbName string) (Grants, error)
}

// denyGuard is the deny-closed default until a real guard is wired. It permits the
// KB read but grants NO groups and only "public" clearance and no region — so any
// classified or ACL-restricted chunk is filtered out, while public/unrestricted
// content stays retrievable (the module is usable without but governs
// conservatively). Start() warns once. It never errors (it makes no external call).
type denyGuard struct{}

func (denyGuard) Resolve(_ context.Context, _ model.TenantID, _, _, _ string) (Grants, error) {
	return Grants{Allowed: true, Groups: nil, Clearance: classPublic, Region: "", Reason: "no retrieval guard wired; only public, unrestricted content is retrievable"}, nil
}

// RetrievalScopeGate is the source-scoping pre-flight, a SEPARATE axis from the
// RetrievalGuard: the guard resolves WHAT sensitivity/groups an identity may see; this
// gate resolves WHICH workspace/agent-group may reach the knowledge base at all (the
// KB→scope binding, decided by the grant engine + containment, model B). Keeping
// them orthogonal avoids conflating "who/sensitivity" with "which scope" (DLP
// and ACL are unchanged). It is resolved ONCE per query, OUTSIDE the store
// transaction (it consults the scoped-grant engine, which opens its own view), and is
// DENY-CLOSED: an error or a false verdict denies the whole retrieval. An UNBOUND KB
// (no binding) is allowed (back-compat). The actor scope VALUES are read from the
// stored agent row named by agentRef — the SAME caller-declared, agent-centric model
// this module's RetrievalGuard already uses for clearance/ACL (its workspace is the
// store's, not the body's).
type RetrievalScopeGate interface {
	// Allowed reports whether the agent (agentRef) acting under principal may retrieve
	// from the knowledge base kbRef. An error means deny (fail closed).
	Allowed(ctx context.Context, tenant model.TenantID, principal auth.Principal, agentRef, kbRef string) (allowed bool, reason string, err error)
}

// allowRetrievalScope is the unwired default: with no source-scoping resolver wired no
// KB is ever out of scope (back-compat — the knowledge plane behaves as before). The
// composition root replaces it with the sourcescope-backed gate.
type allowRetrievalScope struct{}

func (allowRetrievalScope) Allowed(context.Context, model.TenantID, auth.Principal, string, string) (bool, string, error) {
	return true, "source scoping not configured", nil
}

// ----------------------------------------------------------------------------
// SensitivityClassifier — the PII/sensitivity discovery seam.
// ----------------------------------------------------------------------------

// SensitivityHit is one sensitivity finding in a text: a class (the DLP-facing
// label, e.g. "pii.financial", "secret.credential"), the named deterministic rule
// that fired (explainability — forensics can always trace a label to a rule), how
// many times it matched, and the rule's severity. It NEVER carries a matched
// value (docs/SECURITY-HARDENING.md).
type SensitivityHit struct {
	Class    string
	Rule     string
	Count    int
	Severity sdkmodel.Severity
}

// SensitivityClassifier classifies one text into sensitivity hits. The production
// adapter (composition root) wraps the security module's deterministic catalog
// — default-on, zero-egress, reproducible; a semantic classifier may be
// wired behind the same seam as an OPT-IN (it must keep the contract: classes +
// rules + counts, never raw values, and Version must change when its behavior
// does). An error fails the scan honestly — never a silent "no PII found".
//
// Un-wired default: nil. Discovery endpoints refuse (409) and ingest persists NO
// label rows — under an enabled DLP policy that unscanned content is DENIED at
// retrieval (deny-closed), so an un-wired classifier degrades conservatively,
// never permissively. Start() warns once.
type SensitivityClassifier interface {
	// Classify returns the sensitivity hits for one text, deterministic for a
	// given Version. It is called OUTSIDE store transactions (it may be slow on
	// large texts) and must never return raw matched values.
	Classify(text string) ([]SensitivityHit, error)
	// Version identifies the classifier catalog; it is recorded on scan evidence
	// so a result is reproducible (same content + same version = same hits).
	Version() string
}

// ----------------------------------------------------------------------------
// HoldGate — the legal-hold seam over the compliance module.
// ----------------------------------------------------------------------------

// Data-class ids this module's destructive endpoints check, from the
// retention/legal-hold registry. They travel as plain string literals — the cross-module contract, like the ext kinds — because knowledge
// never imports compliance.
const (
	holdClassKnowledgeContent = "knowledge.content"
	holdClassAgentMemory      = "agent.memory"
)

// Hold subject kinds this module destroys (the §4 vocabulary). user and
// session join with the session store: a user/session-scoped memory entry is additionally
// gated on a subject hold naming its user or session — the §4 matching is exact
// string equality on (kind, ref), so the open vocabulary extends without a
// compliance-side change (a hold nobody placed simply never matches).
const (
	holdSubjectKB       = "kb"
	holdSubjectAgent    = "agent"
	holdSubjectDocument = "document"
	holdSubjectUser     = "user"
	holdSubjectSession  = "session"
)

// maxBlockingHolds caps how many DISTINCT blocking holds a 423 body lists when a
// multi-subject check (every document of a KB / of an ingest batch) trips many
// holds: enough to act on (each id resolves via GET /v1/m/compliance/holds/{id}),
// bounded so the error envelope cannot balloon with the corpus.
const maxBlockingHolds = 20

// HoldRef identifies one blocking legal hold: enough for the 423 body (and a
// follow-up GET /v1/m/compliance/holds/{id}) — ids, matter and scope only, never
// content. It mirrors compliance's HoldRef in this module's own terms; the
// composition-root adapter maps between the two.
type HoldRef struct {
	ID        string `json:"id"`
	MatterRef string `json:"matter_ref"`
	ScopeKind string `json:"scope_kind"`
}

// HoldGate answers whether an active legal hold covers a (subject, data-class)
// pair — the hold-gate, adapted by the composition root over
// compliance.CheckHold (tenant-wide + class + subject evaluated in ONE call, the
// single §4 matching rule). subjectKind/subjectRef may be empty (class-only
// check) and dataClass may be empty (subject-only check). The consumer treats an
// error as DENY (fail closed): destruction must not proceed while the hold
// ledger is unreadable — over-preserving is the safe direction.
type HoldGate interface {
	Check(ctx context.Context, tenant model.TenantID, subjectKind, subjectRef, dataClass string) (held bool, holds []HoldRef, err error)
}

// enforceHold runs the wired hold-gate for one (subject, class) pair BEFORE a
// destructive transaction (the gate calls into another module — outside the
// store tx, the RetrievalGuard discipline). It reports true when the caller must
// STOP, the deny response already written: an active hold renders 423 Locked,
// code legal_hold, listing the blocking holds; a gate ERROR renders 503 (fail
// closed — a hold that cannot be ruled out blocks the purge). nil gate = OPEN:
// unwired means the feature is absent (the enforcement posture — the
// shipped binary ALWAYS wires it).
func (m *Module) enforceHold(ctx context.Context, w http.ResponseWriter, tenant model.TenantID, subjectKind, subjectRef, dataClass string) bool {
	if m.holdGate == nil {
		return false
	}
	held, holds, err := m.holdGate.Check(ctx, tenant, subjectKind, subjectRef, dataClass)
	if err != nil {
		m.errorf("knowledge: legal-hold check failed; denying deletion (fail closed)",
			"subject_kind", subjectKind, "data_class", dataClass, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("legal-hold check unavailable; deletion denied (fail closed)"))
		return true
	}
	if !held {
		return false
	}
	writeJSON(w, http.StatusLocked, legalHoldBody(holds))
	return true
}

// enforceDocumentHolds runs the wired hold-gate once per document id — subject
// ("document", id) + class knowledge.content — BEFORE a path that would destroy
// those documents' content (the KB delete cascade, a re-ingest chunk
// replacement). Like enforceHold it runs OUTSIDE any store tx and reports true
// when the caller must STOP, the deny response already written: any held
// document renders 423 Locked listing the blocking holds (deduped across
// documents, capped at maxBlockingHolds); a gate ERROR at ANY point denies the
// WHOLE operation with 503 (fail closed) — every check concludes before the
// caller deletes or embeds anything, so a mid-loop error destroys nothing.
// nil gate = OPEN (unwired means the feature is absent, the §5 posture).
// Per-document point checks are deliberate: these are admin-rare/replace paths
// where simple and correct beats batched.
func (m *Module) enforceDocumentHolds(ctx context.Context, w http.ResponseWriter, tenant model.TenantID, docIDs []string) bool {
	if m.holdGate == nil || len(docIDs) == 0 {
		return false
	}
	heldAny := false
	seen := map[string]bool{}
	var blocking []HoldRef
	for _, docID := range docIDs {
		held, holds, err := m.holdGate.Check(ctx, tenant, holdSubjectDocument, docID, holdClassKnowledgeContent)
		if err != nil {
			m.errorf("knowledge: legal-hold check failed; denying deletion (fail closed)",
				"subject_kind", holdSubjectDocument, "data_class", holdClassKnowledgeContent, "err", err)
			writeJSON(w, http.StatusServiceUnavailable, errorBody("legal-hold check unavailable; deletion denied (fail closed)"))
			return true
		}
		if !held {
			continue
		}
		heldAny = true
		for _, h := range holds {
			if seen[h.ID] {
				continue
			}
			seen[h.ID] = true
			if len(blocking) < maxBlockingHolds {
				blocking = append(blocking, h)
			}
		}
	}
	if !heldAny {
		return false
	}
	writeJSON(w, http.StatusLocked, legalHoldBody(blocking))
	return true
}

// legalHoldBody is the 423 envelope: errorBody's shape extended with the
// machine-readable code and the blocking holds (ids/matter/scope only).
// adopts this exact rendering for a held erasure.
func legalHoldBody(holds []HoldRef) map[string]any {
	if holds == nil {
		holds = []HoldRef{}
	}
	return map[string]any{"error": map[string]any{
		"code":    "legal_hold",
		"message": "blocked by an active legal hold",
		"holds":   holds,
	}}
}

// ----------------------------------------------------------------------------
// VectorIndex — the pluggable vector-ranking seam.
// ----------------------------------------------------------------------------

// VectorCandidate is one chunk's id + vector, ALREADY governance-filtered, handed
// to the index for ranking. Because the module pre-filters the candidate set, the
// classification/ACL/residency gate is guaranteed to run BEFORE ranking and an
// external backend never sees a chunk the identity may not retrieve.
type VectorCandidate struct {
	ChunkID string
	Vector  []float32
}

// ScoredChunk is one ranked result: a chunk id and its similarity score.
type ScoredChunk struct {
	ChunkID string
	Score   float64
}

// VectorIndex ranks governance-filtered candidates against a query vector. The
// DEFAULT (vector.go cosineIndex) ranks in-process with exact cosine — correct,
// zero extra services, the right self-host/air-gap default; its honest limit is a
// linear scan (fine to ~10^5 chunks/tenant). The composition root may wire an
// external backend (pgvector on Postgres at scale, Qdrant/Vectorize/Pinecone)
// behind this same interface; such a backend MUST honor the supplied candidate set
// so governance still precedes ranking. Swapping the index never changes the
// governance contract.
type VectorIndex interface {
	// Rank returns up to topK candidates ordered by descending similarity to query.
	// It honors ctx and must not reorder governance (candidates are pre-filtered).
	Rank(ctx context.Context, query []float32, candidates []VectorCandidate, topK int) ([]ScoredChunk, error)
}

// ----------------------------------------------------------------------------
// RetrievalContentScanner — Untrusted-data scan at the return point.
// ----------------------------------------------------------------------------

// RetrievalScanVerdict is the scanner's decision for one retrieval result.
type RetrievalScanVerdict struct {
	// Blocked is true when the content must be WITHHELD from the caller (a
	// high-severity injection marker or an unscanned-denied chunk).
	Blocked bool
	// Markers are the injection marker ids found (non-sensitive rule names,
	// never matched content). Empty when clean.
	Markers []string
	// Unscanned is true when the content could not be classified (opaque/
	// binary) and the deny-closed unscanned policy applies.
	Unscanned bool
	// Reason is a short, non-sensitive explanation for the audit/lineage trail.
	Reason string
}

// RetrievalContentScanner scans retrieved content as UNTRUSTED DATA ("tool
// return values are data, not instructions" —) before it reaches the
// caller. The CORE implementation runs textscan injection markers and the
// deny-closed unscanned posture; the ENTERPRISE depth adds the three
// deterministic detectors (prompt-injection / exfiltration / unsafe-action)
// via the enterprise/contentfirewall add-on. Nil = no scan (the un-wired
// default; the composition root ALWAYS wires the core scanner).
//
// Honesty: the scan is verified-deployed at the retrieval return point,
// never "impossible to bypass" — content retrieved outside this pipeline
// (direct store access, a different API) is not scanned. The heuristics
// raise the cost of an attack and leave evidence; they do not prove the
// content is safe. Zero fabricated detection benchmarks.
type RetrievalContentScanner interface {
	// ScanChunk inspects one retrieved chunk's text and returns a verdict.
	// It is called per-result AFTER ranking, BEFORE the results are returned
	// to the caller. An error is treated as BLOCKED (fail closed).
	ScanChunk(ctx context.Context, text, sourceKind, sourceRef string) RetrievalScanVerdict
}

// ----------------------------------------------------------------------------
// Redactor — the MINIMIZATION seam (B-02).
// ----------------------------------------------------------------------------

// Redactor removes recognized secret/PII values from a text before that text is
// chunked, embedded, hashed for storage or persisted. It is the WRITE-path
// counterpart of SensitivityClassifier: the classifier says what a text contains,
// the redactor makes it stop containing it.
//
// It exists because those two jobs had drifted apart. This module's built-in
// scrub removed ten credential shapes and exactly ONE personal-data shape
// (email), while the security module's catalog recognized eighteen more —
// IBAN, Luhn-valid cards, US SSN, ES NIF/NIE, IPs, MACs, wallets. The catalog ran
// AFTER the scrub and only LABELED, so those values were detected precisely
// because they had survived, and then persisted in the chunk text in the clear.
// The product's own contract says secrets and PII never reach the store or an
// embedder; for the PII half that was not true.
//
// Un-wired default: the module's built-in shapes (redact.go). That degrades to
// exactly the historical behavior rather than to no redaction at all — an
// unwired minimizer must never be a WEAKER guarantee than the one it replaced.
type Redactor interface {
	// Redact returns the text with every recognized value replaced by a labeled
	// marker, plus what was removed (class, rule, count — never the value). It
	// runs on the ingest write path, on prompt templates and on agent memory.
	Redact(text string) (string, []SensitivityHit)
	// Version identifies the rule catalog, so a redaction is reproducible and a
	// catalog change is visible in evidence.
	Version() string
}
