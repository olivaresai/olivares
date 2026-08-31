// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// Signed-model admission (G15). Extends the SOFTWARE supply-chain machinery
// (cosign/SBOM/in-toto) to MODELS and DATASETS: verify the signature/provenance
// of a self-hosted or third-party model artifact (OpenSSF Model Signing v1.0 /
// Sigstore model-signing) before it is admitted, deny-closed PER POLICY.
//
// Two entities, both AGPL, both self-audited to the real principal:
//
//   - models.admission_policy: the per-tenant trust root — require_signed plus the
//     allow-lists (Sigstore identities/issuers, bare keys, CA roots) that
//     core/secure/modelsign verifies against. Default ABSENT ⇒ require_signed=false
//     ⇒ OBSERVE mode (record verdicts, enforce nothing): this never breaks the
//     existing all-unsigned estate. An operator opts INTO deny-closed enforcement.
//   - models.model_admission: one verdict per model_version, claim-vs-verified
//     exactly like models.gpai_posture — signature_verified is the honest core flag,
//     promoted only by a real cryptographic verification, never by a client claim.
//
// HONEST SCOPE: admission covers SELF-HOSTED / THIRD-PARTY model artifacts (Ollama/
// vLLM/HuggingFace-style weights) and datasets — NOT Claude inference (Anthropic
// publishes no weights §G3). The runtime deny-closed gate is therefore the
// self-hosted inference_deployment (owned.go), not the brokered routing/execute path.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure/modelsign"
	"github.com/olivaresai/olivares/core/store"
)

// Entity kinds for signed-model admission.
const (
	admissionPolicyKind model.Kind = "models.admission_policy"
	modelAdmissionKind  model.Kind = "models.model_admission"
)

const (
	admissionPolicyTable = "models_admission_policy"
	modelAdmissionTable  = "models_model_admission"
)

// admission_policy columns (the per-tenant trust root). The key/cert lists hold
// PUBLIC material only (PEM public keys / CA certificates) — never a private key
// (validated on write); they are not secrets (docs/SECURITY-HARDENING.md).
const (
	colAPScope        = "policy_scope" // singleton marker ("default")
	colAPRequire      = "require_signed"
	colAPRequireDig   = "require_artifact_digests"
	colAPIdentities   = "allowed_identities" // JSON []string (regexp)
	colAPIssuers      = "allowed_issuers"    // JSON []string
	colAPKeys         = "trusted_keys"       // JSON []string (PEM PUBLIC KEY)
	colAPRoots        = "trusted_roots"      // JSON []string (PEM CERTIFICATE)
	colAPNote         = "note"
	colAPAttestedBy   = "attested_by"
	colAPAttestedAt   = "attested_at"
	admissionPolicyID = "default"
)

// model_admission columns. signature_verified mirrors the gpai_posture verified
// flag: it is the deny-closed core verdict, true only after a real verification.
const (
	colAdmVersion   = "version_ref"
	colAdmModelRef  = "model_ref" // optional: the brokered/served model ref the artifact corresponds to
	colAdmSubject   = "subject_name"
	colAdmDigest    = "subject_digest"
	colAdmPredicate = "predicate_type"
	colAdmMethod    = "method"
	colAdmIdentity  = "signer_identity"
	colAdmIssuer    = "signer_issuer"
	colAdmRoots     = "signer_roots" // JSON []string of "root:<fp>" anchoring-root markers
	colAdmVerified  = "signature_verified"
	colAdmArtifact  = "artifact_verified"
	colAdmTLogSeen  = "tlog_present"
	colAdmTLogOK    = "tlog_verified"
	colAdmResources = "resource_count"
	colAdmCoverage  = "coverage_note"
	colAdmAIBOM     = "aibom_ref"
	colAdmReason    = "reason"
	colAdmNote      = "note"
	colAdmAttBy     = "attested_by"
	colAdmAttAt     = "attested_at"
)

