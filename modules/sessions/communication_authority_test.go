// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var _ func(*Module, *auth.Authenticator, *auth.Authorizer) = (*Module).UseCommunicationRequestAuthority

type communicationAuthorityResolverRecorder struct {
	resolved auth.Principal
	err      error
	calls    int
	refs     []auth.PrincipalRef
	tenants  []model.TenantID
	trace    *[]string
}

func (r *communicationAuthorityResolverRecorder) ResolvePrincipalScope(
	_ context.Context,
	ref auth.PrincipalRef,
	tenant model.TenantID,
) (auth.Principal, error) {
	r.calls++
	r.refs = append(r.refs, ref)
	r.tenants = append(r.tenants, tenant)
	if r.trace != nil {
		*r.trace = append(*r.trace, "resolve")
	}
	return r.resolved, r.err
}

type communicationAuthoritySourceRecorder struct {
	evidence auth.AuthorizationEvidence
	calls    int
	requests []auth.Request
	trace    *[]string
}

type communicationAuthorityScopedEvidenceAuthorizer struct {
	decision    auth.ScopedEvidenceDecision
	typedCalls  int
	legacyCalls int
}

func (a *communicationAuthorityScopedEvidenceAuthorizer) Scoped(
	context.Context,
	auth.Request,
) (auth.ScopedDecision, error) {
	a.legacyCalls++
	return auth.ScopedDecision{Effect: auth.EffectForbid},
		errors.New("legacy scoped authorization must not run")
}

func (a *communicationAuthorityScopedEvidenceAuthorizer) ScopedEvidence(
	context.Context,
	auth.Request,
) (auth.ScopedEvidenceDecision, error) {
	a.typedCalls++
	return a.decision, nil
}

var (
	_ auth.ScopedAuthorizer         = (*communicationAuthorityScopedEvidenceAuthorizer)(nil)
	_ auth.ScopedEvidenceAuthorizer = (*communicationAuthorityScopedEvidenceAuthorizer)(nil)
)

func (s *communicationAuthoritySourceRecorder) AuthorizeEvidence(
	_ context.Context,
	request auth.Request,
) auth.AuthorizationEvidence {
	s.calls++
	s.requests = append(s.requests, request)
	if s.trace != nil {
		*s.trace = append(*s.trace, "authorize")
	}
	return s.evidence
}

type communicationAuthorityTestFixture struct {
	module                 *Module
	st                     store.Store
	authenticator          *auth.Authenticator
	issuer                 auth.Principal
	tenant                 model.TenantID
	workspace              model.ID
	principal              auth.Principal
	ref                    auth.PrincipalRef
	secondPrincipal        auth.Principal
	communicationPrincipal auth.Principal
	workPrincipal          auth.Principal
}

func newCommunicationAuthorityTestFixture(t *testing.T) communicationAuthorityTestFixture {
	t.Helper()
	module := New()
	h := newHarness(t, module)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "communication-authority")
	firstToken := h.viewerToken(admin, tenant, "authority-one@example.test")
	secondToken := h.viewerToken(admin, tenant, "authority-two@example.test")
	authenticator := auth.NewAuthenticator(h.st, nil)
	principal, err := authenticator.Authenticate(context.Background(), firstToken)
	if err != nil {
		t.Fatalf("authenticate first authority principal: %v", err)
	}
	ref, ok := principal.Ref()
	if !ok {
		t.Fatal("authenticated authority principal has no opaque ref")
	}
	second, err := authenticator.Authenticate(context.Background(), secondToken)
	if err != nil {
		t.Fatalf("authenticate second authority principal: %v", err)
	}
	var workspace model.ID
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		value, err := sc.DefaultWorkspace(context.Background())
		if err == nil {
			workspace = value.ID
		}
		return err
	}); err != nil {
		t.Fatalf("read communication authority default workspace: %v", err)
	}
	system, err := auth.NewSystemOperator(
		"test:sessions-communication-authority", "mint test runtime credentials",
	)
	if err != nil {
		t.Fatalf("build authority test issuer: %v", err)
	}
	communication, err := authenticator.IssueCommunicationSessionCredential(
		context.Background(), system, auth.CommunicationSessionCredentialSpec{
			Tenant: tenant, WorkspaceID: workspace,
			SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
			AgentRef: "agent:" + model.NewID().String(), ClaimFence: 7,
		},
	)
	if err != nil {
		t.Fatalf("issue communication-session authority credential: %v", err)
	}
	communicationPrincipal, err := authenticator.Authenticate(
		context.Background(), communication.Token,
	)
	if err != nil {
		t.Fatalf("authenticate communication-session authority credential: %v", err)
	}
	work, err := authenticator.IssueWorkSessionCredential(
		context.Background(), system, auth.WorkSessionCredentialSpec{
			Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
			RunRef: model.NewID().String(), AgentRef: "agent:" + model.NewID().String(),
			ClaimFence: 3,
		},
	)
	if err != nil {
		t.Fatalf("issue work-session authority credential: %v", err)
	}
	workPrincipal, err := authenticator.Authenticate(context.Background(), work.Token)
	if err != nil {
		t.Fatalf("authenticate work-session authority credential: %v", err)
	}
	return communicationAuthorityTestFixture{
		module: module, st: h.st, authenticator: authenticator, issuer: system,
		tenant: tenant, workspace: workspace, principal: principal, ref: ref,
		secondPrincipal: second, communicationPrincipal: communicationPrincipal,
		workPrincipal: workPrincipal,
	}
}

