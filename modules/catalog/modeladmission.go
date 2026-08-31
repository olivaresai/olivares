// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

// the catalog's deny-closed admission overlay for MODEL entries (G15: the
// "admission gate in the catalog, XIV"). When a model entry is approved into the
// catalog and the tenant has opted INTO signed-model admission (models module's
// models.admission_policy.require_signed), the model_version the entry curates MUST
// have a VERIFIED admission verdict (models.model_admission.signature_verified). This
// makes "an approved MODEL catalog entry ⇒ its provenance was verified" a hard
// invariant of the curated inventory.
//
// It is a deliberate, scoped exception to the catalog's general "record, don't
// enforce" posture (which defers instantiation policy to governance) — sanctioned
// specifically for the model-admission gate. It is DECOUPLED: the catalog never
// imports the models module; it reads models.* by KIND string + column name via
// sc.Ext, exactly the pattern modules/compliance uses to probe sibling evidence. The
// policy default (absent / require_signed=false) means non-model entries and tenants
// that have not configured a trust root are entirely unaffected.

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure/modelsign"
	"github.com/olivaresai/olivares/core/store"
)

// Decoupled references to the models module's entities (KIND + column names
// only — the loose-coupling contract, no import).
const (
	admissionPolicyExtKind model.Kind = "models.admission_policy"
	modelAdmissionExtKind  model.Kind = "models.model_admission"
	colExtPolicyScope                 = "policy_scope"
	colExtPolicyScopeValue            = "default"
	colExtRequireSigned               = "require_signed"
	colExtRequireArtifact             = "require_artifact_digests"
	colExtAdmVersion                  = "version_ref"
	colExtAdmVerified                 = "signature_verified"
	colExtAdmArtifact                 = "artifact_verified"
	colExtAdmReason                   = "reason"
	// Anchor columns on models.model_admission, re-checked against the current policy so a
	// rotated-out trust anchor cannot keep approving via a stale verdict (parity with the MCP and
	// connector approve gates). Read by column name — the decoupled sc.Ext contract, no import.
	colExtAdmMethod   = "method"
	colExtAdmIdentity = "signer_identity"
	colExtAdmIssuer   = "signer_issuer"
	colExtAdmRoots    = "signer_roots" // JSON []string of "root:<fp>" anchoring-root markers
	// Trust-anchor columns on models.admission_policy, used to reconstruct the verifier's
	// TrustPolicy for the anchor re-check.
	colExtPolIdentities = "allowed_identities"
	colExtPolIssuers    = "allowed_issuers"
	colExtPolKeys       = "trusted_keys"
	colExtPolRoots      = "trusted_roots"
	specKeyVersionRef   = "version_ref"
)

