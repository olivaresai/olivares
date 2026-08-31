// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// The repository decorators behind ConfineWorkspace. They share one shape:
//
//   - List REPLACES any caller predicate on the lineage column with the forced
//     one and delegates once, so paging stays keyset-native in SQL;
//   - Get resolves the row's effective workspace and turns a foreign row into
//     ErrNotFound — never a distinguishable "forbidden", which would make the
//     handle an oracle for ids in other workspaces;
//   - Create/Update/Delete verify BEFORE delegating, and refuse rather than
//     silently rewriting the workspace of an incoming row: rewriting would hide
//     a handler bug behind a correct-looking result.

// confinedRepo confines a typed core repository whose entity carries a workspace
// id field.
type confinedRepo[T any] struct {
	raw         Repository[T]
	b           workspaceBoundary
	spec        model.WorkspaceLineageSpec
	workspaceOf func(T) model.ID
}

func (r confinedRepo[T]) List(ctx context.Context, q model.Query) ([]T, model.Page, error) {
	return r.raw.List(ctx, forceQuery(q, r.b.filterFor(r.spec)))
}

func (r confinedRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	v, err := r.raw.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if r.b.effectiveID(r.workspaceOf(v)) != r.b.id {
		var zero T
		return zero, ErrNotFound
	}
	return v, nil
}

func (r confinedRepo[T]) Lock(ctx context.Context, id model.ID) (T, error) {
	locker, ok := r.raw.(RowLocker[T])
	if !ok {
		var zero T
		return zero, ErrRowLockUnavailable
	}
	v, err := locker.Lock(ctx, id)
	if err != nil {
		return v, err
	}
	if r.b.effectiveID(r.workspaceOf(v)) != r.b.id {
		var zero T
		return zero, ErrNotFound
	}
	return v, nil
}

func (r confinedRepo[T]) Create(ctx context.Context, v T) (T, error) {
	if err := r.checkIncoming(v); err != nil {
		var zero T
		return zero, err
	}
	return r.raw.Create(ctx, v)
}

func (r confinedRepo[T]) Update(ctx context.Context, v T) (T, error) {
	if err := r.checkIncoming(v); err != nil {
		var zero T
		return zero, err
	}
	return r.raw.Update(ctx, v)
}

func (r confinedRepo[T]) Delete(ctx context.Context, id model.ID) error {
	// Get through the decorator first: a foreign id is ErrNotFound and never
	// reaches the raw Delete.
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return r.raw.Delete(ctx, id)
}

// checkIncoming refuses a row whose workspace is not the confined one. An UNSET
// workspace is accepted only when unset means the default workspace AND the
// caller is confined to it — otherwise the row would land in somebody else's.
func (r confinedRepo[T]) checkIncoming(v T) error {
	ws := r.workspaceOf(v)
	if ws.IsZero() && r.spec.Unset != model.WorkspaceUnsetMeansDefault {
		return deniedWrite("a row with no workspace cannot be written by a workspace-confined caller")
	}
	if r.b.effectiveID(ws) != r.b.id {
		return deniedWrite("a row belonging to another workspace cannot be written")
	}
	return nil
}

// confinedResourceRepo adds the tree operations to the flat CRUD. Children,
// Subtree and Move each have their own way of reaching rows, so each is
// confined explicitly rather than inheriting a filter that only covers List.
type confinedResourceRepo struct {
	raw  ResourceRepo
	flat confinedRepo[model.Resource]
	b    workspaceBoundary
}

func (r confinedResourceRepo) List(ctx context.Context, q model.Query) ([]model.Resource, model.Page, error) {
	return r.flat.List(ctx, q)
}
func (r confinedResourceRepo) Get(ctx context.Context, id model.ID) (model.Resource, error) {
	return r.flat.Get(ctx, id)
}
func (r confinedResourceRepo) Create(ctx context.Context, v model.Resource) (model.Resource, error) {
	return r.flat.Create(ctx, v)
}
func (r confinedResourceRepo) Update(ctx context.Context, v model.Resource) (model.Resource, error) {
	if _, err := r.flat.Get(ctx, v.ID); err != nil {
		return model.Resource{}, err
	}
	return r.flat.Update(ctx, v)
}
func (r confinedResourceRepo) Delete(ctx context.Context, id model.ID) error {
	return r.flat.Delete(ctx, id)
}