func TestModuleCommunicationRequestAuthorityRequiresCompletePair(t *testing.T) {
	t.Parallel()

	module := New()
	resolver := &communicationAuthorityResolverRecorder{}
	source := &communicationAuthoritySourceRecorder{}

	module.useCommunicationRequestAuthoritySources(resolver, nil)
	if module.communicationAuthoritySources != nil {
		t.Fatal("resolver-only authority pair remained bound")
	}
	if _, err := module.bindCurrentCommunicationRequestAuthority(
		context.Background(), auth.PrincipalRef{}, communicationAuthorityQuestion{},
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) || resolver.calls != 0 || source.calls != 0 {
		t.Fatalf("resolver-only bind = %v, calls %d/%d", err, resolver.calls, source.calls)
	}

	module.useCommunicationRequestAuthoritySources(nil, source)
	if module.communicationAuthoritySources != nil {
		t.Fatal("source-only authority pair remained bound")
	}
	if _, err := module.bindCurrentCommunicationRequestAuthority(
		context.Background(), auth.PrincipalRef{}, communicationAuthorityQuestion{},
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) || resolver.calls != 0 || source.calls != 0 {
		t.Fatalf("source-only bind = %v, calls %d/%d", err, resolver.calls, source.calls)
	}

	var typedNilResolver *communicationAuthorityResolverRecorder
	module.useCommunicationRequestAuthoritySources(typedNilResolver, source)
	if module.communicationAuthoritySources != nil {
		t.Fatal("typed-nil resolver remained bound")
	}
	var typedNilSource *communicationAuthoritySourceRecorder
	module.useCommunicationRequestAuthoritySources(resolver, typedNilSource)
	if module.communicationAuthoritySources != nil {
		t.Fatal("typed-nil source remained bound")
	}

	module.communicationAuthoritySources = &communicationRequestAuthoritySources{
		resolver: typedNilResolver,
		source:   source,
	}
	if _, err := module.bindCurrentCommunicationRequestAuthority(
		context.Background(), auth.PrincipalRef{}, communicationAuthorityQuestion{},
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) || resolver.calls != 0 || source.calls != 0 {
		t.Fatalf("malformed resolver bundle bind = %v, calls %d/%d",
			err, resolver.calls, source.calls)
	}
	module.communicationAuthoritySources = &communicationRequestAuthoritySources{
		resolver: resolver,
		source:   typedNilSource,
	}
	if _, err := module.bindCurrentCommunicationRequestAuthority(
		context.Background(), auth.PrincipalRef{}, communicationAuthorityQuestion{},
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) || resolver.calls != 0 || source.calls != 0 {
		t.Fatalf("malformed source bundle bind = %v, calls %d/%d",
			err, resolver.calls, source.calls)
	}
}

func TestModuleUseCommunicationRequestAuthorityBindsConcretePair(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	authorizer := auth.NewAuthorizer(nil)
	fixture.module.UseCommunicationRequestAuthority(fixture.authenticator, authorizer)
	sources := fixture.module.communicationAuthoritySources
	if sources == nil || sources.resolver != fixture.authenticator || sources.source != authorizer {
		t.Fatalf("public authority sources = %#v, want exact concrete pair", sources)
	}

	now := time.Now().UTC()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
	defer cancel()
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		channelKind, model.NewID(), CommunicationRead,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.module.bindCurrentCommunicationRequestAuthority(
		ctx, fixture.ref, question,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("bare public authorizer bind = %v, want UNKNOWN without AuthorizationEpoch", err)
	}

	var authorizationFact store.AuthorizationFactRef
	if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return errors.New("authority fixture lacks authorization epoch reader")
		}
		var err error
		authorizationFact, err = reader.ReadAuthorizationEpoch(ctx)
		return err
	}); err != nil {
		t.Fatalf("read current AuthorizationEpoch: %v", err)
	}
	scoped := &communicationAuthorityScopedEvidenceAuthorizer{
		decision: auth.ScopedEvidenceDecision{
			Effect: auth.EffectAbstain,
			ResourceGuard: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "resource_guard_clean",
			},
			ForbidAbsence: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "scoped_forbid_absent",
			},
			Facts:      []store.AuthorizationFactRef{authorizationFact},
			ObservedAt: now.Add(-time.Minute), FreshUntil: now.Add(5 * time.Minute),
		},
	}
	typedAuthorizer := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped))
	fixture.module.UseCommunicationRequestAuthority(fixture.authenticator, typedAuthorizer)
	sources = fixture.module.communicationAuthoritySources
	if sources == nil || sources.resolver != fixture.authenticator || sources.source != typedAuthorizer {
		t.Fatalf("typed public authority sources = %#v, want exact concrete pair", sources)
	}
	if _, err := fixture.module.bindCurrentCommunicationRequestAuthority(
		ctx, fixture.ref, question,
	); err != nil {
		t.Fatalf("bind typed public concrete authority pair: %v", err)
	}
	if scoped.typedCalls != 1 || scoped.legacyCalls != 0 {
		t.Fatalf("scoped evidence calls typed/legacy = %d/%d, want 1/0",
			scoped.typedCalls, scoped.legacyCalls)
	}

	var nilResolver *auth.Authenticator
	fixture.module.UseCommunicationRequestAuthority(nilResolver, typedAuthorizer)
	if fixture.module.communicationAuthoritySources != nil {
		t.Fatal("typed-nil public resolver retained authority pair")
	}
	fixture.module.UseCommunicationRequestAuthority(fixture.authenticator, typedAuthorizer)
	var nilAuthorizer *auth.Authorizer
	fixture.module.UseCommunicationRequestAuthority(fixture.authenticator, nilAuthorizer)
	if fixture.module.communicationAuthoritySources != nil {
		t.Fatal("typed-nil public authorizer retained authority pair")
	}
}

