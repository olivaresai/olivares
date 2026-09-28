// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A credential binding lets a long-running subject, such as a workflow run,
// keep acting under the exact credential that started it, and nothing else.
// core/auth is its only producer: it keeps the row, checks the subject, pins
// the credential revision and records every succession. The subject owner
// stores an opaque CredentialBinding and resolves it at each effect; the
// resolved PrincipalRef then goes through the ordinary exact pair
// (ResolvePrincipalScope and AuthorizeEvidence), so no authority is decided
// here.
//
// A binding pins one credential revision. Anything that updates the credential
// row — a session refresh rotating the selector and secret, a step-up changing
// the assurance of the same bearer (a conservative revision invalidation, not a
// rotation), revocation, deletion or expiry — makes it stop resolving, and only
// a succession through the subject's owning operation binds a new revision.
// Nothing here ever looks for the newest credential of the same account.

// ErrCredentialBindingInvalid is the single refusal for a binding that is
// absent, malformed, sealed inconsistently, bound to another subject, tenant or
// account, superseded, or whose credential cannot be bound. It deliberately
// says nothing more.
var ErrCredentialBindingInvalid = errors.New("auth: credential binding is invalid")

// ErrCredentialBindingUnavailable means the binding's evidence could not be
// read or written: the store failed, the context was canceled or ran out of
// time, or the credential's evidence was unavailable. It is operational, never
// a statement about the credential or the binding, so callers must not treat
// it as invalid provenance or as a reason to reauthenticate. It wraps its
// cause for errors.Is; callers expose only their own diagnostic.
var ErrCredentialBindingUnavailable = errors.New("auth: credential binding evidence is unavailable")

// ErrCredentialBindingCeiling means a succession's credential exceeds the
// subject's recorded authority ceiling: a session continuing a token-started
// subject, a higher tenant role, another or no workspace confinement where the
// subject was confined, or another agent identity. It is a domain refusal of
// that credential, not of the binding.
var ErrCredentialBindingCeiling = errors.New("auth: credential exceeds the binding's authority ceiling")

// ErrCredentialBindingUnproven means the subject has no binding: nothing
// authoritative records the credential it was started with, so no ceiling or
// confinement a continuation must keep can be proved. A subject from before
// credential bindings, or one whose initial bind was refused, is never
// continued; its owner starts a new one.
var ErrCredentialBindingUnproven = errors.New("auth: the subject's original credential ceiling cannot be proved")

// ErrCredentialBindingConflict means a succession lost to another one: the
// subject already has a successor for this generation, or its current binding
// is newer than the caller's generation.
var ErrCredentialBindingConflict = errors.New("auth: credential binding succession conflicted")

// CredentialBindingWorkflowRun is the only subject kind of this version: a
// workflow run, referenced by its pre-assigned run id.
const CredentialBindingWorkflowRun = "orchestration.workflow_run"

const (
	credentialBindingSealDomain = "olivares.core.credential_binding.v1"
	// credentialBindingTimeout bounds a bind or succession whose caller set no
	// deadline, because ResolvePrincipalScope requires a finite one.
	credentialBindingTimeout = 15 * time.Second
)

// CredentialBindingSubject names what a binding is for. Every field is
// selected by server code from durable state — the tenant from the request's
// resolved tenant, the run id and its initiating account from the run row —
// never from request input.
type CredentialBindingSubject struct {
	Tenant model.TenantID
	Kind   string
	Ref    model.ID
	User   model.ID
}

func (s CredentialBindingSubject) valid() bool {
	return validPrincipalEvidenceTenant(s.Tenant) && s.Kind == CredentialBindingWorkflowRun &&
		validPrincipalEvidenceID(s.Ref) && validPrincipalEvidenceID(s.User)
}

// CredentialBinding is the opaque handle of one binding row. It confers
// nothing by itself: resolving it needs the server-side row, the exact subject
// and, for a workflow run, the run's open gate. It prints and serializes as
// nothing, so it cannot leak through a log line, an event or a DTO.
type CredentialBinding struct {
	id model.ID
}

