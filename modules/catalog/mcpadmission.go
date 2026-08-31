// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

// signed admission for FEDERATED MCP catalog entries, the flow
// (modules/models + core/secure/modelsign) reused for the MCP supply chain: the
// entries the plane curates from federated catalogs (the Docker MCP Catalog's
// Docker-built images ship signed SBOMs + provenance attestations; GitHub-released
// servers ship SLSA provenance) carry verifiable in-toto attestations, and the
// catalog must be able to demand a VERIFIED one before an MCP entry is approved
// into the served registry (subregistry: lo servido = lo aprobado).
//
// Mirrors modeladmission.go/models.admission exactly, in-module:
//
//   - catalog.mcp_admission_policy: the per-tenant trust root (require_signed +
//     Sigstore identity/issuer pins, bare keys, CA roots, and the in-toto
//     PREDICATE allow-list). Default ABSENT ⇒ OBSERVE mode — nothing breaks for
//     tenants that have not opted in.
//   - catalog.mcp_admission: one claim-vs-verified verdict per catalog ENTRY
//     (entries are per-version rows), recorded by POST /entries/{id}/admit, which
//     verifies the supplied Sigstore bundle with modelsign.VerifyAttestation
//     (predicate allow-listed; optionally bound to an expected artifact digest —
//     e.g. the sha256 the Docker MCP Catalog pins for the image).
//   - the deny-closed approve gate: with require_signed on, approving a kindMCP
//     entry REQUIRES a verified admission verdict (entries.go), exactly like the
//     kindModel overlay.
//
// The attestation bundle is operator-supplied bytes (cosign download attestation /
// gh attestation download), the same UX as model admission; fetching
// attestations from OCI referrers is a documented external step, like the Rekor
// inclusion proof (modelsign's honest seam).

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure/modelsign"
	"github.com/olivaresai/olivares/core/store"
)

// Entity kinds for signed MCP-entry admission.
const (
	mcpAdmissionPolicyKind model.Kind = "catalog.mcp_admission_policy"
	mcpAdmissionKind       model.Kind = "catalog.mcp_admission"
)

const (
	mcpAdmissionPolicyTable = "catalog_mcp_admission_policy"
	mcpAdmissionTable       = "catalog_mcp_admission"
)

// mcp_admission_policy columns (public trust material only, never a secret).
const (
	colMAPScope          = "policy_scope" // singleton marker ("default")
	colMAPRequire        = "require_signed"
	colMAPRequireDigest  = "require_subject_digest"
	colMAPIdentities     = "allowed_identities" // JSON []string (regexp)
	colMAPIssuers        = "allowed_issuers"    // JSON []string
	colMAPKeys           = "trusted_keys"       // JSON []string (PEM PUBLIC KEY)
	colMAPRoots          = "trusted_roots"      // JSON []string (PEM CERTIFICATE)
	colMAPPredicates     = "allowed_predicates" // JSON []string (in-toto predicateType URIs)
	colMAPNote           = "note"
	colMAPAttestedBy     = "attested_by"
	colMAPAttestedAt     = "attested_at"
	mcpAdmissionPolicyID = "default"
)

// mcp_admission columns (the claim-vs-verified verdict per entry).
const (
	colMAdmEntry     = "entry_ref"
	colMAdmSubject   = "subject_name"
	colMAdmDigest    = "subject_digest"
	colMAdmPredicate = "predicate_type"
	colMAdmMethod    = "method"
	colMAdmIdentity  = "signer_identity"
	colMAdmIssuer    = "signer_issuer"
	colMAdmRoots     = "signer_roots" // JSON []string of "root:<fp>" anchoring-root markers
	colMAdmVerified  = "signature_verified"
	colMAdmArtifact  = "artifact_verified"
	colMAdmTLogSeen  = "tlog_present"
	colMAdmTLogOK    = "tlog_verified"
	colMAdmCoverage  = "coverage_note"
	colMAdmReason    = "reason"
	colMAdmNote      = "note"
	colMAdmAttBy     = "attested_by"
	colMAdmAttAt     = "attested_at"
)

// defaultAllowedPredicates is the predicate allow-list when neither the policy
// nor the request narrows it: the supply-chain attestation types federated MCP
// entries actually carry, plus OMS (an MCP server may ship model-signing-shaped
// bundles for its own artifacts).
func defaultAllowedPredicates() []string {
	return []string{
		modelsign.PredicateTypeSLSAProvenanceV1,
		modelsign.PredicateTypeSLSAProvenanceV02,
		modelsign.PredicateTypeSPDX,
		modelsign.PredicateTypeCycloneDX,
		modelsign.PredicateTypeOMSv1,
	}
}

