// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package identitysource is the typed, Apache-licensed contract that the Olivares
// AI identity connectors (AD/LDAP, Okta/Entra, HashiCorp Vault, Infisical,
// SPIFFE/SPIRE) expose to module VI (identity/permissions/governance) in
// ADDITION to sdk.SourceConnector.
//
// Why a typed contract and not a fourth observation type. The SDK's
// model.Observation sum type is sealed (S02 §3): a SourceConnector can only emit
// EdgeObservation, CostSample or FindingReport through its Sink. An identity
// directory roster — who exists (human and non-human), and who belongs to which
// group/role — is reference/inventory data, not a flow fact, exactly like the
// model/provider Catalog. The same dilemma was met before; by design, such data
// travels a typed Go contract in /connectors, NOT a fourth
// sealed observation type (which would crack the frozen S02 wire contract and
// reach into /core). This package mirrors connectors/modelprovider.CatalogProvider
// for identities: the host or module type-asserts an identity connector to
// GraphProvider and reads its Snapshot.
//
// What flows where. The roster (identities + groups/roles + memberships) is the
// Graph returned by Snapshot. Where a source additionally exposes an
// identity→resource permitted grant (a Vault entity/role allowed to read or write
// a secret path), the connector emits that as an EdgeObservation with
// model.SignalPolicy through its Sink — populating the PERMITTED side of the
// permitted-vs-observed diff (ARCHITECTURE.md) without inventing a new observation
// kind. Group/role memberships are NOT edges (no resource is touched), so they
// only ever travel the typed Graph.
//
// Minimal data (docs/SECURITY-HARDENING.md-3). The Graph carries identity METADATA only: natural
// references, display labels, the human/NHI classification, account status and a
// small map of non-sensitive attributes. It NEVER carries credential material —
// no passwords, no Vault secret values, no API-key secrets, no private keys. A
// connector reads identities, not the secrets behind them.
//
// It imports only the SDK (for the shared model.Confidence / model vocabulary it
// reuses), never the engine.
package identitysource

import (
	"context"
	"time"
)

// SourceKind names the identity source a Graph (or one of its rows) came from. It
// is the per-source provenance the product shows so governance can tell an Okta
// group from an LDAP group from a Vault policy. It is a plain string so an
// operator-built identity connector can introduce its own.
type SourceKind string

