// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var (
	communicationChannelEntity = api.EntityRef{
		Kind: channelKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
	}
	communicationChannelReadEntity = api.EntityRef{
		Kind: channelKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
		ConcealDeniedAsNotFound: true,
	}
	// communicationChannelAdministrationEntity is the administrative sheet's OWN
	// entity reference. It conceals a denial as 404 for the same reason the read
	// point does — an enumeration oracle is an enumeration oracle whichever
	// permission it is built on — and it is a separate value from
	// communicationChannelEntity so that concealment never reaches the existing
	// grant and revoke mutations, whose 403/404 policy is unchanged.
	communicationChannelAdministrationEntity = api.EntityRef{
		Kind: channelKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
		ConcealDeniedAsNotFound: true,
	}
	communicationChannelBodyEntity = api.EntityRef{
		Kind: channelKind, BodyIDField: "channel_id", WorkspaceColumn: colWorkWorkspaceID,
	}
	communicationSendChannelEntity = api.EntityRef{
		Kind: channelKind, BodyIDField: "channel_id", WorkspaceColumn: colWorkWorkspaceID,
		ResourceKind: "message-send",
	}
	communicationMessageEntity = api.EntityRef{
		Kind: messageKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
		ConcealDeniedAsNotFound: true,
	}
	communicationDeliveryEntity = api.EntityRef{
		Kind: messageDeliveryKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
		ConcealDeniedAsNotFound: true,
	}
	communicationDeliveryWriteEntity = api.EntityRef{
		Kind: messageDeliveryKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
	}
	communicationCursorDeliveryEntity = api.EntityRef{
		Kind: messageDeliveryKind, BodyIDField: "delivery_id", WorkspaceColumn: colWorkWorkspaceID,
	}
	communicationHandoffEntity = api.EntityRef{
		Kind: handoffKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
	}
)

func (m *Module) communicationRoutes(reg api.RouteRegistrar) {
	// The two workspace-scoped reading collections mount first, through the registrar
	// that corroborates their workspace selector BEFORE the decision.
	m.communicationScopedCollectionRoutes(reg)
	reg.Handle("POST", "/channels", permChannelWrite, m.handleCommunicationChannelCreate)
	reg.HandleEntity("PATCH", "/channels", permChannelAdmin,
		communicationChannelBodyEntity, m.handleCommunicationChannelUpdate)
	// The administrative collection is a LITERAL segment under /channels and its
	// routes are registered before the parameterized point read, so
	// "administration" is never resolved as a Channel identifier.
	m.communicationAdministrationRoutes(reg)
	reg.HandleEntity("GET", "/channels/{id}", permChannelRead,
		communicationChannelReadEntity, m.handleCommunicationChannelGet)
	reg.HandleEntity("POST", "/channels/{id}/grants", permChannelAdmin,
		communicationChannelEntity, m.handleCommunicationChannelGrant)
	reg.HandleEntity("POST", "/channels/{id}/grants/{grant_id}/revoke", permChannelAdmin,
		communicationChannelEntity, m.handleCommunicationChannelGrantRevoke)

	reg.HandleEntity("POST", "/messages/send", permMessageSendWrite,
		communicationSendChannelEntity, m.handleCommunicationMessageSend)
	reg.HandleEntity("GET", "/messages/{id}", permMessageRead,
		communicationMessageEntity, m.handleCommunicationMessageGet)
	reg.HandleEntity("GET", "/deliveries/{id}", permDeliveryRead,
		communicationDeliveryEntity, m.handleCommunicationDeliveryGet)
	reg.Handle("GET", "/inbox/handoffs", permDeliveryRead, m.handleCommunicationIncomingHandoffList)
	reg.HandleEntity("GET", "/deliveries/{id}/handoff", permDeliveryRead,
		communicationDeliveryEntity, m.handleCommunicationIncomingHandoffGet)
	reg.Handle("GET", "/inbox/cursors/personal/{recipient}", permDeliveryRead,
		m.handleCommunicationCursorGet)
	reg.HandleEntity("PUT", "/inbox/cursors/personal/{recipient}", permDeliveryWrite,
		communicationCursorDeliveryEntity, m.handleCommunicationCursorPut)
	reg.HandleEntity("POST", "/deliveries/{id}/ack", permDeliveryWrite,
		communicationDeliveryWriteEntity, m.handleCommunicationDeliveryAck)
	reg.HandleEntity("POST", "/handoffs", permMessageSendWrite,
		communicationSendChannelEntity, m.handleCommunicationHandoffOffer)
	reg.HandleEntity("POST", "/handoffs/{id}/responses", permHandoffResponseWrite,
		communicationHandoffEntity, m.handleCommunicationHandoffResponse)
}

