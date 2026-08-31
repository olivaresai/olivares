// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/sessions"
	sdkevent "github.com/olivaresai/olivares/sdk/event"
)

// workContentGuard keeps the modules independent: sessions owns the port and
// the composition root adapts the canonical security detector.
type workContentGuard struct{}

func (workContentGuard) Inspect(_ context.Context, _ model.TenantID, _ model.ID, _ string, content []byte) (sessions.ContentDecision, error) {
	for _, hit := range security.ClassifySensitivity(string(content)) {
		if hit.Class == security.SensSecretCredential {
			return sessions.ContentDecision{Allowed: false, Code: "secret_rejected"}, nil
		}
	}
	return sessions.ContentDecision{Allowed: true, Code: "clean"}, nil
}

type workEventSink struct{ eventing *eventing.Module }

func (s workEventSink) IngestDurable(ctx context.Context, e sessions.WorkEventEnvelope) error {
	if s.eventing == nil {
		return eventing.ErrDurableIntakeUnavailable
	}
	at, err := model.ParseTimestamp(e.OccurredAt)
	if err != nil {
		return err
	}
	return s.eventing.IngestDurable(ctx, sdkevent.Event{
		ID: e.EventID.String(), Type: sdkevent.Type(e.Type), Tenant: e.TenantID.String(),
		Source: "olivares.sessions", Time: at.Time(), Payload: e.Payload,
	})
}

// workIdentityResolver is intentionally held in cmd: user lookup needs the
// auth partition and sessions must never receive a raw Store.
//
// The SESSION case is different and is delegated, not implemented here. This
// resolver used to answer it against a core model.Session keyed by the bare uuid
// inside a canonical sid, which could never resolve: a sid is minted as "osn_" +
// a FRESH uuid and nothing creates a core Session with that primary key, so
// owner_kind="session" answered not-found before any authorization ran. The
// sessions module owns the identity plane, the alias that ties an operated
// session to its run, and now the session's workspace dimension, so it is the
// only place that can answer without borrowing somebody else's key.
type workIdentityResolver struct {
	st             store.Store
	sessions       *sessions.Module
	agentLifecycle workAgentLifecycle
}

// workAgentLifecycle is implemented by governance in the composition root.
// Keeping only scalar model vocabulary here preserves the module boundary:
// sessions never imports governance, and cmd never interprets lifecycle rows.
type workAgentLifecycle interface {
	AgentEligibleForWork(context.Context, model.TenantID, string) (bool, error)
}

type workAgentLifecycleInScope interface {
	AgentWorkAuthorityFactsInScope(
		context.Context, store.Scope, string,
	) (bool, []store.AuthorizationFactRef, error)
}

func (r workIdentityResolver) ResolveParticipant(ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string) (sessions.Participant, error) {
	switch kind {
	case "user":
		return r.resolveUser(ctx, tenant, workspace, ref)
	case "agent":
		return r.resolveAgent(ctx, tenant, workspace, ref)
	case "session":
		return r.resolveSession(ctx, tenant, workspace, ref)
	default:
		// ⛔ EL LLAMANTE SE EQUIVOCO, Y ESO ES UNA DECISION, NO UNA CEGUERA. Se envuelve
		// en store.ErrInvalidID para que checkParticipant pueda clasificarlo: sin el
		// centinela, el modulo solo ve "un error" y contesta la TERCERA respuesta, que
		// invita a reintentar algo que nunca va a funcionar.
		return sessions.Participant{}, fmt.Errorf("unknown work participant kind %q: %w", kind, store.ErrInvalidID)
	}
}

func (r workIdentityResolver) resolveUser(ctx context.Context, tenant model.TenantID, workspace model.ID, ref string) (sessions.Participant, error) {
	id, err := model.ParseID(ref)
	if err != nil {
		// Un owner_ref mal formado es dato del LLAMANTE, no un plano caido.
		return sessions.Participant{}, fmt.Errorf("owner_ref %q is not an id: %w", ref, store.ErrInvalidID)
	}
	out := sessions.Participant{Kind: "user", CanonicalRef: id.String()}
	err = r.st.AuthView(ctx, func(sc store.AuthScope) error {
		user, err := sc.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		out.Active = user.Status == model.StatusActive
		memberships, _, err := sc.Memberships().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: id.String()},
			{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
		}, Limit: 100})
		if err != nil {
			return err
		}
		for _, membership := range memberships {
			if membership.WorkspaceID.IsZero() || membership.WorkspaceID == workspace {
				out.WorkspaceEligible = true
				break
			}
		}
		return nil
	})
	return out, err
}