func (r confinedResourceRepo) CreateUnder(ctx context.Context, parent model.ID, v model.Resource) (model.Resource, error) {
	if !parent.IsZero() {
		if _, err := r.flat.Get(ctx, parent); err != nil {
			return model.Resource{}, err
		}
	}
	if err := r.flat.checkIncoming(v); err != nil {
		return model.Resource{}, err
	}
	return r.raw.CreateUnder(ctx, parent, v)
}

func (r confinedResourceRepo) Children(ctx context.Context, parent model.ID, q model.Query) ([]model.Resource, model.Page, error) {
	if !parent.IsZero() {
		if _, err := r.flat.Get(ctx, parent); err != nil {
			return nil, model.Page{}, err
		}
	}
	return r.raw.Children(ctx, parent, forceQuery(q, r.b.filterFor(r.flat.spec)))
}

func (r confinedResourceRepo) Subtree(ctx context.Context, root model.ID, q model.Query) ([]model.Resource, model.Page, error) {
	// The anchor must be visible before its subtree is walked: a foreign root is
	// ErrNotFound, so the prefix scan never runs on somebody else's tree. The
	// forced filter then also covers a subtree that spans workspaces.
	if _, err := r.flat.Get(ctx, root); err != nil {
		return nil, model.Page{}, err
	}
	return r.raw.Subtree(ctx, root, forceQuery(q, r.b.filterFor(r.flat.spec)))
}

func (r confinedResourceRepo) Move(ctx context.Context, node, newParent model.ID) (model.Resource, error) {
	if _, err := r.flat.Get(ctx, node); err != nil {
		return model.Resource{}, err
	}
	if !newParent.IsZero() {
		if _, err := r.flat.Get(ctx, newParent); err != nil {
			return model.Resource{}, err
		}
	}
	return r.raw.Move(ctx, node, newParent)
}

// confinedSelfRepo confines the Workspace repository by IDENTITY: the caller's
// own workspace row is the only one that exists for it.
type confinedSelfRepo struct {
	raw Repository[model.Workspace]
	b   workspaceBoundary
}

func (r confinedSelfRepo) List(ctx context.Context, q model.Query) ([]model.Workspace, model.Page, error) {
	return r.raw.List(ctx, forceQuery(q, model.Filter{
		Column: model.ColID, Op: model.OpEq, Value: r.b.id.String(),
	}))
}

func (r confinedSelfRepo) Get(ctx context.Context, id model.ID) (model.Workspace, error) {
	if id != r.b.id {
		return model.Workspace{}, ErrNotFound
	}
	return r.raw.Get(ctx, id)
}

func (r confinedSelfRepo) Create(ctx context.Context, v model.Workspace) (model.Workspace, error) {
	return model.Workspace{}, deniedWrite("a workspace-confined caller cannot create a workspace")
}

func (r confinedSelfRepo) Update(ctx context.Context, v model.Workspace) (model.Workspace, error) {
	if v.ID != r.b.id {
		return model.Workspace{}, ErrNotFound
	}
	return r.raw.Update(ctx, v)
}

func (r confinedSelfRepo) Delete(ctx context.Context, id model.ID) error {
	return deniedWrite("a workspace-confined caller cannot delete a workspace")
}

// confinedMemberRepo allows the point operations on (group → agent) membership
// when BOTH ends resolve inside the boundary, and refuses List, which would need
// a join the query model cannot express.
type confinedMemberRepo struct {
	raw   Repository[model.AgentGroupMember]
	scope *workspaceConfinedScope
}

func (r confinedMemberRepo) List(ctx context.Context, q model.Query) ([]model.AgentGroupMember, model.Page, error) {
	return nil, model.Page{}, denied("agent-group membership rows (no direct workspace column)")
}

