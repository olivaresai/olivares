// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/updatecheck"
)

// The DTOs are the stable JSON shapes the API exposes. They mirror core entities
// but render ids and timestamps as strings and never expose secret fields
// (password hashes, token secret hashes) — those live only in the auth partition.

// listResponse is the envelope for a paged collection — the engine's own routes
// use the same exported shape the modules do (core/api/listresponse.go), so an
// empty page is `{"items":[]}` on every route of the API without exception.
type listResponse[T any] = ListResponse[T]

// AgentDTO is the JSON shape of an agent.
type AgentDTO struct {
	ID          string         `json:"id"`
	TenantID    string         `json:"tenant_id"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	ExternalID  string         `json:"external_id,omitempty"`
	Status      string         `json:"status"`
	IdentityID  string         `json:"identity_id,omitempty"`
	Labels      map[string]any `json:"labels,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	Version     int64          `json:"version"`
}

// AgentInput is the create/update payload for an agent.
type AgentInput struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	ExternalID  string         `json:"external_id"`
	Status      string         `json:"status"`
	IdentityID  string         `json:"identity_id"`
	WorkspaceID string         `json:"workspace_id"`
	Labels      map[string]any `json:"labels"`
	Metadata    map[string]any `json:"metadata"`
}

func toAgentDTO(a model.Agent) AgentDTO {
	return AgentDTO{
		ID: a.ID.String(), TenantID: a.TenantID.String(), WorkspaceID: idOrEmpty(a.WorkspaceID),
		Name: a.Name, Kind: a.Kind,
		ExternalID: a.ExternalID, Status: string(a.Status), IdentityID: idOrEmpty(a.IdentityID),
		Labels: a.Labels, Metadata: a.Metadata,
		CreatedAt: a.CreatedAt.String(), UpdatedAt: a.UpdatedAt.String(), Version: a.Version,
	}
}

func (in AgentInput) apply(a *model.Agent) {
	a.Name = in.Name
	a.Kind = in.Kind
	a.ExternalID = in.ExternalID
	a.Status = model.LifecycleStatus(in.Status)
	if a.Status == "" {
		a.Status = model.StatusActive
	}
	a.IdentityID = model.ID(in.IdentityID)
	a.WorkspaceID = model.ID(in.WorkspaceID)
	a.Labels = in.Labels
	a.Metadata = in.Metadata
}

// AccessEdgeDTO is the JSON shape of an access edge (the R/RW map view).
type AccessEdgeDTO struct {
	ID              string `json:"id"`
	OriginKind      string `json:"origin_kind"`
	OriginID        string `json:"origin_id"`
	ResourceID      string `json:"resource_id"`
	Mode            string `json:"mode"`
	SignalSource    string `json:"signal_source"`
	Confidence      string `json:"confidence"`
	Permitted       bool   `json:"permitted"`
	Observed        bool   `json:"observed"`
	OccurrenceCount int64  `json:"occurrence_count"`
	FirstSeen       string `json:"first_seen"`
	LastSeen        string `json:"last_seen"`
}

func toAccessEdgeDTO(e model.AccessEdge) AccessEdgeDTO {
	return AccessEdgeDTO{
		ID: e.ID.String(), OriginKind: e.OriginKind, OriginID: e.OriginID.String(),
		ResourceID: e.ResourceID.String(), Mode: string(e.Mode), SignalSource: string(e.SignalSource),
		Confidence: string(e.Confidence), Permitted: e.Permitted, Observed: e.Observed,
		OccurrenceCount: e.OccurrenceCount, FirstSeen: e.FirstSeen.String(), LastSeen: e.LastSeen.String(),
	}
}

// AuditEventDTO is the JSON shape of one ledger event. Hashes are hex; the
// signature is base64. It carries the integrity fields so a client can re-verify.
type AuditEventDTO struct {
	ID         string `json:"id"`
	Seq        int64  `json:"seq"`
	OccurredAt string `json:"occurred_at"`
	Actor      string `json:"actor"`
	ActorKind  string `json:"actor_kind"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	PrevHash   string `json:"prev_hash"`
	Hash       string `json:"hash"`
	Sig        string `json:"sig,omitempty"`
}

func toAuditDTO(e model.AuditEvent) AuditEventDTO {
	d := AuditEventDTO{
		ID: e.ID.String(), Seq: e.Seq, OccurredAt: e.OccurredAt.String(),
		Actor: e.Actor, ActorKind: e.ActorKind, Action: e.Action,
		TargetKind: string(e.TargetKind), TargetID: idOrEmpty(e.TargetID),
		PrevHash: hex.EncodeToString(e.PrevHash), Hash: hex.EncodeToString(e.Hash),
	}
	if len(e.Sig) > 0 {
		d.Sig = base64.StdEncoding.EncodeToString(e.Sig)
	}
	return d
}

// OrgDTO is the JSON shape of a tenant org.
type OrgDTO struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Status   string `json:"status"`
	// DataRegion is the residency pin: the region this tenant's
	// control-plane data resides in. Empty when the tenant is unpinned.
	DataRegion string `json:"data_region,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func toOrgDTO(o model.Org) OrgDTO {
	return OrgDTO{ID: o.ID.String(), TenantID: o.TenantID.String(), Name: o.Name,
		Slug: o.Slug, Status: string(o.Status), DataRegion: o.DataRegion, CreatedAt: o.CreatedAt.String()}
}

