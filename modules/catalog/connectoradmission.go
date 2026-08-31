// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

// S142 — signed admission for third-party CONNECTOR catalog entries (EXT-3): the
// MCP admission pair mirrored for the external-connector ecosystem. A
// kindConnector entry curates a RELEASED, signed connector plugin artifact (a
// built binary / OCI image; its spec records artifact_digest, the release/OCI
// ref, the publisher and the descriptor name), and this file is the tenant-facing
// CERTIFICATION record of that ecosystem: the per-tenant trust root, one
// claim-vs-verified verdict per entry, and the deny-closed approve gate that
// makes "approved connector entry ⇒ its provenance/SBOM was verified" a hard
// invariant of the curated inventory. The HOST-side execution gate for connector
// plugins lives in cmd/olivares — this module certifies what the org
// approved; it never loads or executes a plugin.
//
// Mirrors mcpadmission.go deliberately — same column shape, OWN kinds and tables
// (catalog.connector_admission_policy / catalog.connector_admission). The
// lesson applies: compliance evidence is counted BY KIND, so connector verdicts
// must never share rows or kinds with MCP verdicts — sharing would inflate one
// ecosystem's evidence with the other's.
//
// Deltas vs the MCP pair:
//
//   - defaultConnectorPredicates() excludes OMS: a connector plugin is a built
//     binary/OCI artifact, not weights (see the function comment).
//   - the admit flow DEFAULTS expected_digest from the entry's
//     spec.artifact_digest — the catalog entry names the artifact it curates, so
//     the admission binds to THAT artifact unless the request explicitly pins
//     another (see handleAdmitConnectorEntry).
//   - handleAdmitEntry (below) is the POST /entries/{id}/admit KIND DISPATCH for
//     both flows: kind mcp → mcpadmission.go, kind connector → this file, any
//     other kind → 400.

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

// Entity kinds for signed connector-entry admission.
const (
	connectorAdmissionPolicyKind model.Kind = "catalog.connector_admission_policy"
	connectorAdmissionKind       model.Kind = "catalog.connector_admission"
)

const (
	connectorAdmissionPolicyTable = "catalog_connector_admission_policy"
	connectorAdmissionTable       = "catalog_connector_admission"
)

// connector_admission_policy columns (public trust material only, never a
// secret). Same column NAMES as the MCP policy — same shape, own table.
const (
	colCAPScope                = "policy_scope" // singleton marker ("default")
	colCAPRequire              = "require_signed"
	colCAPRequireDigest        = "require_subject_digest"
	colCAPIdentities           = "allowed_identities" // JSON []string (regexp)
	colCAPIssuers              = "allowed_issuers"    // JSON []string
	colCAPKeys                 = "trusted_keys"       // JSON []string (PEM PUBLIC KEY)
	colCAPRoots                = "trusted_roots"      // JSON []string (PEM CERTIFICATE)
	colCAPPredicates           = "allowed_predicates" // JSON []string (in-toto predicateType URIs)
	colCAPNote                 = "note"
	colCAPAttestedBy           = "attested_by"
	colCAPAttestedAt           = "attested_at"
	connectorAdmissionPolicyID = "default"
)

// connector_admission columns (the claim-vs-verified verdict per entry).
const (
	colCAdmEntry     = "entry_ref"
	colCAdmSubject   = "subject_name"
	colCAdmDigest    = "subject_digest"
	colCAdmPredicate = "predicate_type"
	colCAdmMethod    = "method"
	colCAdmIdentity  = "signer_identity"
	colCAdmIssuer    = "signer_issuer"
	colCAdmRoots     = "signer_roots" // JSON []string of "root:<fp>" anchoring-root markers
	colCAdmVerified  = "signature_verified"
	colCAdmArtifact  = "artifact_verified"
	colCAdmTLogSeen  = "tlog_present"
	colCAdmTLogOK    = "tlog_verified"
	colCAdmCoverage  = "coverage_note"
	colCAdmReason    = "reason"
	colCAdmNote      = "note"
	colCAdmAttBy     = "attested_by"
	colCAdmAttAt     = "attested_at"
)

// specKeyArtifactDigest is the connector entry's spec field naming the sha256 of
// the released plugin artifact the entry curates. The admit flow defaults its
// expected-digest binding from it (see handleAdmitConnectorEntry).
const specKeyArtifactDigest = "artifact_digest"