// communicationAdministrationRoutes mounts the two K3 administrative reads so that
// EVERY answer they produce carries `Cache-Control: no-store` — the 200, and just
// as importantly the refusals the engine writes before the module handler exists.
// An authorization denial, a concealed not-found and a rejected query are exactly
// the answers a private cache must never replay to the next caller, and the
// ratified contract puts the directive on the route rather than on its happy path.
//
// ⛔ THE REGISTRAR IS WRAPPED, THE REGISTRATIONS STAY LITERAL. Both calls below are
// ordinary `reg.Handle` / `reg.HandleEntity` with literal method and pattern, and
// that is a requirement rather than a style: scripts/openapi-op-descriptions reads
// this registration with go/parser to learn which operations exist and under which
// namespace, and it follows exactly one indirection — a method of the SAME receiver
// handed the registrar itself. Hiding the two registrations behind a parameterized
// helper made that gate exit 2 (CANNOT LOOK) rather than fail quietly, which is how
// this shape was chosen.
func (m *Module) communicationAdministrationRoutes(reg api.RouteRegistrar) {
	// ⛔ SCOPE FIRST, THEN no-store, AND THE ORDER IS LOAD-BEARING. The scoped
	// registrar is the engine's own value, so it still carries the no-store
	// capability the wrapper below asserts; wrapping the other way round would hand
	// the assertion a module struct that does not implement it, and the administrative
	// reads would silently lose their cache directive.
	reg = communicationScopedCollectionRegistrar(reg)
	reg = communicationNoStoreRoutes(reg)
	reg.Handle("GET", "/channels/administration", permChannelAdmin,
		m.handleCommunicationChannelAdministration)
	reg.HandleEntity("GET", "/channels/{id}/grants", permChannelAdmin,
		communicationChannelAdministrationEntity, m.handleCommunicationChannelGrantAdministration)
}

// communicationScopedCollectionRoutes mounts the two workspace-scoped reading
// collections through the registrar that corroborates their `workspace_id` selector
// BEFORE the outer authorization decision, so the decision is made against the exact
// workspace the request is about.
//
// ⛔ IT IS A METHOD OF THE SAME RECEIVER HANDED THE REGISTRAR, exactly like
// communicationAdministrationRoutes above, and for exactly the reason written there:
// scripts/openapi-op-descriptions follows that ONE indirection and reads `Handle`
// calls made on a registrar PARAMETER. Assigning the scoped registrar to a local
// variable inside communicationRoutes would leave these two registrations unreadable
// to that gate, and the published document would lose their descriptions.
//
// ⛔ AND THE SET IS THESE THREE COLLECTIONS AND NO OTHERS. `POST /channels` creates a
// Channel IN a workspace rather than reading a workspace's rows, so a scoped grant on
// it would authorize creation from a selector the caller chose; the handoff inbox and
// the personal cursors keep their existing admission. Widening this set is an
// authorization change, not a tidy-up.
func (m *Module) communicationScopedCollectionRoutes(reg api.RouteRegistrar) {
	reg = communicationScopedCollectionRegistrar(reg)
	reg.Handle("GET", "/channels", permChannelRead, m.handleCommunicationChannelList)
	reg.Handle("GET", "/inbox", permDeliveryRead, m.handleCommunicationInbox)
}