// registerMCPAdmissionSchemas registers the two entities. Unique indexes
// lead with tenant_id.
func registerMCPAdmissionSchemas(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind: mcpAdmissionPolicyKind, Table: mcpAdmissionPolicyTable,
		Fields: []model.FieldSpec{
			{Name: colMAPScope, Kind: model.KindText, Indexed: true},
			{Name: colMAPRequire, Kind: model.KindBool, Indexed: true},
			{Name: colMAPRequireDigest, Kind: model.KindBool},
			{Name: colMAPIdentities, Kind: model.KindJSON, Nullable: true},
			{Name: colMAPIssuers, Kind: model.KindJSON, Nullable: true},
			{Name: colMAPKeys, Kind: model.KindJSON, Nullable: true},
			{Name: colMAPRoots, Kind: model.KindJSON, Nullable: true},
			{Name: colMAPPredicates, Kind: model.KindJSON, Nullable: true},
			{Name: colMAPNote, Kind: model.KindText, Nullable: true},
			{Name: colMAPAttestedBy, Kind: model.KindText, Nullable: true},
			{Name: colMAPAttestedAt, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{Name: "catalog_mcp_admission_policy_uniq", Columns: []string{model.ColTenantID, colMAPScope}, Unique: true}},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind: mcpAdmissionKind, Table: mcpAdmissionTable,
		Fields: []model.FieldSpec{
			{Name: colMAdmEntry, Kind: model.KindUUID, Indexed: true},
			{Name: colMAdmSubject, Kind: model.KindText, Nullable: true},
			{Name: colMAdmDigest, Kind: model.KindText, Nullable: true},
			{Name: colMAdmPredicate, Kind: model.KindText, Nullable: true},
			{Name: colMAdmMethod, Kind: model.KindText, Nullable: true},
			{Name: colMAdmIdentity, Kind: model.KindText, Nullable: true},
			{Name: colMAdmIssuer, Kind: model.KindText, Nullable: true},
			{Name: colMAdmRoots, Kind: model.KindJSON, Nullable: true},
			{Name: colMAdmVerified, Kind: model.KindBool, Indexed: true},
			{Name: colMAdmArtifact, Kind: model.KindBool},
			{Name: colMAdmTLogSeen, Kind: model.KindBool},
			{Name: colMAdmTLogOK, Kind: model.KindBool},
			{Name: colMAdmCoverage, Kind: model.KindText, Nullable: true},
			{Name: colMAdmReason, Kind: model.KindText, Nullable: true},
			{Name: colMAdmNote, Kind: model.KindText, Nullable: true},
			{Name: colMAdmAttBy, Kind: model.KindText, Nullable: true},
			{Name: colMAdmAttAt, Kind: model.KindText, Nullable: true},
		},
		// One verdict per catalog entry (per tenant); re-admit upserts.
		Indexes: []model.IndexSpec{{Name: "catalog_mcp_admission_uniq", Columns: []string{model.ColTenantID, colMAdmEntry}, Unique: true}},
	})
}

// --- policy (trust root) -------------------------------------------------------

type mcpAdmissionPolicyDTO struct {
	RequireSigned        bool     `json:"require_signed"`
	RequireSubjectDigest bool     `json:"require_subject_digest"`
	AllowedIdentities    []string `json:"allowed_identities,omitempty"`
	AllowedIssuers       []string `json:"allowed_issuers,omitempty"`
	TrustedKeys          []string `json:"trusted_keys,omitempty"`
	TrustedRoots         []string `json:"trusted_roots,omitempty"`
	AllowedPredicates    []string `json:"allowed_predicates,omitempty"`
	Note                 string   `json:"note,omitempty"`
	AttestedBy           string   `json:"attested_by,omitempty"`
	AttestedAt           string   `json:"attested_at,omitempty"`
}

