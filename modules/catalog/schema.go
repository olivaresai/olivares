// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables.
const (
	entryKind     model.Kind = "catalog.entry"
	entryTable               = "catalog_entry"
	instanceKind  model.Kind = "catalog.instance"
	instanceTable            = "catalog_instance"
)

// Catalog entry kinds (what the entry curates).
const (
	kindAgent    = "agent"
	kindMCP      = "mcp"
	kindSkill    = "skill"
	kindTemplate = "template"
	// kindModel (G15) curates an admitted MODEL: a signed, versioned model
	// artifact published into the approved catalog (XIV). A model entry's spec
	// references the model_version it curates (spec.version_ref); approving it is
	// deny-closed under the tenant's signed-model-admission policy (modeladmission.go).
	kindModel = "model"
	// kindConnector (S142, EXT-3) curates a VERIFIED third-party connector — a
	// released, signed connector plugin artifact. A connector entry's spec records
	// the artifact it curates (artifact_digest sha256, release/OCI ref, publisher,
	// descriptor name); approving it is deny-closed under the tenant's
	// connector-admission policy (connectoradmission.go).
	kindConnector = "connector"
)

// Catalog entry lifecycle states. A registry artifact is created as a draft, may
// be submitted for review (pending), approved (frozen + hashed + optionally
// signed) and later deprecated. Only a draft is mutable.
const (
	statusDraft      = "draft"
	statusPending    = "pending"
	statusApproved   = "approved"
	statusDeprecated = "deprecated"
)

// Instance lifecycle states. A self-service instantiation is requested; the
// approval DECISION and provisioning belong to governance and deployment
// — this module records and transitions the request.
const (
	instRequested = "requested"
	instApproved  = "approved"
	instRejected  = "rejected"
	instActive    = "active"
)

// entry columns.
const (
	colEntryKind   = "entry_kind"
	colName        = "name"
	colSlug        = "slug"
	colVersion     = "semver" // the entry's semantic version ("version" is a reserved base column)
	colStatus      = "status"
	colSummary     = "summary"
	colSpec        = "spec"
	colOwnerRef    = "owner_ref"
	colContentHash = "content_hash"
	colSignature   = "signature"
	colSigAlg      = "sig_alg"
	colSignedBy    = "signed_by"
	colApprovedBy  = "approved_by"
	colApprovedAt  = "approved_at"
)

// instance columns.
const (
	colEntryID      = "entry_id"
	colEntrySlug    = "entry_slug"
	colEntryVersion = "entry_version"
	colInstName     = "name"
	colTargetRef    = "target_ref"
	colInstStatus   = "status"
	colRequestedBy  = "requested_by"
	colDecidedBy    = "decided_by"
	colNote         = "note"
)