// IsZero reports whether b names no binding.
func (b CredentialBinding) IsZero() bool { return b.id.IsZero() }

// StorageValue is the value the subject owner stores in its own row. It is the
// only way to read the handle and must not be written anywhere else.
func (b CredentialBinding) StorageValue() string { return b.id.String() }

// ParseCredentialBindingStorage reads a stored handle. An empty value is the
// zero handle; a malformed one is ErrCredentialBindingInvalid.
func ParseCredentialBindingStorage(raw string) (CredentialBinding, error) {
	if raw == "" {
		return CredentialBinding{}, nil
	}
	id, err := model.ParseID(raw)
	if err != nil || !validPrincipalEvidenceID(id) {
		return CredentialBinding{}, ErrCredentialBindingInvalid
	}
	return CredentialBinding{id: id}, nil
}

// String redacts the handle.
func (CredentialBinding) String() string { return "[credential-binding]" }

// GoString redacts the handle.
func (CredentialBinding) GoString() string { return "auth.CredentialBinding{[redacted]}" }

// MarshalJSON refuses: a handle is never part of a serialized document.
func (CredentialBinding) MarshalJSON() ([]byte, error) {
	return nil, errors.New("auth: a credential binding is not serializable")
}

// MarshalText refuses for the same reason.
func (CredentialBinding) MarshalText() ([]byte, error) {
	return nil, errors.New("auth: a credential binding is not serializable")
}

// credentialBindingProof names, on a PrincipalRef, the binding the reference
// was read from. The zero value is an ordinary reference.
type credentialBindingProof struct {
	id      model.ID
	subject CredentialBindingSubject
}

func (p credentialBindingProof) bound() bool { return !p.id.IsZero() }

// verifyCredentialBindingProof proves, inside ResolvePrincipalScope's AuthView
// and after its first directory-epoch read, that the binding ref was read from
// is still the current binding of its subject for exactly ref's credential
// revision. A supersession moves that same epoch in its own transaction, so
// one committed before this read is seen here, and one committed after it
// moves the epoch that the view's second read or the effect's commit checks:
// the binding's liveness is proved at the epoch the resolved authority carries
// and the effect transaction locks. A binding that no longer holds is
// ErrUnauthenticated, as a credential that is no longer current is.
func verifyCredentialBindingProof(
	ctx context.Context,
	as store.AuthScope,
	ref PrincipalRef,
	tenant model.TenantID,
) error {
	proof := ref.binding
	if proof.subject.Tenant != tenant || !proof.subject.valid() {
		return ErrUnauthenticated
	}
	bindings, err := credentialBindingStore(as)
	if err != nil {
		return principalEvidenceUnavailable("credential binding custody", err)
	}
	row, err := bindings.Get(ctx, proof.id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrUnauthenticated
	}
	if err != nil {
		return principalEvidenceUnavailable("read the credential binding", err)
	}
	if !credentialBindingNames(row, proof.subject) || !row.SupersededBy.IsZero() || row.SupersededAt != nil ||
		PrincipalKind(row.CredentialKind) != ref.kind || row.CredentialID != ref.credentialID ||
		row.CredentialVersion != ref.version {
		return ErrUnauthenticated
	}
	return nil
}

// credentialPin is the exact credential revision a binding row records,
// together with the account that credential's own rows name and the authority
// ceiling the credential holds in the subject's tenant.
type credentialPin struct {
	kind    PrincipalKind
	id      model.ID
	version int64
	user    model.ID
	ceiling credentialCeiling
}

// credentialCeiling is the authority a credential holds in one tenant, as the
// existing providers report it: its kind, its role (Principal.RoleIn), its
// workspace confinement (Principal.ConfinedWorkspaceIn) and its agent identity.
type credentialCeiling struct {
	kind      PrincipalKind
	role      string
	workspace model.ID
	agent     string
}

func recordedCeiling(row model.CredentialBinding) credentialCeiling {
	return credentialCeiling{
		kind: PrincipalKind(row.CeilingKind), role: row.CeilingRole,
		workspace: row.CeilingWorkspaceID, agent: row.CeilingAgent,
	}
}

