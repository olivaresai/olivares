// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.knowledge"

// Namespace is the module's store and API namespace: its entities are
// "knowledge.<entity>" and its routes mount under /v1/m/knowledge/.
const Namespace = "knowledge"

// The module's permissions, granted to the built-in roles by verb tier (viewer→
// read, editor→write, admin/owner→admin). Reading KBs/documents/lineage/prompts/
// memory/policies and GOVERNED RETRIEVAL are read-tier; declaring KBs, ingesting,
// versioning prompts and writing memory/policies are write-tier; deleting/purging
// (the destructive governance of the data plane) is admin-tier.
const (
	permKBRead        auth.Permission = "knowledge:kb:read"
	permKBWrite       auth.Permission = "knowledge:kb:write"
	permKBAdmin       auth.Permission = "knowledge:kb:admin"
	permRetrievalRead auth.Permission = "knowledge:retrieval:read"
	permLineageRead   auth.Permission = "knowledge:lineage:read"
	permPromptRead    auth.Permission = "knowledge:prompt:read"
	permPromptWrite   auth.Permission = "knowledge:prompt:write"
	permMemoryRead    auth.Permission = "knowledge:memory:read"
	permMemoryWrite   auth.Permission = "knowledge:memory:write"
	permMemoryAdmin   auth.Permission = "knowledge:memory:admin"
	permContextRead   auth.Permission = "knowledge:context:read"
	permContextWrite  auth.Permission = "knowledge:context:write"
	// PII discovery + DLP: running a scan is write-tier (it labels the
	// corpus), reading labels/scan evidence is read-tier, and the DLP egress
	// policy is admin-tier (it opens/closes the perimeter).
	permScanRead  auth.Permission = "knowledge:scan:read"
	permScanWrite auth.Permission = "knowledge:scan:write"
	permDLPRead   auth.Permission = "knowledge:dlp:read"
	permDLPAdmin  auth.Permission = "knowledge:dlp:admin"
	// data product catalog, data contracts and enforcement evidence.
	permDataProductRead  auth.Permission = "knowledge:data_product:read"
	permDataProductWrite auth.Permission = "knowledge:data_product:write"
	permDataProductAdmin auth.Permission = "knowledge:data_product:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithEmbedder wires the model-backed embedder. Without it the module uses
// the LOCAL, zero-egress LocalHashEmbedder (non-semantic, air-gap-safe default).
func WithEmbedder(e Embedder) Option { return func(m *Module) { m.embedder = e } }

// WithRetrievalGuard wires the governance guard. Without it only public,
// unrestricted content is retrievable (deny-closed for everything classified/ACL'd).
func WithRetrievalGuard(g RetrievalGuard) Option { return func(m *Module) { m.guard = g } }

// WithRetrievalScopeGate wires the source-scope pre-flight (the KB→workspace/
// agent-group binding). Without it no KB is ever out of scope (back-compat; see
// allowRetrievalScope). It is orthogonal to the RetrievalGuard (sensitivity/ACL).
func WithRetrievalScopeGate(g RetrievalScopeGate) Option {
	return func(m *Module) { m.scopeGate = g }
}

// WithVectorIndex wires an external vector backend (pgvector/Qdrant/…). Without it
// the module ranks governance-filtered candidates in-process with exact cosine.
func WithVectorIndex(v VectorIndex) Option { return func(m *Module) { m.index = v } }

// WithMemoryPortabilityKeys wires the governed-memory portability keypair (a
// DEDICATED Ed25519 key, domain-separated from the license/OTA/DDIL keys). priv
// signs the export manifest; pub verifies an import manifest before any entry is
// persisted. Passing only pub makes the deployment import-only (verify but not
// mint); passing neither leaves both portability endpoints failing closed (501).
func WithMemoryPortabilityKeys(priv ed25519.PrivateKey, pub ed25519.PublicKey) Option {
	return func(m *Module) { m.memPortSignKey, m.memPortVerifyKey = priv, pub }
}

// WithSensitivityClassifier wires the PII/sensitivity classifier (the
// composition root wraps the security module's deterministic catalog —
// default-on, zero-egress; a semantic classifier is an opt-in behind the same
// seam). Without it discovery endpoints refuse (409) and ingest writes no label
// rows (and DELETES a stale one when re-ingesting) — under an enabled DLP
// policy that unscanned content is DENIED at retrieval (deny-closed), never
// silently allowed.
func WithSensitivityClassifier(c SensitivityClassifier) Option {
	return func(m *Module) { m.classifier = c }
}

// WithRedactor wires the B-02 write-path minimizer (the composition root
// wraps the security module's deterministic catalog, the SAME catalog the
// classifier uses, so what the product detects is what it removes). Without it
// the module falls back to its own built-in shapes, which redact credentials and
// email only — the historical behavior, never anything weaker.
func WithRedactor(r Redactor) Option {
	return func(m *Module) { m.redactor = r }
}

// WithHoldGate wires the legal-hold gate (the composition root adapts the
// compliance module's CheckHold). Default nil = OPEN: unwired means the feature
// is absent — the enforcement posture; the shipped binary ALWAYS
// wires it. Once wired, an active hold OR a gate error DENIES the destructive
// endpoints (423 legal_hold / 503, fail closed — docs/contracts).
func WithHoldGate(g HoldGate) Option { return func(m *Module) { m.holdGate = g } }

// WithRetrievalContentScanner wires the untrusted-data scanner: each
// retrieved chunk's text is scanned AFTER ranking and BEFORE the results are
// returned to the caller. The CORE scanner (coreRetrievalScanner) runs textscan
// injection markers (HIGH severity = deny-closed block). Default nil = no scan
// (back-compat); the composition root ALWAYS wires the core scanner.
func WithRetrievalContentScanner(s RetrievalContentScanner) Option {
	return func(m *Module) { m.contentScanner = s }
}

// WithSource registers a named contentsource.Source (a data connector) the module
// can drive from POST /kbs/{id}/ingest {"source":"<name>"}. The composition root
// registers the real gdrive/confluence/notion/sharepoint/s3content connectors;
// tests register a fake source. A source whose Kind() is not ClassDocument is
// rejected at ingest (the boundary).
func WithSource(name string, src contentsource.Source) Option {
	return func(m *Module) {
		if m.sources == nil {
			m.sources = map[string]contentsource.Source{}
		}
		m.sources[name] = src
	}
}

// AddSource registers a content source AFTER construction. The composition root
// uses it to wire a document source whose secret-bearing config (a credential_ref)
// can only be resolved and opened once the engine's secret store exists —
// which is after this module is built but BEFORE it starts. It must be called
// before Start (no ingest is in flight), so it needs no lock; a source that fails
// to resolve/open is simply never added (the same "only openable sources are
// wired" contract WithSource had at boot).
func (m *Module) AddSource(name string, src contentsource.Source) {
	if m.sources == nil {
		m.sources = map[string]contentsource.Source{}
	}
	m.sources[name] = src
}

// Module is module VIII — data, knowledge & context: the GOVERNED DATA PLANE for
// what agents know and use. See doc.go for the bounded context, the RED LINE
// (customer data is governed, NEVER exfiltrated), and the deny-/local-closed
// defaults of its composition-root seams.
type Module struct {
	log            *slog.Logger
	data           api.ModuleData
	host           sdk.Host
	clock          model.Clock
	embedder       Embedder
	guard          RetrievalGuard
	scopeGate      RetrievalScopeGate
	index          VectorIndex
	classifier     SensitivityClassifier
	redactor       Redactor
	holdGate       HoldGate
	contentScanner RetrievalContentScanner
	sources        map[string]contentsource.Source

	// Governed-memory portability keys (anti-lock-in export/import). Both are
	// a DEDICATED Ed25519 keypair, domain-separated from the license/OTA/DDIL keys
	// via sigbundle.TagMemoryPortability. Export needs the private half to sign the
	// manifest; import needs the public half to verify it before persisting. When a
	// half is unwired the corresponding endpoint fails CLOSED (501), never emits an
	// unsigned bundle or imports an unverifiable one — the composition root wires
	// them from the same key custody as the other engine signing keys.
	memPortSignKey   ed25519.PrivateKey
	memPortVerifyKey ed25519.PublicKey

	// tamperSeen dedups memory tamper EVIDENCE (audit event + finding)
	// once per (tenant, kind, id) per process — see reportMemoryTamper. The
	// deny on the read path is never deduplicated.
	tamperMu   sync.Mutex
	tamperSeen map[string]struct{}
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam. RegisterSchema (the engine-side
// SchemaProvider seam) is structural and verified by the runtime at boot/test.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a knowledge module with safe defaults: a local zero-egress embedder,
// a deny-closed retrieval guard, and the in-process exact vector index. The
// composition root replaces the embedder/guard with real adapters via
// options; a vector backend is optional.
func New(opts ...Option) *Module {
	m := &Module{
		clock:     model.SystemClock{},
		embedder:  LocalHashEmbedder{},
		guard:     denyGuard{},
		scopeGate: allowRetrievalScope{},
		index:     cosineIndex{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Data, knowledge & context",
		Description: "The governed data plane: knowledge bases + RAG over a pluggable vector index, read-only data connectors, retrieval governed by identity/classification/residency, append-only data lineage proving the customer's data never leaves the perimeter, a versioned prompt registry, and governed agent memory and context/compaction policies.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init keeps the host for publishing findings (a secret redacted on ingest, a
// residency/egress violation). The module is request-driven; it holds no bus
// subscription in v1. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	return nil
}

// Start has no background work — a module cannot enumerate tenants, so
// embedding-recovery and memory purge are tenant-scoped endpoints (reindex/purge)
// plus lazy expiry on read, never a cross-tenant sweep. Start warns once per seam
// that is un-wired or running in a degraded mode, so a silently non-semantic or
// ungoverned knowledge plane is VISIBLE rather than a surprise.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("knowledge: started without a data handle; knowledge bases, documents and memory will not persist")
	}
	if _, ok := m.guard.(denyGuard); ok {
		m.log.Warn("knowledge: no retrieval guard wired; only PUBLIC, unrestricted content is retrievable — classified/ACL'd content is denied by default")
	}
	if _, ok := m.embedder.(LocalHashEmbedder); ok {
		// Retrieval answers lexically via the local embedder. Same fact the composition
		// root already reports (cmd/olivares/claude_inference.go) — the DUPLICATION is
		// named here and left for its own change; the LEVEL is corrected now.
		m.log.Info("knowledge: no model-backed embedder wired; using the LOCAL, zero-egress feature-hash embedder — retrieval is lexical, NOT semantic (embed_model=local-hash)")
	}
	if m.classifier == nil {
		m.log.Warn("knowledge: no sensitivity classifier wired; PII discovery refuses, ingest writes no sensitivity labels, and an enabled DLP policy DENIES the unlabeled content at retrieval (deny-closed)")
	}
	if m.holdGate == nil {
		m.log.Warn("knowledge: no legal-hold gate wired; KB delete and memory delete/purge run WITHOUT a legal-hold check — destruction under an active hold would not be blocked")
	}
	return nil
}

// Stop is a no-op (no background work, no live subscription); idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace returns the module's namespace; it roots routes at /v1/m/knowledge/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require so the built-in
// roles grant them by verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permKBRead, permKBWrite, permKBAdmin, permRetrievalRead, permLineageRead,
		permPromptRead, permPromptWrite, permMemoryRead, permMemoryWrite, permMemoryAdmin,
		permContextRead, permContextWrite,
		permScanRead, permScanWrite, permDLPRead, permDLPAdmin,
		permDataProductRead, permDataProductWrite, permDataProductAdmin,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs, and
// pins the data handle to the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Knowledge bases (the governed collections + their classification/residency/
	// embed policy). Create/update validate the embed-policy↔egress gate (the red
	// line). Delete cascades documents+chunks and is admin-tier.
	reg.Handle("GET", "/kbs", permKBRead, m.handleListKBs)
	reg.Handle("POST", "/kbs", permKBWrite, m.handleCreateKB)
	reg.Handle("GET", "/kbs/{id}", permKBRead, m.handleGetKB)
	reg.Handle("PUT", "/kbs/{id}", permKBWrite, m.handleUpdateKB)
	reg.Handle("DELETE", "/kbs/{id}", permKBAdmin, m.handleDeleteKB)

	// Ingest (chunk → REDACT → embed[outside tx] → index) and the documents view.
	reg.Handle("POST", "/kbs/{id}/ingest", permKBWrite, m.handleIngest)
	reg.Handle("POST", "/kbs/{id}/reindex", permKBWrite, m.handleReindex)
	reg.Handle("POST", "/kbs/{id}/sync", permKBWrite, m.handleSync)
	reg.Handle("GET", "/kbs/{id}/documents", permKBRead, m.handleListDocuments)
	reg.Handle("GET", "/documents/{id}", permKBRead, m.handleGetDocument)

	// GOVERNED retrieval (identity/classification/residency filter BEFORE ranking)
	// + the append-only lineage it writes.
	reg.Handle("POST", "/kbs/{id}/query", permRetrievalRead, m.handleQuery)
	reg.Handle("GET", "/lineage", permLineageRead, m.handleListLineage)
	reg.Handle("GET", "/lineage/{id}", permLineageRead, m.handleGetLineage)

	// Prompt registry (versioned, immutable revisions, rollback = move the pointer).
	reg.Handle("GET", "/prompts", permPromptRead, m.handleListPrompts)
	reg.Handle("POST", "/prompts", permPromptWrite, m.handleCreatePrompt)
	reg.Handle("GET", "/prompts/{id}", permPromptRead, m.handleGetPrompt)
	reg.Handle("POST", "/prompts/{id}/revisions", permPromptWrite, m.handleAddRevision)
	reg.Handle("GET", "/prompts/{id}/revisions", permPromptRead, m.handleListRevisions)
	reg.Handle("GET", "/prompts/{id}/revisions/{rev}", permPromptRead, m.handleGetRevision)
	reg.Handle("POST", "/prompts/{id}/rollback", permPromptWrite, m.handleRollback)

	// Governed agent memory (retention/purge + quota).: reads are scoped to
	// the DECLARED user/session context (deny-closed isolation); the admin tier
	// gets the cross-scope governance view and the ledger-anchored integrity
	// verification (the verify-before-trust contract Dreams consumes).
	reg.Handle("GET", "/memory", permMemoryRead, m.handleListMemory)
	reg.Handle("POST", "/memory", permMemoryWrite, m.handlePutMemory)
	// Data portability (anti-lock-in): export is a READ (same clearance-filtered
	// predicate as the list, per-caller — never the admin cross-scope view), import is
	// a governed WRITE (same fail-closed write path as PUT, entry by entry).
	reg.Handle("GET", "/memory/export", permMemoryRead, m.handleExportMemory)
	reg.Handle("POST", "/memory/import", permMemoryWrite, m.handleImportMemory)
	reg.Handle("GET", "/memory/all", permMemoryAdmin, m.handleListAllMemory)
	reg.Handle("POST", "/memory/verify", permMemoryAdmin, m.handleVerifyMemory)
	reg.Handle("GET", "/memory/{id}", permMemoryRead, m.handleGetMemory)
	reg.Handle("DELETE", "/memory/{id}", permMemoryWrite, m.handleDeleteMemory)
	reg.Handle("POST", "/memory/purge", permMemoryAdmin, m.handlePurgeMemory)

	// Context/compaction policies (governed data orchestration drives them).
	reg.Handle("GET", "/context-policies", permContextRead, m.handleListContextPolicies)
	reg.Handle("POST", "/context-policies", permContextWrite, m.handlePutContextPolicy)

	// PII discovery (at rest over a KB; across a document store WITHOUT
	// ingesting), the sensitivity labels + append-only scan evidence, and the DLP
	// egress policy the retrieval/ingest gates enforce deny-closed.
	reg.Handle("POST", "/kbs/{id}/scan", permScanWrite, m.handleScanKB)
	reg.Handle("POST", "/sources/{name}/scan", permScanWrite, m.handleScanSource)
	reg.Handle("GET", "/labels", permScanRead, m.handleListLabels)
	reg.Handle("GET", "/scans", permScanRead, m.handleListScans)
	reg.Handle("GET", "/dlp/rules", permDLPRead, m.handleListDLPRules)
	reg.Handle("PUT", "/dlp/rules", permDLPAdmin, m.handlePutDLPRule)
	reg.Handle("DELETE", "/dlp/rules/{id}", permDLPAdmin, m.handleDeleteDLPRule)

	// data product catalog + versioned data contracts + enforcement
	// events. Data products govern KB ingest and retrieval once published.
	reg.Handle("GET", "/data-products", permDataProductRead, m.handleListDataProducts)
	reg.Handle("POST", "/data-products", permDataProductWrite, m.handleCreateDataProduct)
	reg.Handle("GET", "/data-products/{id}", permDataProductRead, m.handleGetDataProduct)
	reg.Handle("PUT", "/data-products/{id}", permDataProductWrite, m.handleUpdateDataProduct)
	reg.Handle("DELETE", "/data-products/{id}", permDataProductAdmin, m.handleDeleteDataProduct)
	reg.Handle("POST", "/data-products/{id}/publish", permDataProductWrite, m.handlePublishDataProduct)
	reg.Handle("POST", "/data-products/{id}/deprecate", permDataProductWrite, m.handleDeprecateDataProduct)
	reg.Handle("POST", "/data-products/{id}/archive", permDataProductAdmin, m.handleArchiveDataProduct)
	reg.Handle("GET", "/data-products/{id}/health", permDataProductRead, m.handleDataProductHealth)
	reg.Handle("POST", "/data-products/{id}/validate", permDataProductWrite, m.handleValidateDataProduct)
	reg.Handle("GET", "/data-products/{id}/contracts", permDataProductRead, m.handleListDataContracts)
	reg.Handle("POST", "/data-products/{id}/contracts", permDataProductWrite, m.handleCreateDataContract)
	reg.Handle("GET", "/data-products/{id}/contracts/active", permDataProductRead, m.handleGetActiveDataContract)
	reg.Handle("GET", "/data-products/{id}/contracts/{ver}", permDataProductRead, m.handleGetDataContract)
	reg.Handle("GET", "/data-products/{id}/events", permDataProductRead, m.handleListDataProductEvents)
}

// debugf / errorf log if a logger is set. errorf is used where a best-effort
// secondary write (a finding, a lineage row) fails: the primary outcome still
// returns, but a lost governance/evidence record is surfaced, never swallowed
// (docs/SECURITY-HARDENING.md — never a silent gap).
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

func (m *Module) errorf(msg string, args ...any) {
	if m.log != nil {
		m.log.Error(msg, args...)
	}
}