// validate applies the same input guards as the policy: public material
// only, keyless pins set together, an enforcing policy needs an anchor.
func (d *mcpAdmissionPolicyDTO) validate() string {
	for _, k := range d.TrustedKeys {
		if strings.Contains(k, "PRIVATE KEY") {
			return "trusted_keys must contain PUBLIC keys only (PEM 'PUBLIC KEY'), never a private key"
		}
	}
	for _, c := range d.TrustedRoots {
		if strings.Contains(c, "PRIVATE KEY") {
			return "trusted_roots must contain CA CERTIFICATES only, never a private key"
		}
	}
	for _, p := range d.AllowedPredicates {
		if strings.TrimSpace(p) == "" {
			return "allowed_predicates must not contain empty entries"
		}
	}
	if d.RequireSigned && len(d.TrustedRoots) == 0 && len(d.TrustedKeys) == 0 {
		return "require_signed=true needs at least one trusted_root or trusted_key; a deny-closed gate with no trust anchor would reject everything"
	}
	if (len(d.AllowedIdentities) > 0) != (len(d.AllowedIssuers) > 0) {
		return "keyless verification requires BOTH allowed_identities and allowed_issuers (cosign-style); set both or neither"
	}
	return ""
}

func (d mcpAdmissionPolicyDTO) toRecord(actor, at string) model.Record {
	return model.Record{
		colMAPScope: mcpAdmissionPolicyID, colMAPRequire: d.RequireSigned, colMAPRequireDigest: d.RequireSubjectDigest,
		colMAPIdentities: marshalJSONStrings(d.AllowedIdentities), colMAPIssuers: marshalJSONStrings(d.AllowedIssuers),
		colMAPKeys: marshalJSONStrings(d.TrustedKeys), colMAPRoots: marshalJSONStrings(d.TrustedRoots),
		colMAPPredicates: marshalJSONStrings(d.AllowedPredicates),
		colMAPNote:       d.Note, colMAPAttestedBy: actor, colMAPAttestedAt: at,
	}
}

func toMCPAdmissionPolicyDTO(rec model.Record) mcpAdmissionPolicyDTO {
	return mcpAdmissionPolicyDTO{
		RequireSigned: rec.Bool(colMAPRequire), RequireSubjectDigest: rec.Bool(colMAPRequireDigest),
		AllowedIdentities: parseJSONStrings(rec.String(colMAPIdentities)), AllowedIssuers: parseJSONStrings(rec.String(colMAPIssuers)),
		TrustedKeys: parseJSONStrings(rec.String(colMAPKeys)), TrustedRoots: parseJSONStrings(rec.String(colMAPRoots)),
		AllowedPredicates: parseJSONStrings(rec.String(colMAPPredicates)),
		Note:              rec.String(colMAPNote), AttestedBy: rec.String(colMAPAttestedBy), AttestedAt: rec.String(colMAPAttestedAt),
	}
}

// trustPolicy maps the stored policy to the verifier's trust anchor.
func (d mcpAdmissionPolicyDTO) trustPolicy() modelsign.TrustPolicy {
	return modelsign.TrustPolicy{
		Roots: d.TrustedRoots, AllowedIdentities: d.AllowedIdentities,
		AllowedIssuers: d.AllowedIssuers, Keys: d.TrustedKeys,
	}
}

// loadMCPAdmissionPolicy reads the per-tenant singleton. Absent ⇒ observe mode.
func loadMCPAdmissionPolicy(r *http.Request, sc store.Scope) (mcpAdmissionPolicyDTO, bool, error) {
	repo, err := sc.Ext(mcpAdmissionPolicyKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return mcpAdmissionPolicyDTO{}, false, nil
		}
		return mcpAdmissionPolicyDTO{}, false, err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colMAPScope, mcpAdmissionPolicyID)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return mcpAdmissionPolicyDTO{}, false, err
	}
	return toMCPAdmissionPolicyDTO(recs[0]), true, nil
}

func (m *Module) handleGetMCPAdmissionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var (
		out mcpAdmissionPolicyDTO
		ok  bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var e error
		out, ok, e = loadMCPAdmissionPolicy(r, sc)
		return e
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"require_signed":         false,
			"require_subject_digest": false,
			"configured":             false,
			"note":                   "no MCP-entry admission policy configured; attestation verification runs in OBSERVE mode (verdicts are recorded, approvals are not gated). Set require_signed with a trust anchor to enforce.",
		})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePutMCPAdmissionPolicy upserts the per-tenant trust root. Admin, audited.
func (m *Module) handlePutMCPAdmissionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in mcpAdmissionPolicyDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out mcpAdmissionPolicyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(mcpAdmissionPolicyKind)
		if err != nil {
			return err
		}
		actor := mc.Principal.Actor()
		at := model.NewTimestamp(m.clock.Now().Time()).String()
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colMAPScope, mcpAdmissionPolicyID)}, Limit: 1})
		if err != nil {
			return err
		}
		var rec model.Record
		if len(existing) > 0 {
			rec = existing[0]
			for k, v := range in.toRecord(actor, at) {
				rec[k] = v
			}
			rec, err = repo.Update(r.Context(), rec)
		} else {
			rec, err = repo.Create(r.Context(), in.toRecord(actor, at))
		}
		if err != nil {
			return err
		}
		out = toMCPAdmissionPolicyDTO(rec)
		_, err = sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: actor, ActorKind: mc.Principal.ActorKind(),
			Action: "catalog.mcp_admission.configure", TargetKind: mcpAdmissionPolicyKind,
			TargetID: model.ID(rec.String(model.ColID)),
		})
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- admission verdict -----------------------------------------------------------

type mcpAdmissionDTO struct {
	ID                string   `json:"id,omitempty"`
	EntryRef          string   `json:"entry_ref"`
	SubjectName       string   `json:"subject_name,omitempty"`
	SubjectDigest     string   `json:"subject_digest,omitempty"`
	PredicateType     string   `json:"predicate_type,omitempty"`
	Method            string   `json:"method,omitempty"`
	SignerIdentity    string   `json:"signer_identity,omitempty"`
	SignerIssuer      string   `json:"signer_issuer,omitempty"`
	SignerRoots       []string `json:"signer_roots,omitempty"`
	SignatureVerified bool     `json:"signature_verified"`
	ArtifactVerified  bool     `json:"artifact_verified"`
	TLogPresent       bool     `json:"tlog_present"`
	TLogVerified      bool     `json:"tlog_verified"`
	CoverageNote      string   `json:"coverage_note,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	Note              string   `json:"note,omitempty"`
	AttestedBy        string   `json:"attested_by,omitempty"`
	AttestedAt        string   `json:"attested_at,omitempty"`
}

func toMCPAdmissionDTO(rec model.Record) mcpAdmissionDTO {
	return mcpAdmissionDTO{
		ID: rec.String(model.ColID), EntryRef: rec.String(colMAdmEntry),
		SubjectName: rec.String(colMAdmSubject), SubjectDigest: rec.String(colMAdmDigest),
		PredicateType: rec.String(colMAdmPredicate), Method: rec.String(colMAdmMethod),
		SignerIdentity: rec.String(colMAdmIdentity), SignerIssuer: rec.String(colMAdmIssuer),
		SignerRoots:       parseJSONStrings(rec.String(colMAdmRoots)),
		SignatureVerified: rec.Bool(colMAdmVerified), ArtifactVerified: rec.Bool(colMAdmArtifact),
		TLogPresent: rec.Bool(colMAdmTLogSeen), TLogVerified: rec.Bool(colMAdmTLogOK),
		CoverageNote: rec.String(colMAdmCoverage), Reason: rec.String(colMAdmReason),
		Note: rec.String(colMAdmNote), AttestedBy: rec.String(colMAdmAttBy), AttestedAt: rec.String(colMAdmAttAt),
	}
}

// mcpAdmitRequestDTO is the POST /entries/{id}/admit body: the attestation bundle
// (a Sigstore bundle: cosign download attestation / gh attestation download),
// optional predicate narrowing, and the expected artifact digest to bind the
// statement subject to (e.g. the sha256 the Docker MCP Catalog pins).
type mcpAdmitRequestDTO struct {
	Bundle         json.RawMessage `json:"bundle"`
	PredicateTypes []string        `json:"predicate_types,omitempty"`
	ExpectedDigest string          `json:"expected_digest,omitempty"`
	Note           string          `json:"note,omitempty"`
}

type mcpAdmitResponseDTO struct {
	Admitted  bool            `json:"admitted"`
	Enforced  bool            `json:"enforced"`
	Admission mcpAdmissionDTO `json:"admission"`
}

// effectivePredicates resolves the predicate allow-list: the request narrows the
// policy's set (or the defaults) and may never WIDEN it — a per-request predicate
// outside the policy is dropped, and an empty intersection refuses (deny-closed,
// inside VerifyAttestation).
func effectivePredicates(policy mcpAdmissionPolicyDTO, requested []string) []string {
	allowed := policy.AllowedPredicates
	if len(allowed) == 0 {
		allowed = defaultAllowedPredicates()
	}
	if len(requested) == 0 {
		return allowed
	}
	allowedSet := map[string]struct{}{}
	for _, p := range allowed {
		allowedSet[p] = struct{}{}
	}
	var out []string
	for _, p := range requested {
		if _, ok := allowedSet[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// handleAdmitMCPEntry verifies a supply-chain attestation for an MCP catalog
// entry and records the claim-vs-verified verdict (upsert). Deny-closed under the
// active policy; a malformed bundle is 400; a well-formed bundle that fails to
// verify is a RECORDED verdict (200), exactly like the model admission.
// Since S142 the route POST /entries/{id}/admit is served by the kind dispatch
// (handleAdmitEntry, connectoradmission.go), which routes kindMCP here; the
// in-transaction kind guard below stays as the authoritative check (the dispatch
// peek is routing only — a draft's kind is editable between the two reads).
func (m *Module) handleAdmitMCPEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in mcpAdmitRequestDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Bundle) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("bundle (a Sigstore attestation bundle) is required"))
		return
	}
	var (
		out      mcpAdmitResponseDTO
		badInput string
		notFound bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		entries, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		entry, err := entries.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		if entry.String(colEntryKind) != kindMCP {
			badInput = admitWrongKindMsg
			return nil
		}
		policy, _, err := loadMCPAdmissionPolicy(r, sc)
		if err != nil {
			return err
		}
		verdict, verr := modelsign.VerifyAttestation(in.Bundle, policy.trustPolicy(), effectivePredicates(policy, in.PredicateTypes), in.ExpectedDigest)
		if errors.Is(verr, modelsign.ErrMalformedBundle) {
			badInput = "bundle is not a parseable Sigstore attestation: " + verr.Error()
			return nil
		}
		if verr != nil {
			return verr
		}

		admitted := verdict.Verified
		if policy.RequireSubjectDigest {
			admitted = admitted && verdict.ArtifactVerified
		}

		actor := mc.Principal.Actor()
		at := model.NewTimestamp(m.clock.Now().Time()).String()
		rec := model.Record{
			colMAdmEntry:   id.String(),
			colMAdmSubject: verdict.SubjectName, colMAdmDigest: verdict.SubjectDigest,
			colMAdmPredicate: verdict.PredicateType, colMAdmMethod: verdict.Method,
			colMAdmIdentity: verdict.SignerIdentity, colMAdmIssuer: verdict.SignerIssuer,
			colMAdmRoots:    marshalJSONStrings(verdict.SignerRoots),
			colMAdmVerified: verdict.Verified, colMAdmArtifact: verdict.ArtifactVerified,
			colMAdmTLogSeen: verdict.TransparencyLogPresent, colMAdmTLogOK: verdict.TransparencyLogVerified,
			colMAdmCoverage: admissionCoverageNote(verdict), colMAdmReason: verdict.Reason,
			colMAdmNote: in.Note, colMAdmAttBy: actor, colMAdmAttAt: at,
		}
		repo, err := sc.Ext(mcpAdmissionKind)
		if err != nil {
			return err
		}
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colMAdmEntry, id.String())}, Limit: 1})
		if err != nil {
			return err
		}
		var saved model.Record
		if len(existing) > 0 {
			saved = existing[0]
			for k, v := range rec {
				saved[k] = v
			}
			saved, err = repo.Update(r.Context(), saved)
		} else {
			saved, err = repo.Create(r.Context(), rec)
		}
		if err != nil {
			return err
		}
		out = mcpAdmitResponseDTO{Admitted: admitted, Enforced: policy.RequireSigned, Admission: toMCPAdmissionDTO(saved)}
		_, err = sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: actor, ActorKind: mc.Principal.ActorKind(),
			Action: "catalog.mcp_admission.admit", TargetKind: mcpAdmissionKind,
			TargetID: model.ID(saved.String(model.ColID)),
			Meta:     map[string]any{"entry": id.String(), "verified": verdict.Verified, "predicate": verdict.PredicateType},
		})
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if badInput != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(badInput))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// admissionCoverageNote folds the verifier's honest coverage notes into one line
// (shared by the MCP and S142 connector admission flows — the Verdict shape
// is the verifier's, not either flow's).
func admissionCoverageNote(v modelsign.Verdict) string {
	parts := []string{}
	if v.ArtifactCoverage != "" {
		parts = append(parts, v.ArtifactCoverage)
	}
	if v.TransparencyLogNote != "" {
		parts = append(parts, v.TransparencyLogNote)
	}
	return strings.Join(parts, "; ")
}

// handleListMCPAdmissions lists the recorded MCP-entry admission verdicts.
func (m *Module) handleListMCPAdmissions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("entry_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colMAdmEntry, v))
	}
	if r.URL.Query().Get("verified") == "true" {
		q.Filters = append(q.Filters, model.Filter{Column: colMAdmVerified, Op: model.OpEq, Value: true})
	}
	out := listResponse[mcpAdmissionDTO]{Items: []mcpAdmissionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(mcpAdmissionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toMCPAdmissionDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- the deny-closed approve gate (entries.go calls this for kindMCP) -----------

// mcpAdmissionRefusal returns a non-empty reason when an MCP entry must NOT be
// approved (deny-closed), or "" when approval may proceed. Observe mode (no
// policy / require_signed off) never gates — the existing estate keeps working
// until the tenant opts in, exactly like the kindModel overlay.
func mcpAdmissionRefusal(r *http.Request, sc store.Scope, entryID model.ID) (string, error) {
	policy, ok, err := loadMCPAdmissionPolicy(r, sc)
	if err != nil {
		return "", err
	}
	if !ok || !policy.RequireSigned {
		return "", nil
	}
	repo, err := sc.Ext(mcpAdmissionKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return "deny-closed: signed MCP-entry admission is required but the admission entity is unavailable", nil
		}
		return "", err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colMAdmEntry, entryID.String())}, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "deny-closed: this MCP entry has no attestation-admission verdict (POST /entries/{id}/admit a provenance/SBOM attestation before approving it into the served registry)", nil
	}
	rec := recs[0]
	if !rec.Bool(colMAdmVerified) {
		reason := rec.String(colMAdmReason)
		if reason == "" {
			reason = "attestation not verified"
		}
		return "deny-closed: this MCP entry's attestation admission did not verify (" + reason + ")", nil
	}
	if policy.RequireSubjectDigest && !rec.Bool(colMAdmArtifact) {
		return "deny-closed: policy requires the attestation subject to be bound to the expected artifact digest, which is unconfirmed for this entry", nil
	}
	// The verdict's booleans were computed against the trust policy AT ADMIT TIME. Re-check
	// that the anchor that verified it is STILL trusted by the CURRENT policy, so a rotated-
	// out trusted key (e.g. a compromised anchor removed) cannot keep certifying via a stale
	// verdict. The recorded signer identity is enough — no bundle needed.
	if !modelsign.AnchorStillTrusted(policy.trustPolicy(), modelsign.RecordedAnchor{
		Identity: rec.String(colMAdmIdentity), Issuer: rec.String(colMAdmIssuer),
		Roots: parseJSONStrings(rec.String(colMAdmRoots)), Method: rec.String(colMAdmMethod),
	}) {
		return "deny-closed: the trust anchor that admitted this MCP entry is no longer in the tenant's admission policy (it was rotated out or its anchoring root was replaced); re-admit an attestation under the current trust anchors before approving", nil
	}
	return "", nil
}

// invalidateMCPAdmission deletes every recorded MCP-entry admission verdict for the
// entry and returns how many were removed. It exists because the MCP approve gate,
// unlike the model gate (re-binds on the current spec.version_ref, modeladmission.go:71)
// and the connector gate (re-binds on the current spec.artifact_digest,
// connectoradmission.go:704), inspects only the verdict's frozen booleans and never
// re-validates it against the entry's current served spec. An MCP entry's spec IS
// what gets served (transport/endpoint/secret_refs), so a served-spec edit between
// admit and approve must force a fresh attestation: dropping the verdict makes the
// gate deny-closed ("no attestation-admission verdict") until the entry is re-admitted.
// A no-op when no verdict exists or the admission entity is not mounted.
func invalidateMCPAdmission(r *http.Request, sc store.Scope, entryID model.ID) (int, error) {
	repo, err := sc.Ext(mcpAdmissionKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return 0, nil
		}
		return 0, err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colMAdmEntry, entryID.String())}, Limit: 1000})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range recs {
		if err := repo.Delete(r.Context(), model.ID(rec.String(model.ColID))); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- shared JSON []string column helpers ----------------------------------------

func marshalJSONStrings(v []string) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseJSONStrings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