// Principal declarations shared by the catalog entity descriptors (this file,
// mcpadmission.go and connectoradmission.go). Each cited line holds for every
// column that uses the declaration.
var (
	// pdeclActorEvidence is the acting principal's audit actor string ("user:<id>"
	// or "token:<id>"), recorded as provenance and only rendered.
	pdeclActorEvidence = model.Ref(model.EncodeUserRef, model.ClassEvidence)
	pdeclNoneEntryKind = model.None("a curated entry kind from a closed set: entries.go:23, entries.go:47-50, instances.go:101")
	// Attestation admission (MCP and connector entries): the policy's public trust
	// material and the verifier's recorded verdict. A signer identity is an
	// external certificate identity the verifier matches against the policy; no
	// reader resolves it to an account.
	pdeclNoneEntryRef      = model.None("a catalog entry row id, used only as a lookup filter: mcpadmission.go:587, connectoradmission.go:680")
	pdeclNonePolicyScope   = model.None("the admission policy singleton marker, a constant: mcpadmission.go:73, connectoradmission.go:76")
	pdeclNoneAdmitNote     = model.None("operator prose, only rendered: mcpadmission.go:217, mcpadmission.go:354, connectoradmission.go:234, connectoradmission.go:371")
	pdeclNoneAttestedAt    = model.None("a timestamp the writer formats: mcpadmission.go:288, mcpadmission.go:463, connectoradmission.go:305, connectoradmission.go:558")
	pdeclNoneSubjectName   = model.None("the signed in-toto subject name set by the verifier, only rendered: core/secure/modelsign/attestation.go:74, core/secure/modelsign/attestation.go:94")
	pdeclNoneSubjectDigest = model.None("a sha256 digest set by the verifier, only rendered: core/secure/modelsign/attestation.go:75, core/secure/modelsign/attestation.go:95")
	pdeclNonePredicate     = model.None("the in-toto predicate type, checked only against the allow-list: core/secure/modelsign/attestation.go:61-69")
	pdeclNoneMethod        = model.None("a signing method from a closed set: core/secure/modelsign/modelsign.go:41-45, core/secure/modelsign/modelsign.go:727")
	pdeclNoneSignerID      = model.None("the artifact signer's certificate SAN, subject DN or key fingerprint, compared only with the trust policy: core/secure/modelsign/modelsign.go:449, core/secure/modelsign/modelsign.go:464, core/secure/modelsign/modelsign.go:484, core/secure/modelsign/modelsign.go:752")
	pdeclNoneSignerIssuer  = model.None("an OIDC issuer URL, compared only with the trust policy: core/secure/modelsign/modelsign.go:459, core/secure/modelsign/modelsign.go:756")
	pdeclNoneCoverage      = model.None("verifier coverage prose, only rendered: mcpadmission.go:523-532")
	pdeclNoneVerdictReason = model.None("verifier prose, only rendered in a refusal: mcpadmission.go:596-600, connectoradmission.go:689")
	pdeclSignerRoots       = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a CA root fingerprint marker, compared only with the policy's roots: core/secure/modelsign/modelsign.go:640-643, core/secure/modelsign/modelsign.go:746")))
	pdeclAllowedIdentities = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a signer-identity regexp matched only against certificate SANs: core/secure/modelsign/modelsign.go:554-562, core/secure/modelsign/modelsign.go:586-594, core/secure/modelsign/modelsign.go:752")))
	pdeclAllowedIssuers = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("an OIDC issuer URL compared only with the certificate issuer: core/secure/modelsign/modelsign.go:465, core/secure/modelsign/modelsign.go:756")))
	pdeclTrustedKeys = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a PEM public key, private keys refused, parsed only by the verifier: mcpadmission.go:177-181, connectoradmission.go:194-198, core/secure/modelsign/modelsign.go:477-484")))
	pdeclTrustedRoots = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a PEM CA certificate, private keys refused, loaded only as a root pool: mcpadmission.go:182-186, connectoradmission.go:199-203, core/secure/modelsign/modelsign.go:392-397")))
	pdeclAllowedPredicates = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("an in-toto predicate type URI compared only with the statement type: core/secure/modelsign/attestation.go:66-69")))
)