func (r confinedMemberRepo) Get(ctx context.Context, id model.ID) (model.AgentGroupMember, error) {
	v, err := r.raw.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if err := r.bothEndsVisible(ctx, v); err != nil {
		return model.AgentGroupMember{}, err
	}
	return v, nil
}

func (r confinedMemberRepo) Create(ctx context.Context, v model.AgentGroupMember) (model.AgentGroupMember, error) {
	if err := r.bothEndsVisible(ctx, v); err != nil {
		return model.AgentGroupMember{}, err
	}
	return r.raw.Create(ctx, v)
}

func (r confinedMemberRepo) Update(ctx context.Context, v model.AgentGroupMember) (model.AgentGroupMember, error) {
	// Both the row as it stands and the row as proposed must be inside: one check
	// alone would let a member be moved into or out of the boundary.
	if _, err := r.Get(ctx, v.ID); err != nil {
		return model.AgentGroupMember{}, err
	}
	if err := r.bothEndsVisible(ctx, v); err != nil {
		return model.AgentGroupMember{}, err
	}
	return r.raw.Update(ctx, v)
}

func (r confinedMemberRepo) Delete(ctx context.Context, id model.ID) error {
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return r.raw.Delete(ctx, id)
}

// bothEndsVisible resolves the group and the agent through the CONFINED
// accessors, so a membership can never name a row the caller cannot see.
func (r confinedMemberRepo) bothEndsVisible(ctx context.Context, v model.AgentGroupMember) error {
	if _, err := r.scope.AgentGroups().Get(ctx, v.GroupID); err != nil {
		return err
	}
	if _, err := r.scope.Agents().Get(ctx, v.AgentID); err != nil {
		return err
	}
	return nil
}

// confinedIdentityRepo allows Get only for an identity that a VISIBLE agent is
// bound to. The identity is then not new information: the agent row the caller
// already reads names it. Everything else is refused, so holding a UUID never
// becomes authority.
type confinedIdentityRepo struct {
	raw    MutableRepository[model.Identity]
	agents Repository[model.Agent]
}

func (r confinedIdentityRepo) List(ctx context.Context, q model.Query) ([]model.Identity, model.Page, error) {
	return nil, model.Page{}, denied("identities")
}

func (r confinedIdentityRepo) Get(ctx context.Context, id model.ID) (model.Identity, error) {
	if err := r.requireVisibleBinding(ctx, id); err != nil {
		return model.Identity{}, err
	}
	return r.raw.Get(ctx, id)
}

func (r confinedIdentityRepo) Lock(ctx context.Context, id model.ID) (model.Identity, error) {
	if err := r.requireVisibleBinding(ctx, id); err != nil {
		return model.Identity{}, err
	}
	locker, ok := r.raw.(RowLocker[model.Identity])
	if !ok {
		return model.Identity{}, ErrRowLockUnavailable
	}
	return locker.Lock(ctx, id)
}

func (r confinedIdentityRepo) requireVisibleBinding(ctx context.Context, id model.ID) error {
	if id.IsZero() {
		return ErrNotFound
	}
	// One row is enough to prove the binding; the agent repo is already confined,
	// so this cannot see an agent outside the boundary.
	bound, _, err := r.agents.List(ctx, model.Query{
		Filters: []model.Filter{{Column: "identity_id", Op: model.OpEq, Value: id.String()}},
		Limit:   1,
	})
	if err != nil {
		return err
	}
	if len(bound) == 0 {
		return ErrNotFound
	}
	return nil
}

func (r confinedIdentityRepo) Create(ctx context.Context, v model.Identity) (model.Identity, error) {
	return model.Identity{}, deniedWrite("identities are not writable by a workspace-confined caller")
}
func (r confinedIdentityRepo) Update(ctx context.Context, v model.Identity) (model.Identity, error) {
	return model.Identity{}, deniedWrite("identities are not writable by a workspace-confined caller")
}

