// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"math"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/nativepam"
	"github.com/olivaresai/olivares/core/store"
)

// OSAccounts is the physical native account/PAM boundary, never a product grant.
// Engine composition supplies the native implementation; tests replace only it.
type OSAccounts interface {
	UID(context.Context, string) (uint32, error)
	Name(context.Context, uint32) (string, error)
	Verify(context.Context, string, []byte) (nativepam.Result, error)
}

// NativeOSAccounts uses the operating system's current account directory and the
// fixed account-control PAM worker. No email, GECOS or group yields a subject.
type NativeOSAccounts struct{}

func (NativeOSAccounts) UID(ctx context.Context, name string) (uint32, error) {
	if ctx == nil || ctx.Err() != nil {
		return 0, nativepam.ErrRefused
	}
	uid, err := nativepam.LocalAccount(name)
	if ctx.Err() != nil {
		return 0, nativepam.ErrRefused
	}
	return uid, err
}
func (NativeOSAccounts) Name(ctx context.Context, uid uint32) (string, error) {
	if ctx == nil || ctx.Err() != nil || uid == 0 {
		return "", nativepam.ErrRefused
	}
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil || ctx.Err() != nil {
		return "", nativepam.ErrRefused
	}
	return account.Username, nil
}
func (NativeOSAccounts) Verify(ctx context.Context, name string, password []byte) (nativepam.Result, error) {
	return nativepam.CheckAccount(ctx, name, password)
}

// OSAccountMapping is administrative metadata. It carries neither the private
// binding handle nor any credential reference and cannot authorize an act.
type OSAccountMapping struct {
	Tenant  model.TenantID `json:"tenant"`
	User    model.ID       `json:"user_id"`
	UID     uint32         `json:"uid"`
	Account string         `json:"account"`
	Digest  string         `json:"digest"`
}

// OSAccountCeremony names one expiring, single-use verification attempt. Its ID
// conveys no subject authentication or administrative permission.
type OSAccountCeremony struct {
	ID model.ID `json:"ceremony_id"`
}
type osAccountCeremony struct {
	subject CredentialBindingSubject
	admin   PrincipalRef
}

// OSAccountBindings owns the existing core credential-binding lifecycle. Both
// proofs are reconstructed from private native credential references; caller
// fields or an administrator's session cannot authenticate the target subject.
type osAccountAuthority struct {
	bundle store.AuthoritySnapshotBundle
	until  time.Time
}

type OSAccountBindings struct {
	a      *Authenticator
	az     *Authorizer
	native OSAccounts
}

func NewOSAccountBindings(a *Authenticator, az *Authorizer, native OSAccounts) *OSAccountBindings {
	return &OSAccountBindings{a: a, az: az, native: native}
}
func validOSAccountName(name string) bool {
	return len(name) > 0 && len(name) <= nativepam.MaxLogin && !strings.ContainsAny(name, "\x00\r\n")
}
func (b *OSAccountBindings) ready() bool {
	return b != nil && b.a != nil && b.a.st != nil && b.az != nil && b.native != nil
}
func (b *OSAccountBindings) account(ctx context.Context, name string) (uint32, error) {
	if !validOSAccountName(name) {
		return 0, ErrCredentialBindingInvalid
	}
	uid, err := b.native.UID(ctx, name)
	if err != nil || uid == 0 || ctx.Err() != nil {
		return 0, ErrCredentialBindingInvalid
	}
	canonical, err := b.native.Name(ctx, uid)
	if err != nil || canonical != name || ctx.Err() != nil {
		return 0, ErrCredentialBindingInvalid
	}
	return uid, nil
}
func (b *OSAccountBindings) principal(ctx context.Context, ref PrincipalRef, tenant model.TenantID) (Principal, error) {
	if ref.kind != KindUser {
		return Principal{}, ErrCredentialBindingInvalid
	}
	if err := b.a.credentialBindingAdmissionFor(ctx, ref, tenant, true); err != nil {
		return Principal{}, err
	}
	p, err := b.a.ResolvePrincipalScope(ctx, ref, tenant)
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}
func (b *OSAccountBindings) administrator(ctx context.Context, ref PrincipalRef, tenant model.TenantID, target model.ID) (Principal, osAccountAuthority, error) {
	p, err := b.principal(ctx, ref, tenant)
	if err != nil {
		return Principal{}, osAccountAuthority{}, err
	}
	if !b.a.stepUpSatisfied(ctx, p) {
		return Principal{}, osAccountAuthority{}, ErrStepUpRequired
	}
	request := Request{Principal: p, Tenant: tenant, Permission: "user:write", Resource: ResourceAttrs{Kind: "core.user", ID: target.String()}}
	decision, err := b.az.AuthorizeRouteMutation(ctx, request)
	if err != nil {
		return Principal{}, osAccountAuthority{}, err
	}
	bundle, err := decision.AuthorityFor(b.a.clock.Now().Time(), request)
	if err != nil {
		return Principal{}, osAccountAuthority{}, err
	}
	metadata, err := decision.MetadataFor(b.a.clock.Now().Time(), request)
	return p, osAccountAuthority{bundle: bundle, until: metadata.FreshUntil}, err
}