// registerAdmissionSchemas registers the admission policy + model-admission
// entities. Each unique index leads with tenant_id.
func registerAdmissionSchemas(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind: admissionPolicyKind, Table: admissionPolicyTable,
		Fields: []model.FieldSpec{
			{Name: colAPScope, Kind: model.KindText, Indexed: true},
			{Name: colAPRequire, Kind: model.KindBool, Indexed: true},
			{Name: colAPRequireDig, Kind: model.KindBool},
			{Name: colAPIdentities, Kind: model.KindJSON, Nullable: true},
			{Name: colAPIssuers, Kind: model.KindJSON, Nullable: true},
			{Name: colAPKeys, Kind: model.KindJSON, Nullable: true},
			{Name: colAPRoots, Kind: model.KindJSON, Nullable: true},
			{Name: colAPNote, Kind: model.KindText, Nullable: true},
			{Name: colAPAttestedBy, Kind: model.KindText, Nullable: true},
			{Name: colAPAttestedAt, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{Name: "models_admission_policy_uniq", Columns: []string{model.ColTenantID, colAPScope}, Unique: true}},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind: modelAdmissionKind, Table: modelAdmissionTable,
		Fields: []model.FieldSpec{
			{Name: colAdmVersion, Kind: model.KindText, Indexed: true},
			{Name: colAdmModelRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colAdmSubject, Kind: model.KindText, Nullable: true},
			{Name: colAdmDigest, Kind: model.KindText, Nullable: true},
			{Name: colAdmPredicate, Kind: model.KindText, Nullable: true},
			{Name: colAdmMethod, Kind: model.KindText, Nullable: true},
			{Name: colAdmIdentity, Kind: model.KindText, Nullable: true},
			{Name: colAdmIssuer, Kind: model.KindText, Nullable: true},
			{Name: colAdmRoots, Kind: model.KindJSON, Nullable: true},
			{Name: colAdmVerified, Kind: model.KindBool, Indexed: true},
			{Name: colAdmArtifact, Kind: model.KindBool},
			{Name: colAdmTLogSeen, Kind: model.KindBool},
			{Name: colAdmTLogOK, Kind: model.KindBool},
			{Name: colAdmResources, Kind: model.KindInt},
			{Name: colAdmCoverage, Kind: model.KindText, Nullable: true},
			{Name: colAdmAIBOM, Kind: model.KindText, Nullable: true},
			{Name: colAdmReason, Kind: model.KindText, Nullable: true},
			{Name: colAdmNote, Kind: model.KindText, Nullable: true},
			{Name: colAdmAttBy, Kind: model.KindText, Nullable: true},
			{Name: colAdmAttAt, Kind: model.KindText, Nullable: true},
		},
		// One admission verdict per model_version per tenant (re-admit upserts).
		Indexes: []model.IndexSpec{{Name: "models_model_admission_uniq", Columns: []string{model.ColTenantID, colAdmVersion}, Unique: true}},
	})
}

// --- admission policy (trust root) ------------------------------------------

type admissionPolicyDTO struct {
	RequireSigned          bool     `json:"require_signed"`
	RequireArtifactDigests bool     `json:"require_artifact_digests"`
	AllowedIdentities      []string `json:"allowed_identities,omitempty"`
	AllowedIssuers         []string `json:"allowed_issuers,omitempty"`
	TrustedKeys            []string `json:"trusted_keys,omitempty"`
	TrustedRoots           []string `json:"trusted_roots,omitempty"`
	Note                   string   `json:"note,omitempty"`
	AttestedBy             string   `json:"attested_by,omitempty"`
	AttestedAt             string   `json:"attested_at,omitempty"`
}

// validate rejects PRIVATE-key PEM in the public trust lists (minimal-data, docs/SECURITY-HARDENING.md
// §3): the trust root holds public verification material only, never a secret.
func (d *admissionPolicyDTO) validate() string {
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
	return ""
}