func TestModuleCommunicationRequestAuthorityRebindsOneExactBundle(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	now := time.Now().UTC()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
	defer cancel()
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		channelKind, model.NewID(), CommunicationMessageSend,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence := communicationAuthorityAllowEvidence(
		fixture.tenant, now.Add(-time.Minute), now.Add(5*time.Minute),
	)
	resolverA := &communicationAuthorityResolverRecorder{resolved: fixture.principal}
	sourceA := &communicationAuthoritySourceRecorder{evidence: evidence}
	fixture.module.useCommunicationRequestAuthoritySources(resolverA, sourceA)
	firstBundle := fixture.module.communicationAuthoritySources
	if firstBundle == nil {
		t.Fatal("complete authority pair was not retained")
	}
	if _, err := fixture.module.bindCurrentCommunicationRequestAuthority(
		ctx, fixture.ref, question,
	); err != nil {
		t.Fatalf("bind first module authority: %v", err)
	}

	resolverB := &communicationAuthorityResolverRecorder{resolved: fixture.principal}
	sourceB := &communicationAuthoritySourceRecorder{evidence: evidence}
	fixture.module.useCommunicationRequestAuthoritySources(resolverB, sourceB)
	if fixture.module.communicationAuthoritySources == nil ||
		fixture.module.communicationAuthoritySources == firstBundle {
		t.Fatal("authority rebind did not replace the indivisible bundle")
	}
	if _, err := fixture.module.bindCurrentCommunicationRequestAuthority(
		ctx, fixture.ref, question,
	); err != nil {
		t.Fatalf("bind rebound module authority: %v", err)
	}
	if resolverA.calls != 1 || sourceA.calls != 1 || resolverB.calls != 1 || sourceB.calls != 1 {
		t.Fatalf("authority pair calls A=%d/%d B=%d/%d",
			resolverA.calls, sourceA.calls, resolverB.calls, sourceB.calls)
	}
	if len(resolverB.refs) != 1 || resolverB.refs[0] != fixture.ref ||
		len(resolverB.tenants) != 1 || resolverB.tenants[0] != question.entity.TenantID ||
		len(sourceB.requests) != 1 || sourceB.requests[0].Permission != question.permission ||
		sourceB.requests[0].Resource.Kind != string(question.entity.Kind) ||
		sourceB.requests[0].Resource.ID != question.entity.ID.String() ||
		sourceB.requests[0].Resource.WorkspaceID != question.entity.WorkspaceID {
		t.Fatalf("rebound exact request = refs %#v tenants %#v requests %#v",
			resolverB.refs, resolverB.tenants, sourceB.requests)
	}

	fixture.module.useCommunicationRequestAuthoritySources(resolverB, nil)
	if fixture.module.communicationAuthoritySources != nil {
		t.Fatal("partial rebind retained an old authority half")
	}
	if _, err := fixture.module.bindCurrentCommunicationRequestAuthority(
		ctx, fixture.ref, question,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) || resolverB.calls != 1 || sourceB.calls != 1 {
		t.Fatalf("partial rebound bind = %v, calls %d/%d", err, resolverB.calls, sourceB.calls)
	}

	fixture.module.useCommunicationRequestAuthoritySources(resolverB, sourceB)
	fixture.module.useCommunicationRequestAuthoritySources(nil, sourceB)
	if fixture.module.communicationAuthoritySources != nil {
		t.Fatal("nil resolver rebind retained an old authority bundle")
	}
	fixture.module.useCommunicationRequestAuthoritySources(resolverB, sourceB)
	var typedNilResolver *communicationAuthorityResolverRecorder
	fixture.module.useCommunicationRequestAuthoritySources(typedNilResolver, sourceB)
	if fixture.module.communicationAuthoritySources != nil {
		t.Fatal("typed-nil resolver rebind retained an old authority bundle")
	}
	fixture.module.useCommunicationRequestAuthoritySources(resolverB, sourceB)
	var typedNilSource *communicationAuthoritySourceRecorder
	fixture.module.useCommunicationRequestAuthoritySources(resolverB, typedNilSource)
	if fixture.module.communicationAuthoritySources != nil {
		t.Fatal("typed-nil source rebind retained an old authority bundle")
	}
}

func TestCommunicationRequestAuthorityCannotRelabelEffectQuestion(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	now := time.Now().UTC()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
	defer cancel()
	scope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace}
	question, err := newCommunicationAuthorityQuestion(
		scope, messageDeliveryKind, model.NewID(), CommunicationRead,
	)
	if err != nil {
		t.Fatalf("new bound delivery-read question: %v", err)
	}
	resolver := &communicationAuthorityResolverRecorder{resolved: fixture.principal}
	source := &communicationAuthoritySourceRecorder{evidence: communicationAuthorityAllowEvidence(
		fixture.tenant, now.Add(-time.Minute), now.Add(5*time.Minute),
	)}
	bound, err := bindCommunicationRequestAuthority(
		ctx, resolver, source, fixture.ref, question,
	)
	if err != nil {
		t.Fatalf("bind delivery-read authority: %v", err)
	}

	differentEntity, err := newCommunicationAuthorityQuestion(
		scope, messageDeliveryKind, model.NewID(), CommunicationRead,
	)
	if err != nil {
		t.Fatalf("new different-entity question: %v", err)
	}
	differentOperation, err := newCommunicationAuthorityQuestion(
		scope, messageDeliveryKind, question.entity.ID, CommunicationDeliveryWrite,
	)
	if err != nil {
		t.Fatalf("new different-operation question: %v", err)
	}
	differentWorkspace, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: model.NewID()},
		messageDeliveryKind, question.entity.ID, CommunicationRead,
	)
	if err != nil {
		t.Fatalf("new different-workspace question: %v", err)
	}
	differentTenant, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: model.NewTenantID(), WorkspaceID: fixture.workspace},
		messageDeliveryKind, question.entity.ID, CommunicationRead,
	)
	if err != nil {
		t.Fatalf("new different-tenant question: %v", err)
	}
	tamperedPermission := question
	tamperedPermission.permission = permDeliveryAdmin

	counting := &countingCommunicationModuleData{inner: fixture.module.data}
	fixture.module.data = counting
	callbackCalls := 0
	for name, expected := range map[string]communicationAuthorityQuestion{
		"entity":     differentEntity,
		"operation":  differentOperation,
		"workspace":  differentWorkspace,
		"tenant":     differentTenant,
		"permission": tamperedPermission,
	} {
		t.Run(name, func(t *testing.T) {
			if _, inspectErr := bound.contextFor(expected); !errors.Is(inspectErr, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("relabeled request authority inspection = %v, want UNKNOWN", inspectErr)
			}
			if mutateErr := fixture.module.mutateCommunicationWithAuthority(
				ctx, expected, bound, CommunicationClaimAuthoritySnapshot{},
				func(*communicationTx, communicationRequestAuthorityContext) error {
					callbackCalls++
					return nil
				},
			); mutateErr == nil {
				t.Fatal("relabeled request authority unexpectedly opened a mutation")
			}
		})
	}
	if counting.mutateCalls != 0 || callbackCalls != 0 {
		t.Fatalf("relabeled authority opened Mutate/callback = %d/%d, want zero/zero",
			counting.mutateCalls, callbackCalls)
	}
	if nilCallbackErr := fixture.module.mutateCommunicationWithAuthority(
		ctx, question, bound, CommunicationClaimAuthoritySnapshot{}, nil,
	); !errors.Is(nilCallbackErr, errCommunicationTransactionUnavailable) {
		t.Fatalf("nil authority callback error = %v, want unavailable", nilCallbackErr)
	}
	if counting.mutateCalls != 0 {
		t.Fatalf("nil authority callback opened Mutate %d times, want zero", counting.mutateCalls)
	}
	malformedCopy := communicationRequestAuthority{}
	if _, inspectErr := malformedCopy.contextFor(question); !errors.Is(
		inspectErr, ErrCommunicationEvidenceUnknown,
	) {
		t.Fatalf("malformed authority inspection = %v, want UNKNOWN", inspectErr)
	}
	if _, _, snapshotErr := malformedCopy.transactionSnapshot(
		question, CommunicationClaimAuthoritySnapshot{},
	); !errors.Is(
		snapshotErr, ErrCommunicationEvidenceUnknown,
	) {
		t.Fatalf("mutated bound authority snapshot = %v, want UNKNOWN", snapshotErr)
	}

	stop := errors.New("stop exact authority mutation after constructor")
	replayCopy := bound
	sawBoundSnapshot := false
	err = fixture.module.mutateCommunicationWithAuthority(
		ctx, question, bound, CommunicationClaimAuthoritySnapshot{},
		func(tx *communicationTx, boundContext communicationRequestAuthorityContext) error {
			callbackCalls++
			sawBoundSnapshot = tx != nil && tx.hasBoundAuthority() &&
				equalCommunicationAuthorityFacts(tx.requestAuthorityFacts, source.evidence.Facts) &&
				tx.requestObservedAt.Equal(source.evidence.ObservedAt) &&
				tx.requestFreshUntil.Equal(source.evidence.FreshUntil) &&
				boundContext.question == question &&
				boundContext.principal == (CommunicationPrincipal{UserID: fixture.principal.UserID})
			return stop
		},
	)
	if !errors.Is(err, stop) || counting.mutateCalls != 1 || callbackCalls != 1 ||
		!sawBoundSnapshot {
		t.Fatalf("exact authority mutation = error %v Mutate/callback %d/%d bound=%v, want stop/1/1/true",
			err, counting.mutateCalls, callbackCalls, sawBoundSnapshot)
	}
	if resolver.calls != 1 || source.calls != 1 {
		t.Fatalf("mutation repeated auth resolution/evaluation = %d/%d, want one/one total",
			resolver.calls, source.calls)
	}
	if replayErr := fixture.module.mutateCommunicationWithAuthority(
		ctx, question, replayCopy, CommunicationClaimAuthoritySnapshot{},
		func(*communicationTx, communicationRequestAuthorityContext) error {
			callbackCalls++
			return nil
		},
	); !errors.Is(replayErr, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("second exact use error = %v, want UNKNOWN", replayErr)
	}
	if counting.mutateCalls != 1 || callbackCalls != 1 {
		t.Fatalf("replayed authority opened Mutate/callback = %d/%d, want one/one total",
			counting.mutateCalls, callbackCalls)
	}
}