func (r workIdentityResolver) resolveAgent(ctx context.Context, tenant model.TenantID, workspace model.ID, ref string) (sessions.Participant, error) {
	if r.agentLifecycle == nil {
		return sessions.Participant{}, fmt.Errorf("agent lifecycle plane is not wired")
	}
	identityID, err := model.ParseID(ref)
	if err != nil {
		return sessions.Participant{}, fmt.Errorf("owner_ref %q is not an id: %w", ref, store.ErrInvalidID)
	}
	out := sessions.Participant{Kind: "agent", CanonicalRef: identityID.String()}
	var identityRef string
	err = r.st.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, identityID)
		if err != nil {
			return err
		}
		identityRef = identity.ExternalID
		if identityRef == "" {
			return fmt.Errorf("canonical agent identity has no external reference")
		}
		agents, _, err := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{{
			Column: "identity_id", Op: model.OpEq, Value: identityID.String(),
		}}, Limit: 100})
		if err != nil {
			return err
		}
		for _, agent := range agents {
			agentWorkspace := agent.WorkspaceID
			if agentWorkspace.IsZero() {
				defaultWorkspace, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				agentWorkspace = defaultWorkspace.ID
			}
			if agentWorkspace == workspace {
				out.WorkspaceEligible = true
				out.Active = out.Active || agent.Status == model.StatusActive
			}
		}
		if !out.WorkspaceEligible {
			return store.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	eligible, err := r.agentLifecycle.AgentEligibleForWork(ctx, tenant, identityRef)
	if err != nil {
		out.Active = false
		return out, err
	}
	out.Active = out.Active && eligible
	return out, nil
}

type workAgentAuthorityToken struct {
	facts []store.AuthorizationFactRef
}

// ObserveAgentWorkAuthority closes the tenant-wide half of the eligibility
// seam without passing a raw store handle into sessions. canonicalRef is always
// WorkItem.owner_ref (Identity.ID), never a body-selected ExternalID. The exact
// fact refs remain private to cmd; sessions sees only a digest and opaque token.
func (r workIdentityResolver) ObserveAgentWorkAuthority(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	canonicalRef string,
	authenticatedAgentRef string,
) (sessions.WorkAgentAuthoritySnapshot, error) {
	identityID, err := model.ParseID(canonicalRef)
	if err != nil {
		return sessions.WorkAgentAuthoritySnapshot{}, err
	}
	var facts []store.AuthorizationFactRef
	var identityRef string
	activeInWorkspace := false
	err = r.st.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, identityID)
		if err != nil {
			return err
		}
		if identity.ID != identityID || identity.ExternalID == "" {
			return nil
		}
		identityRef = identity.ExternalID
		matchingIdentities, page, err := sc.Identities().List(ctx, model.Query{
			Filters: []model.Filter{{
				Column: "external_id", Op: model.OpEq, Value: identityRef,
			}},
			Limit: 2,
		})
		if err != nil {
			return err
		}
		if page.HasMore || len(matchingIdentities) != 1 || matchingIdentities[0].ID != identity.ID {
			identityRef = ""
			return nil
		}
		facts = append(facts, store.AuthorizationFactRef{
			Kind: "core.identity", ID: identity.ID, Version: identity.Version,
		})
		agents, page, err := sc.Agents().List(ctx, model.Query{
			Filters: []model.Filter{{
				Column: "identity_id", Op: model.OpEq, Value: identityID.String(),
			}},
			Limit: 100,
		})
		if err != nil {
			return err
		}
		if page.HasMore {
			return fmt.Errorf("agent identity binding enumeration is truncated")
		}
		for _, agent := range agents {
			agentWorkspace := agent.WorkspaceID
			if agentWorkspace.IsZero() {
				defaultWorkspace, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				agentWorkspace = defaultWorkspace.ID
			}
			if agent.IdentityID == identityID && agent.Status == model.StatusActive &&
				agentWorkspace == workspace {
				activeInWorkspace = true
				facts = append(facts, store.AuthorizationFactRef{
					Kind: "core.agent", ID: agent.ID, Version: agent.Version,
				})
			}
		}
		if !activeInWorkspace {
			return nil
		}
		if authenticatedAgentRef != "" && authenticatedAgentRef != identityRef {
			activeInWorkspace = false
			return nil
		}
		lifecycle, ok := r.agentLifecycle.(workAgentLifecycleInScope)
		if !ok {
			return store.ErrRowLockUnavailable
		}
		eligible, lifecycleFacts, err := lifecycle.AgentWorkAuthorityFactsInScope(
			ctx, sc, identityRef,
		)
		if err != nil {
			return err
		}
		if !eligible {
			activeInWorkspace = false
			return nil
		}
		if !validWorkLifecycleAuthorityFacts(identityID, lifecycleFacts) {
			return errors.New("agent lifecycle returned malformed authority facts")
		}
		facts = append(facts, lifecycleFacts...)
		return nil
	})
	if err != nil {
		return sessions.WorkAgentAuthoritySnapshot{}, err
	}
	if !activeInWorkspace || identityRef == "" {
		return sessions.WorkAgentAuthoritySnapshot{Eligible: false}, nil
	}
	return sessions.WorkAgentAuthoritySnapshot{
		Eligible: true,
		Digest:   workAuthorityDigest(facts),
		Token:    workAgentAuthorityToken{facts: append([]store.AuthorizationFactRef(nil), facts...)},
	}, nil
}