// The seeded identity sources.
const (
	// SourceLDAP is Active Directory / any LDAP v3 directory.
	SourceLDAP SourceKind = "ldap"
	// SourceOkta is Okta (users, groups, apps via the API/SCIM).
	SourceOkta SourceKind = "okta"
	// SourceEntra is Microsoft Entra ID (Azure AD) via Microsoft Graph.
	SourceEntra SourceKind = "entra"
	// SourceVault is HashiCorp Vault (entities, groups, policies, auth roles).
	SourceVault SourceKind = "vault"
	// SourceInfisical is Infisical (machine identities, projects, memberships).
	SourceInfisical SourceKind = "infisical"
	// SourceSPIFFE is SPIFFE/SPIRE (registered workload SPIFFE IDs).
	SourceSPIFFE SourceKind = "spiffe"
	// SourceAnthropic is the Anthropic (Claude) organization: API keys, workspaces,
	// service accounts, organization members and federation issuers as governed NHI
	// (connectors/claude-wif).
	SourceAnthropic SourceKind = "anthropic"
	// SourceKeycloak is a Keycloak realm directory (users, clients/service accounts,
	// realm roles, groups) read via the Admin REST API (connectors/keycloak).
	SourceKeycloak SourceKind = "keycloak"
	// SourcePingOne is a PingOne (cloud) environment directory — users, WORKER
	// applications (M2M NHIs), groups and admin role assignments — read via the
	// PingOne Platform Management API behind the connectors/keycloak provider switch
	//. PingOne is NOT Keycloak-compatible (a wholly different HAL REST API and
	// OAuth2 worker-token auth), so it carries its own SourceKind per this contract's
	// honest-provenance rule, never SourceKeycloak.
	SourcePingOne SourceKind = "pingone"
	// SourceForgeRock is a Ping Identity Platform / PingIDM (ex-ForgeRock IDM)
	// directory — managed users, managed roles and their members — read via the
	// Common REST (CREST) managed-object API behind the same provider switch.
	// Also not Keycloak-compatible, so its own SourceKind.
	SourceForgeRock SourceKind = "forgerock"

	// The secrets/PKI/KMS layer. Each names the custodian of
	// the secrets it observes/inventories. The keys/secrets themselves are RESOURCES
	// on EdgeObservations; these sources name the STORE as a governed NHI for the
	// unified secret-manager inventory.
	//
	// SourceAWSKMS is AWS KMS / Secrets Manager observed via CloudTrail (awskms).
	SourceAWSKMS SourceKind = "awskms"
	// SourceGCPKMS is Google Cloud KMS / Secret Manager observed via Cloud Audit
	// Logs (gcpkms).
	SourceGCPKMS SourceKind = "gcpkms"
	// SourceAzureKeyVault is Azure Key Vault / Managed HSM observed via diagnostic
	// AuditEvent logs (azurekeyvault).
	SourceAzureKeyVault SourceKind = "azurekeyvault"
	// SourceKMIP is an on-prem OASIS KMIP v2.1 key-management server inventoried
	// read-only (kmip).
	SourceKMIP SourceKind = "kmip"
	// SourceExternalSecrets is the External Secrets Operator's CRDs — which K8s
	// secret is hydrated from which backend store (externalsecrets).
	SourceExternalSecrets SourceKind = "externalsecrets"
	// SourceSOPS is SOPS+age GitOps metadata — which recipient keys encrypt which
	// files (sops).
	SourceSOPS SourceKind = "sops"

	// The hyperscaler agent-identity registries federated by (FED-1). Each is
	// a registry whose per-agent rows (Identity.Kind == KindAgentIdentity or
	// KindWorkloadIdentity) are DEDICATED, non-shared identities for exactly one
	// agent workload — which is what lets module III attribute an access firmly
	// (modules/access-map attribution, the axis). Rows of any other kind a
	// federation connector emits (blueprint principals, credential providers,
	// service-account-backed agents) are governed NHIs but NEVER a firm per-agent
	// signal.
	//
	// SourceEntraAgent is Microsoft Entra Agent ID (GA 2026-04): agentIdentity
	// service principals, their blueprints and blueprint principals, read via
	// Microsoft Graph v1.0 (entra-agent).
	SourceEntraAgent SourceKind = "entra-agent"
	// SourceAgentCore is AWS Bedrock AgentCore Identity: the per-account workload
	// identity directory, token-vault credential providers and AgentCore Policy
	// (Cedar) engines, read via bedrock-agentcore-control (agentcore).
	SourceAgentCore SourceKind = "agentcore"
	// SourceGoogleAgent is Google Agent Identity (SPIFFE-based, GA for Agent
	// Runtime 2026-05-07): reasoning engines and their agent identities, read via
	// aiplatform.googleapis.com. Agent-identity rows use the FULL SPIFFE ID as Ref
	// so they converge with the SPIFFE roster by external_id.
	SourceGoogleAgent SourceKind = "google-agent"

	// The agent control-tower registries syncs READ-ONLY (export to them is
	//) and the agent-descriptor/secret-access sources.
	//
	// SourceAgent365 is the Microsoft Agent 365 registry (GA 2026-05-01) — the
	// package-level inventory including agents WITHOUT an Entra agent identity
	// (agent365). Complementary to entra-agent, never a duplicate.
	SourceAgent365 SourceKind = "agent365"
	// SourceFoundry is Microsoft Foundry as an agent platform — projects, agent
	// applications and current Agent Service agents (foundry-agents).
	SourceFoundry SourceKind = "foundry"
	// SourceAIControlTower is the ServiceNow AI Control Tower AI-asset inventory
	// (digital-asset tables over the Table API), read-only (ai-control-tower).
	SourceAIControlTower SourceKind = "ai-control-tower"
	// SourceOASF is an AGNTCY/OASF agent-descriptor set with optional Agent Badge
	// verification — EXPERIMENTAL until the identity spec is VCDM 2.0 conformant
	// (oasf).
	SourceOASF SourceKind = "oasf"
	// SourceOnePassword is the 1Password Events Reporting custodian: the account
	// as a secret_store NHI plus item-usage secret-access attribution
	// (onepassword).
	SourceOnePassword SourceKind = "onepassword"
)

// KindSecretStore is the Identity.Kind a secrets connector stamps on a
// secret-manager custodian it inventories (a KMS/Key-Vault/HSM scope, a KMIP
// server, an ESO backend, a SOPS recipient) for the unified secret-manager
// inventory. It is always paired with PrincipalNHI: a store is a
// governed non-human custodian, distinct from the keys/secrets it holds (those are
// RESOURCES carried on EdgeObservations). The roster is queryable by this kind, so
// "where do the estate's secrets live" is GET /governance/identities?kind=secret_store.
const KindSecretStore = "secret_store"

