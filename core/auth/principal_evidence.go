// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrPrincipalEvidenceUnavailable means the engine could not reconstruct an
// authenticated principal together with one exact, tenant-local directory
// witness. It deliberately differs from ErrUnauthenticated: the latter says
// the referenced credential is conclusively gone, changed, revoked, or expired;
// this sentinel says evidence was unavailable or malformed and must not be
// treated as an authentication result.
var ErrPrincipalEvidenceUnavailable = errors.New("auth: principal evidence unavailable")

// principalEvidenceProvenance binds a successful reconstruction to the exact
// credential reference, business tenant, directory generation, finite
// database-time window, and canonical authority seal. AuthorizeEvidence may
// consume it, while product routes and readiness remain OFF until the later K3
// composition cut.
type principalEvidenceProvenance struct {
	tenant         model.TenantID
	ref            PrincipalRef
	directoryEpoch store.AuthorizationFactRef
	observedAt     time.Time
	freshUntil     time.Time
	seal           [sha256.Size]byte
}

// ResolvePrincipalScope reconstructs the current authority for one exact
// authenticated credential inside exactly one AuthView. It never trusts grants,
// groups, AAL, expiry, or provenance carried by a previously authenticated
// Principal, and it never substitutes the application's clock for database time.
//
// The returned Principal carries private provenance only. K3 consumers remain
// deliberately unwired, so this method changes no authorization or readiness
// behavior by itself.
func (a *Authenticator) ResolvePrincipalScope(
	ctx context.Context,
	ref PrincipalRef,
	tenant model.TenantID,
) (Principal, error) {
	if ctx == nil || a == nil || a.st == nil || !validPrincipalRef(ref) || !validPrincipalEvidenceTenant(tenant) {
		return Principal{}, principalEvidenceUnavailable("invalid resolver input", nil)
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || deadline.IsZero() {
		return Principal{}, principalEvidenceUnavailable("finite context deadline is required", nil)
	}

	var (
		resolved      Principal
		callbackCalls int
	)
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		callbackCalls++
		if callbackCalls != 1 {
			return principalEvidenceUnavailable("auth view callback was invoked more than once", nil)
		}
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok || evidence == nil {
			return principalEvidenceUnavailable("auth scope lacks principal evidence capability", nil)
		}

		before, err := evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil || !validPrincipalDirectoryEpochFact(tenant, before) {
			return principalEvidenceUnavailable("directory generation before reconstruction", err)
		}

		var material principalEvidenceMaterial
		switch ref.kind {
		case KindUser:
			material, err = resolveSessionEvidenceMaterial(ctx, as, ref, tenant)
		case KindToken:
			material, err = resolveTokenEvidenceMaterial(ctx, as, ref, tenant)
		default:
			return principalEvidenceUnavailable("unsupported credential kind", nil)
		}
		if err != nil {
			return err
		}

		after, err := evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil || !validPrincipalDirectoryEpochFact(tenant, after) || after != before {
			return principalEvidenceUnavailable("directory generation changed during reconstruction", err)
		}

		now, err := evidence.TransactionNow(ctx)
		if err != nil || now.IsZero() {
			return principalEvidenceUnavailable("database clock is unavailable", err)
		}
		if !deadline.After(now.Time()) {
			return principalEvidenceUnavailable("context deadline is not after database time", nil)
		}

		principal, freshUntil, err := material.finalize(now, deadline)
		if err != nil {
			return err
		}
		if !freshUntil.After(now.Time()) {
			return principalEvidenceUnavailable("credential window is not finite and future", nil)
		}
		principal = principal.withCredentialRef(ref.version)
		principal.evidence = principalEvidenceProvenance{
			tenant:         tenant,
			ref:            ref,
			directoryEpoch: before,
			observedAt:     now.Time(),
			freshUntil:     freshUntil,
		}
		seal, err := computePrincipalAuthoritySeal(principal)
		if err != nil {
			return principalEvidenceUnavailable("seal reconstructed principal authority", err)
		}
		principal.evidence.seal = seal
		resolved = principal
		return nil
	})
	if callbackCalls != 1 {
		return Principal{}, principalEvidenceUnavailable("auth view callback was not invoked exactly once", err)
	}
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			return Principal{}, ErrUnauthenticated
		}
		if errors.Is(err, ErrPrincipalEvidenceUnavailable) {
			return Principal{}, err
		}
		return Principal{}, principalEvidenceUnavailable("auth view failed", err)
	}
	return resolved, nil
}