// within reports whether a credential holding c may continue a subject whose
// recorded ceiling is recorded. A token-started subject is never continued by
// a session, which also carries the account's group grants; the role ranks no
// higher; a confined subject stays confined to exactly its workspace; and the
// agent identity is the same, including none. A narrower credential is within.
func (c credentialCeiling) within(recorded credentialCeiling) bool {
	switch {
	case c.kind != KindUser && c.kind != KindToken:
		return false
	case recorded.kind == KindToken && c.kind != KindToken:
		return false
	case recorded.kind != KindUser && recorded.kind != KindToken:
		return false
	case RoleRank(c.role) > RoleRank(recorded.role):
		return false
	case !recorded.workspace.IsZero() && c.workspace != recorded.workspace:
		return false
	}
	return c.agent == recorded.agent
}

// BindCredential records that subject s continues under p's exact credential
// revision. It reads p's private credential reference only — never the
// principal's exported fields — and reconstructs that credential in s.Tenant
// through ResolvePrincipalScope, so a synthetic, copied or edited principal,
// a credential not admitted to the tenant and a credential of another account
// than s.User are all refused. A subject is bound once; later bindings are
// successions (RebindCredential).
func (a *Authenticator) BindCredential(
	ctx context.Context,
	p Principal,
	s CredentialBindingSubject,
) (CredentialBinding, error) {
	ctx, cancel := credentialBindingContext(ctx)
	defer cancel()
	pin, err := a.credentialBindingPin(ctx, p, s)
	if err != nil {
		return CredentialBinding{}, err
	}
	var bound model.CredentialBinding
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		bindings, err := credentialBindingStore(as)
		if err != nil {
			return err
		}
		if _, err := bindings.AtGeneration(ctx, s.Tenant, s.Kind, s.Ref, 0); err == nil {
			return ErrCredentialBindingConflict
		} else if !errors.Is(err, store.ErrNotFound) {
			return credentialBindingUnavailable("read the subject's first binding", err)
		}
		if err := verifyCredentialPin(ctx, as, pin); err != nil {
			return err
		}
		bound, err = bindings.Create(ctx, sealedCredentialBinding(s, 0, pin, pin.ceiling, ""))
		return err
	})
	if err != nil {
		return CredentialBinding{}, credentialBindingError("write the binding", err)
	}
	return CredentialBinding{id: bound.ID}, nil
}

// ResolveCredentialBinding returns the exact credential reference b pins after
// checking, in one AuthView, that the row exists, its seal holds, it names
// exactly s and no successor superseded it. The caller must pass the result to
// the exact pair; whether that credential is still current, admitted and
// authorized is decided there. This read alone proves nothing for an effect: a
// supersession may land right after it. The returned reference carries the
// binding, and ResolvePrincipalScope proves it current again inside its own
// view, at the directory epoch the effect's commit consumes. ctx must carry a
// finite deadline.
func (a *Authenticator) ResolveCredentialBinding(
	ctx context.Context,
	b CredentialBinding,
	s CredentialBindingSubject,
) (PrincipalRef, error) {
	if ctx == nil || a == nil || a.st == nil {
		return PrincipalRef{}, credentialBindingUnavailable("resolver input is incomplete", nil)
	}
	// An empty handle or a malformed subject is invalid provenance whatever the
	// context: it is decided without reading anything, so it never waits on the
	// deadline the store read below requires.
	if b.IsZero() || !s.valid() {
		return PrincipalRef{}, ErrCredentialBindingInvalid
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
		return PrincipalRef{}, credentialBindingUnavailable("finite context deadline is required", nil)
	}
	var ref PrincipalRef
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		bindings, err := credentialBindingStore(as)
		if err != nil {
			return err
		}
		row, err := bindings.Get(ctx, b.id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrCredentialBindingInvalid
		}
		if err != nil {
			return credentialBindingUnavailable("read the binding", err)
		}
		if !credentialBindingNames(row, s) || !row.SupersededBy.IsZero() || row.SupersededAt != nil {
			return ErrCredentialBindingInvalid
		}
		ref = PrincipalRef{
			kind: PrincipalKind(row.CredentialKind), credentialID: row.CredentialID,
			version: row.CredentialVersion, binding: credentialBindingProof{id: row.ID, subject: s},
		}
		return nil
	})
	if err != nil {
		return PrincipalRef{}, credentialBindingError("resolve the binding", err)
	}
	if !validPrincipalRef(ref) {
		return PrincipalRef{}, ErrCredentialBindingInvalid
	}
	return ref, nil
}