// communicationScopedCollectionRegistrar returns reg declaring the K3 workspace
// selector when the registrar carries the capability, and reg itself when it does not.
//
// ⛔ THE FALLBACK IS SAFE, AND IT IS SAFE IN THE SAME DIRECTION AS THE no-store ONE. A
// registrar without the capability mounts the identical route with the identical
// admission and authorizes at collection level, which is what these routes have always
// done: the thing lost is the ability of a WORKSPACE-scoped grant to match, and losing
// it grants LESS. Both in-tree registrars implement it and the HTTP battery drives the
// scoped grant end to end, so a real regression is a red test rather than a widening.
func communicationScopedCollectionRegistrar(reg api.RouteRegistrar) api.RouteRegistrar {
	scoped, capable := reg.(api.CollectionScopeRouteRegistrar)
	if !capable {
		return reg
	}
	return scoped.WithCollectionScope(api.CollectionScopeRef{
		WorkspaceQueryParam: communicationWorkspaceSelector,
	})
}

// communicationWorkspaceSelector is the ONE spelling of the K3 workspace selector,
// shared by the registration declaration and by the handlers that read it back. Two
// spellings of the same parameter is how a route comes to corroborate one value and
// filter on another.
const communicationWorkspaceSelector = "workspace_id"

// communicationNoStoreRegistrar mounts every route handed to it through the
// engine's no-store capability, leaving the registration call shape untouched.
type communicationNoStoreRegistrar struct {
	api.RouteRegistrar
	noStore api.NoStoreRouteRegistrar
}

// communicationNoStoreRoutes returns reg wrapped when it carries the capability,
// and reg itself when it does not.
//
// ⛔ THE FALLBACK IS SAFE HERE, AND THAT IS NOT TRUE OF EVERY OPTIONAL CAPABILITY.
// api.HandlePolicy forbids falling back to Handle because dropping it would drop
// the AAL floor, the Cedar action, the scoped-grant requirement and the row
// lineage: the route would SERVE under weaker authorization. This capability
// carries no authorization term at all — it declares one response header — so a
// registrar without it mounts the identical route with the identical admission and
// the only thing lost is a cache directive. Both in-tree registrars implement it,
// and the HTTP battery asserts the header on real refusals, so a real regression
// to the fallback is a red test rather than a silent downgrade.
func communicationNoStoreRoutes(reg api.RouteRegistrar) api.RouteRegistrar {
	noStore, capable := reg.(api.NoStoreRouteRegistrar)
	if !capable {
		return reg
	}
	return communicationNoStoreRegistrar{RouteRegistrar: reg, noStore: noStore}
}

func (r communicationNoStoreRegistrar) Handle(
	method, pattern string, perm auth.Permission, handler api.ModuleHandler,
) {
	r.noStore.HandleNoStore(method, pattern, perm, handler)
}

func (r communicationNoStoreRegistrar) HandleEntity(
	method, pattern string, perm auth.Permission, ref api.EntityRef, handler api.ModuleHandler,
) {
	r.noStore.HandleEntityNoStore(method, pattern, perm, ref, handler)
}

func communicationHTTPContext(r *http.Request) (context.Context, context.CancelFunc) {
	deadline, ok := r.Context().Deadline()
	if !ok {
		deadline = time.Now().Add(15 * time.Second)
	}
	// The shared route has already resolved the tenant/entity workspace and run
	// the outer PEP before installing core/api's private module-data confinement.
	// The communication kernel repeats current credential, policy, directory,
	// grant and row checks and transaction-locks their facts. Give that kernel an
	// engine context so its tenant-wide authority projections (Claim, directory
	// epoch and authorization epoch) remain observable; carrying the module
	// boundary into those projections makes every exact-session credential
	// permanently UNKNOWN. Only lifetime is inherited; request values are not.
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	stop := context.AfterFunc(r.Context(), cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func communicationEntityScopeFromContext(mc api.ModuleContext) (DirectoryScopeRef, error) {
	workspace := mc.Resource.WorkspaceID
	if workspace.IsZero() {
		return DirectoryScopeRef{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"stored communication resource workspace is unavailable",
		)
	}
	scope := DirectoryScopeRef{TenantID: mc.Tenant, WorkspaceID: workspace}
	return scope, scope.Validate()
}

func communicationSelectedScope(
	r *http.Request,
	mc api.ModuleContext,
	raw string,
) (DirectoryScopeRef, error) {
	workspace, err := model.ParseID(raw)
	if err != nil || workspace.IsZero() || workspace.String() != raw {
		return DirectoryScopeRef{}, communicationError(
			ErrInvalidCommunicationModel, "a canonical workspace_id selector is required",
		)
	}
	if confined, ok := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); ok &&
		!mc.Principal.Superadmin && confined != workspace {
		return DirectoryScopeRef{}, communicationError(
			ErrCommunicationForbidden, "workspace selector crosses principal confinement",
		)
	}
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		current, err := sc.Workspaces().Get(r.Context(), workspace)
		if err != nil {
			return err
		}
		if current.ID != workspace || current.TenantID != mc.Tenant {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workspace selector crossed stored scope",
			)
		}
		if current.Status != model.StatusActive {
			return communicationError(
				ErrCommunicationForbidden, "workspace is not active",
			)
		}
		return nil
	})
	if err != nil {
		return DirectoryScopeRef{}, err
	}
	scope := DirectoryScopeRef{TenantID: mc.Tenant, WorkspaceID: workspace}
	return scope, scope.Validate()
}