// UserDTO is the JSON shape of a user (never includes the password hash).
type UserDTO struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name,omitempty"`
	Status       string `json:"status"`
	IsSuperadmin bool   `json:"is_superadmin"`
	CreatedAt    string `json:"created_at"`
}

func toUserDTO(u model.User) UserDTO {
	return UserDTO{ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName,
		Status: string(u.Status), IsSuperadmin: u.IsSuperadmin, CreatedAt: u.CreatedAt.String()}
}

// --- request inputs ----------------------------------------------------------

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type setupInput struct {
	Token    string `json:"token"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// Organization OPTIONALLY names the first organization first-boot setup
	// creates and grants the new superadmin ownership of. Empty falls back to
	// firstOrgDefaultName — an install must be usable whether or not the operator
	// had a name in mind.
	Organization string `json:"organization,omitempty"`
}

// setupResponse is the first-boot setup result. It embeds the created superadmin
// (so the historical flat user shape is unchanged for existing clients) and adds
// the organization that was created with it: the console needs the tenant id to
// select a tenant and send X-Olivares-Tenant, and must not have to guess it from
// a follow-up listing.
type setupResponse struct {
	UserDTO
	Organization OrgDTO `json:"organization"`
}

type createUserInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Superadmin  bool   `json:"superadmin"`
}

type issueTokenInput struct {
	Name       string `json:"name"`
	Tenant     string `json:"tenant"`
	Role       string `json:"role"`
	Superadmin bool   `json:"superadmin"`
}

type grantMembershipInput struct {
	UserID string `json:"user_id"`
	Tenant string `json:"tenant"`
	Role   string `json:"role"`
	// WorkspaceID OPTIONALLY confines the membership to one workspace of the granted tenant
	//. Empty is the historical tenant-wide membership. It is validated to name a
	// real workspace in that tenant before the grant lands (deny-closed against a typo).
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type createOrgInput struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	// DataRegion optionally pins the new tenant to a residency region. It is
	// validated against the residency registry; empty leaves the tenant unpinned.
	DataRegion string `json:"data_region"`
}

// setOrgRegionInput sets/clears a tenant's residency pin. An empty region
// clears the pin (the tenant becomes unpinned).
type setOrgRegionInput struct {
	DataRegion string `json:"data_region"`
}

// setOrgStatusInput withdraws or restores a tenant's service. "suspended"
// stops serving the tenant without deleting anything; "active" restores it. The
// two are the only accepted values — an org has no other service state, and
// storing one the guard has no rule for would be a tenant nobody can classify.
type setOrgStatusInput struct {
	Status string `json:"status"`
}

// --- Token DTOs -------------------------------------------------------

// TokenDTO is the JSON shape of an API token (never includes the secret hash).
type TokenDTO struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	UserID        string  `json:"user_id,omitempty"`
	BoundTenantID string  `json:"bound_tenant_id,omitempty"`
	Role          string  `json:"role,omitempty"`
	IsSuperadmin  bool    `json:"is_superadmin,omitempty"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
	Revoked       bool    `json:"revoked"`
	LastUsedAt    *string `json:"last_used_at,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

func toTokenDTO(t model.APIToken) TokenDTO {
	dto := TokenDTO{
		ID: t.ID.String(), Name: t.Name, UserID: idOrEmpty(t.UserID),
		BoundTenantID: string(t.BoundTenantID), Role: t.Role,
		IsSuperadmin: t.IsSuperadmin, Revoked: t.Revoked,
		CreatedAt: t.CreatedAt.String(),
	}
	if t.ExpiresAt != nil {
		s := t.ExpiresAt.String()
		dto.ExpiresAt = &s
	}
	if t.LastUsedAt != nil {
		s := t.LastUsedAt.String()
		dto.LastUsedAt = &s
	}
	return dto
}

// --- Setup/health DTOs ------------------------------------------------

type setupStepDTO struct {
	ID        string `json:"id"`
	Completed bool   `json:"completed"`
	// Applicable is false for a step this BUILD or deployment cannot complete —
	// present only then, so an existing client sees the shape it always saw. Without
	// it a step nobody can finish holds `completed` at false forever and tells the
	// operator their correct install is unfinished.
	Applicable *bool `json:"applicable,omitempty"`
	// Reason names WHY a step is incomplete or not applicable. An incomplete step
	// with no reason is a to-do list item with no instructions.
	Reason string `json:"reason,omitempty"`
}

type setupStatusDTO struct {
	Completed bool           `json:"completed"`
	Steps     []setupStepDTO `json:"steps"`
}

// EffectiveConfigEntry is one already-redacted production configuration value
// supplied by the composition root. Source is "env" or "activation".
type EffectiveConfigEntry struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Redacted bool   `json:"redacted"`
	Source   string `json:"source"`
}

type effectiveConfigResponse struct {
	Entries          []EffectiveConfigEntry `json:"entries"`
	StrictViolations []string               `json:"strict_violations"`
}

// KeyCustodyInfo is the non-secret key inventory supplied by the composition
// root. Keys intentionally carries metadata only; no private or symmetric key
// material has a field in this contract.
type KeyCustodyInfo struct {
	Keys []KeyInfo `json:"keys"`
}

// KeyInfo is one custody-inventory entry. Signing keys, the embedded license
// verification key, and symmetric sealers share the list but serialize only the
// fields relevant to their kind:
//   - audit/catalog/policy: public verification and custody metadata;
//   - license: embedded-key origin/fingerprint metadata;
//   - eventing/sso/secret-store: source and presence only.
type KeyInfo struct {
	Purpose     string `json:"purpose"`
	Algorithm   string `json:"algorithm,omitempty"`
	CustodyMode string `json:"custody_mode,omitempty"`
	KEK         string `json:"kek,omitempty"`
	Created     string `json:"created,omitempty"`
	PublicKey   string `json:"public_key,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	PriorCount  int    `json:"prior_count,omitempty"`
	Origin      string `json:"origin,omitempty"`
	Source      string `json:"source,omitempty"`
	Present     bool   `json:"present,omitempty"`
}