// deniedRepo refuses every operation for an entity with no workspace lineage.
type deniedRepo[T any] struct{ what string }

func (r deniedRepo[T]) List(ctx context.Context, q model.Query) ([]T, model.Page, error) {
	return nil, model.Page{}, denied(r.what)
}
func (r deniedRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	var zero T
	return zero, denied(r.what)
}
func (r deniedRepo[T]) Create(ctx context.Context, v T) (T, error) {
	var zero T
	return zero, denied(r.what)
}
func (r deniedRepo[T]) Update(ctx context.Context, v T) (T, error) {
	var zero T
	return zero, denied(r.what)
}
func (r deniedRepo[T]) Delete(ctx context.Context, id model.ID) error { return denied(r.what) }

// deniedAccessEdgeRepo refuses the differential access graph: an edge joins two
// nodes that need not share a workspace, so no row predicate is total.
type deniedAccessEdgeRepo struct{ deniedRepo[model.AccessEdge] }

func (deniedAccessEdgeRepo) Neighbors(ctx context.Context, node model.NodeRef, dir model.Direction) ([]model.AccessEdge, error) {
	return nil, denied("the access graph")
}
func (deniedAccessEdgeRepo) Drift(ctx context.Context, q model.Query) ([]model.PrivilegeDrift, error) {
	return nil, denied("privilege drift")
}
func (deniedAccessEdgeRepo) Upsert(ctx context.Context, e model.AccessEdge) (model.AccessEdge, error) {
	return model.AccessEdge{}, denied("the access graph")
}

// confinedAuditLog lets a confined caller APPEND (so a sensitive read can still
// record that it happened) and refuses the reads, which span the tenant's whole
// chain.
const (
	// confinedAuditWorkspaceBindingV1 is a retained metadata contract. Readers
	// must keep recognizing it when a future appender version is introduced.
	confinedAuditWorkspaceBindingV1 int64 = 1
	// confinedAuditCurrentWorkspaceBindingVersion selects the marker stamped on
	// new events. A future version changes this selector, not the historical v1
	// discriminator used by the reader below.
	confinedAuditCurrentWorkspaceBindingVersion = confinedAuditWorkspaceBindingV1
)

type confinedAuditLog struct {
	raw       AuditLog
	workspace model.ID
}

// confinedVerifiedAuditLog preserves the bounded exact-anchor capability only
// when the raw audit log actually provides it. The workspace check happens in
// this decorator, against the canonical metadata already covered by the
// verified chain hash, so a confined caller never gains a tenant-wide audit
// oracle merely by knowing a sequence number.
type confinedVerifiedAuditLog struct {
	confinedAuditLog
	reader    VerifiedAuditAnchorReader
	workspace model.ID
}

type confinedAppendLockedAuditLog struct {
	confinedAuditLog
	locker AuditAppendLocker
}

type confinedVerifiedAppendLockedAuditLog struct {
	confinedVerifiedAuditLog
	locker AuditAppendLocker
}

func confineAuditLog(raw AuditLog, workspace model.ID) AuditLog {
	base := confinedAuditLog{raw: raw, workspace: workspace}
	reader, hasReader := raw.(VerifiedAuditAnchorReader)
	locker, hasLocker := raw.(AuditAppendLocker)
	switch {
	case hasReader && hasLocker:
		return confinedVerifiedAppendLockedAuditLog{
			confinedVerifiedAuditLog: confinedVerifiedAuditLog{
				confinedAuditLog: base, reader: reader, workspace: workspace,
			},
			locker: locker,
		}
	case hasReader:
		return confinedVerifiedAuditLog{
			confinedAuditLog: base, reader: reader, workspace: workspace,
		}
	case hasLocker:
		return confinedAppendLockedAuditLog{confinedAuditLog: base, locker: locker}
	default:
		return base
	}
}