// principalEvidenceMaterial postpones all database-time-dependent checks until
// after the second directory read. That preserves the causal sequence
// G-before -> exact row/read reconstruction -> G-after -> DB clock, while
// still making expiry and elevated-assurance windows authoritative.
type principalEvidenceMaterial struct {
	principal Principal
	// credentialExpiry is nil only for an ordinary non-expiring API token.
	credentialExpiry *model.Timestamp
	// session is non-nil only for a session principal and carries the live AAL
	// state whose effective value must be computed against DB time.
	session *model.AuthSession
}

func (m principalEvidenceMaterial) finalize(
	now model.Timestamp,
	deadline time.Time,
) (Principal, time.Time, error) {
	if m.credentialExpiry != nil {
		// A credential is not evidence at its exact expiry boundary. This is
		// intentionally stricter than historical hot-path token/session checks.
		if m.credentialExpiry.IsZero() {
			return Principal{}, time.Time{}, principalEvidenceUnavailable("credential expiry is malformed", nil)
		}
		if !now.Time().Before(m.credentialExpiry.Time()) {
			return Principal{}, time.Time{}, ErrUnauthenticated
		}
		deadline = earlierDeadline(deadline, m.credentialExpiry.Time())
	}
	p := m.principal
	if m.session != nil {
		aal, elevatedUntil, err := effectiveEvidenceAAL(*m.session, now)
		if err != nil {
			return Principal{}, time.Time{}, err
		}
		p.AAL = aal
		p.AMR = defensiveAMR(m.session.AMR)
		if !elevatedUntil.IsZero() {
			deadline = earlierDeadline(deadline, elevatedUntil.Time())
		}
	}
	return p, deadline.UTC(), nil
}

func resolveSessionEvidenceMaterial(
	ctx context.Context,
	as store.AuthScope,
	ref PrincipalRef,
	tenant model.TenantID,
) (principalEvidenceMaterial, error) {
	session, err := as.Sessions().Get(ctx, ref.credentialID)
	if errors.Is(err, store.ErrNotFound) {
		return principalEvidenceMaterial{}, ErrUnauthenticated
	}
	if err != nil {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("read session credential", err)
	}
	if !exactSessionCredentialRow(session, ref) {
		if session.ID == ref.credentialID && session.Version != ref.version {
			return principalEvidenceMaterial{}, ErrUnauthenticated
		}
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("session credential row is malformed", nil)
	}
	if session.Revoked || session.DeletedAt != nil {
		return principalEvidenceMaterial{}, ErrUnauthenticated
	}
	if !validPrincipalEvidenceID(session.UserID) {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("session user id is malformed", nil)
	}
	user, err := as.Users().Get(ctx, session.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return principalEvidenceMaterial{}, ErrUnauthenticated
	}
	if err != nil {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("read session user", err)
	}
	if !exactAuthSystemRow(user.BaseFields, session.UserID) {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("session user row is malformed", nil)
	}
	if user.DeletedAt != nil {
		return principalEvidenceMaterial{}, ErrUnauthenticated
	}
	if user.Status != model.StatusActive {
		if user.Status != model.StatusInactive {
			return principalEvidenceMaterial{}, principalEvidenceUnavailable("session user status is malformed", nil)
		}
		return principalEvidenceMaterial{}, ErrUnauthenticated
	}
	if user.IsSuperadmin {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("global superadmin session cannot be scoped", nil)
	}
	grants, groups, confined, err := loadPrincipalEvidenceGrants(ctx, as, user.ID, tenant)
	if err != nil {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("reconstruct session grants", err)
	}
	// A directory epoch is tenant-local. A session user without a direct
	// membership in this tenant is not fenced by that epoch: a later Cedar cut
	// may grant User::<id> directly while credential revocation has no reason to
	// bump this tenant's directory generation. Keep that edge unavailable until
	// a fact that covers both authorities is composed explicitly.
	if _, admitted := grants[tenant]; !admitted {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("session user lacks a direct tenant membership", nil)
	}
	principal := newPrincipal(
		KindUser,
		user.ID,
		session.ID,
		false,
		user.DisplayName,
		grants,
		groups,
	).withConfinements(confined)
	return principalEvidenceMaterial{
		principal:        principal,
		credentialExpiry: &session.ExpiresAt,
		session:          &session,
	}, nil
}