// admitWrongKindMsg is the 400 for an admit on an entry kind with no attestation
// admission flow (agents/skills/templates curate definitions, not released
// artifacts, so there is nothing to attest; models use the models module's
// admission). Shared by the dispatch and the per-flow in-transaction guards.
const admitWrongKindMsg = "attestation admission applies to MCP and connector catalog entries; models use the models module admission"

// defaultConnectorPredicates is the predicate allow-list when neither the policy
// nor the request narrows it: SLSA provenance (v1 plus the v0.2 many builders
// still emit) and SBOM attestations (SPDX/CycloneDX). Deliberately NOT OMS —
// unlike the MCP default (an MCP server may ship model-signing-shaped bundles
// for its own artifacts), a connector plugin is a BUILT binary/OCI artifact
// whose supply-chain attestations are provenance/SBOM; OpenSSF Model Signing
// manifests are weights-shaped (per-file model manifests), and admitting one
// for a binary would certify nothing about the build that produced it.
func defaultConnectorPredicates() []string {
	return []string{
		modelsign.PredicateTypeSLSAProvenanceV1,
		modelsign.PredicateTypeSLSAProvenanceV02,
		modelsign.PredicateTypeSPDX,
		modelsign.PredicateTypeCycloneDX,
	}
}

// registerConnectorAdmissionSchemas registers the two S142 entities. Unique
// indexes lead with tenant_id.
func registerConnectorAdmissionSchemas(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind: connectorAdmissionPolicyKind, Table: connectorAdmissionPolicyTable,
		Fields: []model.FieldSpec{
			{Name: colCAPScope, Kind: model.KindText, Indexed: true},
			{Name: colCAPRequire, Kind: model.KindBool, Indexed: true},
			{Name: colCAPRequireDigest, Kind: model.KindBool},
			{Name: colCAPIdentities, Kind: model.KindJSON, Nullable: true},
			{Name: colCAPIssuers, Kind: model.KindJSON, Nullable: true},
			{Name: colCAPKeys, Kind: model.KindJSON, Nullable: true},
			{Name: colCAPRoots, Kind: model.KindJSON, Nullable: true},
			{Name: colCAPPredicates, Kind: model.KindJSON, Nullable: true},
			{Name: colCAPNote, Kind: model.KindText, Nullable: true},
			{Name: colCAPAttestedBy, Kind: model.KindText, Nullable: true},
			{Name: colCAPAttestedAt, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{Name: "catalog_connector_admission_policy_uniq", Columns: []string{model.ColTenantID, colCAPScope}, Unique: true}},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind: connectorAdmissionKind, Table: connectorAdmissionTable,
		Fields: []model.FieldSpec{
			{Name: colCAdmEntry, Kind: model.KindUUID, Indexed: true},
			{Name: colCAdmSubject, Kind: model.KindText, Nullable: true},
			{Name: colCAdmDigest, Kind: model.KindText, Nullable: true},
			{Name: colCAdmPredicate, Kind: model.KindText, Nullable: true},
			{Name: colCAdmMethod, Kind: model.KindText, Nullable: true},
			{Name: colCAdmIdentity, Kind: model.KindText, Nullable: true},
			{Name: colCAdmIssuer, Kind: model.KindText, Nullable: true},
			{Name: colCAdmRoots, Kind: model.KindJSON, Nullable: true},
			{Name: colCAdmVerified, Kind: model.KindBool, Indexed: true},
			{Name: colCAdmArtifact, Kind: model.KindBool},
			{Name: colCAdmTLogSeen, Kind: model.KindBool},
			{Name: colCAdmTLogOK, Kind: model.KindBool},
			{Name: colCAdmCoverage, Kind: model.KindText, Nullable: true},
			{Name: colCAdmReason, Kind: model.KindText, Nullable: true},
			{Name: colCAdmNote, Kind: model.KindText, Nullable: true},
			{Name: colCAdmAttBy, Kind: model.KindText, Nullable: true},
			{Name: colCAdmAttAt, Kind: model.KindText, Nullable: true},
		},
		// One verdict per catalog entry (per tenant); re-admit upserts.
		Indexes: []model.IndexSpec{{Name: "catalog_connector_admission_uniq", Columns: []string{model.ColTenantID, colCAdmEntry}, Unique: true}},
	})
}

// --- policy (trust root) -------------------------------------------------------

