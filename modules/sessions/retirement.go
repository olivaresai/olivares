// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The sessions retirement step. When a tenant removes an account, this step
// removes what sessions stores in that tenant that lets the account act there,
// and reports, by id, what the tenant itself must resolve:
//   - the account's active user ChannelGrants are revoked with the revoke
//     discipline: each turns revoked under the system actor, its Channel's
//     version and ACL revision advance, and the revoke is audited. The schema
//     refuses to delete a ChannelGrant row, so the revoked generation stays as
//     history and lets nobody act;
//   - a Channel subscription or a communication endpoint the account holds, a
//     DecisionRequest it requested or owns and a Handoff that names it, while
//     each is not terminal, a protocol interrupt still awaiting its response and
//     an MCP task it owns while its binding is open block the retirement until
//     the tenant resolves them;
//   - a delivery or an audience arc naming the account and a WorkItem it owns are
//     kept and reported by nothing: a delivery is bound to the tenant's
//     membership, which the offboard already removed, and a WorkItem owner is a
//     duty the tenant reassigns through the work plane.
// The step's transaction first pins the tenant's authorization epoch and the
// account's authority version the pass read, so a pass that read before a lift or
// a new offboard conflicts and changes nothing.

// retirementModule is the declared module name of this step.
const retirementModule = "sessions"

// communicationChannelRetireAudit is the audit action of a grant a retirement
// revoked.
const communicationChannelRetireAudit = "sessions.communication.channel.retire"

// retirementCovers are the counted columns this module's step reads.
var retirementCovers = []string{
	string(runKind) + "." + colRunQueuedActor,
	string(runKind) + "." + colRunQueuedUserID,
	string(runKind) + "." + colRunQueuedCredentialID,
	string(channelGrantKind) + "." + colCommSubjectRef,
	string(channelSubscriptionKind) + "." + colCommSubscriberRef,
	string(communicationEndpointKind) + "." + colCommOwnerRef,
	string(messageDeliveryKind) + "." + colCommRecipientRef,
	string(messageAudienceRecipientKind) + "." + colCommRecipientRef,
	string(decisionRequestKind) + "." + colCommRequesterRef,
	string(decisionRequestKind) + "." + colCommOwnerRef,
	string(handoffKind) + "." + colCommFromRef,
	string(handoffKind) + "." + colCommToRef,
	string(protocolInterruptKind) + "." + colInterruptRecipientUserID,
	string(workItemKind) + "." + colWorkOwnerRef,
	string(protocolBindingKind) + "." + colBindingMCPTaskJSON,
}

// RetirementStep returns this module's retirement step.
func (m *Module) RetirementStep() auth.RetirementStep { return retirementStep{m: m} }

// RetirementCovers returns the counted columns this module's step reads, as
// kind.column.
func (m *Module) RetirementCovers() []string { return append([]string(nil), retirementCovers...) }

type retirementStep struct{ m *Module }

// Module implements auth.RetirementStep.
func (s retirementStep) Module() string { return retirementModule }

// RetireUser implements auth.RetirementStep.
func (s retirementStep) RetireUser(ctx context.Context, req auth.RetirementRequest) (auth.RetirementOutcome, error) {
	m := s.m
	if m == nil || m.data == nil {
		return auth.RetirementOutcome{}, errors.New("sessions: the retirement step has no data handle")
	}
	var out auth.RetirementOutcome
	err := m.data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		fact, err := auth.PinRetirement(ctx, sc, req)
		if err != nil {
			return err
		}
		if err := retireChannelGrants(ctx, sc, req.User); err != nil {
			return err
		}
		blocking, unknown, err := retirementBlockers(ctx, sc, req)
		if err != nil {
			return err
		}
		out.Blocking, out.UnknownKinds, out.FactVersion = blocking, unknown, fact
		return nil
	})
	if err != nil {
		return auth.RetirementOutcome{}, err
	}
	return out, nil
}

// declaredColumn returns the principal declaration of column in repo's
// descriptor: the same object the write seam and the census read.
func declaredColumn(repo store.GenericRepo, column string) (*model.ColumnDecl, error) {
	desc := repo.Descriptor()
	for _, f := range desc.Fields {
		if f.Name == column && f.Principal != nil {
			return f.Principal, nil
		}
	}
	return nil, fmt.Errorf("sessions: %s.%s declares no principal", desc.Kind, column)
}

// userRefFilters selects the rows whose (kind, ref) pair spells user.
func userRefFilters(kindColumn, refColumn string, user model.ID) []model.Filter {
	return []model.Filter{
		{Column: kindColumn, Op: model.OpEq, Value: "user"},
		{Column: refColumn, Op: model.OpEq, Value: user.String()},
	}
}