func workAuthorityDigest(facts []store.AuthorizationFactRef) string {
	ordered := append([]store.AuthorizationFactRef(nil), facts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Kind != ordered[j].Kind {
			return ordered[i].Kind < ordered[j].Kind
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	h := sha256.New()
	for _, fact := range ordered {
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%d\x00", fact.Kind, fact.ID, fact.Version)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validWorkLifecycleAuthorityFacts(
	ownerIdentityID model.ID,
	facts []store.AuthorizationFactRef,
) bool {
	if len(facts) != 2 {
		return false
	}
	var sponsor, lifecycle int
	for _, fact := range facts {
		if fact.ID.IsZero() || fact.Version < 1 {
			return false
		}
		switch fact.Kind {
		case "core.identity":
			if fact.ID == ownerIdentityID {
				return false
			}
			sponsor++
		case "governance.nhi_lifecycle":
			lifecycle++
		default:
			return false
		}
	}
	return sponsor == 1 && lifecycle == 1
}

func (r workIdentityResolver) LockAgentWorkAuthority(
	ctx context.Context,
	sc store.Scope,
	snapshot sessions.WorkAgentAuthoritySnapshot,
) error {
	token, ok := snapshot.Token.(workAgentAuthorityToken)
	if !ok || !snapshot.Eligible || len(token.facts) == 0 ||
		snapshot.Digest != workAuthorityDigest(token.facts) {
		return store.ErrRowLockUnavailable
	}
	locker, ok := sc.(store.AuthoritySnapshotLocker)
	if !ok {
		return store.ErrRowLockUnavailable
	}
	return locker.LockAuthoritySnapshot(ctx, token.facts)
}

func (r workIdentityResolver) resolveSession(ctx context.Context, tenant model.TenantID, workspace model.ID, ref string) (sessions.Participant, error) {
	if r.sessions == nil {
		// Deny-closed, and it names itself: an unwired plane is "I could not look",
		// never "this session is not eligible".
		return sessions.Participant{}, fmt.Errorf("session identity plane is not wired")
	}
	return r.sessions.SessionWorkParticipant(ctx, tenant, workspace, ref)
}

func (r workIdentityResolver) SessionActsForAgent(
	ctx context.Context,
	tenant model.TenantID,
	sid string,
	agentRef string,
) (bool, error) {
	if r.sessions == nil || r.st == nil {
		return false, fmt.Errorf("work identity planes are not wired")
	}
	if agentRef == "" {
		return false, nil
	}

	// WorkItem.owner_ref uses the canonical core Identity.ID. The authenticated
	// AgentIdentity recorded on sessions_run is instead the identity's stable
	// external_id (the convergence anchor). Translate through the tenant
	// store rather than accepting either spelling: passing the UUID straight to
	// sessions makes every real ID != external_id owner unreachable, while
	// accepting a caller-supplied external ref would create a second authority
	// spelling that ResolveParticipant never validated.
	identityID, err := model.ParseID(agentRef)
	if err != nil {
		return false, err
	}
	var externalRef string
	if err := r.st.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, identityID)
		if err != nil {
			return err
		}
		externalRef = identity.ExternalID
		return nil
	}); err != nil {
		return false, err
	}
	if externalRef == "" {
		return false, fmt.Errorf("canonical agent identity has no external reference")
	}
	return r.sessions.SessionActsForAgent(ctx, tenant, sid, externalRef)
}

// AuthenticatedAgentMatches bridges the two server-owned identity namespaces
// without accepting either as an alternate request spelling. canonicalRef is
// WorkItem.owner_ref (Identity.ID); authenticatedRef is Principal.AgentIdentity
// (Identity.ExternalID). Both are tenant-scoped facts loaded or authenticated
// before this call.
func (r workIdentityResolver) AuthenticatedAgentMatches(
	ctx context.Context,
	tenant model.TenantID,
	canonicalRef string,
	authenticatedRef string,
) (bool, error) {
	if r.st == nil || canonicalRef == "" || authenticatedRef == "" {
		return false, nil
	}
	identityID, err := model.ParseID(canonicalRef)
	if err != nil {
		return false, err
	}
	var matches bool
	err = r.st.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, identityID)
		if err != nil {
			return err
		}
		matches = identity.ExternalID != "" && identity.ExternalID == authenticatedRef
		return nil
	})
	return matches, err
}