type connectorAdmissionPolicyDTO struct {
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

// validate applies the same input guards as the policies: public
// material only, keyless pins set together, an enforcing policy needs an anchor.
func (d *connectorAdmissionPolicyDTO) validate() string {
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

func (d connectorAdmissionPolicyDTO) toRecord(actor, at string) model.Record {
	return model.Record{
		colCAPScope: connectorAdmissionPolicyID, colCAPRequire: d.RequireSigned, colCAPRequireDigest: d.RequireSubjectDigest,
		colCAPIdentities: marshalJSONStrings(d.AllowedIdentities), colCAPIssuers: marshalJSONStrings(d.AllowedIssuers),
		colCAPKeys: marshalJSONStrings(d.TrustedKeys), colCAPRoots: marshalJSONStrings(d.TrustedRoots),
		colCAPPredicates: marshalJSONStrings(d.AllowedPredicates),
		colCAPNote:       d.Note, colCAPAttestedBy: actor, colCAPAttestedAt: at,
	}
}

func toConnectorAdmissionPolicyDTO(rec model.Record) connectorAdmissionPolicyDTO {
	return connectorAdmissionPolicyDTO{
		RequireSigned: rec.Bool(colCAPRequire), RequireSubjectDigest: rec.Bool(colCAPRequireDigest),
		AllowedIdentities: parseJSONStrings(rec.String(colCAPIdentities)), AllowedIssuers: parseJSONStrings(rec.String(colCAPIssuers)),
		TrustedKeys: parseJSONStrings(rec.String(colCAPKeys)), TrustedRoots: parseJSONStrings(rec.String(colCAPRoots)),
		AllowedPredicates: parseJSONStrings(rec.String(colCAPPredicates)),
		Note:              rec.String(colCAPNote), AttestedBy: rec.String(colCAPAttestedBy), AttestedAt: rec.String(colCAPAttestedAt),
	}
}

// trustPolicy maps the stored policy to the verifier's trust anchor.
func (d connectorAdmissionPolicyDTO) trustPolicy() modelsign.TrustPolicy {
	return modelsign.TrustPolicy{
		Roots: d.TrustedRoots, AllowedIdentities: d.AllowedIdentities,
		AllowedIssuers: d.AllowedIssuers, Keys: d.TrustedKeys,
	}
}

// loadConnectorAdmissionPolicy reads the per-tenant singleton. Absent ⇒ observe mode.
func loadConnectorAdmissionPolicy(r *http.Request, sc store.Scope) (connectorAdmissionPolicyDTO, bool, error) {
	repo, err := sc.Ext(connectorAdmissionPolicyKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return connectorAdmissionPolicyDTO{}, false, nil
		}
		return connectorAdmissionPolicyDTO{}, false, err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colCAPScope, connectorAdmissionPolicyID)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return connectorAdmissionPolicyDTO{}, false, err
	}
	return toConnectorAdmissionPolicyDTO(recs[0]), true, nil
}

func (m *Module) handleGetConnectorAdmissionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var (
		out connectorAdmissionPolicyDTO
		ok  bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var e error
		out, ok, e = loadConnectorAdmissionPolicy(r, sc)
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
			"note":                   "no connector-entry admission policy configured; attestation verification for CONNECTOR entries runs in OBSERVE mode (verdicts are recorded, approvals are not gated). Set require_signed with a trust anchor to enforce.",
		})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePutConnectorAdmissionPolicy upserts the per-tenant trust root. Admin, audited.
func (m *Module) handlePutConnectorAdmissionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in connectorAdmissionPolicyDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out connectorAdmissionPolicyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(connectorAdmissionPolicyKind)
		if err != nil {
			return err
		}
		actor := mc.Principal.Actor()
		at := model.NewTimestamp(m.clock.Now().Time()).String()
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colCAPScope, connectorAdmissionPolicyID)}, Limit: 1})
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
		out = toConnectorAdmissionPolicyDTO(rec)
		_, err = sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: actor, ActorKind: mc.Principal.ActorKind(),
			Action: "catalog.connector_admission.configure", TargetKind: connectorAdmissionPolicyKind,
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