func (d admissionPolicyDTO) toRecord(actor, at string) model.Record {
	return model.Record{
		colAPScope: admissionPolicyID, colAPRequire: d.RequireSigned, colAPRequireDig: d.RequireArtifactDigests,
		colAPIdentities: marshalStrings(d.AllowedIdentities), colAPIssuers: marshalStrings(d.AllowedIssuers),
		colAPKeys: marshalStrings(d.TrustedKeys), colAPRoots: marshalStrings(d.TrustedRoots),
		colAPNote: trimClamp(d.Note), colAPAttestedBy: actor, colAPAttestedAt: at,
	}
}

func toAdmissionPolicyDTO(rec model.Record) admissionPolicyDTO {
	return admissionPolicyDTO{
		RequireSigned: rec.Bool(colAPRequire), RequireArtifactDigests: rec.Bool(colAPRequireDig),
		AllowedIdentities: parseStrings(rec.String(colAPIdentities)), AllowedIssuers: parseStrings(rec.String(colAPIssuers)),
		TrustedKeys: parseStrings(rec.String(colAPKeys)), TrustedRoots: parseStrings(rec.String(colAPRoots)),
		Note: rec.String(colAPNote), AttestedBy: rec.String(colAPAttestedBy), AttestedAt: rec.String(colAPAttestedAt),
	}
}

// trustPolicy maps the stored admission policy to the verifier's trust anchor.
func (d admissionPolicyDTO) trustPolicy() modelsign.TrustPolicy {
	return modelsign.TrustPolicy{
		Roots: d.TrustedRoots, AllowedIdentities: d.AllowedIdentities,
		AllowedIssuers: d.AllowedIssuers, Keys: d.TrustedKeys,
	}
}

// loadAdmissionPolicy reads the per-tenant singleton policy. A missing policy is the
// honest default (require_signed=false, observe mode), never an error — mirroring
// compliance's tolerance of an absent sibling.
func loadAdmissionPolicy(r *http.Request, sc store.Scope) (admissionPolicyDTO, bool, error) {
	repo, err := sc.Ext(admissionPolicyKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return admissionPolicyDTO{}, false, nil
		}
		return admissionPolicyDTO{}, false, err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colAPScope, admissionPolicyID)}, Limit: 1})
	if err != nil {
		return admissionPolicyDTO{}, false, err
	}
	if len(recs) == 0 {
		return admissionPolicyDTO{}, false, nil
	}
	return toAdmissionPolicyDTO(recs[0]), true, nil
}