func TestCommunicationRequestAuthorityRejectsForgedEmptyConsumptionBeforeMutate(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		channelKind, model.NewID(), CommunicationMessageSend,
	)
	if err != nil {
		t.Fatalf("new forged-consumption question: %v", err)
	}
	forged := communicationRequestAuthority{access: func(
		communicationRequestAuthorityAccess,
		communicationAuthorityQuestion,
		CommunicationClaimAuthoritySnapshot,
	) (communicationRequestAuthorityAccessResult, error) {
		return communicationRequestAuthorityAccessResult{}, nil
	}}
	counting := &countingCommunicationModuleData{inner: fixture.module.data}
	fixture.module.data = counting
	callbackCalls := 0
	err = fixture.module.mutateCommunicationWithAuthority(
		context.Background(), question, forged, CommunicationClaimAuthoritySnapshot{},
		func(*communicationTx, communicationRequestAuthorityContext) error {
			callbackCalls++
			return nil
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		counting.mutateCalls != 0 || callbackCalls != 0 {
		t.Fatalf("forged empty consumption = %v mutate/callback %d/%d, want UNKNOWN 0/0",
			err, counting.mutateCalls, callbackCalls)
	}
}

func TestCommunicationRequestAuthorityCopiesShareOneConcurrentConsumption(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	now := time.Now().UTC()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
	defer cancel()
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		messageDeliveryKind, model.NewID(), CommunicationRead,
	)
	if err != nil {
		t.Fatalf("new delivery-read question: %v", err)
	}
	bound, err := bindCommunicationRequestAuthority(
		ctx,
		&communicationAuthorityResolverRecorder{resolved: fixture.principal},
		&communicationAuthoritySourceRecorder{evidence: communicationAuthorityAllowEvidence(
			fixture.tenant, now.Add(-time.Minute), now.Add(5*time.Minute),
		)},
		fixture.ref,
		question,
	)
	if err != nil {
		t.Fatalf("bind delivery-read authority: %v", err)
	}

	const contenders = 16
	start := make(chan struct{})
	errs := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		copyOfBound := bound
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, snapshotErr := copyOfBound.transactionSnapshot(
				question, CommunicationClaimAuthoritySnapshot{},
			)
			errs <- snapshotErr
		}()
	}
	close(start)
	wait.Wait()
	close(errs)

	succeeded := 0
	rejected := 0
	for snapshotErr := range errs {
		switch {
		case snapshotErr == nil:
			succeeded++
		case errors.Is(snapshotErr, ErrCommunicationEvidenceUnknown):
			rejected++
		default:
			t.Fatalf("concurrent consumption error = %v", snapshotErr)
		}
	}
	if succeeded != 1 || rejected != contenders-1 {
		t.Fatalf("concurrent consumptions succeeded/rejected = %d/%d, want 1/%d",
			succeeded, rejected, contenders-1)
	}
}