// RebindCredential writes the successor of subject s for generation, pinning
// p's exact credential revision, and supersedes the subject's current binding
// in the same system transaction. old is the handle the subject owner holds; it
// is recorded, not trusted.
//
// The subject owner reserves generation in its own state first and publishes
// the returned handle only by a compare-and-set on that generation, so the
// succession is conditional end to end:
//   - p must reconstruct, through its private reference, to exactly s.User,
//     the account of every binding of the subject: another account's credential
//     is refused even when it is an administrator's;
//   - p may not exceed the subject's recorded ceiling (credentialCeiling.within):
//     the successor copies the ceiling of the subject's first binding, so a
//     reauthorization never widens what the run may do; a wider credential is
//     ErrCredentialBindingCeiling, and a subject never bound, whose ceiling
//     nothing records, is ErrCredentialBindingUnproven;
//   - authorizedBy is reconstructed the same way and recorded as the
//     authorizing actor; it never becomes the credential the subject acts under;
//   - a subject has at most one row per generation. The same credential at the
//     same generation returns the same successor (a retried call), anything else
//     is ErrCredentialBindingConflict, and so is a current binding newer than
//     generation (a stale caller). A current binding older than generation — the
//     live original or a successor whose owner never published it — is
//     superseded.
func (a *Authenticator) RebindCredential(
	ctx context.Context,
	old CredentialBinding,
	generation int64,
	p Principal,
	s CredentialBindingSubject,
	authorizedBy Principal,
) (CredentialBinding, error) {
	ctx, cancel := credentialBindingContext(ctx)
	defer cancel()
	if generation < 1 {
		return CredentialBinding{}, ErrCredentialBindingInvalid
	}
	pin, err := a.credentialBindingPin(ctx, p, s)
	if err != nil {
		return CredentialBinding{}, err
	}
	authorizer, err := a.credentialBindingAuthorizer(ctx, authorizedBy, s.Tenant)
	if err != nil {
		return CredentialBinding{}, err
	}
	var successor model.CredentialBinding
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		bindings, err := credentialBindingStore(as)
		if err != nil {
			return err
		}
		// Only the requested generation and the current row are read, never the
		// subject's history, so a long history cannot look like a conflict.
		existing, err := bindings.AtGeneration(ctx, s.Tenant, s.Kind, s.Ref, generation)
		switch {
		case err == nil:
			if !credentialBindingNames(existing, s) {
				return ErrCredentialBindingInvalid
			}
			if existing.CredentialKind == string(pin.kind) && existing.CredentialID == pin.id &&
				existing.CredentialVersion == pin.version && existing.AuthorizedByActor == authorizer {
				successor = existing
				return nil
			}
			return ErrCredentialBindingConflict
		case !errors.Is(err, store.ErrNotFound):
			return credentialBindingUnavailable("read the subject's generation", err)
		}
		live, err := currentCredentialBinding(ctx, bindings, s)
		if err != nil {
			return err
		}
		if len(live) == 1 && live[0].SubjectGeneration > generation {
			return ErrCredentialBindingConflict
		}
		ceiling, err := continuationCeiling(live, pin)
		if err != nil {
			return err
		}
		var recorded model.ID
		if !old.IsZero() {
			previous, err := bindings.Get(ctx, old.id)
			switch {
			case err == nil && credentialBindingNames(previous, s):
				recorded = previous.ID
			case err != nil && !errors.Is(err, store.ErrNotFound):
				return credentialBindingUnavailable("read the recorded binding", err)
			}
		}
		if err := verifyCredentialPin(ctx, as, pin); err != nil {
			return err
		}
		successor, err = bindings.Create(ctx, sealedCredentialBinding(s, generation, pin, ceiling, authorizer))
		if err != nil {
			return err
		}
		if len(live) == 1 {
			clock, ok := as.(store.TransactionClock)
			if !ok {
				return credentialBindingUnavailable("auth scope has no transaction clock", nil)
			}
			now, err := clock.TransactionNow(ctx)
			if err != nil {
				return credentialBindingUnavailable("read database time", err)
			}
			if _, err := bindings.Supersede(ctx, live[0], successor.ID, now); err != nil {
				return err
			}
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: authorizer, ActorKind: authorizedByKind(authorizer),
			Action:     "auth.credential_binding.rebind",
			TargetKind: model.Kind(s.Kind), TargetID: s.Ref,
			Meta: map[string]any{
				"generation":         generation,
				"credential_kind":    string(pin.kind),
				"old_binding_sha256": credentialBindingDigest(recorded),
				"new_binding_sha256": credentialBindingDigest(successor.ID),
			},
		})
		return err
	})
	if err != nil {
		return CredentialBinding{}, credentialBindingError("write the successor", err)
	}
	return CredentialBinding{id: successor.ID}, nil
}