func (m *Module) handleGetAdmissionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var (
		out admissionPolicyDTO
		ok  bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var e error
		out, ok, e = loadAdmissionPolicy(r, sc)
		return e
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !ok {
		// Honest default: no policy ⇒ observe mode, nothing is enforced yet.
		writeJSON(w, http.StatusOK, map[string]any{
			"require_signed":           false,
			"require_artifact_digests": false,
			"configured":               false,
			"note":                     "no model-admission policy configured; signed-model admission runs in OBSERVE mode (verdicts are recorded, nothing is denied). Set require_signed with an allow-list to enforce.",
		})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePutAdmissionPolicy upserts the per-tenant trust root. Admin-tier, audited.
func (m *Module) handlePutAdmissionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in admissionPolicyDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if in.RequireSigned && len(in.TrustedRoots) == 0 && len(in.TrustedKeys) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("require_signed=true needs at least one trusted_root (Sigstore/Fulcio or PKI) or trusted_key (bare key); a deny-closed gate with no trust anchor would reject everything"))
		return
	}
	// Keyless verification pins BOTH the signer identity and the OIDC issuer (cosign
	// semantics): configuring one without the other is a misconfiguration that would
	// silently reject every certificate, so require them together or neither.
	if (len(in.AllowedIdentities) > 0) != (len(in.AllowedIssuers) > 0) {
		writeJSON(w, http.StatusBadRequest, errorBody("keyless verification requires BOTH allowed_identities and allowed_issuers (cosign-style: pin identity + OIDC issuer together); set both or neither"))
		return
	}
	var out admissionPolicyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(admissionPolicyKind)
		if err != nil {
			return err
		}
		actor := mc.Principal.Actor()
		at := model.NewTimestamp(time.Now()).String()
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colAPScope, admissionPolicyID)}, Limit: 1})
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
		out = toAdmissionPolicyDTO(rec)
		return auditOwned(r.Context(), sc, mc, admissionPolicyKind, "configure", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- model admission verdict ------------------------------------------------

type modelAdmissionDTO struct {
	ID                string   `json:"id,omitempty"`
	VersionRef        string   `json:"version_ref"`
	ModelRef          string   `json:"model_ref,omitempty"`
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
	ResourceCount     int64    `json:"resource_count"`
	CoverageNote      string   `json:"coverage_note,omitempty"`
	AIBOMRef          string   `json:"aibom_ref,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	Note              string   `json:"note,omitempty"`
	AttestedBy        string   `json:"attested_by,omitempty"`
	AttestedAt        string   `json:"attested_at,omitempty"`
}

func toModelAdmissionDTO(rec model.Record) modelAdmissionDTO {
	return modelAdmissionDTO{
		ID: rec.String(model.ColID), VersionRef: rec.String(colAdmVersion), ModelRef: rec.String(colAdmModelRef),
		SubjectName: rec.String(colAdmSubject), SubjectDigest: rec.String(colAdmDigest), PredicateType: rec.String(colAdmPredicate),
		Method: rec.String(colAdmMethod), SignerIdentity: rec.String(colAdmIdentity), SignerIssuer: rec.String(colAdmIssuer),
		SignerRoots:       parseStrings(rec.String(colAdmRoots)),
		SignatureVerified: rec.Bool(colAdmVerified), ArtifactVerified: rec.Bool(colAdmArtifact),
		TLogPresent: rec.Bool(colAdmTLogSeen), TLogVerified: rec.Bool(colAdmTLogOK), ResourceCount: rec.Int(colAdmResources),
		CoverageNote: rec.String(colAdmCoverage), AIBOMRef: rec.String(colAdmAIBOM), Reason: rec.String(colAdmReason),
		Note: rec.String(colAdmNote), AttestedBy: rec.String(colAdmAttBy), AttestedAt: rec.String(colAdmAttAt),
	}
}

// admitRequestDTO is the POST /model-versions/{id}/admit body: the OMS signature
// bundle to verify, optional locally-computed per-file digests (to additionally
// re-hash the on-disk artifact against the signed manifest), and metadata refs.
type admitRequestDTO struct {
	Bundle          json.RawMessage   `json:"bundle"`
	ResolvedDigests map[string]string `json:"resolved_digests,omitempty"`
	ModelRef        string            `json:"model_ref,omitempty"`
	AIBOMRef        string            `json:"aibom_ref,omitempty"`
	Note            string            `json:"note,omitempty"`
}

// admitResponseDTO is the verdict plus the admitted decision under the active policy.
type admitResponseDTO struct {
	Admitted  bool              `json:"admitted"`
	Enforced  bool              `json:"enforced"` // whether require_signed was on (deny-closed) vs observe
	Admission modelAdmissionDTO `json:"admission"`
}

// handleAdmitVersion verifies an OMS v1.0 signature for a model_version and records
// the verdict (upsert, claim-vs-verified). It is DENY-CLOSED under the active policy:
// with require_signed on, a verdict that does not verify is recorded and the model is
// NOT admitted (admitted=false). With no policy (observe mode) the verdict is recorded
// and admitted reflects the raw verification result. A malformed bundle is a 400; a
// well-formed bundle that fails to verify is a recorded verdict (200), not an error.
func (m *Module) handleAdmitVersion(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	versionID := chi.URLParam(r, "id")
	if model.ID(versionID).IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in admitRequestDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Bundle) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("bundle (an OMS/Sigstore signature bundle) is required"))
		return
	}

	var (
		out      admitResponseDTO
		badInput string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// The model_version must exist in this tenant (referential sanity).
		if err := checkRef(r.Context(), sc, modelVersionKind, versionID); err != nil {
			return err
		}
		policy, _, err := loadAdmissionPolicy(r, sc)
		if err != nil {
			return err
		}
		verdict, verr := modelsign.Verify(in.Bundle, policy.trustPolicy(), in.ResolvedDigests)
		if errors.Is(verr, modelsign.ErrMalformedBundle) {
			badInput = "bundle is not a parseable OMS/Sigstore signature: " + verr.Error()
			return nil
		}
		if verr != nil {
			return verr
		}

		admitted := verdict.Verified
		if policy.RequireArtifactDigests {
			admitted = admitted && verdict.ArtifactVerified
		}

		actor := mc.Principal.Actor()
		at := model.NewTimestamp(time.Now()).String()
		rec := model.Record{
			colAdmVersion: versionID, colAdmModelRef: trimClamp(in.ModelRef),
			colAdmSubject: trimClamp(verdict.SubjectName), colAdmDigest: trimClamp(verdict.SubjectDigest),
			colAdmPredicate: trimClamp(verdict.PredicateType), colAdmMethod: verdict.Method,
			colAdmIdentity: trimClamp(verdict.SignerIdentity), colAdmIssuer: trimClamp(verdict.SignerIssuer),
			colAdmRoots:    marshalStrings(verdict.SignerRoots),
			colAdmVerified: verdict.Verified, colAdmArtifact: verdict.ArtifactVerified,
			colAdmTLogSeen: verdict.TransparencyLogPresent, colAdmTLogOK: verdict.TransparencyLogVerified,
			colAdmResources: int64(verdict.ResourceCount), colAdmCoverage: coverageNote(verdict),
			colAdmAIBOM: trimClamp(in.AIBOMRef), colAdmReason: trimClamp(verdict.Reason),
			colAdmNote: trimClamp(in.Note), colAdmAttBy: actor, colAdmAttAt: at,
		}

		repo, err := sc.Ext(modelAdmissionKind)
		if err != nil {
			return err
		}
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colAdmVersion, versionID)}, Limit: 1})
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
		out = admitResponseDTO{Admitted: admitted, Enforced: policy.RequireSigned, Admission: toModelAdmissionDTO(saved)}
		return auditOwned(r.Context(), sc, mc, modelAdmissionKind, "admit", model.ID(saved.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if badInput != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(badInput))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// coverageNote folds the verifier's honest coverage notes (artifact re-hash + tlog
// seam) into one human-readable line for the stored verdict.
func coverageNote(v modelsign.Verdict) string {
	parts := []string{}
	if v.ArtifactCoverage != "" {
		parts = append(parts, v.ArtifactCoverage)
	}
	if v.TransparencyLogNote != "" {
		parts = append(parts, v.TransparencyLogNote)
	}
	return strings.Join(parts, "; ")
}

func (m *Module) handleListAdmissions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("version_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colAdmVersion, v))
	}
	// The signature-verification result is a real two-sided filter: verified=true
	// lists admitted verdicts, verified=false lists the denied/failed ones (the
	// triage view the "Unverified only" console control needs). Any other value
	// (or none) omits the filter and returns the full history.
	if v := r.URL.Query().Get("verified"); v == "true" {
		q.Filters = append(q.Filters, model.Filter{Column: colAdmVerified, Op: model.OpEq, Value: true})
	} else if v == "false" {
		q.Filters = append(q.Filters, model.Filter{Column: colAdmVerified, Op: model.OpEq, Value: false})
	}
	out := listResponse[modelAdmissionDTO]{Items: []modelAdmissionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelAdmissionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toModelAdmissionDTO(rec))
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

// admissionDeniesDeployment is the runtime deny-closed gate for SELF-HOSTED models. It
// is discriminated by deployment_type (D-08), NOT by the presence of a version_ref
// (inferring "not self-hosted" from an empty version_ref was the D-08 bypass):
//
//   - brokered — a hosted provider (e.g. Claude), no version_ref, never gated (contract §G3);
//   - stopped (any type) — not serving, so not gated;
//   - local (active) under require_signed — the version MUST have a verified admission
//     verdict (and a re-hashed artifact when the policy requires it), and the anchor that
//     admitted it must STILL be trusted; a missing/failed/version-less one is denied;
//   - unclassified (active) under require_signed — deny-closed: a pre-ambiguous row
//     must be classified before it can serve.
//
// Returns (true, reason) to deny. Consulted by the create/update handlers (owned.go).
func (m *Module) admissionDeniesDeployment(r *http.Request, sc store.Scope, deploymentType, status, versionRef string) (bool, string, error) {
	if deploymentType == depTypeBrokered {
		return false, "", nil
	}
	if status != "active" {
		return false, "", nil // a stopped deployment is not serving → not gated
	}
	policy, ok, err := loadAdmissionPolicy(r, sc)
	if err != nil || !ok || !policy.RequireSigned {
		return false, "", err // observe mode (or no policy): nothing enforced
	}
	if deploymentType == depTypeUnclassified {
		return true, "deny-closed: this deployment is unclassified under a require_signed policy; classify it (a local deployment of an admitted model version, or brokered) before activating it", nil
	}
	// deploymentType == local (validate guaranteed a non-empty version_ref, but stay
	// defensive: absence of a version can never be a pass under require_signed).
	if versionRef == "" {
		return true, "deny-closed: a local deployment under a require_signed policy must reference an admitted model version", nil
	}
	repo, err := sc.Ext(modelAdmissionKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			// Policy requires signing but the admission entity is unavailable: deny-closed.
			return true, "model-admission is required but no admission record exists for this version (deny-closed)", nil
		}
		return false, "", err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colAdmVersion, versionRef)}, Limit: 1})
	if err != nil {
		return false, "", err
	}
	if len(recs) == 0 {
		return true, "deny-closed: model-admission policy requires a verified signature, but this model version has no admission record (admit it first)", nil
	}
	rec := recs[0]
	if !rec.Bool(colAdmVerified) {
		reason := rec.String(colAdmReason)
		if reason == "" {
			reason = "signature not verified"
		}
		return true, "deny-closed: this model version's admission did not verify (" + reason + ")", nil
	}
	if policy.RequireArtifactDigests && !rec.Bool(colAdmArtifact) {
		return true, "deny-closed: policy requires the on-disk artifact to be re-hashed against the signed manifest, which has not been confirmed for this version", nil
	}
	// The verdict's booleans were computed against the trust policy AT ADMIT TIME. Re-check that
	// the anchor that verified it is STILL trusted by the CURRENT policy, so a rotated-out trusted
	// key/root (a compromised anchor removed) cannot keep certifying a deployment through a stale
	// verdict — the same revocation guard the catalog MCP/connector approve gates apply, here on
	// the model axis at the runtime deployment gate. The recorded signer anchor is enough; no bundle.
	if !modelsign.AnchorStillTrusted(policy.trustPolicy(), modelsign.RecordedAnchor{
		Identity: rec.String(colAdmIdentity), Issuer: rec.String(colAdmIssuer),
		Roots: parseStrings(rec.String(colAdmRoots)), Method: rec.String(colAdmMethod),
	}) {
		return true, "deny-closed: the trust anchor that admitted this model version is no longer in the tenant's admission policy (it was rotated out or its anchoring root was replaced); re-admit under the current trust anchors before deploying", nil
	}
	return false, "", nil
}

// --- shared JSON []string column helpers ------------------------------------

func marshalStrings(v []string) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseStrings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