// MarshalJSON keeps the heterogeneous key list least-disclosing. In
// particular, a symmetric-sealer entry contains ONLY purpose/source/present,
// including present=false, while signing-key entries retain explicit empty
// custody fields where the creation time or KEK is genuinely unknown.
func (k KeyInfo) MarshalJSON() ([]byte, error) {
	switch {
	case k.Source != "" || k.Purpose == "eventing" || k.Purpose == "sso" || k.Purpose == "secret-store":
		return json.Marshal(struct {
			Purpose string `json:"purpose"`
			Source  string `json:"source"`
			Present bool   `json:"present"`
		}{
			Purpose: k.Purpose,
			Source:  k.Source,
			Present: k.Present,
		})
	case k.Purpose == "license":
		return json.Marshal(struct {
			Purpose     string `json:"purpose"`
			Algorithm   string `json:"algorithm"`
			Origin      string `json:"origin"`
			Fingerprint string `json:"fingerprint"`
		}{
			Purpose:     k.Purpose,
			Algorithm:   k.Algorithm,
			Origin:      k.Origin,
			Fingerprint: k.Fingerprint,
		})
	default:
		return json.Marshal(struct {
			Purpose     string `json:"purpose"`
			Algorithm   string `json:"algorithm"`
			CustodyMode string `json:"custody_mode"`
			KEK         string `json:"kek"`
			Created     string `json:"created"`
			PublicKey   string `json:"public_key"`
			Fingerprint string `json:"fingerprint"`
			PriorCount  int    `json:"prior_count"`
		}{
			Purpose:     k.Purpose,
			Algorithm:   k.Algorithm,
			CustodyMode: k.CustodyMode,
			KEK:         k.KEK,
			Created:     k.Created,
			PublicKey:   k.PublicKey,
			Fingerprint: k.Fingerprint,
			PriorCount:  k.PriorCount,
		})
	}
}