// VerifyCredentialContinuation reports, reading only, whether p may continue
// subject s: p reconstructs through its private reference to exactly s.User in
// s.Tenant, the subject has a current binding, and p does not exceed the
// ceiling that binding records. The subject owner calls it before reserving a succession, so an
// incompatible continuation writes nothing; RebindCredential repeats every
// check inside its own transaction and stays the authority.
func (a *Authenticator) VerifyCredentialContinuation(
	ctx context.Context,
	p Principal,
	s CredentialBindingSubject,
) error {
	ctx, cancel := credentialBindingContext(ctx)
	defer cancel()
	pin, err := a.credentialBindingPin(ctx, p, s)
	if err != nil {
		return err
	}
	err = a.st.AuthView(ctx, func(as store.AuthScope) error {
		bindings, err := credentialBindingStore(as)
		if err != nil {
			return err
		}
		live, err := currentCredentialBinding(ctx, bindings, s)
		if err != nil {
			return err
		}
		_, err = continuationCeiling(live, pin)
		return err
	})
	return credentialBindingError("verify the continuation", err)
}

// currentCredentialBinding reads subject s's unsuperseded rows: none for a
// subject never bound, one otherwise. More than one is a broken custody
// invariant and is reported as unavailable evidence, never resolved.
func currentCredentialBinding(
	ctx context.Context,
	bindings store.CredentialBindingStore,
	s CredentialBindingSubject,
) ([]model.CredentialBinding, error) {
	live, err := bindings.Current(ctx, s.Tenant, s.Kind, s.Ref)
	if err != nil {
		return nil, credentialBindingUnavailable("read the subject's current binding", err)
	}
	if len(live) > 1 {
		return nil, credentialBindingUnavailable("the subject holds more than one current binding", nil)
	}
	for _, row := range live {
		if !credentialBindingNames(row, s) {
			return nil, ErrCredentialBindingInvalid
		}
	}
	return live, nil
}

// continuationCeiling returns the ceiling a successor records. The subject
// keeps the ceiling its first binding recorded, and pin may not exceed it. A
// subject that was never bound has no authoritative record of the credential
// it started with, so no ceiling or confinement can be proved for it: it is
// ErrCredentialBindingUnproven, never the successor's own ceiling.
func continuationCeiling(live []model.CredentialBinding, pin credentialPin) (credentialCeiling, error) {
	if len(live) == 0 {
		return credentialCeiling{}, ErrCredentialBindingUnproven
	}
	ceiling := recordedCeiling(live[0])
	if !pin.ceiling.within(ceiling) {
		return credentialCeiling{}, ErrCredentialBindingCeiling
	}
	return ceiling, nil
}