// communicationCorroboratedScope is how a route whose workspace the ENGINE already
// corroborated reads that workspace back.
//
// ⛔ IT DOES NOT CORROBORATE AGAIN, AND THAT IS THE POINT. The engine parsed this exact
// selector, proved the workspace current, active, in-tenant and inside the caller's
// confinement, and then authorized AGAINST it. Re-deriving the scope here from the raw
// query would create a second producer of the same fact, and the day the two disagreed
// the route would filter on one workspace while having been authorized for another.
//
// What it does check is that they are the SAME value: the selector still has to be
// canonical, and it has to equal the workspace the decision was made against. A
// mismatch is not corrected and not denied — it is unknown authority, because it means
// the two halves of one request no longer describe one question.
func communicationCorroboratedScope(mc api.ModuleContext, raw string) (DirectoryScopeRef, error) {
	workspace, err := model.ParseID(raw)
	if err != nil || workspace.IsZero() || workspace.String() != raw {
		return DirectoryScopeRef{}, communicationError(
			ErrInvalidCommunicationModel, "a canonical workspace_id selector is required",
		)
	}
	if mc.Resource.WorkspaceID != workspace {
		return DirectoryScopeRef{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"authorized collection workspace crossed its selector",
		)
	}
	scope := DirectoryScopeRef{TenantID: mc.Tenant, WorkspaceID: workspace}
	return scope, scope.Validate()
}

func communicationQueryValues(
	r *http.Request,
	allowed map[string]bool,
) (map[string]string, error) {
	result := make(map[string]string, len(allowed))
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return nil, communicationError(ErrInvalidCommunicationModel, "invalid communication query")
		}
		result[key] = values[0]
	}
	return result, nil
}

func decodeCommunicationJSON(r *http.Request, target any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return errors.New("communication request body is required")
	}
	decoder := jsonDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("communication request body has trailing data")
		}
		return err
	}
	return nil
}

func communicationRequestPrincipal(mc api.ModuleContext) (auth.PrincipalRef, error) {
	ref, ok := mc.Principal.Ref()
	if !ok {
		return auth.PrincipalRef{}, communicationError(
			ErrCommunicationEvidenceUnknown, "authenticated principal reference is unavailable",
		)
	}
	return ref, nil
}

// handleCommunicationChannelCreate creates an active Channel with only the
// explicit initial grants supplied by an authorized caller; no grant is implied.
func (m *Module) handleCommunicationChannelCreate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var request struct {
		WorkspaceID string `json:"workspace_id"`
		ChannelCreateCommand
	}
	if err := decodeCommunicationJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid channel command"))
		return
	}
	cmd := request.ChannelCreateCommand
	if cmd.Kind == "" {
		cmd.Kind = ChannelCoordination
	}
	if cmd.Sensitivity == "" {
		cmd.Sensitivity = ChannelInternal
	}
	if cmd.ContentProtection == "" {
		cmd.ContentProtection = ContentProtectionApplicationSealed
	}
	if cmd.DefaultAckPolicy == "" {
		cmd.DefaultAckPolicy = AckPolicyNone
	}
	if cmd.DefaultWake == "" {
		cmd.DefaultWake = WakeNone
	}
	if cmd.MaxFanout == 0 {
		cmd.MaxFanout = 1
	}
	scope, err := communicationSelectedScope(r, mc, request.WorkspaceID)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.CreateChannel(ctx, scope, ref, cmd)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	writeJSON(w, http.StatusCreated, result)
}