// The DEDICATED per-agent identity kinds (FED-1). A federation connector
// stamps exactly one of these on a roster row ONLY when the source guarantees the
// identity belongs to a single agent workload (an Entra agentIdentity service
// principal, an AgentCore workload identity, a Google SPIFFE-based agent
// identity). The access-map attribution axis treats a roster identity from
// a federation source as a FIRM per-agent signal only for these kinds — anything
// else from the same source (blueprint principals, credential providers,
// service-account-backed agents) stays approximate. They are always paired with
// PrincipalNHI.
const (
	// KindAgentIdentity is a registry-native per-agent identity (Entra Agent ID
	// agentIdentity, Google Agent Identity).
	KindAgentIdentity = "agent_identity"
	// KindWorkloadIdentity is an AgentCore workload identity (the directory's
	// per-agent primitive; "agent identity" is its specialized form).
	KindWorkloadIdentity = "workload_identity"
)

// FindingLongLivedCredential is the drift-class FindingReport.Kind a federation
// connector emits when an AGENT identity surface holds a static, long-lived
// credential (an Entra agent blueprint with client secrets, an AgentCore API-key
// credential provider). Grounded in the Five Eyes joint guidance "Careful
// adoption of agentic AI services" (ASD/CISA/NSA/CCCS/NCSC-NZ/NCSC-UK,
// 2026-05-01): "Replace static, long-lived secrets with ephemeral credentials".
// Posture is detect/alert-first (SeverityMedium) — escalation and blocking stay
// with the NHI-lifecycle machinery, never the connector. The nhi_* namespace
// rides the existing notify routing for NHI findings.
const FindingLongLivedCredential = "nhi_longlived_credential"

// FindingFederationDrift is the drift-class FindingReport.Kind a federation connector
// emits when the org's LIVE workload-identity-federation configuration diverges from
// what the operator declared/governs — an undeclared (shadow) live rule, a declared
// rule that no longer exists, an over-broad match, an orphan issuer/rule, a scope or
// token-lifetime that drifted from the governed baseline. It is the declared-vs-actual
// reconciliation signal for non-human identity federation, the federation analog of
// FindingLongLivedCredential. The specific case rides the finding's Title and a stable,
// non-sensitive DetailHash discriminator; the SubjectRef is the raw provider id of the
// drifting object (fdrl_/fdis_/svac_), so it resolves to the same governed NHI/policy
// row. Posture is detect/alert-first — the connector never blocks; only Severity carries
// weight. The nhi_* namespace rides the existing notify routing for NHI findings.
const FindingFederationDrift = "nhi_federation_drift"

// PrincipalType is the human/non-human classification of an identity. It is the
// spine of identity governance: a human user is governed differently from a
// non-human identity (a service account, a database role, a workload). The
// product never guesses — an identity whose nature a source does not reveal is
// PrincipalUnknown, shown honestly rather than defaulted.
type PrincipalType string

// The principal types.
const (
	// PrincipalUnknown means the source did not reveal whether the identity is a
	// human or a machine. It is never guessed (ARCHITECTURE.md honest confidence).
	PrincipalUnknown PrincipalType = "unknown"
	// PrincipalHuman is a human user (an interactive account).
	PrincipalHuman PrincipalType = "human"
	// PrincipalNHI is a non-human identity: a service account, a database role, an
	// IAM principal, a workload, a machine identity.
	PrincipalNHI PrincipalType = "nhi"
)

// Valid reports whether p is a known principal type.
func (p PrincipalType) Valid() bool {
	switch p {
	case PrincipalUnknown, PrincipalHuman, PrincipalNHI:
		return true
	default:
		return false
	}
}

// CollectionKind classifies a Collection: a directory group, an assignable role,
// or a policy an identity is bound to. Governance treats them differently (a
// group is membership; a role/policy is a grant), so the kind is explicit.
type CollectionKind string

// The collection kinds.
const (
	// KindGroup is a directory group (AD/LDAP group, Okta/Entra group).
	KindGroup CollectionKind = "group"
	// KindRole is an assignable role (Entra app role, Vault auth role, Okta admin
	// role, Infisical project role).
	KindRole CollectionKind = "role"
	// KindPolicy is a policy an identity is bound to (a Vault policy).
	KindPolicy CollectionKind = "policy"
)

// MemberKind says whether a membership's member is an identity or a nested
// collection (a group within a group), so the consumer can walk a nested graph.
type MemberKind string

// The member kinds.
const (
	// MemberIdentity is an Identity that belongs to a Collection.
	MemberIdentity MemberKind = "identity"
	// MemberCollection is a Collection nested inside another Collection (a group of
	// groups), so the consumer can resolve transitive membership.
	MemberCollection MemberKind = "collection"
)