// retireChannelGrants revokes user's active user ChannelGrants in the scope's
// tenant, Channel by Channel in id order. A repeated pass finds none.
func retireChannelGrants(ctx context.Context, sc store.Scope, user model.ID) error {
	grants, err := sc.Ext(channelGrantKind)
	if err != nil {
		return err
	}
	decl, err := declaredColumn(grants, colCommSubjectRef)
	if err != nil {
		return err
	}
	filters := append(userRefFilters(colCommSubjectKind, colCommSubjectRef, user),
		model.Filter{Column: colCommState, Op: model.OpEq, Value: string(ChannelGrantActive)})
	named, _, err := auth.RowsNaming(ctx, grants, decl, colCommSubjectRef, user, filters...)
	if err != nil || len(named) == 0 {
		return err
	}
	byChannel := make(map[model.ID][]model.ID)
	for _, rec := range named {
		channel := model.ID(rec.String(colCommChannelID))
		byChannel[channel] = append(byChannel[channel], model.ID(rec.String(model.ColID)))
	}
	channelIDs := make([]model.ID, 0, len(byChannel))
	for id := range byChannel {
		channelIDs = append(channelIDs, id)
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })
	now, err := transactionNow(ctx, sc)
	if err != nil {
		return err
	}
	channels, err := sc.Ext(channelKind)
	if err != nil {
		return err
	}
	for _, channelID := range channelIDs {
		if err := revokeRetiredChannelGrants(ctx, sc, channels, grants, channelID, byChannel[channelID], now); err != nil {
			return err
		}
	}
	return nil
}

// retirementActorRef is the system actor a retirement revokes a grant as.
const retirementActorRef = "account-retirement"