// handleCommunicationChannelList lists the active Channels of the selected workspace that the
// current caller may read, with the caller's own current local grant bits, without changing any
// state. Visibility requires the exact core Channel read decision and a current local read
// grant; the opaque continuation is anchored only to the last Channel returned.
func (m *Module) handleCommunicationChannelList(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	values, err := communicationQueryValues(r, map[string]bool{
		"workspace_id": true, "continuation": true, "limit": true,
	})
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	request := ChannelCatalogRequest{Continuation: values["continuation"]}
	if raw := values["limit"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > channelCatalogMaximumLimit || strconv.Itoa(value) != raw {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel catalog query"))
			return
		}
		request.Limit = value
	}
	scope, err := communicationCorroboratedScope(mc, values[communicationWorkspaceSelector])
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.ListVisibleChannels(ctx, scope, ref, request)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationChannelAdministration lists the Channels of the selected workspace that the
// current caller may administer, each with the strong Channel ETag the existing mutations take as
// their precondition, without changing any state. Visibility requires the exact core Channel admin
// decision and a current local admin grant; it neither requires nor grants content read.
func (m *Module) handleCommunicationChannelAdministration(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	values, err := communicationQueryValues(r, map[string]bool{
		"workspace_id": true, "state": true, "continuation": true, "limit": true,
	})
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	// A present-but-empty optional parameter is NON-CANONICAL, not absent: the
	// administrative query set is closed, so `state=` is a 400 rather than a
	// silent default.
	var request ChannelAdministrationRequest
	if raw, present := values["continuation"]; present {
		if raw == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel administration query"))
			return
		}
		request.Continuation = raw
	}
	if raw, present := values["state"]; present {
		request.State = ChannelAdministrationStateFilter(raw)
		if !request.State.Valid() {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel administration query"))
			return
		}
	}
	if raw, present := values["limit"]; present {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > channelAdministrationMaximumLimit || strconv.Itoa(value) != raw {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel administration query"))
			return
		}
		request.Limit = value
	}
	scope, err := communicationCorroboratedScope(mc, values[communicationWorkspaceSelector])
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.ListAdministrableChannels(ctx, scope, ref, request)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	// No representation validator and no stored response: the body varies with
	// filters, pagination and the observation instant without Channel.version
	// moving, so an ETag here would validate a page it does not describe.
	// Cache-Control: no-store is already on the ResponseWriter — the route
	// declared it, so every refusal above carries it too.
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationChannelGrantAdministration returns one administrable Channel, its precondition
// ETag and a bounded page of the stored grant generations the requested filters select, without
// opening content or changing any state.
func (m *Module) handleCommunicationChannelGrantAdministration(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.String() != mc.Resource.ID {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	values, err := communicationQueryValues(r, map[string]bool{
		"workspace_id": true, "state": true, "subject_kind": true, "subject_ref": true,
		"continuation": true, "limit": true,
	})
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	// Same closed set, same rule: present-but-empty is non-canonical.
	request := ChannelGrantAdministrationRequest{ChannelID: id}
	if raw, present := values["continuation"]; present {
		if raw == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel grant administration query"))
			return
		}
		request.Continuation = raw
	}
	if raw, present := values["state"]; present {
		request.State = ChannelGrantAdministrationStateFilter(raw)
		if !request.State.Valid() {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel grant administration query"))
			return
		}
	}
	kind, kindPresent := values["subject_kind"]
	ref, refPresent := values["subject_ref"]
	if kindPresent != refPresent {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind and subject_ref are required together"))
		return
	}
	if kindPresent {
		request.Subject = CommunicationSubjectRef{Kind: CommunicationSubjectKind(kind), Ref: ref}
		request.HasSubject = true
		if request.Subject.Validate() != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel grant administration subject"))
			return
		}
	}
	if raw, present := values["limit"]; present {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > channelGrantAdministrationMaximumLimit || strconv.Itoa(value) != raw {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid channel grant administration query"))
			return
		}
		request.Limit = value
	}
	// The workspace selector is validated as a current, active, confined
	// selection AND compared with the workspace the resolved Channel is STORED
	// in. It cannot replace it: a crossed selection returns nothing.
	selected, err := communicationSelectedScope(r, mc, values["workspace_id"])
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	if selected != scope {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	principal, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.ListChannelGrantAdministration(ctx, scope, principal, request)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationChannelUpdate updates one Channel under conditional-write semantics while
// preserving the irreversible application-sealed content boundary.
func (m *Module) handleCommunicationChannelUpdate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var cmd ChannelUpdateCommand
	if err := decodeCommunicationJSON(r, &cmd); err != nil || cmd.ChannelID != model.ID(mc.Resource.ID) {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid channel update"))
		return
	}
	cmd.IfMatch = r.Header.Get("If-Match")
	m.handleChannelAdminCommand(w, r, mc, func(ctx context.Context, scope DirectoryScopeRef, ref auth.PrincipalRef) (ChannelMutationResult, error) {
		return m.UpdateChannel(ctx, scope, ref, cmd)
	})
}

// handleCommunicationChannelGet returns one Channel only after current core and local read authorization.
func (m *Module) handleCommunicationChannelGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.String() != mc.Resource.ID {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	channel, err := m.GetChannel(ctx, scope, ref, id)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", communicationVersionETag(channel.Version))
	writeJSON(w, http.StatusOK, channel)
}

// handleCommunicationChannelGrant adds one explicit, optionally expiring grant generation.
func (m *Module) handleCommunicationChannelGrant(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	var input ChannelGrantInput
	if err != nil || decodeCommunicationJSON(r, &input) != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid channel grant"))
		return
	}
	cmd := ChannelGrantCommand{ChannelID: id, Grant: input, IfMatch: r.Header.Get("If-Match")}
	m.handleChannelAdminCommand(w, r, mc, func(ctx context.Context, scope DirectoryScopeRef, ref auth.PrincipalRef) (ChannelMutationResult, error) {
		return m.GrantChannel(ctx, scope, ref, cmd)
	})
}