func resolveTokenEvidenceMaterial(
	ctx context.Context,
	as store.AuthScope,
	ref PrincipalRef,
	tenant model.TenantID,
) (principalEvidenceMaterial, error) {
	token, err := as.Tokens().Get(ctx, ref.credentialID)
	if errors.Is(err, store.ErrNotFound) {
		return principalEvidenceMaterial{}, ErrUnauthenticated
	}
	if err != nil {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("read API token credential", err)
	}
	if !exactTokenCredentialRow(token, ref) {
		if token.ID == ref.credentialID && token.Version != ref.version {
			return principalEvidenceMaterial{}, ErrUnauthenticated
		}
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("API token credential row is malformed", nil)
	}
	if token.Revoked || token.DeletedAt != nil {
		return principalEvidenceMaterial{}, ErrUnauthenticated
	}
	if token.IsSuperadmin {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("global superadmin token cannot be scoped", nil)
	}
	if !token.UserID.IsZero() && !validPrincipalEvidenceID(token.UserID) {
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("API token owner id is malformed", nil)
	}

	var principal Principal
	switch token.Purpose {
	case "":
		if token.SessionRef != "" || !token.WorkspaceID.IsZero() || token.SessionRunRef != "" ||
			token.SessionFence != 0 || tokenCarriesDelegationBinding(token) ||
			!validPrincipalEvidenceTenant(token.BoundTenantID) ||
			token.BoundTenantID != tenant || !IsRole(token.Role) {
			return principalEvidenceMaterial{}, principalEvidenceUnavailable("ordinary token binding is malformed or out of scope", nil)
		}
		principal = newPrincipal(
			KindToken,
			token.UserID,
			token.ID,
			false,
			token.Name,
			map[model.TenantID]string{token.BoundTenantID: token.Role},
			nil,
		)
	case WorkSessionCredentialPurpose:
		if token.BoundTenantID != tenant {
			return principalEvidenceMaterial{}, principalEvidenceUnavailable("work-session token is out of scope", nil)
		}
		var ok bool
		principal, ok = workSessionPrincipal(token)
		if !ok {
			return principalEvidenceMaterial{}, principalEvidenceUnavailable("work-session token binding is malformed", nil)
		}
	case CommunicationSessionCredentialPurpose:
		if token.BoundTenantID != tenant {
			return principalEvidenceMaterial{}, principalEvidenceUnavailable("communication-session token is out of scope", nil)
		}
		var ok bool
		principal, ok = communicationSessionPrincipal(token)
		if !ok {
			return principalEvidenceMaterial{}, principalEvidenceUnavailable("communication-session token binding is malformed", nil)
		}
	default:
		return principalEvidenceMaterial{}, principalEvidenceUnavailable("API token purpose is unsupported for evidence", nil)
	}
	return principalEvidenceMaterial{
		principal:        principal,
		credentialExpiry: token.ExpiresAt,
	}, nil
}

func exactSessionCredentialRow(row model.AuthSession, ref PrincipalRef) bool {
	return ref.kind == KindUser && exactAuthSystemRow(row.BaseFields, ref.credentialID) &&
		row.Version == ref.version
}

func exactTokenCredentialRow(row model.APIToken, ref PrincipalRef) bool {
	return ref.kind == KindToken && exactAuthSystemRow(row.BaseFields, ref.credentialID) &&
		row.Version == ref.version
}

