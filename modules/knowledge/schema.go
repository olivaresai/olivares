// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables. All stay within the 40-char
// module-table cap: the longest, knowledge_sensitivity_label, is 27 chars.
//
// The module is the GOVERNED DATA PLANE for module VIII. Some tables are
// APPEND-ONLY because their integrity is the product's compliance/forensics
// promise (docs/SECURITY-HARDENING.md): prompt_revision (the immutable prompt version history),
// lineage (the immutable origin→answer record that proves the customer's data
// never left the perimeter — the red line), scan/DLP evidence and data
// product enforcement events. The rest are mutable: re-ingest replaces a
// document's chunks, memory is retained/purged, a KB's counts advance.
const (
	baseKind       model.Kind = "knowledge.base"
	baseTable                 = "knowledge_base"
	documentKind   model.Kind = "knowledge.document"
	documentTable             = "knowledge_document"
	chunkKind      model.Kind = "knowledge.chunk"
	chunkTable                = "knowledge_chunk"
	promptKind     model.Kind = "knowledge.prompt"
	promptTable               = "knowledge_prompt"
	revisionKind   model.Kind = "knowledge.prompt_revision"
	revisionTable             = "knowledge_prompt_revision"
	memoryKind     model.Kind = "knowledge.memory"
	memoryTable               = "knowledge_memory"
	ctxPolicyKind  model.Kind = "knowledge.context_policy"
	ctxPolicyTable            = "knowledge_context_policy"
	lineageKind    model.Kind = "knowledge.lineage"
	lineageTable              = "knowledge_lineage"
	// PII discovery + classification + DLP. These are separate entities
	// because they model separate governed lifecycles/evidence streams. Since
	// sqlstore reconcileColumns ALTER-TABLE-ADDs missing nullable module
	// columns on existing DBs, so appending nullable descriptor fields is the
	// expand-only growth vehicle and needs no numbered SQL migration.
	labelKind     model.Kind = "knowledge.sensitivity_label"
	labelTable               = "knowledge_sensitivity_label"
	piiScanKind   model.Kind = "knowledge.pii_scan"
	piiScanTable             = "knowledge_pii_scan"
	dlpRuleKind   model.Kind = "knowledge.dlp_rule"
	dlpRuleTable             = "knowledge_dlp_rule"
	dlpEventKind  model.Kind = "knowledge.dlp_event"
	dlpEventTable            = "knowledge_dlp_event"
	// per-user/per-session agent memory (OWASP ASI06 isolation). A
	// separate entity keeps the original table as the AGENT-GLOBAL scope (an entry
	// every user/session of the agent shares — the documented opt-in shared mode);
	// rows scoped to a user and/or session live here, and the unique key widens
	// accordingly.
	scopedMemoryKind  model.Kind = "knowledge.memory_scoped"
	scopedMemoryTable            = "knowledge_memory_scoped"
	// live source sync state + external sensitivity labels.
	syncStateKind  model.Kind = "knowledge.sync_state"
	syncStateTable            = "knowledge_sync_state"
	extLabelKind   model.Kind = "knowledge.external_label"
	extLabelTable             = "knowledge_external_label"
	// governed data products: catalog metadata + immutable-enough
	// contract versions + append-only enforcement evidence.
	dataProductKind   model.Kind = "knowledge.data_product"
	dataProductTable             = "knowledge_data_product"
	dataContractKind  model.Kind = "knowledge.data_contract"
	dataContractTable            = "knowledge_data_contract"
	dpEventKind       model.Kind = "knowledge.dp_event"
	dpEventTable                 = "knowledge_dp_event"
)

// knowledge_base columns — a governed knowledge base (collection).
const (
	colName        = "name"
	colClassif     = "classification"   // default classification for docs lacking one
	colResidency   = "residency_region" // "" / "global" = unrestricted; else strict match at retrieval
	colEmbedPolicy = "embed_policy"     // "auto" | "local_only" | "model_backed" — the egress/quality gate
	colEmbedModel  = "embed_model"      // the embedder actually used ("local-hash" or a wired model ref)
	colDim         = "dim"              // embedding dimension
	colDefaultACL  = "default_acl"      // JSON []string of default permission refs
	colOwnerRef    = "owner_ref"        // audit-actor of the creator (provenance)
	colStatus      = "status"           // "active" | "archived"
	colDocCount    = "doc_count"
	colChunkCount  = "chunk_count"
)

// knowledge_document columns — one ingested document's metadata + provenance.
const (
	colKBRef       = "kb_ref"
	colSourceKind  = "source_kind"   // contentsource.SourceKind
	colSourceRef   = "source_ref"    // legacy source ref; currently stores the source kind
	colSourceMode  = "source_mode"   // "export" | "live" | "direct"; absence reads as export
	colSourceDocID = "source_doc_id" // the source's natural doc id
	colTitle       = "title"
	colContentType = "content_type"
	colACL         = "acl"             // JSON []string of permission refs ("" => inherit KB default_acl)
	colContentHash = "content_hash"    // hex SHA-256 of the RAW (pre-redaction) body — dedup/change detection
	colRedactCount = "redaction_count" // how many secrets the module redacted on ingest
	colSpaceRef    = "space_ref"
	colDocChunkCnt = "chunk_count"
)