// Required OS-account proof events gate admission and commit, including when
// the ledger's configured degrade policy drops an event without an error.
func appendOSAccountAudit(ctx context.Context, audit store.AuditLog, draft model.AuditDraft) error {
	event, err := audit.Append(ctx, draft)
	if err != nil {
		return err
	}
	if event.Seq < 1 {
		return credentialBindingUnavailable("OS account proof audit was not persisted", nil)
	}
	return nil
}

func (b *OSAccountBindings) Begin(ctx context.Context, admin Principal, tenant model.TenantID, target model.ID, name string) (OSAccountCeremony, error) {
	if !b.ready() || ctx == nil {
		return OSAccountCeremony{}, ErrCredentialBindingInvalid
	}
	ctx, cancel := credentialBindingContext(ctx)
	defer cancel()
	if !validPrincipalEvidenceTenant(tenant) || !validPrincipalEvidenceID(target) {
		return OSAccountCeremony{}, ErrCredentialBindingInvalid
	}
	ref, ok := admin.Ref()
	if !ok {
		return OSAccountCeremony{}, ErrCredentialBindingInvalid
	}
	current, authority, err := b.administrator(ctx, ref, tenant, target)
	if err != nil {
		return OSAccountCeremony{}, err
	}
	uid, err := b.account(ctx, name)
	if err != nil {
		return OSAccountCeremony{}, err
	}
	subject := CredentialBindingSubject{Tenant: tenant, Kind: CredentialBindingOSAccount, Ref: target, User: target, OSUID: uid, OSAccount: name}
	if err = b.a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if e := pinOSProof(ctx, as, tenant, authority); e != nil {
			return e
		}
		if e := verifyCredentialPin(ctx, as, osPin(current, ref, tenant)); e != nil {
			return e
		}
		e := appendOSAccountAudit(ctx, as.Audit(), model.AuditDraft{Actor: current.Actor(), ActorKind: current.ActorKind(), Action: "auth.os_account.begin", TargetKind: "core.user", TargetID: target, Meta: map[string]any{"tenant": tenant.String()}})
		return e
	}); err != nil {
		return OSAccountCeremony{}, err
	}
	id := model.NewID()
	pending := b.a.ceremonies()
	pending.mu.Lock()
	defer pending.mu.Unlock()
	now := pending.now()
	for key, entry := range pending.m {
		if !now.Before(entry.expires) || (entry.osAccount != nil && entry.osAccount.admin.credentialID == ref.credentialID) {
			delete(pending.m, key)
		}
	}
	pending.m[ceremonyKey(ceremonyOSAccount, id)] = ceremonyEntry{osAccount: &osAccountCeremony{subject: subject, admin: ref}, expires: now.Add(webauthnCeremonyTTL)}
	return OSAccountCeremony{ID: id}, nil
}
func (b *OSAccountBindings) take(id model.ID) (osAccountCeremony, bool) {
	pending := b.a.ceremonies()
	pending.mu.Lock()
	defer pending.mu.Unlock()
	key := ceremonyKey(ceremonyOSAccount, id)
	entry, ok := pending.m[key]
	delete(pending.m, key)
	if !ok || entry.osAccount == nil || !pending.now().Before(entry.expires) {
		return osAccountCeremony{}, false
	}
	return *entry.osAccount, true
}
func osAccountStore(as store.AuthScope) (store.OSAccountBindingStore, error) {
	raw, err := credentialBindingStore(as)
	if err != nil {
		return nil, err
	}
	bindings, ok := raw.(store.OSAccountBindingStore)
	if !ok {
		return nil, credentialBindingUnavailable("auth scope has no OS account custody", nil)
	}
	return bindings, nil
}
func pinOSProof(ctx context.Context, as store.AuthScope, tenant model.TenantID, authority osAccountAuthority) error {
	barrier, ok := as.(store.AuthTenantAuthorityBarrier)
	if !ok {
		return credentialBindingUnavailable("auth scope has no complete authority barrier", nil)
	}
	clock, ok := as.(store.TransactionClock)
	if !ok {
		return ErrCredentialBindingUnavailable
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return err
	}
	if ctx.Err() != nil || authority.until.IsZero() || !now.Time().Before(authority.until) {
		return ErrRouteUndecided
	}
	return barrier.LockAuthTenantAuthority(ctx, tenant, authority.bundle)
}
func osMapping(row model.CredentialBinding) OSAccountMapping {
	return OSAccountMapping{Tenant: row.TargetTenantID, User: row.SubjectUserID, UID: *row.OSUID, Account: row.OSAccount, Digest: credentialBindingDigest(row.ID)}
}
func osPin(p Principal, ref PrincipalRef, tenant model.TenantID) credentialPin {
	role, _ := p.RoleIn(tenant)
	if p.Superadmin {
		role = string(PermSystemAdmin)
	}
	workspace, _ := p.ConfinedWorkspaceIn(tenant)
	return credentialPin{kind: ref.kind, id: ref.credentialID, version: ref.version, user: p.UserID, ceiling: credentialCeiling{kind: ref.kind, role: role, workspace: workspace, agent: p.AgentIdentity}}
}