func exactAuthSystemRow(base model.BaseFields, id model.ID) bool {
	return base.ID == id && validPrincipalEvidenceAuthBase(base)
}

func validPrincipalEvidenceAuthBase(base model.BaseFields) bool {
	return base.TenantID == model.SystemTenantID && base.Version >= 1 &&
		validPrincipalEvidenceID(base.ID)
}

func validLivePrincipalEvidenceAuthBase(base model.BaseFields) bool {
	return base.DeletedAt == nil && validPrincipalEvidenceAuthBase(base)
}

func validPrincipalRef(ref PrincipalRef) bool {
	return (ref.kind == KindUser || ref.kind == KindToken) &&
		ref.version >= 1 && validPrincipalEvidenceID(ref.credentialID)
}

func validPrincipalEvidenceTenant(tenant model.TenantID) bool {
	if tenant.IsZero() || tenant.IsSystem() {
		return false
	}
	parsed, err := model.ParseTenantID(tenant.String())
	return err == nil && parsed == tenant && validPrincipalEvidenceUUIDv7(tenant.String())
}

func validPrincipalEvidenceID(id model.ID) bool {
	if id.IsZero() {
		return false
	}
	parsed, err := model.ParseID(id.String())
	return err == nil && parsed == id && validPrincipalEvidenceUUIDv7(id.String())
}

func validPrincipalEvidenceUUIDv7(raw string) bool {
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed != uuid.Nil && parsed.String() == raw &&
		parsed.Version() == uuid.Version(7) && parsed.Variant() == uuid.RFC4122
}

func validPrincipalDirectoryEpochFact(tenant model.TenantID, fact store.AuthorizationFactRef) bool {
	if fact.Kind != model.DirectoryEpochKind || fact.ID != model.ID(tenant) || fact.Version < 1 ||
		!validPrincipalEvidenceTenant(tenant) || !validPrincipalEvidenceID(fact.ID) {
		return false
	}
	_, _, _, leased := fact.LeaseFenceWitness()
	return !leased
}

func effectiveEvidenceAAL(session model.AuthSession, now model.Timestamp) (int, model.Timestamp, error) {
	if !defensiveAMRValid(session.AMR) {
		return 0, model.Timestamp{}, principalEvidenceUnavailable("session AMR is malformed", nil)
	}
	switch session.AAL {
	case 0, AAL1:
		return AAL1, model.Timestamp{}, nil
	case AAL3:
		if session.AALExpiresAt == nil || !now.Time().Before(session.AALExpiresAt.Time()) {
			return AAL1, model.Timestamp{}, nil
		}
		if !containsElevatedAMR(session.AMR) {
			return 0, model.Timestamp{}, principalEvidenceUnavailable("elevated session lacks verified AMR", nil)
		}
		return AAL3, *session.AALExpiresAt, nil
	default:
		return 0, model.Timestamp{}, principalEvidenceUnavailable("session AAL is malformed", nil)
	}
}

func defensiveAMR(methods []string) []string {
	if len(methods) == 0 {
		return nil
	}
	out := make([]string, len(methods))
	copy(out, methods)
	return out
}

func defensiveAMRValid(methods []string) bool {
	seen := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		if method == "" || len(method) > 64 || strings.TrimSpace(method) != method ||
			strings.ContainsAny(method, "\n\r\x00") {
			return false
		}
		if _, duplicate := seen[method]; duplicate {
			return false
		}
		seen[method] = struct{}{}
	}
	return true
}

func containsElevatedAMR(methods []string) bool {
	for _, method := range methods {
		if method == "webauthn" || method == "piv" {
			return true
		}
	}
	return false
}

func earlierDeadline(left, right time.Time) time.Time {
	if right.Before(left) {
		return right.UTC()
	}
	return left.UTC()
}

func principalEvidenceUnavailable(what string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrPrincipalEvidenceUnavailable, what)
	}
	return fmt.Errorf("%w: %s: %v", ErrPrincipalEvidenceUnavailable, what, cause)
}