// knowledge_chunk columns — one redacted, embedded chunk of a document.
//
// text holds the chunk content AFTER the module's redaction (docs/SECURITY-HARDENING.md); it is a
// plain text column (NOT the descriptor Redact marker, which is for store-hashed
// fields) because the redacted text IS the knowledge we keep. embedding is the
// magic-prefixed float32 vector (vector.go); it is nullable while a chunk awaits
// the async embed worker. classification + acl are inherited from the document so
// the governance filter (retrieval.go) can run at chunk granularity BEFORE ranking.
const (
	colDocRef     = "doc_ref"
	colChunkIndex = "chunk_index"
	colText       = "text"
	colEmbedding  = "embedding" // []byte: magic(4)+dim float32 LE; NULL while pending
	colTokenCount = "token_count"
	colIndexed    = "indexed" // true once an embedding is present
)

// knowledge_prompt columns — a versioned prompt registry entry.
const (
	colCurrentRev = "current_rev" // pointer to the active revision number
	colLatestHash = "latest_hash"
)

// knowledge_prompt_revision columns — the append-only immutable version history.
const (
	colPromptRef    = "prompt_ref"
	colRevNum       = "rev_num" // "version" is a reserved base column, so rev_num
	colLabel        = "label"   // operator tag / semver
	colTemplate     = "template"
	colTemplateHash = "template_hash"
	colNote         = "note"
	colCreatedBy    = "created_by"
)

// knowledge_memory columns — governed persistent agent memory.
const (
	colAgentRef  = "agent_ref"
	colMemKey    = "mkey"
	colContent   = "content"
	colExpiresAt = "expires_at" // NULL = no expiry; else purged/filtered when now > expires_at
)

// knowledge_memory_scoped extra column — the per-user namespace dimension.
// The per-session dimension reuses colSessionRef ("session_ref", declared with
// lineage). Both are NON-nullable: "" means "not scoped on this dimension", so the
// widened unique index below works on every engine (a NULL would make duplicate
// (agent, user, key) rows unique to SQLite/Postgres NULL semantics).
const colUserRef = "user_ref"

// knowledge_context_policy columns — governed context/compaction policy.
const (
	// "session" | "agent" | "user" | "user_group" | "role" |
	// "agent_group" | "kb" | "workspace" | "tenant"
	colScopeKind = "scope_kind"
	colScopeRef  = "scope_ref"
	colMaxTokens = "max_tokens"
	colStrategy  = "strategy" // "truncate" | "summarize" | "window"
	colRedactReq = "redaction_required"
	colSpec      = "spec"
	colEffect    = "effect" // "allow" | "forbid"; empty = allow for legacy rows
)

// knowledge_lineage columns — the APPEND-ONLY origin→answer record (the red line).
const (
	colSessionRef     = "session_ref"
	colQueryHash      = "query_hash"
	colChunkRefs      = "chunk_refs"  // JSON [{chunk_id,kb_ref,doc_ref,source_kind,source_ref,source_mode,content_hash}]
	colSourceRefs     = "source_refs" // JSON []string of distinct doc/source refs
	colDecision       = "decision"    // "allowed" | "denied"
	colReason         = "reason"
	colEgress         = "egress"          // false in-perimeter; true ONLY when a model_backed embedder legitimately sent text out
	colEgressProvider = "egress_provider" // hashed provider id when egress occurred; "" otherwise
	colResultCount    = "result_count"
	colOccurredAt     = "occurred_at"
)

// knowledge_sensitivity_label columns — the CURRENT sensitivity label of one
// scanned subject (a stored document or a not-yet-ingested source document),
// upserted per scan/ingest. classes is the explainable evidence: the named
// deterministic rules that fired and their counts — never a matched value
// (docs/SECURITY-HARDENING.md). basis records WHAT was scanned: "raw" (the pre-redaction body at
// ingest / source scan) or "stored" (the redacted chunk text at rest), so a
// label is always reproducible against its content_hash + detector_version.
const (
	colSubjectKind = "subject_kind" // "document" | "source_document"
	colSubjectRef  = "subject_ref"  // document row id | "<source>/<source_doc_id>"
	colClasses     = "classes"      // JSON [{class,rule,count,severity}]
	colMaxSeverity = "max_severity"
	colRecommended = "recommended_classification" // advisory; NEVER auto-applied
	colBasis       = "basis"                      // "raw" | "stored"
	colDetectorVer = "detector_version"
	colScannedAt   = "scanned_at"
)

// knowledge_pii_scan columns — one APPEND-ONLY discovery scan run (the
// evidence that discovery actually happened: counts + catalog version, no
// content). Multiple scans are distinct events; no unique index.
const (
	colDocsScanned   = "docs_scanned"
	colChunksScanned = "chunks_scanned"
	colDocsWithHits  = "docs_with_hits"
	colHitSummary    = "hit_summary" // JSON {class: total count}
	colRedactedSeen  = "redacted_markers"
)