func (a confinedAuditLog) Append(ctx context.Context, d model.AuditDraft) (model.AuditEvent, error) {
	meta := make(map[string]any, len(d.Meta)+2)
	for key, value := range d.Meta {
		meta[key] = value
	}
	// These two fields are engine-owned. Overwriting instead of trusting the
	// caller makes the new exact-anchor capability safe even when a confined
	// caller attempts to label its event as another workspace. The version
	// marker keeps pre-capability rows, whose metadata was not server-stamped,
	// from becoming readable through this new path.
	meta["workspace_id"] = a.workspace.String()
	meta["workspace_binding_version"] = confinedAuditCurrentWorkspaceBindingVersion
	d.Meta = meta
	return a.raw.Append(ctx, d)
}
func (a confinedAuditLog) Verify(ctx context.Context, fromSeq int64) (VerifyReport, error) {
	return VerifyReport{}, denied("the tenant audit chain")
}
func (a confinedAuditLog) Walk(ctx context.Context, fromSeq int64, fn func(model.AuditEvent) error) error {
	return denied("the tenant audit chain")
}
func (a confinedAuditLog) Head(ctx context.Context) (HeadRef, bool, error) {
	return HeadRef{}, false, denied("the tenant audit chain")
}

func (a confinedVerifiedAuditLog) ReadVerifiedAuditAnchor(
	ctx context.Context,
	seq int64,
) (model.AuditEvent, string, bool, error) {
	event, metaCanonical, found, err := a.reader.ReadVerifiedAuditAnchor(ctx, seq)
	if err != nil || !found {
		// Normalize both unavailable/corrupt foreign anchors and absent anchors:
		// the confinement boundary must not reveal tenant-wide chain health.
		return model.AuditEvent{}, "", false, nil
	}
	decoder := json.NewDecoder(strings.NewReader(metaCanonical))
	decoder.UseNumber()
	var meta map[string]any
	if err := decoder.Decode(&meta); err != nil {
		return model.AuditEvent{}, "", false, nil
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return model.AuditEvent{}, "", false, nil
	}
	workspace, workspaceOK := meta["workspace_id"].(string)
	binding, bindingOK := meta["workspace_binding_version"].(json.Number)
	bindingVersion, bindingErr := binding.Int64()
	if !workspaceOK || workspace != a.workspace.String() || !bindingOK ||
		bindingErr != nil || bindingVersion != confinedAuditWorkspaceBindingV1 {
		// A foreign, absent, or non-string workspace marker is indistinguishable
		// from an absent anchor at this boundary. Returning the row or a distinct
		// error would turn the confined capability into a cross-workspace oracle.
		return model.AuditEvent{}, "", false, nil
	}
	return event, metaCanonical, true, nil
}

func (a confinedAppendLockedAuditLog) LockAppends(ctx context.Context) error {
	return a.locker.LockAppends(ctx)
}

func (a confinedVerifiedAppendLockedAuditLog) LockAppends(ctx context.Context) error {
	return a.locker.LockAppends(ctx)
}

// deniedEvidenceOps refuses the tenant-wide journal of governed external
// effects: none of its primitives can prove a workspace.
type deniedEvidenceOps struct{}

func (deniedEvidenceOps) Get(ctx context.Context, operationID string) (model.EvidenceOperation, error) {
	return model.EvidenceOperation{}, denied("the evidence operation journal")
}
func (deniedEvidenceOps) Claim(ctx context.Context, c EvidenceClaim) (EvidenceClaimResult, error) {
	return EvidenceClaimResult{}, denied("the evidence operation journal")
}
func (deniedEvidenceOps) Settle(ctx context.Context, s EvidenceSettlement) (EvidenceSettleResult, error) {
	return EvidenceSettleResult{}, denied("the evidence operation journal")
}

// confinedGenericRepo confines a MODULE entity through its declared lineage. It
// is the same semantics as the typed decorator, reading the lineage value out of
// the model.Record instead of a struct field.
type confinedGenericRepo struct {
	raw  GenericRepo
	b    workspaceBoundary
	spec model.WorkspaceLineageSpec
}

func (r confinedGenericRepo) Descriptor() model.EntityDescriptor { return r.raw.Descriptor() }