// The existing ceiling role column records the original installation tier for
// OS bindings only. Workflow roles and their ordering remain unchanged.
func osCeilingWithin(current, original credentialCeiling) bool {
	installation := string(PermSystemAdmin)
	if (!IsRole(current.role) && current.role != installation) ||
		(!IsRole(original.role) && original.role != installation) ||
		(current.role == installation && original.role != installation) {
		return false
	}
	if current.role == installation {
		current.role = RoleOwner
	}
	if original.role == installation {
		original.role = RoleOwner
	}
	return current.within(original)
}

func osSessionPin(p Principal, ref PrincipalRef, tenant model.TenantID, session model.AuthSession) credentialPin {
	pin := osPin(p, ref, tenant)
	if !session.TenantScope.IsZero() && pin.ceiling.role == string(PermSystemAdmin) {
		pin.ceiling.role = RoleOwner
	}
	return pin
}

func (b *OSAccountBindings) proofs(ctx context.Context, c osAccountCeremony, ref PrincipalRef) (Principal, Principal, osAccountAuthority, error) {
	admin, authority, err := b.administrator(ctx, c.admin, c.subject.Tenant, c.subject.User)
	if err != nil {
		return Principal{}, Principal{}, osAccountAuthority{}, err
	}
	subject, err := b.principal(ctx, ref, c.subject.Tenant)
	if err != nil || subject.UserID != c.subject.User {
		return Principal{}, Principal{}, osAccountAuthority{}, ErrCredentialBindingInvalid
	}
	native, window, ok := principalCompleteAuthorizationEvidence(subject, c.subject.Tenant)
	if !ok {
		return Principal{}, Principal{}, osAccountAuthority{}, ErrCredentialBindingInvalid
	}
	bundle, err := MergeAuthoritySnapshotBundles(authority.bundle, native)
	until := authority.until
	if window.freshUntil.Before(until) {
		until = window.freshUntil
	}
	return admin, subject, osAccountAuthority{bundle: bundle, until: until}, err
}
func (b *OSAccountBindings) Complete(ctx context.Context, actor Principal, id model.ID, password []byte) (mapping OSAccountMapping, failure error) {
	defer clear(password)
	if !b.ready() || ctx == nil {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	ctx, cancel := credentialBindingContext(ctx)
	defer cancel()
	c, ok := b.take(id)
	if !ok {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	ref, ok := actor.Ref()
	if !ok {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	admin, subject, bundle, err := b.proofs(ctx, c, ref)
	if err != nil {
		return OSAccountMapping{}, err
	}
	uid, err := b.account(ctx, c.subject.OSAccount)
	if err != nil || uid != c.subject.OSUID || !nativepam.ValidRequest(c.subject.OSAccount, password) {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	// Audit admission precedes presenting any credential to the native worker.
	err = b.a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if e := pinOSProof(ctx, as, c.subject.Tenant, bundle); e != nil {
			return e
		}
		if e := verifyCredentialPin(ctx, as, osPin(admin, c.admin, c.subject.Tenant)); e != nil {
			return e
		}
		if e := verifyCredentialPin(ctx, as, osPin(subject, ref, c.subject.Tenant)); e != nil {
			return e
		}
		e := appendOSAccountAudit(ctx, as.Audit(), model.AuditDraft{Actor: subject.Actor(), ActorKind: subject.ActorKind(), Action: "auth.os_account.verify.intent", TargetKind: "core.user", TargetID: c.subject.User, Meta: map[string]any{"tenant": c.subject.Tenant.String(), "admin_actor": admin.Actor()}})
		return e
	})
	if err != nil {
		return OSAccountMapping{}, err
	}
	controlKnown := false
	defer func() {
		// A known native completion followed by a refusal gets a bounded result.
		// A lost/canceled native completion retains only its unknown intent.
		if !controlKnown || failure == nil || ctx.Err() != nil {
			return
		}
		if err := b.a.st.AuthMutate(ctx, func(as store.AuthScope) error {
			err := appendOSAccountAudit(ctx, as.Audit(), model.AuditDraft{Actor: subject.Actor(), ActorKind: subject.ActorKind(), Action: "auth.os_account.verify.refused", TargetKind: "core.user", TargetID: c.subject.User, Meta: map[string]any{"tenant": c.subject.Tenant.String()}})
			return err
		}); err != nil {
			failure = credentialBindingUnavailable("record native account refusal", err)
		}
	}()
	result, nativeErr := b.native.Verify(ctx, c.subject.OSAccount, password)
	controlKnown = nativeErr == nil
	clear(password)
	if nativeErr != nil || !result.Authenticated || !result.AccountAllowed || result.Login != c.subject.OSAccount || result.UID != c.subject.OSUID || ctx.Err() != nil {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	uid, err = b.account(ctx, c.subject.OSAccount)
	if err != nil || uid != c.subject.OSUID {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	admin, subject, bundle, err = b.proofs(ctx, c, ref)
	if err != nil {
		return OSAccountMapping{}, err
	}
	pin := osPin(subject, ref, c.subject.Tenant)
	var bound model.CredentialBinding
	err = b.a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if e := pinOSProof(ctx, as, c.subject.Tenant, bundle); e != nil {
			return e
		}
		if e := verifyCredentialPin(ctx, as, pin); e != nil {
			return e
		}
		if e := verifyCredentialPin(ctx, as, osPin(admin, c.admin, c.subject.Tenant)); e != nil {
			return e
		}
		session, e := as.Sessions().Get(ctx, pin.id)
		if e != nil {
			return e
		}
		pin = osSessionPin(subject, ref, c.subject.Tenant, session)
		bindings, e := osAccountStore(as)
		if e != nil {
			return e
		}
		owners, e := bindings.OSAccountOwner(ctx, c.subject.OSUID)
		if e != nil {
			return e
		}
		if len(owners) > 1 {
			return ErrCredentialBindingInvalid
		}
		generation := int64(0)
		ceiling := pin.ceiling
		var previous model.CredentialBinding
		if len(owners) == 1 {
			first := owners[0]
			if !credentialBindingNames(first, c.subject) {
				return ErrCredentialBindingConflict
			}
			current, e := bindings.Current(ctx, c.subject.Tenant, c.subject.Kind, c.subject.Ref)
			if e != nil {
				return e
			}
			if len(current) != 1 || !credentialBindingNames(current[0], c.subject) {
				return ErrCredentialBindingInvalid
			}
			previous = current[0]
			ceiling = recordedCeiling(first)
			if !osCeilingWithin(pin.ceiling, ceiling) {
				return ErrCredentialBindingCeiling
			}
			if previous.CredentialKind == string(pin.kind) && previous.CredentialID == pin.id && previous.CredentialVersion == pin.version {
				bound = previous
			} else {
				if previous.SubjectGeneration == math.MaxInt64 {
					return ErrCredentialBindingConflict
				}
				generation = previous.SubjectGeneration + 1
			}
		}
		if bound.ID.IsZero() {
			bound, e = bindings.Create(ctx, sealedCredentialBinding(c.subject, generation, pin, ceiling, admin.Actor()))
			if e != nil {
				return e
			}
			if !previous.ID.IsZero() {
				clock, ok := as.(store.TransactionClock)
				if !ok {
					return ErrCredentialBindingUnavailable
				}
				now, e := clock.TransactionNow(ctx)
				if e != nil {
					return e
				}
				if _, e = bindings.Supersede(ctx, previous, bound.ID, now); e != nil {
					return e
				}
			}
		}
		for _, proof := range []struct {
			p      Principal
			action string
		}{{admin, "auth.os_account.admin_authorized"}, {subject, "auth.os_account.subject_control"}} {
			if e = appendOSAccountAudit(ctx, as.Audit(), model.AuditDraft{Actor: proof.p.Actor(), ActorKind: proof.p.ActorKind(), Action: proof.action, TargetKind: "core.user", TargetID: c.subject.User, Meta: map[string]any{"tenant": c.subject.Tenant.String(), "binding_digest": credentialBindingDigest(bound.ID)}}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return OSAccountMapping{}, credentialBindingError("write OS account binding", err)
	}
	controlKnown = false // Both proof outcomes committed with the binding.
	return osMapping(bound), nil
}

// Resolve selects the sole permanent kernel-account reservation and the current
// sealed binding, then reconstructs its exact credential's current lifecycle.
// The engine still must AuthorizeEvidence for the exact requested act.
func (b *OSAccountBindings) Resolve(ctx context.Context, uid uint32, name string) (PrincipalRef, model.TenantID, error) {
	if !b.ready() || ctx == nil || uid == 0 {
		return PrincipalRef{}, model.TenantID(""), ErrCredentialBindingInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return PrincipalRef{}, model.TenantID(""), ErrCredentialBindingUnavailable
	}
	measured, err := b.account(ctx, name)
	if err != nil || measured != uid {
		return PrincipalRef{}, model.TenantID(""), ErrCredentialBindingInvalid
	}
	var row model.CredentialBinding
	var s CredentialBindingSubject
	err = b.a.st.AuthView(ctx, func(as store.AuthScope) error {
		bindings, e := osAccountStore(as)
		if e != nil {
			return e
		}
		owners, e := bindings.OSAccountOwner(ctx, uid)
		if e != nil {
			return e
		}
		if len(owners) != 1 {
			return ErrCredentialBindingInvalid
		}
		first := owners[0]
		s = CredentialBindingSubject{Tenant: first.TargetTenantID, Kind: CredentialBindingOSAccount, Ref: first.SubjectRef, User: first.SubjectUserID, OSUID: uid, OSAccount: name}
		if !s.valid() || !credentialBindingNames(first, s) {
			return ErrCredentialBindingInvalid
		}
		current, e := bindings.Current(ctx, s.Tenant, s.Kind, s.Ref)
		if e != nil {
			return e
		}
		if len(current) != 1 || !credentialBindingNames(current[0], s) {
			return ErrCredentialBindingInvalid
		}
		row = current[0]
		return nil
	})
	if err != nil {
		return PrincipalRef{}, model.TenantID(""), err
	}
	ref, err := b.a.ResolveCredentialBinding(ctx, CredentialBinding{id: row.ID}, s)
	if err != nil {
		return PrincipalRef{}, model.TenantID(""), err
	}
	_, err = b.principal(ctx, ref, s.Tenant)
	if err != nil {
		return PrincipalRef{}, model.TenantID(""), err
	}
	return ref, s.Tenant, nil
}

// ResolveInstallation is called only by the engine's held-peer identity
// adapter. UID/name are native observations, never identity fields from a body.
// The private marker is revalidated against the current sealed OS binding,
// original installation ceiling and native user/credential/directory proofs.
func (b *OSAccountBindings) ResolveInstallation(ctx context.Context, uid uint32, name string) (PrincipalRef, model.TenantID, error) {
	ref, tenant, err := b.Resolve(ctx, uid, name)
	if err != nil {
		return PrincipalRef{}, model.TenantID(""), err
	}
	ref.installation = true
	if _, err = b.a.ResolvePrincipalScope(ctx, ref, tenant); err != nil {
		return PrincipalRef{}, model.TenantID(""), err
	}
	return ref, tenant, nil
}

func (b *OSAccountBindings) Read(ctx context.Context, actor Principal, tenant model.TenantID, target model.ID) (OSAccountMapping, error) {
	if !b.ready() || ctx == nil {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	ctx, cancel := credentialBindingContext(ctx)
	defer cancel()
	ref, ok := actor.Ref()
	if !ok {
		return OSAccountMapping{}, ErrCredentialBindingInvalid
	}
	if _, _, err := b.administrator(ctx, ref, tenant, target); err != nil {
		return OSAccountMapping{}, err
	}
	var row model.CredentialBinding
	err := b.a.st.AuthView(ctx, func(as store.AuthScope) error {
		bindings, e := osAccountStore(as)
		if e != nil {
			return e
		}
		rows, e := bindings.Current(ctx, tenant, CredentialBindingOSAccount, target)
		if e != nil {
			return e
		}
		if len(rows) != 1 || rows[0].OSUID == nil {
			return store.ErrNotFound
		}
		row = rows[0]
		subject := CredentialBindingSubject{Tenant: tenant, Kind: CredentialBindingOSAccount, Ref: target, User: target, OSUID: *row.OSUID, OSAccount: row.OSAccount}
		if !subject.valid() || !credentialBindingNames(row, subject) {
			return ErrCredentialBindingInvalid
		}
		return nil
	})
	if err != nil {
		return OSAccountMapping{}, err
	}
	return osMapping(row), nil
}
func (b *OSAccountBindings) Revoke(ctx context.Context, actor Principal, tenant model.TenantID, target model.ID) error {
	if !b.ready() || ctx == nil {
		return ErrCredentialBindingInvalid
	}
	ctx, cancel := credentialBindingContext(ctx)
	defer cancel()
	ref, ok := actor.Ref()
	if !ok {
		return ErrCredentialBindingInvalid
	}
	admin, bundle, err := b.administrator(ctx, ref, tenant, target)
	if err != nil {
		return err
	}
	return b.a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if e := pinOSProof(ctx, as, tenant, bundle); e != nil {
			return e
		}
		if e := verifyCredentialPin(ctx, as, osPin(admin, ref, tenant)); e != nil {
			return e
		}
		bindings, e := osAccountStore(as)
		if e != nil {
			return e
		}
		rows, e := bindings.Current(ctx, tenant, CredentialBindingOSAccount, target)
		if e != nil {
			return e
		}
		if len(rows) != 1 {
			return ErrCredentialBindingInvalid
		}
		row := rows[0]
		if row.OSUID == nil || !credentialBindingNames(row, CredentialBindingSubject{Tenant: tenant, Kind: CredentialBindingOSAccount, Ref: target, User: target, OSUID: *row.OSUID, OSAccount: row.OSAccount}) {
			return ErrCredentialBindingInvalid
		}
		clock, ok := as.(store.TransactionClock)
		if !ok {
			return ErrCredentialBindingUnavailable
		}
		now, e := clock.TransactionNow(ctx)
		if e != nil {
			return e
		}
		if e = bindings.RevokeOSAccount(ctx, row, now); e != nil {
			return e
		}
		e = appendOSAccountAudit(ctx, as.Audit(), model.AuditDraft{Actor: admin.Actor(), ActorKind: admin.ActorKind(), Action: "auth.os_account.revoke", TargetKind: "core.user", TargetID: target, Meta: map[string]any{"tenant": tenant.String(), "binding_digest": credentialBindingDigest(row.ID)}})
		return e
	})
}