// loadPrincipalEvidenceGrants is the evidence-only, strict counterpart to the
// historical loadGrants hot-path fold. It deliberately lists the complete auth
// partition and self-filters the subject plus requested tenant so a repository
// decorator cannot make an identity-bearing filter silently decide which
// durable authority was read. The output deliberately carries no other tenant's
// grants: this provenance has exactly one tenant-local directory witness.
// Every selected membership/group/edge is validated as a canonical SYSTEM row
// before it affects grants, groups or confinement.
func loadPrincipalEvidenceGrants(
	ctx context.Context,
	as store.AuthScope,
	userID model.ID,
	tenant model.TenantID,
) (map[model.TenantID]string, map[model.TenantID][]string, map[model.TenantID]model.ID, error) {
	if !validPrincipalEvidenceID(userID) || !validPrincipalEvidenceTenant(tenant) {
		return nil, nil, nil, principalEvidenceUnavailable("grant subject id is malformed", nil)
	}
	memberships, err := drainList(ctx, as.Memberships().List, model.Query{})
	if err != nil {
		return nil, nil, nil, principalEvidenceUnavailable("list memberships", err)
	}
	type membershipIdentity struct {
		userID model.ID
		tenant model.TenantID
	}
	seenMembershipIDs := make(map[model.ID]membershipIdentity)
	grants := make(map[model.TenantID]string)
	var confined map[model.TenantID]model.ID
	for _, membership := range memberships {
		relevant := membership.UserID == userID && membership.TargetTenantID == tenant
		// As for group-member edges, track the durable row identity before
		// filtering. A row presented once outside the requested subject/tenant
		// and once inside it is ambiguous whichever order the repository chose.
		if !membership.ID.IsZero() {
			if prior, duplicate := seenMembershipIDs[membership.ID]; duplicate {
				if (prior.userID == userID && prior.tenant == tenant) || relevant {
					return nil, nil, nil, principalEvidenceUnavailable("membership row identity is ambiguous", nil)
				}
			} else {
				seenMembershipIDs[membership.ID] = membershipIdentity{
					userID: membership.UserID,
					tenant: membership.TargetTenantID,
				}
			}
		}
		if !relevant {
			continue
		}
		if !validLivePrincipalEvidenceAuthBase(membership.BaseFields) ||
			membership.UserID != userID || !validPrincipalEvidenceTenant(membership.TargetTenantID) ||
			!IsRole(membership.Role) ||
			(!membership.WorkspaceID.IsZero() && !validPrincipalEvidenceID(membership.WorkspaceID)) {
			return nil, nil, nil, principalEvidenceUnavailable("membership row is malformed", nil)
		}
		if _, duplicate := grants[membership.TargetTenantID]; duplicate {
			return nil, nil, nil, principalEvidenceUnavailable("membership rows are ambiguous", nil)
		}
		grants[membership.TargetTenantID] = membership.Role
		if !membership.WorkspaceID.IsZero() {
			if confined == nil {
				confined = make(map[model.TenantID]model.ID)
			}
			confined[membership.TargetTenantID] = membership.WorkspaceID
		}
	}

	groupMembers, err := drainList(ctx, as.GroupMembers().List, model.Query{})
	if err != nil {
		return nil, nil, nil, principalEvidenceUnavailable("list group memberships", err)
	}
	groupCache := make(map[model.ID]model.UserGroup)
	type groupEdgeIdentity struct {
		userID  model.ID
		groupID model.ID
	}
	seenEdgeIDs := make(map[model.ID]groupEdgeIdentity)
	seenGroupRows := make(map[model.ID]struct{})
	seenSubjects := make(map[model.ID]struct{})
	var subjectGroups map[model.TenantID][]string
	for _, edge := range groupMembers {
		// Track every nonzero row id before filtering the subject. If a
		// decorator presents one durable identity once for another user and once
		// for this user, the order must not decide whether it can add authority.
		// Duplicates wholly outside this subject remain irrelevant to this
		// tenant-local reconstruction.
		if !edge.ID.IsZero() {
			if prior, duplicate := seenEdgeIDs[edge.ID]; duplicate {
				if prior.userID == userID || edge.UserID == userID {
					return nil, nil, nil, principalEvidenceUnavailable("group membership row identity is ambiguous", nil)
				}
			} else {
				seenEdgeIDs[edge.ID] = groupEdgeIdentity{userID: edge.UserID, groupID: edge.GroupID}
			}
		}
		if edge.UserID != userID {
			continue
		}
		if !validLivePrincipalEvidenceAuthBase(edge.BaseFields) || edge.UserID != userID ||
			!validPrincipalEvidenceID(edge.GroupID) {
			return nil, nil, nil, principalEvidenceUnavailable("group membership row is malformed", nil)
		}
		if _, duplicate := seenGroupRows[edge.GroupID]; duplicate {
			return nil, nil, nil, principalEvidenceUnavailable("group membership rows are ambiguous", nil)
		}
		seenGroupRows[edge.GroupID] = struct{}{}
		group, err := evidenceGroupByID(ctx, as, groupCache, edge.GroupID)
		if err != nil {
			return nil, nil, nil, err
		}
		if group.TargetTenantID != tenant {
			continue
		}
		role, directMember := grants[group.TargetTenantID]
		if !directMember {
			continue // the historical per-tenant gate; a group never admits alone.
		}
		mappedRole, err := addEvidenceGroupClosure(ctx, as, groupCache, group, seenSubjects, &subjectGroups)
		if err != nil {
			return nil, nil, nil, err
		}
		if mappedRole != "" && RoleRank(mappedRole) > RoleRank(role) {
			grants[group.TargetTenantID] = mappedRole
		}
	}
	return grants, subjectGroups, confined, nil
}