func TestCommunicationSessionRequestAuthorityRequiresItsExactClaim(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	now := time.Now().UTC()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
	defer cancel()
	ref, ok := fixture.communicationPrincipal.Ref()
	if !ok {
		t.Fatal("communication-session principal has no opaque credential ref")
	}
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		messageDeliveryKind, model.NewID(), CommunicationRead,
	)
	if err != nil {
		t.Fatalf("new delivery-read question: %v", err)
	}
	bound, err := bindCommunicationRequestAuthority(
		ctx,
		&communicationAuthorityResolverRecorder{resolved: fixture.communicationPrincipal},
		&communicationAuthoritySourceRecorder{evidence: communicationAuthorityAllowEvidence(
			fixture.tenant, now.Add(-time.Minute), now.Add(5*time.Minute),
		)},
		ref,
		question,
	)
	if err != nil {
		t.Fatalf("bind communication-session authority: %v", err)
	}

	counting := &countingCommunicationModuleData{inner: fixture.module.data}
	fixture.module.data = counting
	callbackCalls := 0
	callback := func(*communicationTx, communicationRequestAuthorityContext) error {
		callbackCalls++
		return nil
	}
	if err := fixture.module.mutateCommunicationWithAuthority(
		ctx, question, bound, CommunicationClaimAuthoritySnapshot{}, callback,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("session authority without Claim = %v, want UNKNOWN", err)
	}

	claimDeadline := model.NewTimestamp(now.Add(5 * time.Minute))
	wrongSID := "osn_" + model.NewID().String()
	wrongSIDClaims, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{
			SessionSID: wrongSID, Fence: fixture.communicationPrincipal.SessionFence,
		}},
		[]store.AuthorizationFactRef{communicationClaimAuthorityFact(
			t, wrongSID, fixture.communicationPrincipal.SessionFence, claimDeadline,
		)},
	)
	if err != nil {
		t.Fatalf("new wrong-SID Claim authority: %v", err)
	}
	if err := fixture.module.mutateCommunicationWithAuthority(
		ctx, question, bound, wrongSIDClaims, callback,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("session authority with wrong SID = %v, want UNKNOWN", err)
	}

	wrongFence := fixture.communicationPrincipal.SessionFence + 1
	wrongFenceClaims, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{
			SessionSID: fixture.communicationPrincipal.SessionIdentity, Fence: wrongFence,
		}},
		[]store.AuthorizationFactRef{communicationClaimAuthorityFact(
			t, fixture.communicationPrincipal.SessionIdentity, wrongFence, claimDeadline,
		)},
	)
	if err != nil {
		t.Fatalf("new wrong-fence Claim authority: %v", err)
	}
	if err := fixture.module.mutateCommunicationWithAuthority(
		ctx, question, bound, wrongFenceClaims, callback,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("session authority with wrong fence = %v, want UNKNOWN", err)
	}
	if counting.mutateCalls != 0 || callbackCalls != 0 {
		t.Fatalf("invalid session Claims opened Mutate/callback = %d/%d, want zero/zero",
			counting.mutateCalls, callbackCalls)
	}

	exactClaims, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{
			SessionSID: fixture.communicationPrincipal.SessionIdentity,
			Fence:      fixture.communicationPrincipal.SessionFence,
		}},
		[]store.AuthorizationFactRef{communicationClaimAuthorityFact(
			t,
			fixture.communicationPrincipal.SessionIdentity,
			fixture.communicationPrincipal.SessionFence,
			claimDeadline,
		)},
	)
	if err != nil {
		t.Fatalf("new exact Claim authority: %v", err)
	}
	wantPrincipal, err := communicationPrincipalFromResolvedAuth(fixture.communicationPrincipal)
	if err != nil {
		t.Fatalf("derive expected communication principal: %v", err)
	}
	stop := errors.New("stop exact session-bound mutation")
	err = fixture.module.mutateCommunicationWithAuthority(
		ctx, question, bound, exactClaims,
		func(tx *communicationTx, boundContext communicationRequestAuthorityContext) error {
			callbackCalls++
			if boundContext.question != question ||
				boundContext.principal != wantPrincipal ||
				!equalCommunicationAuthorityFacts(tx.claimAuthorityFacts, exactClaims.facts) {
				t.Fatalf("session-bound context/claims = %#v/%#v", boundContext, tx.claimAuthorityFacts)
			}
			return stop
		},
	)
	if !errors.Is(err, stop) || counting.mutateCalls != 1 || callbackCalls != 1 {
		t.Fatalf("exact session Claim mutation = %v Mutate/callback %d/%d, want stop/1/1",
			err, counting.mutateCalls, callbackCalls)
	}
}

func TestCommunicationSessionRequestAuthorityLocksClaimAndCommitsEffect(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	sid := "osn_" + model.NewID().String()
	lease, err := fixture.module.Claim(ctx, fixture.tenant, sid, "authority-e2e-holder", time.Hour)
	if err != nil {
		t.Fatalf("create durable communication Claim: %v", err)
	}
	credential, err := fixture.authenticator.IssueCommunicationSessionCredential(
		ctx,
		fixture.issuer,
		auth.CommunicationSessionCredentialSpec{
			Tenant: fixture.tenant, WorkspaceID: fixture.workspace,
			SessionRef: sid, RunRef: model.NewID().String(),
			AgentRef: "agent:" + model.NewID().String(), ClaimFence: lease.Fence,
		},
	)
	if err != nil {
		t.Fatalf("issue Claim-bound communication credential: %v", err)
	}
	principal, err := fixture.authenticator.Authenticate(ctx, credential.Token)
	if err != nil {
		t.Fatalf("authenticate Claim-bound communication credential: %v", err)
	}
	ref, ok := principal.Ref()
	if !ok {
		t.Fatal("Claim-bound communication principal has no opaque ref")
	}

	var (
		epoch             model.DirectoryEpoch
		authorizationFact store.AuthorizationFactRef
		claimRow          model.Record
	)
	if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return errors.New("authority fixture lacks directory snapshot reader")
		}
		epoch, err = reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		authorizationReader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return errors.New("authority fixture lacks authorization epoch reader")
		}
		authorizationFact, err = authorizationReader.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return err
		}
		var found bool
		claimRow, found, err = findClaim(ctx, sc, sid)
		if err == nil && !found {
			err = errors.New("durable communication Claim is absent")
		}
		return err
	}); err != nil {
		t.Fatalf("read durable authority witnesses: %v", err)
	}
	claimDeadline, err := model.ParseTimestamp(claimRow.String(colLeaseExpires))
	if err != nil {
		t.Fatalf("parse durable Claim deadline: %v", err)
	}
	claimFact, err := store.NewLeaseFenceAuthorizationFactRef(
		claimKind,
		model.ID(claimRow.String(model.ColID)),
		claimRow.Int(model.ColVersion),
		sid,
		lease.Fence,
		claimDeadline,
	)
	if err != nil {
		t.Fatalf("build durable Claim fact: %v", err)
	}
	claims, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{SessionSID: sid, Fence: lease.Fence}},
		[]store.AuthorizationFactRef{claimFact},
	)
	if err != nil {
		t.Fatalf("build durable Claim snapshot: %v", err)
	}
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		channelKind,
		model.NewID(),
		CommunicationMessageSend,
	)
	if err != nil {
		t.Fatalf("new message-send authority question: %v", err)
	}
	evidence := communicationAuthorityAllowEvidence(
		fixture.tenant, now.Add(-time.Minute), now.Add(3*time.Minute),
	)
	evidence.Facts = []store.AuthorizationFactRef{
		authorizationFact,
		{Kind: model.DirectoryEpochKind, ID: epoch.ID, Version: epoch.Version},
	}
	bound, err := bindCommunicationRequestAuthority(
		ctx,
		&communicationAuthorityResolverRecorder{resolved: principal},
		&communicationAuthoritySourceRecorder{evidence: evidence},
		ref,
		question,
	)
	if err != nil {
		t.Fatalf("bind durable communication-session authority: %v", err)
	}
	wantPrincipal, err := communicationPrincipalFromResolvedAuth(principal)
	if err != nil {
		t.Fatalf("derive exact communication principal: %v", err)
	}
	var committed model.AuditEvent
	err = fixture.module.mutateCommunicationWithAuthority(
		ctx,
		question,
		bound,
		claims,
		func(tx *communicationTx, boundContext communicationRequestAuthorityContext) error {
			if boundContext.question != question ||
				boundContext.principal != wantPrincipal {
				return errors.New("sealed communication principal changed before effect")
			}
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			var err error
			committed, err = tx.appendAudit(ctx, model.AuditDraft{
				Actor:      wantPrincipal.SessionID,
				ActorKind:  model.ActorAgent,
				Action:     "sessions.communication.authority_e2e",
				TargetKind: question.entity.Kind,
				TargetID:   question.entity.ID,
			})
			return err
		},
	)
	if err != nil {
		t.Fatalf("commit exact request+Claim authority effect: %v", err)
	}
	if committed.Seq < 1 || committed.TenantID != fixture.tenant {
		t.Fatalf("committed authority audit = %+v", committed)
	}
	if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
		after, found, err := findClaim(ctx, sc, sid)
		if err != nil {
			return err
		}
		if !found || after.Int(model.ColVersion) != claimRow.Int(model.ColVersion)+1 ||
			after.Int(colFence) != lease.Fence {
			return fmt.Errorf("Claim after authority effect = %v, want version+1 and fence %d",
				after, lease.Fence)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify exact Claim OCC touch: %v", err)
	}
}