// handleCommunicationChannelGrantRevoke revokes one active grant without deleting its history.
func (m *Module) handleCommunicationChannelGrantRevoke(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	channelID, channelErr := model.ParseID(chi.URLParam(r, "id"))
	grantID, grantErr := model.ParseID(chi.URLParam(r, "grant_id"))
	if channelErr != nil || grantErr != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid channel grant target"))
		return
	}
	cmd := ChannelGrantRevokeCommand{ChannelID: channelID, GrantID: grantID, IfMatch: r.Header.Get("If-Match")}
	m.handleChannelAdminCommand(w, r, mc, func(ctx context.Context, scope DirectoryScopeRef, ref auth.PrincipalRef) (ChannelMutationResult, error) {
		return m.RevokeChannelGrant(ctx, scope, ref, cmd)
	})
}

func (m *Module) handleChannelAdminCommand(
	w http.ResponseWriter, r *http.Request, mc api.ModuleContext,
	command func(context.Context, DirectoryScopeRef, auth.PrincipalRef) (ChannelMutationResult, error),
) {
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := command(ctx, scope, ref)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationMessageSend publishes one governed direct Message and required Delivery.
func (m *Module) handleCommunicationMessageSend(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var cmd DirectNoticePublishCommand
	if err := decodeCommunicationJSON(r, &cmd); err != nil || cmd.ChannelID.String() != mc.Resource.ID {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid message command"))
		return
	}
	cmd.IdempotencyKey = r.Header.Get("Idempotency-Key")
	cmd.ExpectedPlanHash = r.Header.Get("If-Plan-Hash")
	cmd.HTTPMethod = http.MethodPost
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.PublishDirectNotice(ctx, scope, ref, cmd)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

// handleCommunicationMessageGet opens one authorized user Message after its exact carrier checks.
func (m *Module) handleCommunicationMessageGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleCommunicationPointRead(w, r, mc, false)
}

// handleCommunicationDeliveryGet opens one authorized Delivery for its exact user, agent, or session recipient.
func (m *Module) handleCommunicationDeliveryGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleCommunicationPointRead(w, r, mc, true)
}