func evidenceGroupByID(
	ctx context.Context,
	as store.AuthScope,
	cache map[model.ID]model.UserGroup,
	id model.ID,
) (model.UserGroup, error) {
	if group, ok := cache[id]; ok {
		return group, nil
	}
	group, err := as.Groups().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return model.UserGroup{}, principalEvidenceUnavailable("group edge is dangling", nil)
	}
	if err != nil {
		return model.UserGroup{}, principalEvidenceUnavailable("read group", err)
	}
	if group.ID != id || !validLivePrincipalEvidenceAuthBase(group.BaseFields) ||
		!validPrincipalEvidenceTenant(group.TargetTenantID) ||
		(group.MappedRole != "" && !IsRole(group.MappedRole)) ||
		(!group.ParentGroupID.IsZero() && !validPrincipalEvidenceID(group.ParentGroupID)) {
		return model.UserGroup{}, principalEvidenceUnavailable("group row is malformed", nil)
	}
	cache[id] = group
	return group, nil
}

func addEvidenceGroupClosure(
	ctx context.Context,
	as store.AuthScope,
	cache map[model.ID]model.UserGroup,
	group model.UserGroup,
	seen map[model.ID]struct{},
	out *map[model.TenantID][]string,
) (string, error) {
	tenant := group.TargetTenantID
	mappedRole := group.MappedRole
	local := make(map[model.ID]struct{})
	for {
		if _, cycle := local[group.ID]; cycle {
			return "", principalEvidenceUnavailable("group closure contains a cycle", nil)
		}
		local[group.ID] = struct{}{}
		if _, alreadySeen := seen[group.ID]; !alreadySeen {
			seen[group.ID] = struct{}{}
			if *out == nil {
				*out = make(map[model.TenantID][]string)
			}
			(*out)[tenant] = append((*out)[tenant], group.ID.String())
		}
		if group.ParentGroupID.IsZero() {
			return mappedRole, nil
		}
		parent, err := evidenceGroupByID(ctx, as, cache, group.ParentGroupID)
		if err != nil {
			return "", err
		}
		if parent.TargetTenantID != tenant {
			return "", principalEvidenceUnavailable("group closure crosses tenants", nil)
		}
		group = parent
	}
}