// credentialBindingPin reconstructs p's exact credential in s.Tenant and
// returns the revision to pin. Nothing is read from p except its private
// reference.
func (a *Authenticator) credentialBindingPin(
	ctx context.Context,
	p Principal,
	s CredentialBindingSubject,
) (credentialPin, error) {
	if a == nil || a.st == nil {
		return credentialPin{}, credentialBindingUnavailable("authenticator has no store", nil)
	}
	if !s.valid() {
		return credentialPin{}, ErrCredentialBindingInvalid
	}
	ref, ok := p.Ref()
	if !ok {
		return credentialPin{}, ErrCredentialBindingInvalid
	}
	resolved, err := a.admittedCredential(ctx, ref, s.Tenant)
	if err != nil {
		return credentialPin{}, err
	}
	if resolvedRef, ok := resolved.Ref(); !ok || resolvedRef != ref ||
		resolved.UserID != s.User || !validPrincipalEvidenceID(resolved.UserID) {
		return credentialPin{}, ErrCredentialBindingInvalid
	}
	role, _ := resolved.RoleIn(s.Tenant)
	workspace, _ := resolved.ConfinedWorkspaceIn(s.Tenant)
	return credentialPin{
		kind: ref.kind, id: ref.credentialID, version: ref.version, user: resolved.UserID,
		ceiling: credentialCeiling{kind: ref.kind, role: role, workspace: workspace, agent: resolved.AgentIdentity},
	}, nil
}

// credentialBindingAuthorizer reconstructs the authorizing administrator from
// its own private reference and returns its audit actor.
func (a *Authenticator) credentialBindingAuthorizer(
	ctx context.Context,
	authorizedBy Principal,
	tenant model.TenantID,
) (string, error) {
	ref, ok := authorizedBy.Ref()
	if !ok {
		return "", ErrCredentialBindingInvalid
	}
	resolved, err := a.admittedCredential(ctx, ref, tenant)
	if err != nil {
		return "", err
	}
	if resolvedRef, ok := resolved.Ref(); !ok || resolvedRef != ref {
		return "", ErrCredentialBindingInvalid
	}
	return resolved.Actor(), nil
}

// admittedCredential reconstructs ref in tenant through ResolvePrincipalScope
// and classifies a refusal. The resolver folds domain refusals (a credential
// not admitted to the tenant) and unavailable evidence into one sentinel, so
// the domain conditions are established first from the rows themselves:
// whatever the resolver still refuses after they hold is operational.
func (a *Authenticator) admittedCredential(ctx context.Context, ref PrincipalRef, tenant model.TenantID) (Principal, error) {
	if err := a.credentialBindingAdmission(ctx, ref, tenant); err != nil {
		return Principal{}, err
	}
	resolved, err := a.ResolvePrincipalScope(ctx, ref, tenant)
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return Principal{}, ErrCredentialBindingInvalid
	case err != nil:
		return Principal{}, credentialBindingUnavailable("reconstruct the credential", err)
	}
	return resolved, nil
}

