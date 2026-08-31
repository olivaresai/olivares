// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// subscriptionDTO is the API shape of a subscription. It NEVER carries the
// signing secret: SecretHint is a non-secret fingerprint prefix; the cleartext
// is returned exactly once by create and rotate-secret (the Secret field of
// their responses) and is otherwise unrecoverable through the API.
type subscriptionDTO struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	EventTypes   []string `json:"event_types"`
	MatchSources []string `json:"match_sources,omitempty"`
	Endpoint     string   `json:"endpoint"`
	SecretHint   string   `json:"secret_hint"`
	Role         string   `json:"role"`
	Description  string   `json:"description,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
	// per-subscription auth headers and retry policy.
	AuthType               string `json:"auth_type"`
	AuthValueHint          string `json:"auth_value_hint,omitempty"`
	AuthHeaderName         string `json:"auth_header_name,omitempty"`
	MaxAttempts            int64  `json:"max_attempts,omitempty"`
	InitialIntervalSeconds int64  `json:"initial_interval_seconds,omitempty"`
	// SIEM-sink profile (omitted entirely for a generic-webhook subscription).
	// The credential is never returned — only its non-secret hint.
	SinkKind     string            `json:"sink_kind,omitempty"`
	SinkFormat   string            `json:"sink_format,omitempty"`
	SinkOpts     map[string]string `json:"sink_opts,omitempty"`
	SinkCredHint string            `json:"sink_cred_hint,omitempty"`
}

// createdSubscriptionDTO is the create/rotate response: the DTO plus the
// one-time cleartext secret the consumer must store now.
type createdSubscriptionDTO struct {
	subscriptionDTO
	// Secret is shown exactly once; the platform keeps only a sealed form.
	Secret string `json:"secret"`
}

// subscriptionRequest is the create/update body.
type subscriptionRequest struct {
	Name         string   `json:"name"`
	Enabled      *bool    `json:"enabled,omitempty"` // default true on create, unchanged on update when absent
	EventTypes   []string `json:"event_types"`
	MatchSources []string `json:"match_sources,omitempty"`
	Endpoint     string   `json:"endpoint"`
	Role         string   `json:"role,omitempty"` // default viewer on create
	Description  string   `json:"description,omitempty"`
	// per-subscription auth header + retry policy.
	AuthType               string `json:"auth_type,omitempty"`                // none | bearer | basic | header (default none)
	AuthValue              string `json:"auth_value,omitempty"`               // cleartext credential (sealed at rest, never returned)
	AuthHeaderName         string `json:"auth_header_name,omitempty"`         // custom header name (required for auth_type "header")
	MaxAttempts            int64  `json:"max_attempts,omitempty"`             // 0 = module default
	InitialIntervalSeconds int64  `json:"initial_interval_seconds,omitempty"` // 0 = module default (30s)
	// SIEM-sink profile. Empty SinkKind = the unchanged generic HMAC webhook.
	// SinkCred is the tower credential (Splunk token / Datadog or New Relic key /
	// Sentinel bearer): accepted once, sealed at rest, never returned or logged.
	SinkKind   string            `json:"sink_kind,omitempty"`
	SinkFormat string            `json:"sink_format,omitempty"`
	SinkCred   string            `json:"sink_cred,omitempty"`
	SinkOpts   map[string]string `json:"sink_opts,omitempty"`
}

// Auth type constants for per-subscription authentication headers.
const (
	authTypeNone   = "none"   // HMAC signing only (default)
	authTypeBearer = "bearer" // Authorization: Bearer <credential>
	authTypeBasic  = "basic"  // Authorization: Basic <credential>
	authTypeHeader = "header" // <auth_header_name>: <credential>
)

func validAuthType(t string) bool {
	switch t {
	case "", authTypeNone, authTypeBearer, authTypeBasic, authTypeHeader:
		return true
	}
	return false
}

// hasSink reports whether the request declares a SIEM sink (vs the generic webhook).
func (in *subscriptionRequest) hasSink() bool { return strings.TrimSpace(in.SinkKind) != "" }

// validateSink checks the sink profile and trims its scalar fields. The endpoint
// (validated separately as https) is the tower's ingest URL. A cred-requiring kind
// without a credential on CREATE is rejected; on update an empty cred means "keep
// the existing one" (handled by the caller), so this validation runs on the kind/
// format/opts only when creating-or-changing-the-cred is the caller's concern.
func (in *subscriptionRequest) validateSink() string {
	in.SinkKind = strings.TrimSpace(in.SinkKind)
	in.SinkFormat = strings.TrimSpace(in.SinkFormat)
	if !validSinkKind(in.SinkKind) {
		return "sink_kind must be one of https, splunk_hec, sentinel_dcr, datadog, newrelic"
	}
	if !in.hasSink() {
		return ""
	}
	if !validSinkFormat(in.SinkFormat) {
		// Derived from the catalog so the error can never advertise a different
		// set than validSinkFormat accepts.
		return "sink_format must be one of " +
			strings.ReplaceAll(siemwire.EventingSinkFormats().List(), "|", ", ")
	}
	if len(in.SinkOpts) > maxTypeCount {
		return "too many sink_opts"
	}
	for k, v := range in.SinkOpts {
		if len(k) > maxSourceLen || len(v) > maxEndpointLen {
			return "sink_opts entry too long"
		}
	}
	if len(in.SinkCred) > maxEndpointLen {
		return "sink_cred too long"
	}
	return ""
}

func toSubscriptionDTO(rec model.Record) subscriptionDTO {
	authType := rec.String(colSubAuthType)
	if authType == "" {
		authType = authTypeNone
	}
	return subscriptionDTO{
		ID:                     rec.String(model.ColID),
		Name:                   rec.String(colSubName),
		Enabled:                rec.Bool(colSubEnabled),
		EventTypes:             csvSplit(rec.String(colSubTypes)),
		MatchSources:           csvSplit(rec.String(colSubSources)),
		Endpoint:               rec.String(colSubEndpoint),
		SecretHint:             rec.String(colSubSecretHint),
		Role:                   rec.String(colSubRole),
		Description:            rec.String(colSubDescription),
		CreatedAt:              rec.String(model.ColCreatedAt),
		UpdatedAt:              rec.String(model.ColUpdatedAt),
		AuthType:               authType,
		AuthValueHint:          rec.String(colSubAuthValHint),
		AuthHeaderName:         rec.String(colSubAuthHeaderName),
		MaxAttempts:            rec.Int(colSubMaxAttempts),
		InitialIntervalSeconds: rec.Int(colSubInitInterval),
	}
}

// validateSubscription checks the caller-supplied fields. The role ceiling
// mirrors token minting (a principal never mints authority above its own); the
// type check is static RBAC sanity (auth.RoleGrants) so a subscription that
// could NEVER receive a requested type is rejected at authoring time — the
// authoritative deny-closed check still runs per delivery (ABAC included).
// It is deliberately PURE: every check is in-memory, so it is safe to call inside an
// open store transaction. The operator's destination policy is NOT checked here —
// that needs a DNS lookup, and modules/eventing's own rule is that the store must
// never host a network call inside a transaction. Callers run checkEndpointPolicy
// separately, outside any transaction. See egresspolicy.go.
func (m *Module) validateSubscription(in *subscriptionRequest, p auth.Principal, tenant model.TenantID, create bool) string {
	in.Name = strings.TrimSpace(in.Name)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.Role = strings.TrimSpace(in.Role)
	if in.Role == "" {
		in.Role = auth.RoleViewer
	}
	switch {
	case in.Name == "":
		return "name is required"
	case len(in.Name) > maxNameLen:
		return "name too long"
	case len(in.Description) > maxDescLen:
		return "description too long"
	case len(in.EventTypes) == 0:
		return "event_types is required: choose at least one cataloged type"
	case len(in.EventTypes) > maxTypeCount:
		return "too many event_types"
	case len(in.MatchSources) > maxTypeCount:
		return "too many match_sources"
	}
	for _, s := range in.MatchSources {
		if len(s) > maxSourceLen {
			return "match_sources entry too long"
		}
	}
	if msg := validateEndpointURL(in.Endpoint, m.allowLoopback); msg != "" {
		return msg
	}
	if !auth.IsRole(in.Role) {
		return "role must be one of viewer, editor, admin, owner"
	}
	callerRank := auth.RoleRank(auth.RoleOwner)
	if !p.Superadmin {
		role, ok := p.RoleIn(tenant)
		if !ok {
			return "caller holds no role in this tenant"
		}
		callerRank = auth.RoleRank(role)
	}
	if auth.RoleRank(in.Role) > callerRank {
		return "role exceeds your own role (the role ceiling)"
	}
	for i, t := range in.EventTypes {
		t = strings.TrimSpace(t)
		in.EventTypes[i] = t
		info, ok := typeInfo(event.Type(t))
		if !ok {
			return "unknown event type " + quoteBounded(t) + " (see GET /event-types)"
		}
		if !auth.RoleGrants(in.Role, info.Permission) {
			return "role " + in.Role + " cannot receive " + t + " (requires " + string(info.Permission) + ")"
		}
	}
	// validate auth type + retry policy.
	in.AuthType = strings.TrimSpace(in.AuthType)
	if in.AuthType == "" {
		in.AuthType = authTypeNone
	}
	if !validAuthType(in.AuthType) {
		return "auth_type must be one of none, bearer, basic, header"
	}
	in.AuthHeaderName = strings.TrimSpace(in.AuthHeaderName)
	if in.AuthType == authTypeHeader && in.AuthHeaderName == "" {
		return "auth_header_name is required when auth_type is header"
	}
	if len(in.AuthHeaderName) > maxNameLen {
		return "auth_header_name too long"
	}
	if create && in.AuthType != authTypeNone && strings.TrimSpace(in.AuthValue) == "" {
		return "auth_value is required when auth_type is " + in.AuthType
	}
	if len(in.AuthValue) > maxEndpointLen {
		return "auth_value too long"
	}
	if in.MaxAttempts < 0 || in.MaxAttempts > 20 {
		return "max_attempts must be 0 (default) or 1-20"
	}
	if in.InitialIntervalSeconds < 0 || in.InitialIntervalSeconds > 3600 {
		return "initial_interval_seconds must be 0 (default) or 5-3600"
	}
	if in.InitialIntervalSeconds > 0 && in.InitialIntervalSeconds < 5 {
		return "initial_interval_seconds must be at least 5"
	}
	if msg := in.validateSink(); msg != "" {
		return msg
	}
	// A cred-requiring sink must carry its credential at creation. On update an
	// empty cred means "keep the sealed one already stored" (the caller preserves
	// it), so the requirement is enforced only when creating the profile.
	if create && in.hasSink() && credRequiredFor(in.SinkKind) && strings.TrimSpace(in.SinkCred) == "" {
		return "sink_cred is required for the " + in.SinkKind + " sink"
	}
	return ""
}

// handleListSubscriptions lists the tenant's subscriptions.
func (m *Module) handleListSubscriptions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if e := r.URL.Query().Get("enabled"); e == "true" || e == "false" {
		q.Filters = append(q.Filters, eq(colSubEnabled, e == "true"))
	}
	out := []subscriptionDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			dto := toSubscriptionDTO(rec)
			if err := m.loadSinkDTO(r.Context(), sc, &dto); err != nil {
				return err
			}
			out = append(out, dto)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[subscriptionDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}

// handleGetSubscription returns one subscription (never its secret).
func (m *Module) handleGetSubscription(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out subscriptionDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toSubscriptionDTO(rec)
		return m.loadSinkDTO(r.Context(), sc, &out)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateSubscription creates a subscription. The signing secret is
// generated server-side (never caller-supplied: entropy is guaranteed), sealed
// for storage, and returned EXACTLY ONCE in the response.
func (m *Module) handleCreateSubscription(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in subscriptionRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := m.validateSubscription(&in, mc.Principal, mc.Tenant, true); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	// A CREATE passes NO subscription reference, so it can never inherit a
	// compatibility exception: the record preserves destinations this deployment
	// already had, and it never manufactures a new one.
	if msg := m.checkEndpointPolicy(r.Context(), mc.Tenant, EgressCreate, "", in.Endpoint); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	secret, err := newSecret()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	sealed, err := m.sealer.Seal(r.Context(), mc.Tenant, []byte(secret))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Seal the SIEM sink credential (if any) BEFORE the transaction, like the HMAC
	// secret — the sealer is never invoked inside an open store transaction.
	sealedSink, err := m.sealSinkCred(r.Context(), mc.Tenant, in.SinkCred)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// seal the auth credential (if any) before the transaction.
	var sealedAuth string
	if in.AuthType != authTypeNone && strings.TrimSpace(in.AuthValue) != "" {
		sealedAuth, err = m.sealer.Seal(r.Context(), mc.Tenant, []byte(in.AuthValue))
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	// Unit H: read what the fence requires BEFORE the transaction opens, exactly like the
	// sealer above and for a harder reason — the read takes a pooled connection and SQLite has one,
	// so doing it inside would wait on the connection this transaction holds (writerProof).
	proof, err := m.writerProof(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	var out subscriptionDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSubName: in.Name, colSubEnabled: enabled,
			colSubTypes: csvJoin(in.EventTypes), colSubSources: csvJoin(in.MatchSources),
			colSubEndpoint: in.Endpoint, colSubSecret: sealed, colSubSecretHint: secretHint(secret),
			colSubRole: in.Role, colSubDescription: in.Description,
			colSubOwnerActor: mc.Principal.Actor(), colSubOwnerActorK: mc.Principal.ActorKind(),
			colSubAuthType: in.AuthType, colSubAuthHeaderName: in.AuthHeaderName,
			colSubMaxAttempts: in.MaxAttempts, colSubInitInterval: in.InitialIntervalSeconds,
		}
		if sealedAuth != "" {
			rec[colSubAuthValSealed] = sealedAuth
			rec[colSubAuthValHint] = secretHint(in.AuthValue)
		}
		// Unit H: prove this writer carries the egress gate, in the SAME transaction, and
		// stamp the nonce on the row. A create introduces a destination, so it is governed.
		if err := proof.StampInto(r.Context(), sc, rec); err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toSubscriptionDTO(created)
		if in.hasSink() {
			if err := m.createSinkRow(r.Context(), sc, model.ID(created.String(model.ColID)), &in, sealedSink, proof); err != nil {
				return err
			}
			applySinkDTO(&out, &in)
		}
		if err := appendRevision(r.Context(), sc, mc, model.ID(created.String(model.ColID)), revOpCreate, out); err != nil {
			return err
		}
		// The Meta records ids/names/counts — never the endpoint or a secret.
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.create", subscriptionKind,
			model.ID(created.String(model.ColID)), map[string]any{
				"name": in.Name, "event_types": len(in.EventTypes), "role": in.Role, "enabled": enabled,
				"sink_kind": in.SinkKind,
			})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createdSubscriptionDTO{subscriptionDTO: out, Secret: secret})
}

// handleUpdateSubscription updates a subscription in place. The secret is NOT
// touched here (rotate-secret owns that); enabled is unchanged when absent.
func (m *Module) handleUpdateSubscription(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in subscriptionRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := m.validateSubscription(&in, mc.Principal, mc.Tenant, false); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	// An UPDATE names the subscription, so one whose destination this deployment
	// already had stays editable under compatibility mode. Refusing it would mean an
	// operator who authors a narrow policy makes every pre-existing subscription
	// impossible to touch — including to disable it.
	if msg := m.checkEndpointPolicy(r.Context(), mc.Tenant, EgressUpdate, id, in.Endpoint); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	// Re-seal the sink credential before the transaction only when the caller
	// supplied a new one; an empty cred on update means "keep the stored one".
	sealedSink, err := m.sealSinkCred(r.Context(), mc.Tenant, in.SinkCred)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// re-seal the auth credential only when a new one is supplied.
	var sealedAuth string
	if strings.TrimSpace(in.AuthValue) != "" {
		sealedAuth, err = m.sealer.Seal(r.Context(), mc.Tenant, []byte(in.AuthValue))
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	// Unit H: the fence's requirement, read before the transaction (see writerProof). It is
	// read unconditionally even though only a MOVED destination spends it — the cost is one read on
	// an authoring request, and a conditional read here would put the read back inside the
	// transaction for the branch that needs it.
	proof, err := m.writerProof(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var out subscriptionDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// Read BEFORE the assignments below overwrite them: the fence compares the destination as
		// it IS, not as the request describes it.
		priorEndpoint := rec.String(colSubEndpoint)
		priorEnabled := rec.Bool(colSubEnabled)
		priorAuthType := rec.String(colSubAuthType)
		priorAuthHeader := rec.String(colSubAuthHeaderName)
		priorAuthSealed := rec.String(colSubAuthValSealed)
		rec[colSubName] = in.Name
		if in.Enabled != nil {
			rec[colSubEnabled] = *in.Enabled
		}
		rec[colSubTypes] = csvJoin(in.EventTypes)
		rec[colSubSources] = csvJoin(in.MatchSources)
		rec[colSubEndpoint] = in.Endpoint
		rec[colSubRole] = in.Role
		rec[colSubDescription] = in.Description
		// update auth + retry columns.
		rec[colSubAuthType] = in.AuthType
		rec[colSubAuthHeaderName] = in.AuthHeaderName
		rec[colSubMaxAttempts] = in.MaxAttempts
		rec[colSubInitInterval] = in.InitialIntervalSeconds
		if sealedAuth != "" {
			rec[colSubAuthValSealed] = sealedAuth
			rec[colSubAuthValHint] = secretHint(in.AuthValue)
		} else if in.AuthType == authTypeNone {
			rec[colSubAuthValSealed] = ""
			rec[colSubAuthValHint] = ""
		}
		// Unit H — THE FENCE NEVER BLOCKS TURNING EGRESS OFF; IT GOVERNS TURNING IT ON.
		//
		// Two mutations make a destination effective: MOVING it, and REACTIVATING a dormant one.
		// Both carry a proof. Disabling does not, and neither does a rename or a secret rotation:
		// GenericRepo.Update puts every descriptor field in the SET, so a fence on any update would
		// block disabling a subscription from a node the operator has not replaced yet — and unit G
		// preserves on purpose that a pre-existing subscription stays editable, INCLUDING to disable
		// it, which is what an operator does in an incident.
		//
		// The reactivation half was missed until an adversarial review of the implementation found
		// it. This condition is the SECOND COPY of the trigger's WHEN clause (migrations/*/0004 and
		// 0005); they must say the same thing, and a test pins that they do.
		// rec already carries the incoming values here (assigned above), so this reads the transition
		// exactly as the trigger sees OLD → NEW. The auth fields are in because the credential is
		// part of the destination: it ends up as a header on the same request, and on a multi-tenant
		// collector it is the token that selects the receiving workspace.
		if priorEndpoint != in.Endpoint ||
			(!priorEnabled && rec.Bool(colSubEnabled)) ||
			priorAuthType != rec.String(colSubAuthType) ||
			priorAuthHeader != rec.String(colSubAuthHeaderName) ||
			priorAuthSealed != rec.String(colSubAuthValSealed) {
			if err := proof.StampInto(r.Context(), sc, rec); err != nil {
				return err
			}
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toSubscriptionDTO(updated)
		// Reconcile the 1:1 sink profile: declared => create-or-update it; absent =>
		// drop any existing one (revert to the generic webhook).
		if in.hasSink() {
			if err := m.upsertSinkRow(r.Context(), sc, id, &in, sealedSink, proof); err != nil {
				return err
			}
			if err := m.loadSinkDTO(r.Context(), sc, &out); err != nil {
				return err
			}
		} else if err := m.clearSinkProfile(r.Context(), sc, id, proof); err != nil {
			// The subscription SURVIVES this update, so dropping its profile moves the destination
			// back to the base endpoint. That is a governed mutation, not a cleanup.
			return err
		}
		if err := appendRevision(r.Context(), sc, mc, id, revOpUpdate, out); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.update", subscriptionKind, id,
			map[string]any{"name": in.Name, "event_types": len(in.EventTypes), "role": in.Role, "enabled": out.Enabled, "sink_kind": in.SinkKind})
	})
	if msg, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A re-enabled subscription may have queued deliveries parked on the
	// disabled-recheck deferral; wake a worker so they resume promptly.
	m.nudgeTenant(mc.Tenant)
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteSubscription removes a subscription. Its queued deliveries are
// dead-lettered lazily by the dispatcher (subscription_deleted) — the rows stay
// as evidence until retention prunes them.
func (m *Module) handleDeleteSubscription(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// Snapshot the pre-delete state (with its sink profile) BEFORE anything
		// is dropped: the delete revision is the evidence of what existed.
		last := toSubscriptionDTO(rec)
		if err := m.loadSinkDTO(r.Context(), sc, &last); err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		// Drop the 1:1 sink profile WITH the subscription (idempotent — a generic webhook has
		// none). No proof needed: nothing is being re-pointed, the endpoint row is going away in
		// this same transaction.
		if err := m.deleteSinkRowWithSubscription(r.Context(), sc, id); err != nil {
			return err
		}
		if err := appendRevision(r.Context(), sc, mc, id, revOpDelete, last); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.delete", subscriptionKind, id,
			map[string]any{"name": rec.String(colSubName)})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// handleRotateSecret replaces the subscription's signing secret, returning the
// new cleartext exactly once. The old secret stops signing immediately —
// consumers rotate by updating their verifier first, then calling this.
func (m *Module) handleRotateSecret(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	secret, err := newSecret()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	sealed, err := m.sealer.Seal(r.Context(), mc.Tenant, []byte(secret))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var out subscriptionDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		rec[colSubSecret] = sealed
		rec[colSubSecretHint] = secretHint(secret)
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toSubscriptionDTO(updated)
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.rotate_secret", subscriptionKind, id,
			map[string]any{"name": rec.String(colSubName)})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, createdSubscriptionDTO{subscriptionDTO: out, Secret: secret})
}

// newSecret mints a signing secret: "olvw_" (the credential-prefix convention,
// next to olvk_/olvs_) + 48 hex chars (24 random bytes).
func newSecret() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "olvw_" + hex.EncodeToString(b[:]), nil
}

// secretHint is the non-secret display fingerprint: the first 12 hex chars of
// the secret's SHA-256 (enough to tell two secrets apart, useless to recover).
func secretHint(secret string) string {
	return hashHex([]byte(secret))[:12]
}

// handleRotateAuthValue replaces the subscription's auth credential, returning
// the old hint (the new cleartext is never returned — it is supplied by the
// caller). An auth_type change (e.g. none→bearer) is done via the update
// endpoint; this endpoint only replaces the credential value for an existing
// auth_type that requires one.
func (m *Module) handleRotateAuthValue(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var body struct {
		AuthValue string `json:"auth_value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.AuthValue = strings.TrimSpace(body.AuthValue)
	if body.AuthValue == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("auth_value is required"))
		return
	}
	if len(body.AuthValue) > maxEndpointLen {
		writeJSON(w, http.StatusBadRequest, errorBody("auth_value too long"))
		return
	}
	sealed, err := m.sealer.Seal(r.Context(), mc.Tenant, []byte(body.AuthValue))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Unit H: the credential is part of the destination — it becomes a header on the same
	// request, and on a multi-tenant collector it is the token that selects the receiving workspace.
	// A rotation to the same receiver is indistinguishable from a switch to another, so it is
	// governed like the sink's sealed credential already was. Read before the transaction, as always.
	proof, perr := m.writerProof(r.Context())
	if perr != nil {
		writeStoreError(w, perr)
		return
	}
	var out subscriptionDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		at := rec.String(colSubAuthType)
		if at == "" || at == authTypeNone {
			return validationError("subscription has no auth credential to rotate (auth_type is " + at + ")")
		}
		rec[colSubAuthValSealed] = sealed
		rec[colSubAuthValHint] = secretHint(body.AuthValue)
		if err := proof.StampInto(r.Context(), sc, rec); err != nil {
			return err
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toSubscriptionDTO(updated)
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.rotate_auth", subscriptionKind, id,
			map[string]any{"name": rec.String(colSubName)})
	})
	if msg, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// quoteBounded quotes a possibly hostile string for an error message (bounded).
func quoteBounded(s string) string {
	if len(s) > 64 {
		s = s[:64] + "…"
	}
	return "\"" + s + "\""
}