func (r confinedGenericRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	return r.raw.List(ctx, forceQuery(q, r.b.filterFor(r.spec)))
}

func (r confinedGenericRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	rec, err := r.raw.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.checkOutgoing(rec)
}

func (r confinedGenericRepo) checkOutgoing(rec model.Record) (model.Record, error) {
	inside, ok := r.b.owns(r.spec, rec.String(r.spec.Column))
	if !ok {
		// The column holds something that is not a workspace. That is a fault in
		// the row, not a license to serve it.
		return nil, deniedWrite("lineage column " + r.spec.Column + " holds an unreadable value")
	}
	if !inside {
		return nil, ErrNotFound
	}
	return rec, nil
}

// confinedRowLockingGenericRepo exists only when the raw repository exposes
// RowLocker. Keeping Lock off confinedGenericRepo itself preserves optional
// capability fidelity across confinement instead of manufacturing a method
// that can only fail later.
type confinedRowLockingGenericRepo struct {
	confinedGenericRepo
	locker RowLocker[model.Record]
}

var _ GenericRepo = confinedRowLockingGenericRepo{}
var _ RowLocker[model.Record] = confinedRowLockingGenericRepo{}

func (r confinedRowLockingGenericRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	rec, err := r.locker.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.checkOutgoing(rec)
}

func (r confinedGenericRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	if err := r.checkIncoming(rec); err != nil {
		return nil, err
	}
	return r.raw.Create(ctx, rec)
}

func (r confinedGenericRepo) CreateWithID(
	ctx context.Context, id model.ID, rec model.Record,
) (model.Record, error) {
	if err := r.checkIncoming(rec); err != nil {
		return nil, err
	}
	return r.raw.CreateWithID(ctx, id, rec)
}

func (r confinedGenericRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	if err := r.checkIncoming(rec); err != nil {
		return nil, err
	}
	return r.raw.Update(ctx, rec)
}

type confinedTransactionStampedGenericRepo struct {
	confinedGenericRepo
	stamped TransactionStampedGenericRepo
}

type confinedTransactionStampedRowLockingGenericRepo struct {
	confinedTransactionStampedGenericRepo
	locker RowLocker[model.Record]
}

var _ TransactionStampedGenericRepo = confinedTransactionStampedRowLockingGenericRepo{}
var _ RowLocker[model.Record] = confinedTransactionStampedRowLockingGenericRepo{}

func (r confinedTransactionStampedRowLockingGenericRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	rec, err := r.locker.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.checkOutgoing(rec)
}

var _ TransactionStampedGenericRepo = confinedTransactionStampedGenericRepo{}

func (r confinedTransactionStampedGenericRepo) CreateAtTransactionTime(
	ctx context.Context,
	rec model.Record,
) (model.Record, error) {
	if err := r.checkIncoming(rec); err != nil {
		return nil, err
	}
	return r.stamped.CreateAtTransactionTime(ctx, rec)
}

func (r confinedTransactionStampedGenericRepo) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	rec model.Record,
) (model.Record, error) {
	if err := r.checkIncoming(rec); err != nil {
		return nil, err
	}
	return r.stamped.CreateWithIDAtTransactionTime(ctx, id, rec)
}

func (r confinedTransactionStampedGenericRepo) UpdateAtTransactionTime(
	ctx context.Context,
	rec model.Record,
) (model.Record, error) {
	if err := r.checkIncoming(rec); err != nil {
		return nil, err
	}
	return r.stamped.UpdateAtTransactionTime(ctx, rec)
}

func (r confinedGenericRepo) Delete(ctx context.Context, id model.ID) error {
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return r.raw.Delete(ctx, id)
}

func (r confinedGenericRepo) checkIncoming(rec model.Record) error {
	inside, ok := r.b.owns(r.spec, rec.String(r.spec.Column))
	if !ok {
		return deniedWrite("lineage column " + r.spec.Column + " holds an unreadable value")
	}
	if !inside {
		return deniedWrite("a row belonging to another workspace cannot be written")
	}
	return nil
}