// Identity is one principal (human or NHI) discovered in a source, carrying
// governance METADATA only — never credential material (docs/SECURITY-HARDENING.md). It is the
// substrate module VI resolves to engine Identity entities and uses to
// attack the shared-service-account attribution problem (ARCHITECTURE.md).
type Identity struct {
	// Ref is the source's natural, stable reference for the identity: an LDAP DN,
	// an Okta/Entra object id, a Vault entity id, a SPIFFE ID, an Infisical
	// identity id. It is the de-duplication key the engine resolves on.
	Ref string
	// Type is the human/NHI/unknown classification (never guessed).
	Type PrincipalType
	// Kind is the finer source-native class: "user", "service_account", "db_role",
	// "iam_role", "workload", "machine_identity", "vault_entity". Free text so a
	// source can be precise.
	Kind string
	// DisplayName is a human label (a CN, a display name, a SPIFFE path). It is
	// identity metadata, not a payload; it never carries a secret.
	DisplayName string
	// Source is the provenance of this identity.
	Source SourceKind
	// Disabled is true when the source marks the account disabled/suspended/locked.
	// It is a governance signal (a stale enabled service account is a finding), not
	// a secret.
	Disabled bool
	// Attributes is a small map of non-sensitive, governance-relevant metadata
	// (e.g. "ou", "upn", "trust_domain", "email"). A connector populates it
	// conservatively and NEVER puts credential material in it.
	Attributes map[string]string
}

// Collection is a group, role or policy an identity can belong to or be granted.
// Memberships reference it by Ref.
type Collection struct {
	// Ref is the source's natural reference (group DN, group id, role name, policy
	// name). It is the key Membership.CollectionRef points at.
	Ref string
	// Kind classifies it as a group, role or policy.
	Kind CollectionKind
	// DisplayName is a human label.
	DisplayName string
	// Source is the provenance of this collection.
	Source SourceKind
	// Attributes is a small map of non-sensitive metadata (e.g. a Vault policy's
	// path count). Never credential material.
	Attributes map[string]string
}

// Membership is one edge of the identity graph: a member (an Identity, or a
// nested Collection) belongs to a Collection. It is the raw relationship; module
// VI resolves transitive membership and the agent↔identity bridge.
type Membership struct {
	// MemberRef is the Ref of the member.
	MemberRef string
	// MemberKind says whether the member is an identity or a nested collection.
	MemberKind MemberKind
	// CollectionRef is the Ref of the Collection the member belongs to.
	CollectionRef string
	// Source is the provenance of this membership edge.
	Source SourceKind
}

// Graph is the point-in-time identity snapshot one connector exposes through
// GraphProvider. It is reference data, distinct from any observation stream: the
// identities/collections/memberships populate module VI's governance views and
// feed the shared-service-account resolution. A connector returns its declared
// offline shape (typically empty) when no credential is configured.
type Graph struct {
	// Source is the source this snapshot came from.
	Source SourceKind
	// Identities are the principals discovered (humans and NHIs).
	Identities []Identity
	// Collections are the groups/roles/policies discovered.
	Collections []Collection
	// Memberships are the membership edges (identity∈collection, collection∈collection).
	Memberships []Membership
	// CapturedAt is when the snapshot was taken (the connector's clock, UTC).
	CapturedAt time.Time
	// DeferredAgentIdentities counts identities the connector SAW but deliberately
	// did not emit because a DEDICATED agent-registry connector owns them:
	// the idp connector's Entra path defers ServiceIdentity service principals to
	// entra-agent, so the converged row's Provider never flaps between sources.
	// Surfaced (never silent — docs/SECURITY-HARDENING.md): the roster sync folds a non-zero count
	// into its audit record, so an idp-only estate visibly reports the unwatched
	// agent-identity class until entra-agent is wired.
	DeferredAgentIdentities int
}

// FindIdentity returns the identity with the given ref and whether it was found.
func (g Graph) FindIdentity(ref string) (Identity, bool) {
	for _, id := range g.Identities {
		if id.Ref == ref {
			return id, true
		}
	}
	return Identity{}, false
}

// GraphProvider is implemented by an identity connector in ADDITION to
// sdk.SourceConnector. The SourceConnector half emits any identity→resource
// permitted grants as model.SignalPolicy edges (since the identity
// sources all emit theirs: vault ACL paths, ldap privileged-directory grants,
// idp app/scope assignments, infisical project grants); this half exposes the
// identity roster (who exists, who belongs to what). The host or module VI
// type-asserts a connector to GraphProvider and reads it. Snapshot is read-only
// and must not emit observations.
type GraphProvider interface {
	// Snapshot returns the current identity graph. It performs read-only directory
	// calls (or returns the connector's declared/offline graph when no credential
	// is configured) and honors ctx for cancellation. It never returns credential
	// material.
	Snapshot(ctx context.Context) (Graph, error)
}