func communicationAuthorityAllowEvidence(
	tenant model.TenantID,
	observedAt time.Time,
	freshUntil time.Time,
) auth.AuthorizationEvidence {
	return auth.AuthorizationEvidence{
		Outcome: auth.EvidenceAllow,
		CorePermission: auth.CheckEvidence{
			Verdict: auth.CheckClean, Code: "rbac_permitted",
		},
		ResourceGuard: auth.CheckEvidence{
			Verdict: auth.CheckClean, Code: "resource_guard_clean",
		},
		ForbidAbsence: auth.CheckEvidence{
			Verdict: auth.CheckClean, Code: "forbid_absence_clean",
		},
		Facts: []store.AuthorizationFactRef{
			{Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 11},
			{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 7},
		},
		ObservedAt: observedAt,
		FreshUntil: freshUntil,
	}
}

func TestCommunicationAuthorityQuestionUsesClosedEntityPermissionMap(t *testing.T) {
	t.Parallel()

	scope := DirectoryScopeRef{TenantID: model.NewTenantID(), WorkspaceID: model.NewID()}
	for _, test := range []struct {
		name       string
		kind       model.Kind
		operation  CommunicationOperation
		permission auth.Permission
	}{
		{"channel_read", channelKind, CommunicationRead, permChannelRead},
		{"message_read", messageKind, CommunicationRead, permMessageRead},
		{"delivery_read", messageDeliveryKind, CommunicationRead, permDeliveryRead},
		{"decision_request_read", decisionRequestKind, CommunicationRead, permDecisionRequestRead},
		{"handoff_read", handoffKind, CommunicationRead, permHandoffRead},
		{"message_send", channelKind, CommunicationMessageSend, permMessageSendWrite},
		{"delivery_write", messageDeliveryKind, CommunicationDeliveryWrite, permDeliveryWrite},
		{"delivery_admin", messageDeliveryKind, CommunicationDeliveryAdmin, permDeliveryAdmin},
		{"decision_request_write", decisionRequestKind, CommunicationDecisionRequestWrite, permDecisionRequestWrite},
		{"handoff_response", handoffKind, CommunicationHandoffResponse, permHandoffResponseWrite},
	} {
		t.Run(test.name, func(t *testing.T) {
			question, err := newCommunicationAuthorityQuestion(
				scope, test.kind, model.NewID(), test.operation,
			)
			if err != nil {
				t.Fatalf("new authority question: %v", err)
			}
			if question.permission != test.permission || question.entity.Kind != test.kind ||
				question.operation != test.operation {
				t.Fatalf("question = %#v, want kind %s operation %s permission %s",
					question, test.kind, test.operation, test.permission)
			}
		})
	}

	for _, unsupported := range []struct {
		kind      model.Kind
		operation CommunicationOperation
	}{
		{messageKind, CommunicationMessageSend},
		{channelKind, CommunicationDeliveryWrite},
		{inboxCursorKind, CommunicationDeliveryWrite},
		{messageDeliveryKind, CommunicationDecisionRequestWrite},
		{decisionRequestKind, CommunicationHandoffResponse},
	} {
		if _, err := newCommunicationAuthorityQuestion(
			scope, unsupported.kind, model.NewID(), unsupported.operation,
		); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("unsupported %s/%s error = %v, want invalid model",
				unsupported.kind, unsupported.operation, err)
		}
	}
}