type connectorAdmissionDTO struct {
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

func toConnectorAdmissionDTO(rec model.Record) connectorAdmissionDTO {
	return connectorAdmissionDTO{
		ID: rec.String(model.ColID), EntryRef: rec.String(colCAdmEntry),
		SubjectName: rec.String(colCAdmSubject), SubjectDigest: rec.String(colCAdmDigest),
		PredicateType: rec.String(colCAdmPredicate), Method: rec.String(colCAdmMethod),
		SignerIdentity: rec.String(colCAdmIdentity), SignerIssuer: rec.String(colCAdmIssuer),
		SignerRoots:       parseJSONStrings(rec.String(colCAdmRoots)),
		SignatureVerified: rec.Bool(colCAdmVerified), ArtifactVerified: rec.Bool(colCAdmArtifact),
		TLogPresent: rec.Bool(colCAdmTLogSeen), TLogVerified: rec.Bool(colCAdmTLogOK),
		CoverageNote: rec.String(colCAdmCoverage), Reason: rec.String(colCAdmReason),
		Note: rec.String(colCAdmNote), AttestedBy: rec.String(colCAdmAttBy), AttestedAt: rec.String(colCAdmAttAt),
	}
}

// connectorAdmitRequestDTO is the POST /entries/{id}/admit body for a connector
// entry: the attestation bundle (a Sigstore bundle: cosign download attestation /
// gh attestation download for the released plugin binary or OCI image), optional
// predicate narrowing, and the expected artifact digest. expected_digest is
// OPTIONAL here in a way it is not meaningful for MCP: when omitted, it defaults
// from the entry's spec.artifact_digest (see handleAdmitConnectorEntry).
type connectorAdmitRequestDTO struct {
	Bundle         json.RawMessage `json:"bundle"`
	PredicateTypes []string        `json:"predicate_types,omitempty"`
	ExpectedDigest string          `json:"expected_digest,omitempty"`
	Note           string          `json:"note,omitempty"`
}

type connectorAdmitResponseDTO struct {
	Admitted  bool                  `json:"admitted"`
	Enforced  bool                  `json:"enforced"`
	Admission connectorAdmissionDTO `json:"admission"`
}

// effectiveConnectorPredicates resolves the predicate allow-list: the request
// narrows the policy's set (or the defaults) and may never WIDEN it — a
// per-request predicate outside the policy is dropped, and an empty intersection
// refuses (deny-closed, inside VerifyAttestation).
func effectiveConnectorPredicates(policy connectorAdmissionPolicyDTO, requested []string) []string {
	allowed := policy.AllowedPredicates
	if len(allowed) == 0 {
		allowed = defaultConnectorPredicates()
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

// --- the admit-route kind dispatch ------------------------------------------------

// handleAdmitEntry is the POST /entries/{id}/admit dispatch (one route, S142):
// kind mcp → the MCP flow, kind connector → the S142 connector flow, any
// other kind → 400. The kind peek here is ROUTING only, never the authoritative
// guard: a draft entry's kind is editable, so each flow re-reads the entry and
// re-checks the kind inside its own mutation transaction (the same record it
// verifies against is the record it gates on).
func (m *Module) handleAdmitEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		kind     string
		notFound bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
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
		kind = entry.String(colEntryKind)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	switch kind {
	case kindMCP:
		m.handleAdmitMCPEntry(w, r, mc)
	case kindConnector:
		m.handleAdmitConnectorEntry(w, r, mc)
	default:
		writeJSON(w, http.StatusBadRequest, errorBody(admitWrongKindMsg))
	}
}

// handleAdmitConnectorEntry verifies a supply-chain attestation for a connector
// catalog entry and records the claim-vs-verified verdict (upsert). Deny-closed
// under the active policy; a malformed bundle is 400; a well-formed bundle that
// fails to verify is a RECORDED verdict (200), exactly like.
func (m *Module) handleAdmitConnectorEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in connectorAdmitRequestDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Bundle) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("bundle (a Sigstore attestation bundle) is required"))
		return
	}
	var (
		out      connectorAdmitResponseDTO
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
		if entry.String(colEntryKind) != kindConnector {
			badInput = admitWrongKindMsg
			return nil
		}
		policy, _, err := loadConnectorAdmissionPolicy(r, sc)
		if err != nil {
			return err
		}

		// EXPECTED-DIGEST DEFAULTING (S142, the delta vs the MCP flow): when the
		// request omits expected_digest, default it from the entry's
		// spec.artifact_digest. The catalog entry NAMES the artifact it curates, so
		// the admission binds to THAT artifact by default — otherwise a valid
		// attestation over a different build of the same connector would silently
		// certify the entry. An explicit request value overrides (e.g. re-binding
		// ahead of a spec correction). A spec digest that is present but not
		// sha256-hex is passed through and REFUSED by the verifier
		// (supplied-but-unusable pin, deny-closed), never silently dropped.
		expectedDigest := in.ExpectedDigest
		if expectedDigest == "" {
			if specVal, present := parseSpec(entry.String(colSpec))[specKeyArtifactDigest]; present {
				// The spec PINS an artifact. If it is a usable string, bind to it. If it
				// is present-but-unusable (a JSON number/bool/object, or an empty/
				// whitespace string), it is a supplied-but-unusable pin: pass a
				// deliberately-invalid digest so VerifyAttestation REFUSES it (deny-
				// closed), instead of letting the failed `.(string)` assertion silently
				// degrade to an UNBOUND-but-"verified" verdict — present-but-unusable
				// must refuse, exactly as the comment above promises.
				if d, isStr := specVal.(string); isStr && strings.TrimSpace(d) != "" {
					expectedDigest = d
				} else {
					expectedDigest = "invalid: spec.artifact_digest is present but not a usable sha256 string"
				}
			}
		}

		verdict, verr := modelsign.VerifyAttestation(in.Bundle, policy.trustPolicy(), effectiveConnectorPredicates(policy, in.PredicateTypes), expectedDigest)
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
			colCAdmEntry:   id.String(),
			colCAdmSubject: verdict.SubjectName, colCAdmDigest: verdict.SubjectDigest,
			colCAdmPredicate: verdict.PredicateType, colCAdmMethod: verdict.Method,
			colCAdmIdentity: verdict.SignerIdentity, colCAdmIssuer: verdict.SignerIssuer,
			colCAdmRoots:    marshalJSONStrings(verdict.SignerRoots),
			colCAdmVerified: verdict.Verified, colCAdmArtifact: verdict.ArtifactVerified,
			colCAdmTLogSeen: verdict.TransparencyLogPresent, colCAdmTLogOK: verdict.TransparencyLogVerified,
			colCAdmCoverage: admissionCoverageNote(verdict), colCAdmReason: verdict.Reason,
			colCAdmNote: in.Note, colCAdmAttBy: actor, colCAdmAttAt: at,
		}
		repo, err := sc.Ext(connectorAdmissionKind)
		if err != nil {
			return err
		}
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colCAdmEntry, id.String())}, Limit: 1})
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
		out = connectorAdmitResponseDTO{Admitted: admitted, Enforced: policy.RequireSigned, Admission: toConnectorAdmissionDTO(saved)}
		_, err = sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: actor, ActorKind: mc.Principal.ActorKind(),
			Action: "catalog.connector_admission.admit", TargetKind: connectorAdmissionKind,
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