// RegisterSchema declares the module's owned entities. It satisfies the
// engine-side runtime.SchemaProvider seam (structural — no runtime import) and is
// called once, at store construction, before any Scope exists (S02 §7 /).
// The engine creates the tables, injects the base columns and attaches the tenant
// guards; a module cannot opt out of isolation.
//
// The registry keys an entry uniquely by (kind, slug, version): each version is
// its own immutable artifact, so publishing a new version is a new row and
// approval/signing happen per version (README.md). The unique index leads with
// tenant_id so it cannot couple tenants or leak existence.
//
// Neither entity is descriptor-audited: the descriptor's auto-audit attributes a
// mutation to the SYSTEM actor, which would defeat the self-audit purpose ("who
// approved which entry"). Instead each privileged handler appends a semantic audit
// attributed to the real principal in the same transaction (entries.go,
// instances.go), exactly as module X's key governance does.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  entryKind,
		Table: entryTable,
		Fields: []model.FieldSpec{
			{Name: colEntryKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneEntryKind},
			{Name: colName, Kind: model.KindText, Principal: model.None("an entry display name, hashed and rendered only: entries.go:51-54, sign.go:35-39, entries.go:83")},
			{Name: colSlug, Kind: model.KindText, Indexed: true, Principal: model.None("a lowercase identifier slug, validated and matched only: entries.go:55-58, entries.go:126")},
			{Name: colVersion, Kind: model.KindText, Principal: model.None("a semantic version string, validated: entries.go:59-62")},
			{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: model.None("an entry lifecycle status from a closed set: entries.go:176, entries.go:347-352, entries.go:405-409, entries.go:456")},
			{Name: colSummary, Kind: model.KindText, Nullable: true, Principal: model.None("entry summary prose, hashed and rendered only: sign.go:35-39, entries.go:85")},
			{Name: colSpec, Kind: model.KindJSON, Nullable: true, Principal: model.None("a free-form curated spec; readers take only a model version ref and an artifact digest from it, hash it and render it: modeladmission.go:86, connectoradmission.go:714, sign.go:35-39, entries.go:85")},
			// A caller-declared owner label in no fixed encoding, hashed and
			// rendered only, so it is evidence.
			{Name: colOwnerRef, Kind: model.KindText, Nullable: true, Principal: model.Scan(model.ClassEvidence)},
			{Name: colContentHash, Kind: model.KindText, Nullable: true, Principal: model.None("a hex SHA-256 content pin, recomputed and compared only: sign.go:35-44, sign.go:99, entries.go:455")},
			{Name: colSignature, Kind: model.KindText, Nullable: true, Principal: model.None("a base64 Ed25519 signature over the content pin, verified only: sign.go:50-53, sign.go:104-106")},
			{Name: colSigAlg, Kind: model.KindText, Nullable: true, Principal: model.None("a signature algorithm label fixed in code: entries.go:464, entries.go:469")},
			{Name: colSignedBy, Kind: model.KindText, Nullable: true, Principal: model.None("a base64 Ed25519 public key, used only to verify and fingerprint: sign.go:54, sign.go:103-107, entries.go:91-92")},
			{Name: colApprovedBy, Kind: model.KindText, Nullable: true, Principal: pdeclActorEvidence},
			{Name: colApprovedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "catalog_entry_uniq",
			Columns: []string{model.ColTenantID, colEntryKind, colSlug, colVersion},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// signed MCP-entry admission (policy + verdicts; mcpadmission.go).
	if err := registerMCPAdmissionSchemas(reg); err != nil {
		return err
	}

	// S142: signed CONNECTOR-entry admission (policy + verdicts; connectoradmission.go).
	// Own kinds/tables, same shape as the MCP pair — evidence is counted by kind.
	if err := registerConnectorAdmissionSchemas(reg); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  instanceKind,
		Table: instanceTable,
		Fields: []model.FieldSpec{
			{Name: colEntryID, Kind: model.KindUUID, Indexed: true, Principal: model.None("the source catalog entry row id, written and rendered only: instances.go:100, instances.go:37")},
			{Name: colEntryKind, Kind: model.KindText, Principal: pdeclNoneEntryKind},
			{Name: colEntrySlug, Kind: model.KindText, Principal: model.None("the source entry's slug, copied and rendered only: instances.go:102, instances.go:38")},
			{Name: colEntryVersion, Kind: model.KindText, Principal: model.None("the source entry's semantic version, copied and rendered only: instances.go:103, instances.go:39")},
			{Name: colInstName, Kind: model.KindText, Principal: model.None("an instance name, unique per entry, only rendered: instances.go:104, instances.go:39")},
			{Name: colTargetRef, Kind: model.KindText, Nullable: true, Principal: model.None("a caller-supplied target label, stored and rendered only: instances.go:105, instances.go:40")},
			{Name: colInstStatus, Kind: model.KindText, Indexed: true, Principal: model.None("an instance status from a closed set: instances.go:209-212, instances.go:227")},
			{Name: colRequestedBy, Kind: model.KindText, Nullable: true, Principal: pdeclActorEvidence},
			{Name: colDecidedBy, Kind: model.KindText, Nullable: true, Principal: pdeclActorEvidence},
			{Name: colNote, Kind: model.KindText, Nullable: true, Principal: model.None("operator prose, only rendered: instances.go:108, instances.go:42")},
		},
		Indexes: []model.IndexSpec{{
			// One instance name per source entry. Unique index leads with tenant_id.
			Name:    "catalog_instance_uniq",
			Columns: []string{model.ColTenantID, colEntryID, colInstName},
			Unique:  true,
		}},
	})
}