func TestCommunicationRequestAuthorityBindsDirectExactRequest(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	now := time.Now().UTC()
	deadline := now.Add(10 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		channelKind, model.NewID(), CommunicationMessageSend,
	)
	if err != nil {
		t.Fatalf("new message-send authority question: %v", err)
	}
	trace := []string{}
	evidence := communicationAuthorityAllowEvidence(
		fixture.tenant, now.Add(-time.Minute), now.Add(5*time.Minute),
	)
	resolver := &communicationAuthorityResolverRecorder{
		resolved: fixture.principal, trace: &trace,
	}
	source := &communicationAuthoritySourceRecorder{evidence: evidence, trace: &trace}
	binding, err := bindCommunicationRequestAuthority(
		ctx, resolver, source, fixture.ref, question,
	)
	if err != nil {
		t.Fatalf("bind exact request authority: %v", err)
	}
	if resolver.calls != 1 || source.calls != 1 || len(trace) != 2 ||
		trace[0] != "resolve" || trace[1] != "authorize" {
		t.Fatalf("authority trace/calls = %v %d/%d, want resolve then authorize once",
			trace, resolver.calls, source.calls)
	}
	if len(resolver.refs) != 1 || resolver.refs[0] != fixture.ref ||
		len(resolver.tenants) != 1 || resolver.tenants[0] != fixture.tenant {
		t.Fatalf("resolver binding = refs %#v tenants %#v", resolver.refs, resolver.tenants)
	}
	if len(source.requests) != 1 {
		t.Fatalf("authorization requests = %d, want one", len(source.requests))
	}
	request := source.requests[0]
	requestRef, ok := request.Principal.Ref()
	if !ok || requestRef != fixture.ref || request.Tenant != fixture.tenant ||
		request.Permission != permMessageSendWrite || request.Resource.Kind != string(channelKind) ||
		request.Resource.ID != question.entity.ID.String() ||
		request.Resource.WorkspaceID != fixture.workspace || len(request.Resource.Extra) != 0 {
		t.Fatalf("exact core request = %#v, principal ref ok=%v", request, ok)
	}
	inspection, err := binding.contextFor(question)
	if err != nil {
		t.Fatalf("inspect exact request authority: %v", err)
	}
	if inspection.question != question ||
		inspection.principal != (CommunicationPrincipal{UserID: fixture.principal.UserID}) ||
		inspection.bindingID == nil {
		t.Fatalf("identity-only request authority inspection = %#v", inspection)
	}
	// Inspect must neither expose evidence nor consume the one-shot binding.
	// Mutating the source slice after bind also proves the sealed witness owns
	// its facts before they are released by the successful CAS below.
	evidence.Facts[0].Version = 99
	snapshot, boundContext, err := binding.transactionSnapshot(
		question, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		t.Fatalf("derive transaction snapshot: %v", err)
	}
	if len(snapshot.facts) != 2 || !snapshot.freshUntil.Equal(evidence.FreshUntil) {
		t.Fatalf("transaction snapshot = %#v, want two facts and evidence deadline", snapshot)
	}
	if boundContext.question != question ||
		boundContext.principal != (CommunicationPrincipal{UserID: fixture.principal.UserID}) ||
		boundContext.bindingID != inspection.bindingID ||
		len(boundContext.witness.Facts) != 2 || boundContext.witness.Facts[0].Version != 11 {
		t.Fatalf("sealed request authority context = %#v", boundContext)
	}
	if snapshot.facts[0].Version != 11 {
		t.Fatal("transaction snapshot aliases source fact slices")
	}
	boundContext.witness.Facts[0].Version = 77
	if snapshot.facts[0].Version != 11 {
		t.Fatal("transaction snapshot aliases the consumed witness fact slices")
	}
	for name, value := range map[string]any{
		"binding": binding, "inspection": inspection,
		"context": boundContext, "snapshot": snapshot,
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil || string(encoded) != "{}" {
			t.Fatalf("opaque %s JSON = %s error=%v, want {}", name, encoded, marshalErr)
		}
	}
}

func TestCommunicationRequestAuthorityRejectsInspectionConsumptionSplice(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	now := time.Now().UTC()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
	defer cancel()
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		channelKind, model.NewID(), CommunicationMessageSend,
	)
	if err != nil {
		t.Fatalf("new splice question: %v", err)
	}
	bind := func() communicationRequestAuthority {
		bound, bindErr := bindCommunicationRequestAuthority(
			ctx,
			&communicationAuthorityResolverRecorder{resolved: fixture.principal},
			&communicationAuthoritySourceRecorder{evidence: communicationAuthorityAllowEvidence(
				fixture.tenant, now.Add(-time.Minute), now.Add(5*time.Minute),
			)},
			fixture.ref,
			question,
		)
		if bindErr != nil {
			t.Fatalf("bind splice authority: %v", bindErr)
		}
		return bound
	}
	first, second := bind(), bind()
	spliced := communicationRequestAuthority{access: func(
		access communicationRequestAuthorityAccess,
		expected communicationAuthorityQuestion,
		claims CommunicationClaimAuthoritySnapshot,
	) (communicationRequestAuthorityAccessResult, error) {
		if access == communicationRequestAuthorityInspect {
			return first.access(access, expected, claims)
		}
		return second.access(access, expected, claims)
	}}
	inspection, err := spliced.contextFor(question)
	if err != nil {
		t.Fatalf("inspect spliced authority: %v", err)
	}
	effectReached := false
	err = fixture.module.mutateCommunicationWithAuthority(
		ctx, question, spliced, CommunicationClaimAuthoritySnapshot{},
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			if err := validateConsumedDirectNoticeAuthority(inspection, consumed); err != nil {
				return err
			}
			effectReached = true
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				return err
			}
			return tx.refreshNow(ctx)
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || effectReached {
		t.Fatalf("spliced inspect/consume = %v effect reached=%t, want UNKNOWN/false",
			err, effectReached)
	}
}