// handleListConnectorAdmissions lists the recorded connector-entry admission verdicts.
func (m *Module) handleListConnectorAdmissions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("entry_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colCAdmEntry, v))
	}
	if r.URL.Query().Get("verified") == "true" {
		q.Filters = append(q.Filters, model.Filter{Column: colCAdmVerified, Op: model.OpEq, Value: true})
	}
	out := listResponse[connectorAdmissionDTO]{Items: []connectorAdmissionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(connectorAdmissionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toConnectorAdmissionDTO(rec))
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

// --- the deny-closed approve gate (entries.go calls this for kindConnector) ------

// connectorAdmissionRefusal returns a non-empty reason when a connector entry
// must NOT be approved (deny-closed), or "" when approval may proceed. Observe
// mode (no policy / require_signed off) never gates — the existing estate keeps
// working until the tenant opts in, exactly like the model and MCP overlays.
//
// spec is the entry's CURRENT parsed spec (handleApproveEntry already parses it).
// It is taken here for the same reason modelAdmissionRefusal takes it: the gate
// must bind the stored verdict to the artifact the entry curates RIGHT NOW
// (spec.artifact_digest), not just to the verdict's own booleans. A draft entry's
// spec is editable (handleUpdateEntry) and the verdict survives that edit, so a
// verdict bound to digest X must not certify an entry that now curates digest Y —
// checking only signature_verified/artifact_verified would let an edit-after-admit
// swap the curated build out from under a stale verdict. This makes the file
// header's invariant ("approved connector entry ⇒ its provenance/SBOM was verified
// for the artifact it curates") real: editing the curated digest after an
// admission invalidates the gate and forces a re-admit against the new artifact.
func connectorAdmissionRefusal(r *http.Request, sc store.Scope, entryID model.ID, spec map[string]any) (string, error) {
	policy, ok, err := loadConnectorAdmissionPolicy(r, sc)
	if err != nil {
		return "", err
	}
	if !ok || !policy.RequireSigned {
		return "", nil
	}
	repo, err := sc.Ext(connectorAdmissionKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return "deny-closed: signed connector-entry admission is required but the admission entity is unavailable", nil
		}
		return "", err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colCAdmEntry, entryID.String())}, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "deny-closed: this connector entry has no attestation-admission verdict (POST /entries/{id}/admit a provenance/SBOM attestation before approving it as a verified connector)", nil
	}
	rec := recs[0]
	if !rec.Bool(colCAdmVerified) {
		reason := rec.String(colCAdmReason)
		if reason == "" {
			reason = "attestation not verified"
		}
		return "deny-closed: this connector entry's attestation admission did not verify (" + reason + ")", nil
	}
	if policy.RequireSubjectDigest && !rec.Bool(colCAdmArtifact) {
		return "deny-closed: policy requires the attestation subject to be bound to the expected artifact digest, which is unconfirmed for this entry", nil
	}
	// ANCHOR RE-CHECK: the verdict's booleans were computed against the trust policy AT
	// ADMIT TIME. Re-check that the anchor that verified it is STILL trusted by the CURRENT
	// policy, so a rotated-out trusted key (a compromised anchor removed) cannot keep
	// certifying via a stale verdict. The recorded signer identity is enough — no bundle.
	if !modelsign.AnchorStillTrusted(policy.trustPolicy(), modelsign.RecordedAnchor{
		Identity: rec.String(colCAdmIdentity), Issuer: rec.String(colCAdmIssuer),
		Roots: parseJSONStrings(rec.String(colCAdmRoots)), Method: rec.String(colCAdmMethod),
	}) {
		return "deny-closed: the trust anchor that admitted this connector entry is no longer in the tenant's admission policy (it was rotated out or its anchoring root was replaced); re-admit an attestation under the current trust anchors before approving", nil
	}

	// DIGEST-BINDING RE-CHECK (closes the edit-after-admit hole): the verdict's
	// booleans alone do not say WHICH artifact was verified. Compute the digest the
	// entry curates NOW the same way admit defaults its expected_digest (the entry
	// spec's artifact_digest), normalized identically to the verifier, and require
	// the recorded verdict to have bound to it.
	curated, _ := spec[specKeyArtifactDigest].(string)
	curatedNorm := normalizeConnectorDigest(curated)
	if curatedNorm != "" {
		// The entry curates a usable sha256: the verdict MUST have bound to exactly
		// this artifact. A verdict over a different (now-stale) build cannot certify
		// the entry's current curated artifact, even though its booleans are true.
		if normalizeConnectorDigest(rec.String(colCAdmDigest)) != curatedNorm {
			return "deny-closed: the recorded admission verdict was for a different artifact than the one this entry now curates; re-admit an attestation for the current spec.artifact_digest before approving", nil
		}
		return "", nil
	}
	// The entry has NO usable curated digest (absent, or present-but-unusable). Note
	// admit's defaulting would NOT have set an expected_digest from an absent spec
	// digest, so any artifact binding could only have come from an EXPLICIT request
	// digest — which we cannot tie back to "the artifact this entry curates" because
	// the entry names none. Under require_subject_digest that is not provable
	// binding: artifact_verified alone (true above) does not establish WHAT was
	// bound matches the curated artifact, so keep it refused.
	if policy.RequireSubjectDigest {
		return "deny-closed: this connector entry curates no usable spec.artifact_digest, so the recorded admission cannot be proven to bind to the artifact it curates; pin spec.artifact_digest and re-admit before approving", nil
	}
	// require_signed only (no require_subject_digest): preserve the looser, existing
	// behavior — a verified signature is enough, and the absence of a curated digest
	// is not itself a refusal. Only a PRESENT-but-mismatching curated digest (handled
	// in the curatedNorm != "" branch above) refuses here.
	return "", nil
}

// normalizeConnectorDigest mirrors modelsign.normalizeSHA256 (which is unexported
// there): lowercase, strip an optional "sha256:" prefix, and return "" for
// anything that is not a 64-char hex string. Used to compare the entry's curated
// spec.artifact_digest against the verdict's recorded subject_digest the SAME way
// the verifier compared the bundle subject against the expected digest at admit —
// a malformed digest must never accidentally compare equal.
func normalizeConnectorDigest(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) != 64 {
		return ""
	}
	for _, c := range d {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return d
}