// modelAdmissionRefusal returns a non-empty reason when a model entry must NOT be
// approved (deny-closed), or "" when approval may proceed. It allows approval when
// the tenant has no admission policy or require_signed is off (observe mode); a
// non-nil error is a store error to surface as 5xx, distinct from a deny reason.
func modelAdmissionRefusal(r *http.Request, sc store.Scope, spec map[string]any) (string, error) {
	require, requireArtifact, tp, ok, err := loadRequireSigned(r, sc)
	if err != nil {
		return "", err
	}
	if !ok || !require {
		return "", nil // no policy / observe mode → no enforcement
	}
	// Defense against decoupled-schema drift: the catalog reads the models trust policy by column
	// NAME (no import). On the enforcing path the policy MUST carry a trust anchor (models validates
	// require_signed ⇒ ≥1 root/key) and, if keyless, BOTH allow-lists (models validates both-or-
	// neither). If the reconstruction is anchor-less or half-configured, a models column was renamed/
	// dropped and we could NOT read the policy faithfully — deny-closed rather than risk skipping a
	// pin (fail-closed under drift). Valid policies never trip this; only an unreadable/corrupt one.
	if len(tp.Roots) == 0 && len(tp.Keys) == 0 {
		return "deny-closed: signed-model admission is enforced but the tenant's trust policy exposes no readable anchor (roots/keys); the models admission-policy schema may have drifted — re-check the policy", nil
	}
	if (len(tp.AllowedIdentities) > 0) != (len(tp.AllowedIssuers) > 0) {
		return "deny-closed: the tenant's trust policy has an inconsistent keyless allow-list (identities and issuers must be configured together); the models admission-policy schema may have drifted", nil
	}

	versionRef, _ := spec[specKeyVersionRef].(string)
	if versionRef == "" {
		return "deny-closed: signed-model admission is required, but this model entry's spec has no version_ref identifying the admitted model version", nil
	}
	repo, err := sc.Ext(modelAdmissionExtKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			// Policy requires signing but the models admission entity is unavailable.
			return "deny-closed: signed-model admission is required but no model-admission record exists", nil
		}
		return "", err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colExtAdmVersion, versionRef)}, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "deny-closed: this model version has no signed-model admission verdict (admit it via the models module before approving it into the catalog)", nil
	}
	rec := recs[0]
	if !rec.Bool(colExtAdmVerified) {
		reason := rec.String(colExtAdmReason)
		if reason == "" {
			reason = "signature not verified"
		}
		return "deny-closed: this model version's signed-model admission did not verify (" + reason + ")", nil
	}
	if requireArtifact && !rec.Bool(colExtAdmArtifact) {
		return "deny-closed: policy requires the artifact to be re-hashed against the signed manifest, which is unconfirmed for this version", nil
	}
	// Re-check that the anchor which verified this version at admit time is STILL trusted by the
	// CURRENT policy — a rotated-out trusted key/root must not keep approving via a stale verdict
	// (parity with the MCP/connector approve gates and the models runtime deployment gate). Missing
	// or malformed anchor columns deny-close (AnchorStillTrusted rejects an unknown method), so an
	// approved MODEL catalog entry always maps to a currently-anchored verdict.
	if !modelsign.AnchorStillTrusted(tp, modelsign.RecordedAnchor{
		Identity: rec.String(colExtAdmIdentity), Issuer: rec.String(colExtAdmIssuer),
		Roots: parseJSONStrings(rec.String(colExtAdmRoots)), Method: rec.String(colExtAdmMethod),
	}) {
		return "deny-closed: the trust anchor that admitted this model version is no longer in the tenant's admission policy (it was rotated out or its anchoring root was replaced); re-admit an attestation under the current trust anchors before approving it into the catalog", nil
	}
	return "", nil
}

// loadRequireSigned reads the tenant's models.admission_policy singleton. Returns
// (require_signed, require_artifact_digests, the current TrustPolicy, configured, err). An
// absent policy entity (models module not loaded) or no row is the honest default: not
// configured. The TrustPolicy is reconstructed from the stored trust-anchor columns so the
// approve gate can re-check that a verdict's anchor is STILL trusted (parity with MCP/connector).
func loadRequireSigned(r *http.Request, sc store.Scope) (require, requireArtifact bool, tp modelsign.TrustPolicy, configured bool, err error) {
	repo, err := sc.Ext(admissionPolicyExtKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return false, false, modelsign.TrustPolicy{}, false, nil
		}
		return false, false, modelsign.TrustPolicy{}, false, err
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colExtPolicyScope, colExtPolicyScopeValue)}, Limit: 1})
	if err != nil {
		return false, false, modelsign.TrustPolicy{}, false, err
	}
	if len(recs) == 0 {
		return false, false, modelsign.TrustPolicy{}, false, nil
	}
	rec := recs[0]
	tp = modelsign.TrustPolicy{
		Roots:             parseJSONStrings(rec.String(colExtPolRoots)),
		AllowedIdentities: parseJSONStrings(rec.String(colExtPolIdentities)),
		AllowedIssuers:    parseJSONStrings(rec.String(colExtPolIssuers)),
		Keys:              parseJSONStrings(rec.String(colExtPolKeys)),
	}
	return rec.Bool(colExtRequireSigned), rec.Bool(colExtRequireArtifact), tp, true, nil
}