func TestCommunicationRequestAuthorityFailsClosedBeforeProducingSnapshot(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	now := time.Now().UTC()
	deadline := now.Add(10 * time.Minute)
	question, err := newCommunicationAuthorityQuestion(
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		messageDeliveryKind, model.NewID(), CommunicationRead,
	)
	if err != nil {
		t.Fatalf("new delivery read question: %v", err)
	}
	allow := communicationAuthorityAllowEvidence(
		fixture.tenant, now.Add(-time.Minute), now.Add(5*time.Minute),
	)

	t.Run("finite_deadline_required_before_resolve", func(t *testing.T) {
		resolver := &communicationAuthorityResolverRecorder{resolved: fixture.principal}
		source := &communicationAuthoritySourceRecorder{evidence: allow}
		_, bindErr := bindCommunicationRequestAuthority(
			context.Background(), resolver, source, fixture.ref, question,
		)
		if !errors.Is(bindErr, ErrCommunicationEvidenceUnknown) ||
			resolver.calls != 0 || source.calls != 0 {
			t.Fatalf("no-deadline bind = %v calls %d/%d, want UNKNOWN and zero calls",
				bindErr, resolver.calls, source.calls)
		}
	})

	t.Run("resolved_ref_must_match", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		resolver := &communicationAuthorityResolverRecorder{resolved: fixture.secondPrincipal}
		source := &communicationAuthoritySourceRecorder{evidence: allow}
		_, bindErr := bindCommunicationRequestAuthority(ctx, resolver, source, fixture.ref, question)
		if !errors.Is(bindErr, ErrCommunicationEvidenceUnknown) ||
			resolver.calls != 1 || source.calls != 0 {
			t.Fatalf("crossed-ref bind = %v calls %d/%d", bindErr, resolver.calls, source.calls)
		}
	})

	for _, test := range []struct {
		name     string
		mutate   func(*auth.AuthorizationEvidence)
		wantBase error
	}{
		{
			name: "definitive_deny",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.Outcome = auth.EvidenceDeny
				e.CorePermission = auth.CheckEvidence{
					Verdict: auth.CheckBroken, Code: "credential_ceiling_denied",
				}
				e.ResourceGuard = auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "not_evaluated"}
				e.ForbidAbsence = auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "not_evaluated"}
				e.Facts, e.ObservedAt, e.FreshUntil = nil, time.Time{}, time.Time{}
			},
			wantBase: ErrCommunicationForbidden,
		},
		{
			name: "unknown",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.Outcome = auth.EvidenceUnknown
				e.CorePermission = auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "unavailable"}
				e.ResourceGuard = auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "unavailable"}
				e.ForbidAbsence = auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "unavailable"}
				e.Facts, e.ObservedAt, e.FreshUntil = nil, time.Time{}, time.Time{}
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "contradictory_allow",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.CorePermission.Verdict = auth.CheckBroken
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "noncanonical_facts",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.Facts[0], e.Facts[1] = e.Facts[1], e.Facts[0]
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "cross_tenant_epoch",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.Facts[1].ID = model.NewID()
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "cross_tenant_authorization_epoch",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.Facts[0].ID = model.NewID()
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "missing_directory_epoch",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.Facts = e.Facts[:1]
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "missing_authorization_epoch",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.Facts = e.Facts[1:]
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "freshness_exceeds_context",
			mutate: func(e *auth.AuthorizationEvidence) {
				e.FreshUntil = deadline.Add(time.Second)
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "leased_directory_epoch",
			mutate: func(e *auth.AuthorizationEvidence) {
				leased, leaseErr := store.NewLeaseFenceAuthorizationFactRef(
					model.DirectoryEpochKind, model.ID(fixture.tenant), 7,
					"tenant-directory", 1, model.NewTimestamp(deadline),
				)
				if leaseErr != nil {
					t.Fatalf("build leased directory epoch mutant: %v", leaseErr)
				}
				e.Facts[1] = leased
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "leased_authorization_epoch",
			mutate: func(e *auth.AuthorizationEvidence) {
				leased, leaseErr := store.NewLeaseFenceAuthorizationFactRef(
					model.AuthorizationEpochKind, model.ID(fixture.tenant), 11,
					"tenant-authorization", 1, model.NewTimestamp(deadline),
				)
				if leaseErr != nil {
					t.Fatalf("build leased authorization epoch mutant: %v", leaseErr)
				}
				e.Facts[0] = leased
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			evidence := allow
			evidence.Facts = append([]store.AuthorizationFactRef(nil), allow.Facts...)
			test.mutate(&evidence)
			resolver := &communicationAuthorityResolverRecorder{resolved: fixture.principal}
			source := &communicationAuthoritySourceRecorder{evidence: evidence}
			_, bindErr := bindCommunicationRequestAuthority(
				ctx, resolver, source, fixture.ref, question,
			)
			if !errors.Is(bindErr, test.wantBase) || resolver.calls != 1 || source.calls != 1 {
				t.Fatalf("bind error/calls = %v %d/%d, want %v and one/one",
					bindErr, resolver.calls, source.calls, test.wantBase)
			}
		})
	}
}

func TestCommunicationPrincipalFromResolvedAuthUsesClosedPrecedence(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)

	user, err := communicationPrincipalFromResolvedAuth(fixture.principal)
	if err != nil || user != (CommunicationPrincipal{UserID: fixture.principal.UserID}) {
		t.Fatalf("human principal = %#v error=%v", user, err)
	}

	agentAuth := fixture.principal.WithAgentIdentity("agent:" + model.NewID().String())
	agent, err := communicationPrincipalFromResolvedAuth(agentAuth)
	if err != nil || agent.UserID != "" || agent.AgentExternalID != agentAuth.AgentIdentity {
		t.Fatalf("agent-over-user principal = %#v error=%v", agent, err)
	}

	runtime, err := communicationPrincipalFromResolvedAuth(fixture.communicationPrincipal)
	if err != nil || runtime.SessionID != fixture.communicationPrincipal.SessionIdentity ||
		runtime.SessionRunRef != fixture.communicationPrincipal.SessionRunRef ||
		runtime.SessionFence != fixture.communicationPrincipal.SessionFence ||
		runtime.SessionWorkspaceID != fixture.communicationPrincipal.SessionWorkspaceID ||
		!runtime.PurposeRestricted ||
		runtime.AgentExternalID != fixture.communicationPrincipal.AgentIdentity {
		t.Fatalf("communication-session principal = %#v error=%v", runtime, err)
	}

	for name, malformed := range map[string]auth.Principal{
		"headless": func() auth.Principal {
			value := fixture.principal
			value.UserID = ""
			return value
		}(),
		"dangling_session": func() auth.Principal {
			value := fixture.principal
			value.SessionIdentity = "osn_" + model.NewID().String()
			return value
		}(),
		"work_session": fixture.workPrincipal,
		"delegated":    fixture.principal.WithActAs(model.NewID()),
	} {
		t.Run(name, func(t *testing.T) {
			if got, mapErr := communicationPrincipalFromResolvedAuth(malformed); !errors.Is(mapErr, ErrCommunicationEvidenceUnknown) || got != (CommunicationPrincipal{}) {
				t.Fatalf("unsupported principal = %#v error=%v, want UNKNOWN", got, mapErr)
			}
		})
	}
}