// revokeRetiredChannelGrants is RevokeChannelGrant for the grants a retirement
// revokes on one Channel: the Channel row is locked first, then each grant, in
// id order; each still-active grant turns revoked under the system actor; the
// Channel's version and ACL revision advance once; and every revoke is audited.
func revokeRetiredChannelGrants(
	ctx context.Context,
	sc store.Scope,
	channels, grants store.GenericRepo,
	channelID model.ID,
	grantIDs []model.ID,
	now model.Timestamp,
) error {
	channelLocker, channelLockOK := channels.(store.RowLocker[model.Record])
	channelWriter, channelWriteOK := channels.(store.TransactionStampedGenericRepo)
	grantLocker, grantLockOK := grants.(store.RowLocker[model.Record])
	grantWriter, grantWriteOK := grants.(store.TransactionStampedGenericRepo)
	if !channelLockOK || !channelWriteOK || !grantLockOK || !grantWriteOK {
		return communicationTransactionUnavailable("retirement ChannelGrant revoke", nil)
	}
	channelRecord, err := channelLocker.Lock(ctx, channelID)
	if err != nil {
		return err
	}
	before, err := channelFromRecord(channelRecord)
	if err != nil {
		return err
	}
	actor := CommunicationActorRef{Kind: ActorSystem, Ref: retirementActorRef}
	ids := append([]model.ID(nil), grantIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	revoked := make([]model.ID, 0, len(ids))
	for _, id := range ids {
		locked, err := grantLocker.Lock(ctx, id)
		if err != nil {
			return err
		}
		grant, err := channelGrantFromRecord(locked)
		if err != nil {
			return err
		}
		if grant.ChannelID != channelID || grant.State != ChannelGrantActive {
			continue
		}
		beforeVersion := grant.Version
		grant.Version++
		grant.UpdatedAt = now.Time()
		grant.State = ChannelGrantRevoked
		grant.RevokedBy = &actor
		encoded, err := channelGrantToRecord(grant)
		if err != nil {
			return err
		}
		encoded[model.ColVersion] = beforeVersion
		if _, err := grantWriter.UpdateAtTransactionTime(ctx, encoded); err != nil {
			return err
		}
		revoked = append(revoked, id)
	}
	if len(revoked) == 0 {
		return nil
	}
	after := before
	after.Version++
	after.ACLRevision++
	after.UpdatedAt = now.Time()
	if err := ValidateChannelUpdate(before, after); err != nil {
		return err
	}
	afterRecord, err := channelToRecord(after)
	if err != nil {
		return err
	}
	afterRecord[model.ColVersion] = before.Version
	if _, err := channelWriter.UpdateAtTransactionTime(ctx, afterRecord); err != nil {
		return err
	}
	for _, id := range revoked {
		payload, err := canonicalJSON(struct {
			ChannelID model.ID `json:"channel_id"`
			Before    int64    `json:"before"`
			After     int64    `json:"after"`
			GrantID   model.ID `json:"grant_id"`
		}{ChannelID: channelID, Before: before.Version, After: after.Version, GrantID: id})
		if err != nil {
			return err
		}
		hash := sha256.Sum256(payload)
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: communicationChannelRetireAudit, TargetKind: channelKind, TargetID: channelID,
			PayloadHash: hash[:],
			Meta: map[string]any{
				"workspace_id": before.WorkspaceID.String(), "acl_revision": after.ACLRevision,
				"grant_id": id.String(), "subject_kind": string(SubjectUser),
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

// retirementScan is one counted column whose live rows naming the account block
// the retirement.
type retirementScan struct {
	kind    model.Kind
	column  string
	filters []model.Filter
	live    func(model.Record) bool
}

// retirementBlockers returns, as "<kind>:<id>", every live row that names user
// through a counted column this step blocks on, and the tables holding a row
// whose discriminator no registry knows.
func retirementBlockers(ctx context.Context, sc store.Scope, req auth.RetirementRequest) ([]string, []string, error) {
	user := req.User
	scans := []retirementScan{
		{runKind, colRunQueuedUserID, nil, func(r model.Record) bool { return r.String(colRunQueuedUserID) != "" }},
		{runKind, colRunQueuedActor, nil, func(r model.Record) bool { return r.String(colRunQueuedActor) != "" }},
		{runKind, colRunQueuedCredentialID, nil, func(r model.Record) bool { return r.String(colRunQueuedCredentialID) != "" }},
		{channelSubscriptionKind, colCommSubscriberRef,
			userRefFilters(colCommSubscriberKind, colCommSubscriberRef, user),
			stateOutside(colCommState, string(SubscriptionRevoked))},
		{communicationEndpointKind, colCommOwnerRef,
			userRefFilters(colCommOwnerKind, colCommOwnerRef, user),
			stateOutside(colCommState, string(EndpointDisabled))},
		{decisionRequestKind, colCommRequesterRef,
			userRefFilters(colCommRequesterKind, colCommRequesterRef, user),
			stateOutside(colCommState, string(DecisionResolved), string(DecisionRejected),
				string(DecisionCanceled), string(DecisionExpired))},
		{decisionRequestKind, colCommOwnerRef,
			userRefFilters(colCommOwnerKind, colCommOwnerRef, user),
			stateOutside(colCommState, string(DecisionResolved), string(DecisionRejected),
				string(DecisionCanceled), string(DecisionExpired))},
		{handoffKind, colCommFromRef,
			userRefFilters(colCommFromKind, colCommFromRef, user),
			stateOutside(colCommState, string(HandoffAccepted), string(HandoffRejected),
				string(HandoffWithdrawn), string(HandoffExpired))},
		{handoffKind, colCommToRef,
			userRefFilters(colCommToKind, colCommToRef, user),
			stateOutside(colCommState, string(HandoffAccepted), string(HandoffRejected),
				string(HandoffWithdrawn), string(HandoffExpired))},
		{protocolInterruptKind, colInterruptRecipientUserID,
			[]model.Filter{{Column: colInterruptRecipientUserID, Op: model.OpEq, Value: user.String()}},
			stateOutside(colInterruptState, protocolInterruptResponded)},
		{protocolBindingKind, colBindingMCPTaskJSON,
			[]model.Filter{{Column: colBindingTerminal, Op: model.OpEq, Value: false}},
			func(rec model.Record) bool { return !rec.Bool(colBindingTerminal) }},
	}
	unknown, err := providerAccountRetirementUnknowns(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	var blocking []string
	listed := make(map[string]bool)
	unknownKinds := make(map[model.Kind]bool)
	for _, scan := range scans {
		repo, err := sc.Ext(scan.kind)
		if err != nil {
			return nil, nil, err
		}
		decl, err := declaredColumn(repo, scan.column)
		if err != nil {
			return nil, nil, err
		}
		var named []model.Record
		var unknownKind bool
		if scan.kind == runKind {
			named, unknownKind, err = auth.RowsNamingAccount(ctx, repo, decl, scan.column, req, scan.filters...)
		} else {
			named, unknownKind, err = auth.RowsNaming(ctx, repo, decl, scan.column, user, scan.filters...)
		}
		if err != nil {
			return nil, nil, err
		}
		if unknownKind && !unknownKinds[scan.kind] {
			unknownKinds[scan.kind] = true
			unknown = append(unknown, string(scan.kind))
		}
		for _, rec := range named {
			if !scan.live(rec) {
				continue
			}
			ref := string(scan.kind) + ":" + rec.String(model.ColID)
			if !listed[ref] {
				listed[ref] = true
				blocking = append(blocking, ref)
			}
		}
	}
	return blocking, unknown, nil
}

// stateOutside reports a row live while its state column holds none of the
// terminal states; an unknown state counts as live.
func stateOutside(column string, terminal ...string) func(model.Record) bool {
	return func(rec model.Record) bool {
		state := rec.String(column)
		for _, t := range terminal {
			if state == t {
				return false
			}
		}
		return true
	}
}