// credentialBindingAdmission establishes, in one AuthView, the domain facts
// that make a credential unbindable in tenant: its exact revision is gone,
// changed, revoked or expired; its account is inactive, deleted or the global
// superadmin; a session lacks a direct membership in the tenant or is scoped
// to another one; a token is bound elsewhere, purpose-restricted or ownerless;
// or the account is excluded from the tenant. Each is ErrCredentialBindingInvalid.
// A failure to read them is ErrCredentialBindingUnavailable.
func (a *Authenticator) credentialBindingAdmission(ctx context.Context, ref PrincipalRef, tenant model.TenantID) error {
	if !validPrincipalRef(ref) || !validPrincipalEvidenceTenant(tenant) {
		return ErrCredentialBindingInvalid
	}
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		clock, ok := as.(store.TransactionClock)
		if !ok {
			return credentialBindingUnavailable("auth scope has no transaction clock", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return credentialBindingUnavailable("read database time", err)
		}
		var user, session model.ID
		switch ref.kind {
		case KindUser:
			row, err := as.Sessions().Get(ctx, ref.credentialID)
			if errors.Is(err, store.ErrNotFound) {
				return ErrCredentialBindingInvalid
			}
			if err != nil {
				return credentialBindingUnavailable("read the session", err)
			}
			if row.Version != ref.version || row.Revoked || row.DeletedAt != nil ||
				!now.Time().Before(row.ExpiresAt.Time()) ||
				(!row.TenantScope.IsZero() && row.TenantScope != tenant) {
				return ErrCredentialBindingInvalid
			}
			user, session = row.UserID, row.ID
		case KindToken:
			row, err := as.Tokens().Get(ctx, ref.credentialID)
			if errors.Is(err, store.ErrNotFound) {
				return ErrCredentialBindingInvalid
			}
			if err != nil {
				return credentialBindingUnavailable("read the API token", err)
			}
			if row.Version != ref.version || row.Revoked || row.DeletedAt != nil || row.IsSuperadmin ||
				row.Purpose != "" || row.BoundTenantID != tenant || row.UserID.IsZero() ||
				(row.ExpiresAt != nil && !now.Time().Before(row.ExpiresAt.Time())) {
				return ErrCredentialBindingInvalid
			}
			user = row.UserID
		default:
			return ErrCredentialBindingInvalid
		}
		account, err := as.Users().Get(ctx, user)
		if errors.Is(err, store.ErrNotFound) {
			return ErrCredentialBindingInvalid
		}
		if err != nil {
			return credentialBindingUnavailable("read the account", err)
		}
		if account.DeletedAt != nil || account.Status != model.StatusActive || account.IsSuperadmin {
			return ErrCredentialBindingInvalid
		}
		if ref.kind == KindUser {
			members, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
				{Column: "user_id", Op: model.OpEq, Value: user.String()},
				{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
			}, Limit: 2})
			if err != nil {
				return credentialBindingUnavailable("read the tenant membership", err)
			}
			admitted := false
			for _, member := range members {
				admitted = admitted || member.DeletedAt == nil
			}
			if !admitted {
				return ErrCredentialBindingInvalid
			}
		}
		standing, err := loadStanding(ctx, as, user, session)
		if err != nil {
			return credentialBindingUnavailable("read the tenant exclusions", err)
		}
		if _, excluded := standing.excluded[tenant]; excluded {
			return ErrCredentialBindingInvalid
		}
		return nil
	})
	return credentialBindingError("read the credential's admission", err)
}

// verifyCredentialPin re-reads the pinned credential inside the writing
// transaction, so a row is written only for a revision that still exists.
func verifyCredentialPin(ctx context.Context, as store.AuthScope, pin credentialPin) error {
	switch pin.kind {
	case KindUser:
		session, err := as.Sessions().Get(ctx, pin.id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrCredentialBindingInvalid
		}
		if err != nil {
			return credentialBindingUnavailable("re-read the session", err)
		}
		if session.ID != pin.id || session.Version != pin.version || session.Revoked ||
			session.DeletedAt != nil || session.UserID != pin.user {
			return ErrCredentialBindingInvalid
		}
	case KindToken:
		token, err := as.Tokens().Get(ctx, pin.id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrCredentialBindingInvalid
		}
		if err != nil {
			return credentialBindingUnavailable("re-read the API token", err)
		}
		if token.ID != pin.id || token.Version != pin.version || token.Revoked ||
			token.DeletedAt != nil || token.UserID != pin.user {
			return ErrCredentialBindingInvalid
		}
	default:
		return ErrCredentialBindingInvalid
	}
	return nil
}

func credentialBindingStore(as store.AuthScope) (store.CredentialBindingStore, error) {
	scope, ok := as.(store.AuthCredentialBindingScope)
	if !ok || scope == nil {
		return nil, credentialBindingUnavailable("auth scope has no credential-binding custody", nil)
	}
	bindings := scope.CredentialBindings()
	if bindings == nil {
		return nil, credentialBindingUnavailable("auth scope has no credential-binding custody", nil)
	}
	return bindings, nil
}