// knowledge_dlp_rule columns — the tenant's DLP egress policy, one row per
// sensitivity class. Reserved classes: "*" (any labeled class without an exact
// rule) and "unscanned" (content with no label row). NO rows = DLP not
// configured (the gate is inert, like an unpinned residency); ≥1 row = DLP
// enabled, and anything without an applicable rule DENIES (deny-closed).
const (
	colClass  = "class"
	colAction = "action" // "allow" | "deny"
	// (the rule's note column reuses colNote, declared with prompt_revision)
)

// knowledge_dlp_event columns — APPEND-ONLY DLP enforcement evidence: one row per
// enforcement action (chunks withheld from a retrieval, a retrieval denied, an
// ingest refused before egress). classes carries the class ids that triggered —
// never content.
const (
	colDLPAction  = "dlp_action" // "filtered" | "denied_ingest"
	colDLPClasses = "dlp_classes"
	colChunksHeld = "chunks_withheld"
	colLineageRef = "lineage_ref"
)

// knowledge_sync_state columns — per-KB, per-source sync cursor + status.
const (
	colSourceName     = "source_name"
	colSyncToken      = "sync_token"
	colLastSyncAt     = "last_sync_at"
	colLastSyncStatus = "last_sync_status"
	colDocsSynced     = "docs_synced"
	colDocsDeleted    = "docs_deleted"
	colACLsRefreshed  = "acls_refreshed"
	colSyncErrors     = "sync_errors"
)

// knowledge_external_label columns — per-document external sensitivity labels.
// Note: updated_at is a reserved base column injected by the engine; no custom
// column is needed — query via model.ColUpdatedAt.
const (
	colLabels = "labels"
)

// knowledge_data_product columns — a governed catalog overlay on a KB.
const (
	colDescription         = "description"
	colTags                = "tags"
	colFreshnessSLASeconds = "freshness_sla_seconds"
	colAvailabilityTarget  = "availability_target"
	colQualityScore        = "quality_score"
	colUsageCount          = "usage_count"
	colEnforcementMode     = "enforcement_mode"
	colLastIngestAt        = "last_ingest_at"
	colLastHealthAt        = "last_health_at"
)

// knowledge_data_contract columns — versioned data contracts for a product.
const (
	colProductRef               = "product_ref"
	colContractVersion          = "contract_version"
	colSchemaDefinition         = "schema_definition"
	colValidationMode           = "validation_mode"
	colCompletenessThreshold    = "completeness_threshold"
	colFreshnessOverrideSeconds = "freshness_override_seconds"
)

// knowledge_dp_event columns — APPEND-ONLY data-product enforcement evidence.
const (
	colContractRef = "contract_ref"
	colEventType   = "event_type"
	colSeverity    = "severity"
	colDetails     = "details"
)

// Principal declarations shared by several columns below. Each cited line holds
// for every column that uses the declaration.
var (
	// pdeclActorEvidence is the acting principal's audit actor string ("user:<id>"
	// or "token:<id>"), recorded as provenance and only rendered.
	pdeclActorEvidence = model.Ref(model.EncodeUserRef, model.ClassEvidence)
	pdeclNoneDigest    = model.None("a hex SHA-256 digest produced by hashHex, only compared or rendered: helpers.go:187-190")
	pdeclNoneClassif   = model.None("a classification label from a closed ladder; the only comparer fails closed on any other value: vector.go:33-35, vector.go:51-61")
	pdeclNoneRegion    = model.None("a data-residency region label; readers compare it with a region pin or render it: retrieval.go:348, query.go:193, documents.go:45, lineage.go:49")
	pdeclNoneEmbedder  = model.None("the wired embedder's model ref, recorded and rendered only: kb.go:238, ingest.go:647, kb.go:72")
	pdeclNoneKBRef     = model.None("a knowledge base row id, used only as a lookup filter: documents.go:59, retrieval.go:187, discovery.go:223, external_labels.go:28, sync.go:140")
	pdeclNoneDocRef    = model.None("a knowledge document row id, used only as a lookup key: ingest.go:634, retrieval.go:199, external_labels.go:44")
	pdeclNoneProduct   = model.None("a data product row id, used only as a lookup filter: dataproduct.go:756, dataproduct.go:952")
	pdeclNoneSource    = model.None("a content source kind label: connectors/contentsource/contentsource.go:17-38, ingest.go:601")
	pdeclNoneBasis     = model.None("a scan basis from a closed set: discovery.go:42-45")
	pdeclNoneDetector  = model.None("the classifier catalog version, only rendered: ports.go:169-171, discovery.go:572")
	pdeclNoneSensClass = model.None("a sensitivity class id from the classifier catalog, compared only with DLP rule classes: ports.go:140-151, dlp.go:67-81")
	// pdeclACL is a document permission list. An entry is compared only with the
	// collection refs the retrieval guard resolves for the reading identity, never
	// resolved to an account.
	pdeclACL = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a permission reference, matched only against the reader's resolved group refs: vector.go:63-80, retrieval.go:195, ingest.go:421-424, cmd/olivares/knowledgeguard.go:195-200")))
	// pdeclSensClasses is a list of sensitivity class ids.
	pdeclSensClasses = model.Nested([]string{}, model.ClassEvidence, model.Leaf("[]", pdeclNoneSensClass))
	// pdeclChunkProvenance is one retrieved chunk's provenance on a lineage row.
	pdeclChunkProvenance = model.None("a chunk provenance field (row ids and source labels), recorded and rendered only: query.go:304-307, lineage.go:43-44")
	// The governed memory columns shared by the agent-global and the scoped tables.
	pdeclNoneMemAgent   = model.None("a caller-declared agent namespace ref, matched only for equality and used as the agent hold subject: memory.go:178, memory.go:306, memory.go:579")
	pdeclNoneMemKey     = model.None("a caller-chosen memory key, matched only for upsert and rendered: memory.go:178, memory.go:105")
	pdeclNoneMemContent = model.None("redacted memory content, integrity-checked and returned only: memory.go:161, memory_integrity.go:189-190, memory.go:105")
	pdeclNoneMemRegion  = model.None("a data-residency region label, defaulted and rendered only: memory.go:696-699, memory.go:106")
)