func (m *Module) handleCommunicationPointRead(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, delivery bool) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.String() != mc.Resource.ID {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	var result DirectNoticeReadResult
	if delivery {
		result, err = m.GetDirectNoticeDelivery(ctx, scope, ref, id)
	} else {
		result, err = m.GetDirectNoticeMessage(ctx, scope, ref, id)
	}
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationInbox lists an authenticated principal's exact mailbox without advancing or acknowledging it.
func (m *Module) handleCommunicationInbox(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	values, err := communicationQueryValues(r, map[string]bool{
		"workspace_id": true, "continuation": true, "limit": true,
	})
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	query := DirectNoticeInboxRequest{Continuation: values["continuation"]}
	if raw := values["limit"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > directNoticeInboxMaximumLimit || strconv.Itoa(value) != raw {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid inbox query"))
			return
		}
		query.Limit = value
	}
	scope, err := communicationCorroboratedScope(mc, values[communicationWorkspaceSelector])
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.ListDirectNoticeInbox(ctx, scope, ref, query)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationIncomingHandoffList lists the handoff offers addressed to the authenticated recipient without opening their content.
func (m *Module) handleCommunicationIncomingHandoffList(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	values, err := communicationQueryValues(r, map[string]bool{
		"workspace_id": true, "state": true, "continuation": true, "limit": true,
	})
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	request := IncomingHandoffRequest{Continuation: values["continuation"]}
	if raw := values["state"]; raw != "" {
		state := HandoffState(raw)
		if !validIncomingHandoffStateFilter(state) {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid incoming handoff query"))
			return
		}
		request.State = state
	}
	if raw := values["limit"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > incomingHandoffMaximumLimit || strconv.Itoa(value) != raw {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid incoming handoff query"))
			return
		}
		request.Limit = value
	}
	scope, err := communicationSelectedScope(r, mc, values["workspace_id"])
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.ListIncomingHandoffs(ctx, scope, ref, request)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	// This projection is derived from the CURRENT authority and the current
	// database clock, so it is never revalidated from a cache. The Handoff ETag it
	// carries is the aggregate CAS coordinate for the response endpoint, not an
	// HTTP validator for this body.
	writeIncomingHandoffPrivateHeaders(w)
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationIncomingHandoffGet opens the offer context of one exact Delivery for its own recipient.
func (m *Module) handleCommunicationIncomingHandoffGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if _, err := communicationQueryValues(r, map[string]bool{}); err != nil {
		writeCommunicationError(w, err)
		return
	}
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.String() != mc.Resource.ID {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.GetIncomingHandoffByDelivery(ctx, scope, ref, id)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	writeIncomingHandoffPrivateHeaders(w)
	writeJSON(w, http.StatusOK, result)
}

// writeIncomingHandoffPrivateHeaders marks both personal reads uncacheable. No
// ETag is emitted for the response body: an If-None-Match round trip would let a
// 304 stand in for an authorization this surface re-runs on every request.
func writeIncomingHandoffPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Vary", "Authorization")
}

// handleCommunicationDeliveryAck acknowledges one exact Delivery with CAS and idempotent receipt semantics.
func (m *Module) handleCommunicationDeliveryAck(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.String() != mc.Resource.ID {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	cmd := DirectNoticeDeliveryAckCommand{IfMatch: r.Header.Get("If-Match"), IdempotencyKey: r.Header.Get("Idempotency-Key")}
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.AcknowledgeDirectNoticeDelivery(ctx, scope, ref, id, cmd)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationHandoffOffer creates the carrier and WorkItem offer atomically without changing ownership.
func (m *Module) handleCommunicationHandoffOffer(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var cmd WorkItemHandoffOfferCommand
	if err := decodeCommunicationJSON(r, &cmd); err != nil || cmd.ChannelID.String() != mc.Resource.ID {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid Handoff offer"))
		return
	}
	cmd.IfMatch = r.Header.Get("If-Match")
	cmd.IdempotencyKey = r.Header.Get("Idempotency-Key")
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.OfferWorkItemHandoff(ctx, scope, ref, cmd)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

// handleCommunicationHandoffResponse accepts or rejects an offered Handoff; accept atomically transfers ownership and fences the old lease.
func (m *Module) handleCommunicationHandoffResponse(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	handoffID, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || handoffID.String() != mc.Resource.ID {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	var cmd HandoffResponseCommand
	if err := decodeCommunicationJSON(r, &cmd); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid Handoff response"))
		return
	}
	cmd.IfMatch = r.Header.Get("If-Match")
	cmd.IdempotencyKey = r.Header.Get("Idempotency-Key")
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	ctx, cancel := communicationHTTPContext(r)
	defer cancel()
	result, err := m.RespondHandoff(ctx, scope, ref, handoffID, cmd)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationCursorGet mints an opaque navigation token without advancing durable cursor state.
func (m *Module) handleCommunicationCursorGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	values, err := communicationQueryValues(r, map[string]bool{
		"workspace_id": true, "target": true,
	})
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	scope, err := communicationSelectedScope(r, mc, values["workspace_id"])
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	reader, ref, ctx, cancel, err := m.communicationCursorRequest(r, mc, scope)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	defer cancel()
	result, err := m.GetInboxCursorToken(ctx, scope, ref, reader, values["target"])
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	writeJSON(w, http.StatusOK, result)
}

// handleCommunicationCursorPut rescans current Delivery authority and conditionally advances the personal cursor and barriers.
func (m *Module) handleCommunicationCursorPut(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if _, err := communicationQueryValues(r, map[string]bool{}); err != nil {
		writeCommunicationError(w, err)
		return
	}
	scope, err := communicationEntityScopeFromContext(mc)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	reader, ref, ctx, cancel, err := m.communicationCursorRequest(r, mc, scope)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	defer cancel()
	var body struct {
		Cursor     string   `json:"cursor"`
		DeliveryID model.ID `json:"delivery_id"`
	}
	if err := decodeCommunicationJSON(r, &body); err != nil ||
		body.DeliveryID.IsZero() || body.DeliveryID.String() != mc.Resource.ID {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid cursor command"))
		return
	}
	cmd := DirectNoticeCursorAdvanceCommand{
		CursorToken: body.Cursor, IfMatch: r.Header.Get("If-Match"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Method: http.MethodPut,
		Path: directNoticeCursorPathPrefix + reader.Ref, targetDelivery: body.DeliveryID,
	}
	result, err := m.AdvanceInboxCursor(ctx, scope, ref, reader, cmd)
	if err != nil {
		writeCommunicationError(w, err)
		return
	}
	w.Header().Set("ETag", result.ETag)
	writeJSON(w, http.StatusOK, result)
}

func (m *Module) communicationCursorRequest(
	r *http.Request,
	mc api.ModuleContext,
	scope DirectoryScopeRef,
) (RecipientRef, auth.PrincipalRef, context.Context, context.CancelFunc, error) {
	ref, err := communicationRequestPrincipal(mc)
	if err != nil {
		return RecipientRef{}, auth.PrincipalRef{}, nil, nil, err
	}
	ctx, cancel := communicationHTTPContext(r)
	principal, err := communicationPrincipalFromResolvedAuth(mc.Principal)
	if err != nil {
		cancel()
		return RecipientRef{}, auth.PrincipalRef{}, nil, nil, err
	}
	reader, _, err := m.communicationPrincipalRecipient(ctx, scope, principal)
	if err != nil {
		cancel()
		return RecipientRef{}, auth.PrincipalRef{}, nil, nil, err
	}
	if chi.URLParam(r, "recipient") != reader.Ref {
		cancel()
		return RecipientRef{}, auth.PrincipalRef{}, nil, nil,
			communicationError(ErrCommunicationForbidden, "cursor path does not name the authenticated mailbox")
	}
	return reader, ref, ctx, cancel, nil
}