// credentialBindingNames reports whether row is a well-formed, consistently
// sealed system row that names exactly subject s.
func credentialBindingNames(row model.CredentialBinding, s CredentialBindingSubject) bool {
	if !validPrincipalEvidenceAuthBase(row.BaseFields) || row.DeletedAt != nil ||
		row.TargetTenantID != s.Tenant || row.SubjectKind != s.Kind || row.SubjectRef != s.Ref ||
		row.SubjectUserID != s.User || row.SubjectGeneration < 0 {
		return false
	}
	seal := credentialBindingSeal(row)
	return bytes.Equal(row.Seal, seal[:])
}

// sealedCredentialBinding builds a new row under a pre-assigned id so the seal
// covers the id as well.
func sealedCredentialBinding(
	s CredentialBindingSubject,
	generation int64,
	pin credentialPin,
	ceiling credentialCeiling,
	authorizedBy string,
) model.CredentialBinding {
	row := model.CredentialBinding{
		BaseFields:     model.BaseFields{ID: model.NewID(), TenantID: model.SystemTenantID},
		TargetTenantID: s.Tenant, SubjectKind: s.Kind, SubjectRef: s.Ref,
		SubjectGeneration: generation, SubjectUserID: s.User,
		CredentialKind: string(pin.kind), CredentialID: pin.id, CredentialVersion: pin.version,
		CeilingKind: string(ceiling.kind), CeilingRole: ceiling.role,
		CeilingWorkspaceID: ceiling.workspace, CeilingAgent: ceiling.agent,
		AuthorizedByActor: authorizedBy,
	}
	seal := credentialBindingSeal(row)
	row.Seal = seal[:]
	return row
}

// credentialBindingSeal hashes the immutable fields, each length-prefixed,
// under the domain string. The succession columns are excluded because they
// are written later. The seal detects an inconsistent row; the binding's
// unforgeability comes from server custody and the exact subject match.
func credentialBindingSeal(row model.CredentialBinding) [sha256.Size]byte {
	var buf bytes.Buffer
	write := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		buf.Write(size[:])
		buf.WriteString(value)
	}
	number := func(value int64) {
		var raw [8]byte
		binary.BigEndian.PutUint64(raw[:], uint64(value))
		buf.Write(raw[:])
	}
	write(credentialBindingSealDomain)
	write(row.ID.String())
	write(row.TargetTenantID.String())
	write(row.SubjectKind)
	write(row.SubjectRef.String())
	number(row.SubjectGeneration)
	write(row.SubjectUserID.String())
	write(row.CredentialKind)
	write(row.CredentialID.String())
	number(row.CredentialVersion)
	write(row.CeilingKind)
	write(row.CeilingRole)
	write(row.CeilingWorkspaceID.String())
	write(row.CeilingAgent)
	write(row.AuthorizedByActor)
	return sha256.Sum256(buf.Bytes())
}

// credentialBindingDigest is the audit form of a binding id: evidence that can
// be matched against the row, not a handle.
func credentialBindingDigest(id model.ID) string {
	if id.IsZero() {
		return ""
	}
	digest := sha256.Sum256([]byte(credentialBindingSealDomain + "\x00audit\x00" + id.String()))
	return hex.EncodeToString(digest[:])
}

func authorizedByKind(actor string) string {
	if len(actor) > len("token:") && actor[:len("token:")] == "token:" {
		return "token"
	}
	return model.ActorUser
}

func credentialBindingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= credentialBindingTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, credentialBindingTimeout)
}

// credentialBindingError keeps a typed refusal and turns anything else into
// the operational failure, wrapping its cause. A store conflict inside a
// succession is the losing side of a concurrent one.
func credentialBindingError(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCredentialBindingInvalid), errors.Is(err, ErrCredentialBindingConflict),
		errors.Is(err, ErrCredentialBindingCeiling), errors.Is(err, ErrCredentialBindingUnproven),
		errors.Is(err, ErrCredentialBindingUnavailable):
		return err
	case errors.Is(err, store.ErrConflict):
		return ErrCredentialBindingConflict
	default:
		return credentialBindingUnavailable(what, err)
	}
}

func credentialBindingUnavailable(what string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrCredentialBindingUnavailable, what)
	}
	return fmt.Errorf("%w: %s: %w", ErrCredentialBindingUnavailable, what, cause)
}