// RegisterSchema declares the module's owned entities. It satisfies the
// engine-side runtime.SchemaProvider seam (structural — no runtime import) and is
// called once, at store construction, before any Scope exists (S02 §7 /):
// the engine creates the tables, injects the base columns and attaches the tenant,
// audit and append-only guards. A module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): no column holds a usable credential. A document's ACL
// holds permission references; a memory/prompt/chunk holds content the module
// REDACTED before it ever reached the store. The prompt_revision and lineage
// tables are APPEND-ONLY so the version history and the non-exfiltration evidence
// cannot be silently rewritten (docs/SECURITY-HARDENING.md).
//
// None is descriptor-Audited: the privileged mutations append a SEMANTIC self-audit
// to the real principal in their own transaction (helpers.go auditEvent) — the
// who/what the per-row engine audit could not attribute.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	for _, d := range []model.EntityDescriptor{
		{
			Kind:  baseKind,
			Table: baseTable,
			Fields: []model.FieldSpec{
				{Name: colName, Kind: model.KindText, Indexed: true, Principal: model.None("a knowledge base display name, unique per tenant, only matched and rendered: kb.go:231, kb.go:70")},
				{Name: colClassif, Kind: model.KindText, Principal: pdeclNoneClassif},
				{Name: colResidency, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRegion},
				{Name: colEmbedPolicy, Kind: model.KindText, Principal: model.None("an embed policy from a closed set: kb.go:80-96")},
				{Name: colEmbedModel, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEmbedder},
				{Name: colDim, Kind: model.KindInt},
				{Name: colDefaultACL, Kind: model.KindJSON, Nullable: true, Principal: pdeclACL},
				{Name: colOwnerRef, Kind: model.KindText, Principal: pdeclActorEvidence},
				{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a knowledge base status from a closed set: kb.go:36-39, kb.go:191")},
				{Name: colDocCount, Kind: model.KindInt},
				{Name: colChunkCount, Kind: model.KindInt},
			},
			Indexes: []model.IndexSpec{{
				Name: "knowledge_base_uniq", Columns: []string{model.ColTenantID, colName}, Unique: true,
			}},
		},
		{
			Kind:  documentKind,
			Table: documentTable,
			Fields: []model.FieldSpec{
				{Name: colKBRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colSourceKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSource},
				{Name: colSourceRef, Kind: model.KindText, Nullable: true, Principal: model.None("the legacy source reference, written as the source kind: ingest.go:601")},
				{Name: colSourceMode, Kind: model.KindText, Nullable: true, Indexed: true, Principal: model.None("a source mode from a closed set: source_mode.go:14-18, source_mode.go:41-55")},
				{Name: colSourceDocID, Kind: model.KindText, Indexed: true, Principal: model.None("the source system's natural document id, matched only for upsert and rendered: ingest.go:607-608, documents.go:44")},
				{Name: colTitle, Kind: model.KindText, Principal: model.None("a source document title, only rendered: documents.go:44, query.go:301")},
				{Name: colContentType, Kind: model.KindText, Principal: model.None("a MIME-like content type hint, defaulted and rendered only: ingest.go:756-761, documents.go:45")},
				{Name: colClassif, Kind: model.KindText, Indexed: true, Principal: pdeclNoneClassif},
				{Name: colResidency, Kind: model.KindText, Principal: pdeclNoneRegion},
				{Name: colACL, Kind: model.KindJSON, Nullable: true, Principal: pdeclACL},
				{Name: colContentHash, Kind: model.KindText, Principal: pdeclNoneDigest},
				{Name: colRedactCount, Kind: model.KindInt},
				{Name: colSpaceRef, Kind: model.KindText, Nullable: true, Principal: model.None("the source container reference, provenance only: connectors/contentsource/contentsource.go:100-103, documents.go:47")},
				{Name: colDocChunkCnt, Kind: model.KindInt},
				{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a document status from a closed set: ingest.go:24-27, ingest.go:596-599")},
			},
			Indexes: []model.IndexSpec{{
				// One document row per (kb, source, source_doc_id): re-ingest upserts in
				// place instead of duplicating. Leads with tenant_id.
				Name:    "knowledge_document_uniq",
				Columns: []string{model.ColTenantID, colKBRef, colSourceKind, colSourceDocID},
				Unique:  true,
			}},
		},
		{
			Kind:  chunkKind,
			Table: chunkTable,
			Fields: []model.FieldSpec{
				{Name: colKBRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colDocRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneDocRef},
				{Name: colChunkIndex, Kind: model.KindInt},
				{Name: colText, Kind: model.KindText, Principal: model.None("redacted document content, only ranked and returned as retrieval text: query.go:286, query.go:300-302")},
				{Name: colEmbedding, Kind: model.KindBytes, Nullable: true, Principal: model.None("a magic-prefixed float32 vector, only decoded for ranking: vector.go:94-120")},
				{Name: colEmbedModel, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEmbedder},
				{Name: colDim, Kind: model.KindInt},
				{Name: colTokenCount, Kind: model.KindInt},
				{Name: colContentHash, Kind: model.KindText, Principal: pdeclNoneDigest},
				{Name: colClassif, Kind: model.KindText, Indexed: true, Principal: pdeclNoneClassif},
				{Name: colACL, Kind: model.KindJSON, Nullable: true, Principal: pdeclACL},
				{Name: colIndexed, Kind: model.KindBool, Indexed: true},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_chunk_uniq",
				Columns: []string{model.ColTenantID, colDocRef, colChunkIndex},
				Unique:  true,
			}},
		},
		{
			Kind:  promptKind,
			Table: promptTable,
			Fields: []model.FieldSpec{
				{Name: colName, Kind: model.KindText, Indexed: true, Principal: model.None("a prompt name, unique per tenant, only matched and rendered: prompt.go:115, prompt.go:67")},
				{Name: colCurrentRev, Kind: model.KindInt},
				{Name: colLatestHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDigest},
				{Name: colOwnerRef, Kind: model.KindText, Principal: pdeclActorEvidence},
				{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a prompt status from a closed set: prompt.go:21-24, prompt.go:122")},
			},
			Indexes: []model.IndexSpec{{
				Name: "knowledge_prompt_uniq", Columns: []string{model.ColTenantID, colName}, Unique: true,
			}},
		},
		{
			Kind:       revisionKind,
			Table:      revisionTable,
			AppendOnly: true, // immutable prompt version history (docs/SECURITY-HARDENING.md)
			Fields: []model.FieldSpec{
				{Name: colPromptRef, Kind: model.KindUUID, Indexed: true, Principal: model.None("a prompt row id, written and rendered only: prompt.go:210, prompt.go:74")},
				{Name: colRevNum, Kind: model.KindInt, Indexed: true},
				{Name: colLabel, Kind: model.KindText, Nullable: true, Principal: model.None("an operator revision tag, only rendered: prompt.go:74")},
				{Name: colTemplate, Kind: model.KindText, Principal: model.None("a redacted prompt template, only returned: prompt.go:89, prompt.go:75")},
				{Name: colTemplateHash, Kind: model.KindText, Principal: pdeclNoneDigest},
				{Name: colNote, Kind: model.KindText, Nullable: true, Principal: model.None("operator revision prose, only rendered: prompt.go:75")},
				{Name: colCreatedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_prompt_revision_uniq",
				Columns: []string{model.ColTenantID, colPromptRef, colRevNum},
				Unique:  true,
			}},
		},
		{
			Kind:  memoryKind,
			Table: memoryTable,
			Fields: []model.FieldSpec{
				{Name: colAgentRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneMemAgent},
				{Name: colMemKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneMemKey},
				{Name: colContent, Kind: model.KindText, Principal: pdeclNoneMemContent},
				{Name: colContentHash, Kind: model.KindText, Principal: pdeclNoneDigest},
				{Name: colClassif, Kind: model.KindText, Principal: pdeclNoneClassif},
				{Name: colResidency, Kind: model.KindText, Principal: pdeclNoneMemRegion},
				{Name: colExpiresAt, Kind: model.KindTimestamp, Nullable: true, Indexed: true},
				{Name: colCreatedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_memory_uniq",
				Columns: []string{model.ColTenantID, colAgentRef, colMemKey},
				Unique:  true,
			}},
		},
		{
			// user/session-scoped agent memory. Same governed shape as
			// knowledge.memory plus the two namespace dimensions; at least one of
			// user_ref/session_ref is non-empty by construction (handlers route a
			// fully-unscoped write to the original table). The unique key is the
			// FULL namespace, so the same key coexists per user and per session
			// without colliding with another scope's entry.
			Kind:  scopedMemoryKind,
			Table: scopedMemoryTable,
			Fields: []model.FieldSpec{
				{Name: colAgentRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneMemAgent},
				// A caller-declared namespace ref in no fixed encoding; it is the
				// tenant's own data about a user, so it is evidence.
				{Name: colUserRef, Kind: model.KindText, Indexed: true, Principal: model.Scan(model.ClassEvidence)},
				{Name: colSessionRef, Kind: model.KindText, Indexed: true, Principal: model.None("a caller-declared session namespace ref, matched only for equality and used as the session hold subject: memory_integrity.go:124-125, memory.go:583")},
				{Name: colMemKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneMemKey},
				{Name: colContent, Kind: model.KindText, Principal: pdeclNoneMemContent},
				{Name: colContentHash, Kind: model.KindText, Principal: pdeclNoneDigest},
				{Name: colClassif, Kind: model.KindText, Principal: pdeclNoneClassif},
				{Name: colResidency, Kind: model.KindText, Principal: pdeclNoneMemRegion},
				{Name: colExpiresAt, Kind: model.KindTimestamp, Nullable: true, Indexed: true},
				{Name: colCreatedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_memory_scoped_uniq",
				Columns: []string{model.ColTenantID, colAgentRef, colUserRef, colSessionRef, colMemKey},
				Unique:  true,
			}},
		},
		{
			Kind:  ctxPolicyKind,
			Table: ctxPolicyTable,
			Fields: []model.FieldSpec{
				{Name: colScopeKind, Kind: model.KindText, Indexed: true, Principal: model.None("a policy scope kind from a closed set: context.go:49-52, context.go:103")},
				// Scope kind "user" makes the ref a bare account id; a matching row
				// only lowers a ceiling, adds a redaction floor or denies.
				{Name: colScopeRef, Kind: model.KindText, Indexed: true, Principal: model.KindRef(colScopeKind, model.ClassRestrict)},
				{Name: colMaxTokens, Kind: model.KindInt},
				{Name: colStrategy, Kind: model.KindText, Principal: model.None("a compaction strategy from a closed set: context.go:118-123")},
				{Name: colRedactReq, Kind: model.KindBool},
				{Name: colSpec, Kind: model.KindJSON, Nullable: true, Principal: model.None("a free-form policy document; the resolver reads only excluded_sources as source names and otherwise renders it: context.go:309-310, context.go:444-470, context.go:87-88")},
				{Name: colEffect, Kind: model.KindText, Nullable: true, Principal: model.None("a policy effect from a closed set: context.go:111-117, context.go:376-381")},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_context_policy_uniq",
				Columns: []string{model.ColTenantID, colScopeKind, colScopeRef},
				Unique:  true,
			}},
		},
		{
			Kind:       lineageKind,
			Table:      lineageTable,
			AppendOnly: true, // immutable origin→answer evidence: the red line (docs/SECURITY-HARDENING.md)
			// No unique index: multiple retrievals of the same query are DISTINCT
			// events, each its own append-only row. Deduplicating would erase the
			// audit trail rely on.
			Fields: []model.FieldSpec{
				{Name: colKBRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colAgentRef, Kind: model.KindText, Indexed: true, Principal: model.None("the authenticated agent's identity ref, recorded and used only as a filter: query.go:128, retrieval.go:292, lineage.go:65")},
				{Name: colSessionRef, Kind: model.KindText, Nullable: true, Principal: model.None("a caller-declared session ref, recorded and rendered only: retrieval.go:292, lineage.go:48")},
				{Name: colQueryHash, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDigest},
				{Name: colChunkRefs, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]chunkRef{}, model.ClassEvidence,
					model.Leaf("[].chunk_id", pdeclChunkProvenance),
					model.Leaf("[].kb_ref", pdeclChunkProvenance),
					model.Leaf("[].doc_ref", pdeclChunkProvenance),
					model.Leaf("[].source_kind", pdeclChunkProvenance),
					model.Leaf("[].source_ref", pdeclChunkProvenance),
					model.Leaf("[].source_mode", pdeclChunkProvenance),
					model.Leaf("[].content_hash", pdeclNoneDigest))},
				{Name: colSourceRefs, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]string{}, model.ClassEvidence,
					model.Leaf("[]", model.None("a knowledge document row id, recorded and rendered only: query.go:308, query.go:367, lineage.go:49")))},
				{Name: colResidency, Kind: model.KindText, Principal: pdeclNoneRegion},
				{Name: colDecision, Kind: model.KindText, Indexed: true, Principal: model.None("a retrieval decision from a closed set: retrieval.go:24-25")},
				{Name: colReason, Kind: model.KindText, Nullable: true, Principal: model.None("a generated decision reason, only rendered: query.go:315-354, lineage.go:50")},
				{Name: colEgress, Kind: model.KindBool},
				{Name: colEgressProvider, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDigest},
				{Name: colResultCount, Kind: model.KindInt},
				{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			},
		},
		{
			Kind:  labelKind,
			Table: labelTable,
			Fields: []model.FieldSpec{
				{Name: colSubjectKind, Kind: model.KindText, Indexed: true, Principal: model.None("a label subject kind from a closed set: discovery.go:36-39, discovery.go:288-291")},
				{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: model.None("a document row id or a source document path, matched only for upsert and lookup: discovery.go:37-38, discovery.go:184, discovery.go:233")},
				// Text (not UUID): empty for a source_document label, which has no KB.
				{Name: colKBRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colClasses, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]classHit{}, model.ClassEvidence,
					model.Leaf("[].class", pdeclNoneSensClass),
					model.Leaf("[].rule", model.None("a named classifier rule, only rendered: ports.go:140-151, discovery.go:570")),
					model.Leaf("[].severity", model.None("a rule severity, only ranked: discovery.go:138-147")))},
				{Name: colMaxSeverity, Kind: model.KindText, Nullable: true, Principal: model.None("a severity label, only ranked and rendered: discovery.go:138-147, discovery.go:570")},
				{Name: colRecommended, Kind: model.KindText, Nullable: true, Principal: model.None("an advisory classification from a closed set: discovery.go:154-165")},
				{Name: colBasis, Kind: model.KindText, Principal: pdeclNoneBasis},
				{Name: colContentHash, Kind: model.KindText, Principal: pdeclNoneDigest},
				{Name: colDetectorVer, Kind: model.KindText, Principal: pdeclNoneDetector},
				{Name: colScannedAt, Kind: model.KindTimestamp},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_sensitivity_label_uniq",
				Columns: []string{model.ColTenantID, colSubjectKind, colSubjectRef},
				Unique:  true,
			}},
		},
		{
			Kind:       piiScanKind,
			Table:      piiScanTable,
			AppendOnly: true, // discovery evidence: scans happened, with what catalog
			Fields: []model.FieldSpec{
				{Name: colScopeKind, Kind: model.KindText, Indexed: true, Principal: model.None("a scan scope kind from a closed set: discovery.go:48-51")}, // "kb" | "source"
				{Name: colScopeRef, Kind: model.KindText, Indexed: true, Principal: model.None("a knowledge base id or a registered source name: discovery.go:462, discovery.go:519")},
				{Name: colBasis, Kind: model.KindText, Principal: pdeclNoneBasis},
				{Name: colDocsScanned, Kind: model.KindInt},
				{Name: colChunksScanned, Kind: model.KindInt},
				{Name: colDocsWithHits, Kind: model.KindInt},
				{Name: colHitSummary, Kind: model.KindJSON, Nullable: true, Principal: model.Nested(map[string]int{}, model.ClassEvidence,
					model.Leaf("{key}", pdeclNoneSensClass))},
				{Name: colRedactedSeen, Kind: model.KindInt},
				{Name: colDetectorVer, Kind: model.KindText, Principal: pdeclNoneDetector},
				{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			},
		},
		{
			Kind:  dlpRuleKind,
			Table: dlpRuleTable,
			Fields: []model.FieldSpec{
				{Name: colClass, Kind: model.KindText, Indexed: true, Principal: model.None("a sensitivity class id or a reserved class, compared only with label classes: dlp.go:42-47, dlp.go:67-81")},
				{Name: colAction, Kind: model.KindText, Principal: model.None("a DLP action from a closed set: dlp.go:205-208")},
				{Name: colNote, Kind: model.KindText, Nullable: true, Principal: model.None("operator rule prose, only rendered: dlp.go:163")},
				{Name: colCreatedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
			},
			Indexes: []model.IndexSpec{{
				Name: "knowledge_dlp_rule_uniq", Columns: []string{model.ColTenantID, colClass}, Unique: true,
			}},
		},
		{
			Kind:       dlpEventKind,
			Table:      dlpEventTable,
			AppendOnly: true, // enforcement evidence: the gate fired, on what
			Fields: []model.FieldSpec{
				{Name: colKBRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colDLPAction, Kind: model.KindText, Indexed: true, Principal: model.None("a DLP event action from a closed set: dlp.go:50-53")},
				{Name: colDLPClasses, Kind: model.KindJSON, Nullable: true, Principal: pdeclSensClasses},
				{Name: colChunksHeld, Kind: model.KindInt},
				{Name: colAgentRef, Kind: model.KindText, Nullable: true, Principal: model.None("the retrieving agent's identity ref copied from its lineage row, written only: dlp.go:124, retrieval.go:306-307")},
				{Name: colLineageRef, Kind: model.KindText, Nullable: true, Principal: model.None("a lineage row id, written only: dlp.go:124, retrieval.go:306-307")},
				{Name: colReason, Kind: model.KindText, Nullable: true, Principal: model.None("a generated enforcement reason, written only: dlp.go:125, ingest.go:742")},
				{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			},
		},
		{
			// per-KB, per-source sync cursor + status. Tracks the live-source
			// ACL sync loop: last token, last run time, counts, and any error text.
			Kind:  syncStateKind,
			Table: syncStateTable,
			Fields: []model.FieldSpec{
				{Name: colKBRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colSourceName, Kind: model.KindText, Indexed: true, Principal: model.None("a registered content source name, matched only as a filter: sync.go:111, sync.go:140")},
				{Name: colSyncToken, Kind: model.KindText, Nullable: true, Principal: model.None("the source's opaque delta cursor, only handed back to the source: sync.go:323")},
				{Name: colLastSyncAt, Kind: model.KindTimestamp, Nullable: true},
				{Name: colLastSyncStatus, Kind: model.KindText, Principal: model.None("a sync status from a closed set: sync.go:27-31")},
				{Name: colDocsSynced, Kind: model.KindInt},
				{Name: colDocsDeleted, Kind: model.KindInt},
				{Name: colACLsRefreshed, Kind: model.KindInt},
				{Name: colSyncErrors, Kind: model.KindText, Nullable: true, Principal: model.None("generated sync error lines, stored only: sync.go:134-138")},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_sync_state_uniq",
				Columns: []string{model.ColTenantID, colKBRef, colSourceName},
				Unique:  true,
			}},
		},
		{
			// per-document external sensitivity labels pushed from the source
			// system (e.g. Microsoft Purview, Google Drive sensitivity labels). Upserted
			// on each ACL/label sync; the unique index is on (tenant_id, doc_ref).
			Kind:  extLabelKind,
			Table: extLabelTable,
			Fields: []model.FieldSpec{
				{Name: colDocRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneDocRef},
				{Name: colKBRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colLabels, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]string{}, model.ClassEvidence,
					model.Leaf("[]", model.None("a source sensitivity label, compared only with label clearances: external_labels.go:55-88")))},
				{Name: colSourceKind, Kind: model.KindText, Principal: pdeclNoneSource},
				// updated_at is an engine-injected base column (model.ColUpdatedAt); no custom field.
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_external_label_uniq",
				Columns: []string{model.ColTenantID, colDocRef},
				Unique:  true,
			}},
		},
		{
			Kind:  dataProductKind,
			Table: dataProductTable,
			Fields: []model.FieldSpec{
				{Name: colName, Kind: model.KindText, Indexed: true, Principal: model.None("a data product name, unique per tenant, only matched and rendered: dataproduct.go:477, dataproduct.go:182")},
				{Name: colDescription, Kind: model.KindText, Nullable: true, Principal: model.None("operator description prose, only rendered: dataproduct.go:183")},
				// A caller-chosen owner label in no fixed encoding (the actor string by
				// default); it is only rendered and filtered, so it is evidence.
				{Name: colOwnerRef, Kind: model.KindText, Indexed: true, Principal: model.Scan(model.ClassEvidence)},
				{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a data product status from a closed set: dataproduct.go:27-30, dataproduct.go:288-291")},
				{Name: colKBRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneKBRef},
				{Name: colTags, Kind: model.KindJSON, Nullable: true, Principal: model.None("free-form operator tags, only rendered: dataproduct.go:249-254, dataproduct.go:187")},
				{Name: colFreshnessSLASeconds, Kind: model.KindInt},
				{Name: colAvailabilityTarget, Kind: model.KindText, Nullable: true, Principal: model.None("an operator availability label, only rendered: dataproduct.go:189")},
				{Name: colQualityScore, Kind: model.KindInt},
				{Name: colUsageCount, Kind: model.KindInt},
				{Name: colEnforcementMode, Kind: model.KindText, Principal: model.None("an enforcement mode from a closed set: dataproduct.go:32-34, dataproduct.go:284-286")},
				{Name: colLastIngestAt, Kind: model.KindTimestamp, Nullable: true},
				{Name: colLastHealthAt, Kind: model.KindTimestamp, Nullable: true},
			},
			Indexes: []model.IndexSpec{{
				Name: "knowledge_data_product_uniq", Columns: []string{model.ColTenantID, colName}, Unique: true,
			}},
		},
		{
			Kind:  dataContractKind,
			Table: dataContractTable,
			Fields: []model.FieldSpec{
				{Name: colProductRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneProduct},
				{Name: colContractVersion, Kind: model.KindInt, Indexed: true},
				{Name: colSchemaDefinition, Kind: model.KindJSON, Nullable: true, Principal: model.None("a caller-supplied JSON Schema, used only to validate documents and rendered: dataproduct.go:1537-1545, dataproduct.go:203")},
				{Name: colValidationMode, Kind: model.KindText, Principal: model.None("a validation mode from a closed set: dataproduct.go:36-38, dataproduct.go:798-807")},
				{Name: colCompletenessThreshold, Kind: model.KindInt},
				{Name: colFreshnessOverrideSeconds, Kind: model.KindInt},
				{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a contract status from a closed set: dataproduct.go:40-41")},
				{Name: colCreatedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
				{Name: colNote, Kind: model.KindText, Nullable: true, Principal: model.None("operator contract prose, only rendered: dataproduct.go:209")},
			},
			Indexes: []model.IndexSpec{{
				Name:    "knowledge_data_contract_uniq",
				Columns: []string{model.ColTenantID, colProductRef, colContractVersion},
				Unique:  true,
			}},
		},
		{
			Kind:       dpEventKind,
			Table:      dpEventTable,
			AppendOnly: true, // Enforcement evidence: never rewrite/delete decisions
			Fields: []model.FieldSpec{
				{Name: colProductRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneProduct},
				{Name: colContractRef, Kind: model.KindText, Nullable: true, Principal: model.None("a data contract row id, recorded and rendered only: dataproduct.go:1259, dataproduct.go:217")},
				{Name: colEventType, Kind: model.KindText, Indexed: true, Principal: model.None("an event type from a closed set: dataproduct.go:43-48")},
				{Name: colSeverity, Kind: model.KindText, Principal: model.None("a severity from a closed set: dataproduct.go:50-53")},
				{Name: colSubjectKind, Kind: model.KindText, Nullable: true, Indexed: true, Principal: model.None("an event subject kind fixed in code: dataproduct.go:55-57, dataproduct.go:700")},
				{Name: colSubjectRef, Kind: model.KindText, Nullable: true, Principal: model.None("a data product id, a source document id or a query digest: dataproduct.go:700, dataproduct.go:1316, dataproduct.go:1377")},
				{Name: colDetails, Kind: model.KindJSON, Nullable: true, Principal: model.None("a generated map of labels, counts and validation errors, only rendered: dataproduct.go:1263-1264, dataproduct.go:222")},
				{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			},
		},
	} {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}