type healthSummaryDTO struct {
	Healthy     bool   `json:"healthy"`
	Ready       bool   `json:"ready"`
	StoreEngine string `json:"store_engine"`
	// ConnectorsAvailable is the size of the connector CATALOG — the kinds this
	// build knows how to wire. It is a property of the BINARY, not of the
	// deployment: a clean install offers a hundred kinds with none configured.
	// It replaces the old `connectors` field, which carried this same catalog
	// number under a name the console read as a live fleet and rendered as
	// "N active" — a virgin install claimed a hundred running connectors.
	ConnectorsAvailable int `json:"connectors_available"`
	// ConnectorsConfigured counts the durable roster entries — the connector
	// INSTANCES an operator authored, enabled or not. This is the number that
	// answers "has anything been set up here yet?".
	ConnectorsConfigured int `json:"connectors_configured"`
	// ConnectorsRunning counts roster entries whose live status is "running":
	// what is actually ingesting right now. Same criterion as the `running`
	// field of GET /v1/connectors/health, so the two surfaces cannot disagree.
	ConnectorsRunning int `json:"connectors_running"`
	// ConnectorsErr counts ENABLED roster entries that are not carrying data —
	// "failed" AND "not_wired", plus any status this build does not recognize —
	// via the one classification in rosterclass.go that GET /v1/connectors/health
	// and GET /status also use, so the three surfaces cannot disagree.
	ConnectorsErr int `json:"connectors_error"`
	// ConnectorsMeasured is false when the roster could NOT BE READ. Without it the
	// four counters above serialize as zeros in that case, so "nothing is
	// configured here" and "I have no idea what is configured here" reach the
	// console as the same answer — the second one being exactly when an operator
	// must not be told everything is fine. Omitted while true, so the healthy
	// payload is unchanged and only the honest-failure case grows a field.
	ConnectorsMeasured *bool `json:"connectors_measured,omitempty"`
	// UsersCapped is true when the account census hit its paging budget, so
	// `users` is a LOWER BOUND rather than a count. Absent means it is a count.
	// It used to be neither: a single 1000-row page whose length was reported as
	// the total, so a deployment with more accounts than that read exactly 1000
	// forever and nothing said the number was a page size.
	UsersCapped         *bool                     `json:"users_capped,omitempty"`
	Users               int                       `json:"users"`
	SSOConfigured       bool                      `json:"sso_configured"`
	Version             string                    `json:"version"`
	EmbedderKind        string                    `json:"embedder_kind,omitempty"`
	RetrievalSemantic   bool                      `json:"retrieval_semantic"`
	KnowledgeReason     string                    `json:"knowledge_status_reason,omitempty"`
	GuardProfile        string                    `json:"guard_profile,omitempty"`
	GuardWarning        string                    `json:"guard_warning,omitempty"`
	GuardDowngradeCount int                       `json:"guard_downgrade_count,omitempty"`
	GuardPublicOnlyKBs  []KnowledgeGuardDowngrade `json:"guard_public_only_kbs,omitempty"`
	// Update is the OTA update-availability indicator: present only when
	// update checking is configured; absent on air-gapped deployments (silence).
	Update *updatecheck.Status `json:"update,omitempty"`
	// AuditSpool is present only when an audit-ledger budget is declared; absent
	// otherwise — silence, not error.
	AuditSpool *auditSpoolDTO `json:"audit_spool,omitempty"`
	// TLSNotAfter/TLSDaysLeft are present only when the listener supplies a live
	// certificate-expiry accessor. Pointers preserve a legitimate zero days-left
	// value while still omitting the field when TLS is unknown.
	TLSNotAfter string `json:"tls_not_after,omitempty"`
	TLSDaysLeft *int64 `json:"tls_days_left,omitempty"`
}

type auditSpoolDTO struct {
	MaxBytes           int64  `json:"max_bytes"`
	UsedBytes          int64  `json:"used_bytes"`
	Mode               string `json:"mode"`
	Engaged            bool   `json:"engaged"`
	PendingDropTenants int    `json:"pending_drop_tenants,omitempty"`
	PendingDrops       int64  `json:"pending_drops,omitempty"`
}

type residencyRegistryDTO struct {
	HomeRegion string   `json:"home_region"`
	Regions    []string `json:"regions"`
	Enforces   bool     `json:"enforces"`
}

type busSubscriberDTO struct {
	Name     string `json:"name"`
	Class    string `json:"class"`
	Depth    int    `json:"depth"`
	Capacity int    `json:"capacity"`
}

type busBridgeDTO struct {
	Connected      bool   `json:"connected"`
	PendingMsgs    int    `json:"pending_msgs"`
	PendingBytes   int    `json:"pending_bytes"`
	Dropped        int64  `json:"dropped"`
	PublishErrors  uint64 `json:"publish_errors"`
	DecodeErrors   uint64 `json:"decode_errors"`
	GateSkipped    uint64 `json:"gate_skipped"`
	InvalidSubject uint64 `json:"invalid_subject"`
}

type busSnapshotDTO struct {
	Subscribers      []busSubscriberDTO `json:"subscribers"`
	PublishBlocked   uint64             `json:"publish_blocked"`
	Dropped          uint64             `json:"dropped"`
	DroppedTelemetry uint64             `json:"dropped_telemetry"`
	DroppedNotify    uint64             `json:"dropped_notify"`
	HandlerErrors    uint64             `json:"handler_errors"`
	Enqueued         uint64             `json:"enqueued"`
	Handled          uint64             `json:"handled"`
	Bridge           *busBridgeDTO      `json:"bridge,omitempty"`
}

// idOrEmpty renders a zero id as "".
func idOrEmpty(id model.ID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}
