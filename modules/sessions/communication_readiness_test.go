// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type communicationReadinessStub struct {
	storeReady  bool
	sealerReady bool
	pumpReady   bool
	storeErr    error
	sealerErr   error
	pumpErr     error
	sealerCalls int
	cryptoCalls int
	trace       *[]string
	sealerCtx   context.Context
	useCtxErr   bool
}

func (s *communicationReadinessStub) CommunicationStoreReady(context.Context) (bool, error) {
	s.recordReadinessCall("store")
	return s.storeReady, s.storeErr
}

func (s *communicationReadinessStub) CommunicationContentSealerReady(
	ctx context.Context,
) (bool, error) {
	s.recordReadinessCall("sealer")
	s.sealerCalls++
	s.sealerCtx = ctx
	if s.useCtxErr && ctx.Err() != nil {
		return false, ctx.Err()
	}
	return s.sealerReady, s.sealerErr
}

func (s *communicationReadinessStub) CommunicationPumpReady(context.Context) (bool, error) {
	s.recordReadinessCall("pump")
	return s.pumpReady, s.pumpErr
}

func (s *communicationReadinessStub) recordReadinessCall(name string) {
	if s.trace != nil {
		*s.trace = append(*s.trace, name)
	}
}

func (s *communicationReadinessStub) Seal(context.Context, ContentAAD, []byte) ([]byte, string, error) {
	s.cryptoCalls++
	return nil, "seal-v1", nil
}

func (s *communicationReadinessStub) Open(context.Context, ContentAAD, []byte, string) ([]byte, error) {
	s.cryptoCalls++
	return nil, nil
}

func (s *communicationReadinessStub) Digest(context.Context, ContentAAD, []byte) ([]byte, string, error) {
	s.cryptoCalls++
	return nil, "digest-v1", nil
}

func (s *communicationReadinessStub) VerifyDigest(
	context.Context, ContentAAD, []byte, []byte, string,
) (bool, error) {
	s.cryptoCalls++
	return true, nil
}

type communicationSealerPortOnly struct {
	cryptoCalls int
}

func (s *communicationSealerPortOnly) Seal(
	context.Context, ContentAAD, []byte,
) ([]byte, string, error) {
	s.cryptoCalls++
	return nil, "seal-v1", nil
}

func (s *communicationSealerPortOnly) Open(
	context.Context, ContentAAD, []byte, string,
) ([]byte, error) {
	s.cryptoCalls++
	return nil, nil
}

func (s *communicationSealerPortOnly) Digest(
	context.Context, ContentAAD, []byte,
) ([]byte, string, error) {
	s.cryptoCalls++
	return nil, "digest-v1", nil
}

func (s *communicationSealerPortOnly) VerifyDigest(
	context.Context, ContentAAD, []byte, []byte, string,
) (bool, error) {
	s.cryptoCalls++
	return true, nil
}

func (*communicationReadinessStub) ResolveAudience(
	context.Context, DirectoryScopeRef, []AudienceSelector,
) (DirectorySnapshot, error) {
	return DirectorySnapshot{}, nil
}

func (*communicationReadinessStub) ResolveRecipient(
	context.Context, DirectoryScopeRef, RecipientRef,
) (RecipientSnapshot, error) {
	return RecipientSnapshot{}, nil
}

func (*communicationReadinessStub) ResolvePrincipal(
	context.Context, DirectoryScopeRef, CommunicationPrincipal,
) (PrincipalResolution, error) {
	return PrincipalResolution{}, nil
}

func (*communicationReadinessStub) AttestPublicationAudience(
	context.Context, PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	return DirectorySnapshot{}, PublicationAudienceAttestation{}, nil
}

func (*communicationReadinessStub) ResolveChannelGrantSubjects(
	context.Context, DirectoryScopeRef, CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	return ChannelGrantSubjectClosure{}, nil
}

func (*communicationReadinessStub) AuthorizeEntityRead(
	context.Context, CommunicationPrincipal, EntityRef,
) (ReadWitness, error) {
	return ReadWitness{}, nil
}

func (*communicationReadinessStub) AuthorizeEntityOperation(
	context.Context, CommunicationPrincipal, EntityRef, CommunicationOperation,
) (ReadWitness, error) {
	return ReadWitness{}, nil
}

func (*communicationReadinessStub) Mint(
	context.Context, CommunicationSessionCredentialRequest,
) (CommunicationSessionCredential, error) {
	return CommunicationSessionCredential{}, nil
}

func (*communicationReadinessStub) Renew(
	context.Context, model.ID, CommunicationSessionCredentialRequest,
) (time.Time, error) {
	return time.Time{}, nil
}

func (*communicationReadinessStub) Revoke(
	context.Context, model.ID, CommunicationSessionCredentialRequest,
) error {
	return nil
}

func allCommunicationReadinessComponents() CommunicationReadinessComponents {
	return CommunicationReadinessComponents{
		StoreReady:       true,
		IssuerReady:      true,
		SealerReady:      true,
		ResolverReady:    true,
		PermissionsReady: true,
		PumpReady:        true,
	}
}

func setCommunicationReadinessComponent(
	components *CommunicationReadinessComponents,
	dependency CommunicationReadinessDependency,
	ready bool,
) {
	switch dependency {
	case CommunicationReadinessStore:
		components.StoreReady = ready
	case CommunicationReadinessIssuer:
		components.IssuerReady = ready
	case CommunicationReadinessSealer:
		components.SealerReady = ready
	case CommunicationReadinessResolver:
		components.ResolverReady = ready
	case CommunicationReadinessPermissions:
		components.PermissionsReady = ready
	case CommunicationReadinessPump:
		components.PumpReady = ready
	}
}

func TestCommunicationReadinessRequiresEveryConjunct(t *testing.T) {
	t.Parallel()

	all := evaluateCommunicationReadiness(allCommunicationReadinessComponents())
	if !all.Effective || !all.StoreReady || !all.CompositionReady ||
		all.Verdict != VerdictClean || all.Code != "communication_ready" || len(all.Missing) != 0 {
		t.Fatalf("all-ready result = %+v", all)
	}

	for _, missing := range communicationReadinessDependencyOrder {
		missing := missing
		t.Run(string(missing), func(t *testing.T) {
			t.Parallel()
			components := allCommunicationReadinessComponents()
			setCommunicationReadinessComponent(&components, missing, false)
			got := evaluateCommunicationReadiness(components)
			if got.Effective || got.Verdict != VerdictUnknown || got.Code != "communication_not_ready" {
				t.Fatalf("result with %s absent = %+v", missing, got)
			}
			if !reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{missing}) {
				t.Fatalf("missing with %s absent = %v", missing, got.Missing)
			}
			if got.StoreReady != (missing != CommunicationReadinessStore) {
				t.Fatalf("store_ready with %s absent = %t", missing, got.StoreReady)
			}
			wantComposition := missing == CommunicationReadinessStore
			if got.CompositionReady != wantComposition {
				t.Fatalf("composition_ready with %s absent = %t, want %t",
					missing, got.CompositionReady, wantComposition)
			}
		})
	}
}

func wireCommunicationReadiness(m *Module, stub *communicationReadinessStub, includePump bool) {
	m.UseCommunicationSessionCredentialSource(stub)
	m.UseCommunicationContentSealer(stub)
	m.UseCommunicationDirectorySnapshotResolver(stub)
	m.UseCommunicationPublicationAudienceAttestor(stub)
	m.UseCommunicationChannelGrantSubjectClosureResolver(stub)
	m.UseCommunicationCoreEntityReadAuthorizer(stub)
	m.UseCommunicationCoreEntityOperationAuthorizer(stub)
	m.UseCommunicationStoreReadinessWitness(stub)
	// PermissionsReady is the direct binder since 2026-08-26; without this every
	// readiness test using this helper would fail for the wrong reason.
	m.useCommunicationRequestAuthoritySources(
		&communicationAuthorityResolverRecorder{},
		&communicationAuthoritySourceRecorder{},
	)
	if includePump {
		m.UseCommunicationPumpReadinessWitness(stub)
	}
}

func TestCommunicationReadinessBindingStaysOffUntilWP3AndNeverEnablesCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	module := New()
	initial, err := module.EvaluateCommunicationReadiness(ctx)
	if err != nil || initial.Effective || initial.Verdict != VerdictUnknown ||
		initial.Components.PermissionsReady {
		t.Fatalf("default readiness = %+v, err %v", initial, err)
	}

	issuerOnly := &communicationReadinessStub{
		storeReady: true, sealerReady: true, pumpReady: true,
	}
	module.UseCommunicationSessionCredentialSource(issuerOnly)
	got, err := module.EvaluateCommunicationReadiness(ctx)
	if err != nil || got.Effective || !got.Components.IssuerReady || got.StoreReady {
		t.Fatalf("issuer-only readiness = %+v, err %v", got, err)
	}

	storeOnly := New()
	storeOnly.UseCommunicationStoreReadinessWitness(issuerOnly)
	got, err = storeOnly.EvaluateCommunicationReadiness(ctx)
	if err != nil || got.Effective || !got.StoreReady || got.CompositionReady {
		t.Fatalf("store-only readiness = %+v, err %v", got, err)
	}

	wp2 := New()
	wireCommunicationReadiness(wp2, issuerOnly, false)
	got, err = wp2.EvaluateCommunicationReadiness(ctx)
	if err != nil || got.Effective || got.CompositionReady ||
		!reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{CommunicationReadinessPump}) {
		t.Fatalf("WP-2 readiness without pump = %+v, err %v", got, err)
	}
	if wp2.CommunicationSessionCredentialsEnabled() {
		t.Fatal("WP-2 readiness binding enabled communication-session credentials")
	}

	wp2.UseCommunicationPumpReadinessWitness(issuerOnly)
	got, err = wp2.EvaluateCommunicationReadiness(ctx)
	if err != nil || !got.Effective || got.Verdict != VerdictClean {
		t.Fatalf("complete readiness = %+v, err %v", got, err)
	}
	if wp2.CommunicationSessionCredentialsEnabled() {
		t.Fatal("readiness evaluation enabled communication-session credentials")
	}

	var typedNil *communicationReadinessStub
	wp2.UseCommunicationContentSealer(typedNil)
	got, err = wp2.EvaluateCommunicationReadiness(ctx)
	if err != nil || got.Effective || got.Components.SealerReady {
		t.Fatalf("typed-nil sealer readiness = %+v, err %v", got, err)
	}
}

func TestCommunicationReadinessGroupsRequiredPortsUnderCanonicalTerms(t *testing.T) {
	t.Parallel()
	var typedNil *communicationReadinessStub
	tests := []struct {
		name    string
		remove  func(*Module)
		missing CommunicationReadinessDependency
	}{
		{
			name: "issuer", missing: CommunicationReadinessIssuer,
			remove: func(m *Module) { m.rt.communicationSessionCreds = typedNil },
		},
		{
			name: "sealer", missing: CommunicationReadinessSealer,
			remove: func(m *Module) { m.communicationSealer = typedNil },
		},
		{
			name: "directory resolver", missing: CommunicationReadinessResolver,
			remove: func(m *Module) { m.communicationDirectoryResolver = typedNil },
		},
		{
			name: "audience attestor", missing: CommunicationReadinessResolver,
			remove: func(m *Module) { m.communicationAudienceAttestor = typedNil },
		},
		{
			name: "grant closure", missing: CommunicationReadinessResolver,
			remove: func(m *Module) { m.communicationGrantClosure = typedNil },
		},
		{
			// The two CoreEntity* ports left this table on 2026-08-26: they are an
			// optional PDP seam, not a readiness term. Removing the rows without
			// adding this one would leave the term with NO coverage at all -- a
			// control that goes silent instead of failing.
			name: "request authority bundle", missing: CommunicationReadinessPermissions,
			remove: func(m *Module) { m.communicationAuthoritySources = nil },
		},
		{
			name: "store witness", missing: CommunicationReadinessStore,
			remove: func(m *Module) { m.communicationStoreReadiness = typedNil },
		},
		{
			name: "pump witness", missing: CommunicationReadinessPump,
			remove: func(m *Module) { m.communicationPumpReadiness = typedNil },
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stub := &communicationReadinessStub{
				storeReady: true, sealerReady: true, pumpReady: true,
			}
			module := New()
			wireCommunicationReadiness(module, stub, true)
			test.remove(module)
			got, err := module.EvaluateCommunicationReadiness(context.Background())
			if err != nil || got.Effective ||
				!reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{test.missing}) {
				t.Fatalf("readiness = %+v, err %v", got, err)
			}
		})
	}
}

func TestCommunicationReadinessSamplesDynamicWitnessValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		storeReady  bool
		sealerReady bool
		pumpReady   bool
		missing     CommunicationReadinessDependency
	}{
		{
			name: "store false", sealerReady: true, pumpReady: true,
			missing: CommunicationReadinessStore,
		},
		{
			name: "sealer false", storeReady: true, pumpReady: true,
			missing: CommunicationReadinessSealer,
		},
		{
			name: "pump false", storeReady: true, sealerReady: true,
			missing: CommunicationReadinessPump,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stub := &communicationReadinessStub{
				storeReady:  test.storeReady,
				sealerReady: test.sealerReady,
				pumpReady:   test.pumpReady,
			}
			module := New()
			wireCommunicationReadiness(module, stub, true)
			got, err := module.EvaluateCommunicationReadiness(context.Background())
			if err != nil || got.Effective ||
				!reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{test.missing}) {
				t.Fatalf("readiness = %+v, err %v", got, err)
			}
		})
	}
}

func TestCommunicationReadinessWitnessFailuresAreUnknown(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("store witness unavailable")
	sealerErr := errors.New("sealer witness unavailable")
	pumpErr := errors.New("pump witness unavailable")
	var trace []string
	stub := &communicationReadinessStub{
		storeReady:  true,
		sealerReady: true,
		pumpReady:   true,
		storeErr:    storeErr,
		sealerErr:   sealerErr,
		pumpErr:     pumpErr,
		trace:       &trace,
	}
	module := New()
	wireCommunicationReadiness(module, stub, true)

	got, err := module.EvaluateCommunicationReadiness(context.Background())
	if got.Effective || got.Verdict != VerdictUnknown ||
		got.Code != "communication_readiness_unavailable" {
		t.Fatalf("failed-witness readiness = %+v", got)
	}
	if !reflect.DeepEqual(got.Unavailable, []CommunicationReadinessDependency{
		CommunicationReadinessStore, CommunicationReadinessSealer, CommunicationReadinessPump,
	}) {
		t.Fatalf("unavailable = %v", got.Unavailable)
	}
	if !errors.Is(err, storeErr) || !errors.Is(err, sealerErr) || !errors.Is(err, pumpErr) {
		t.Fatalf("witness error = %v", err)
	}
	if !reflect.DeepEqual(trace, []string{"store", "sealer", "pump"}) {
		t.Fatalf("witness sampling order = %v", trace)
	}
}

func TestCommunicationReadinessSamplesSealerWitnessExactlyOnce(t *testing.T) {
	t.Parallel()
	sealerErr := errors.New("sealer self-test unavailable")
	tests := []struct {
		name            string
		ready           bool
		err             error
		wantEffective   bool
		wantUnavailable bool
	}{
		{name: "ready", ready: true, wantEffective: true},
		{name: "not ready"},
		{name: "true with error", ready: true, err: sealerErr, wantUnavailable: true},
		{name: "false with error", err: sealerErr, wantUnavailable: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stub := &communicationReadinessStub{
				storeReady:  true,
				sealerReady: test.ready,
				pumpReady:   true,
				sealerErr:   test.err,
			}
			module := New()
			wireCommunicationReadiness(module, stub, true)

			got, err := module.EvaluateCommunicationReadiness(context.Background())
			if got.Effective != test.wantEffective || got.Components.SealerReady != test.wantEffective {
				t.Fatalf("readiness = %+v, want effective %t", got, test.wantEffective)
			}
			if stub.sealerCalls != 1 || stub.cryptoCalls != 0 {
				t.Fatalf("sealer calls = readiness %d, crypto %d", stub.sealerCalls, stub.cryptoCalls)
			}
			if test.wantEffective {
				if err != nil || len(got.Missing) != 0 || len(got.Unavailable) != 0 {
					t.Fatalf("ready result = %+v, err %v", got, err)
				}
				return
			}
			if !reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{
				CommunicationReadinessSealer,
			}) {
				t.Fatalf("missing = %v", got.Missing)
			}
			if test.wantUnavailable {
				if got.Code != "communication_readiness_unavailable" ||
					!reflect.DeepEqual(got.Unavailable, []CommunicationReadinessDependency{
						CommunicationReadinessSealer,
					}) || !errors.Is(err, sealerErr) {
					t.Fatalf("unavailable result = %+v, err %v", got, err)
				}
			} else if err != nil || got.Code != "communication_not_ready" ||
				len(got.Unavailable) != 0 {
				t.Fatalf("not-ready result = %+v, err %v", got, err)
			}
			if module.CommunicationSessionCredentialsEnabled() {
				t.Fatal("readiness sampling enabled communication-session credentials")
			}
		})
	}
}

func TestCommunicationReadinessPassesCallerContextToSealerWitness(t *testing.T) {
	t.Parallel()
	stub := &communicationReadinessStub{
		storeReady: true, sealerReady: true, pumpReady: true, useCtxErr: true,
	}
	module := New()
	wireCommunicationReadiness(module, stub, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := module.EvaluateCommunicationReadiness(ctx)
	if got.Effective || got.Components.SealerReady ||
		!reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{
			CommunicationReadinessSealer,
		}) || !reflect.DeepEqual(got.Unavailable, []CommunicationReadinessDependency{
		CommunicationReadinessSealer,
	}) || got.Code != "communication_readiness_unavailable" {
		t.Fatalf("canceled-context readiness = %+v", got)
	}
	if !errors.Is(err, context.Canceled) || stub.sealerCtx != ctx || stub.sealerCalls != 1 {
		t.Fatalf("canceled-context witness = ctx %v, calls %d, err %v",
			stub.sealerCtx == ctx, stub.sealerCalls, err)
	}
}

func TestCommunicationReadinessRequiresWitnessFromBoundSealer(t *testing.T) {
	t.Parallel()
	stub := &communicationReadinessStub{
		storeReady: true, sealerReady: true, pumpReady: true,
	}
	module := New()
	wireCommunicationReadiness(module, stub, true)
	portOnly := &communicationSealerPortOnly{}
	module.UseCommunicationContentSealer(portOnly)
	if module.communicationSealer != portOnly {
		t.Fatal("port-only sealer was not retained as the content port")
	}

	got, err := module.EvaluateCommunicationReadiness(context.Background())
	if err != nil || got.Effective || got.Components.SealerReady ||
		!reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{
			CommunicationReadinessSealer,
		}) || len(got.Unavailable) != 0 {
		t.Fatalf("port-only readiness = %+v, err %v", got, err)
	}
	if stub.sealerCalls != 0 || portOnly.cryptoCalls != 0 {
		t.Fatalf("port-only calls = old witness %d, crypto %d", stub.sealerCalls, portOnly.cryptoCalls)
	}
}

func TestCommunicationContentSealerRebindAndClearDropsPriorWitness(t *testing.T) {
	t.Parallel()
	ready := &communicationReadinessStub{
		storeReady: true, sealerReady: true, pumpReady: true,
	}
	module := New()
	wireCommunicationReadiness(module, ready, true)
	got, err := module.EvaluateCommunicationReadiness(context.Background())
	if err != nil || !got.Effective || ready.sealerCalls != 1 {
		t.Fatalf("initial readiness = %+v, calls %d, err %v", got, ready.sealerCalls, err)
	}

	notReady := &communicationReadinessStub{}
	module.UseCommunicationContentSealer(notReady)
	got, err = module.EvaluateCommunicationReadiness(context.Background())
	if err != nil || got.Effective || got.Components.SealerReady ||
		ready.sealerCalls != 1 || notReady.sealerCalls != 1 {
		t.Fatalf("rebound readiness = %+v, old %d, new %d, err %v",
			got, ready.sealerCalls, notReady.sealerCalls, err)
	}

	portOnly := &communicationSealerPortOnly{}
	module.UseCommunicationContentSealer(portOnly)
	got, err = module.EvaluateCommunicationReadiness(context.Background())
	if err != nil || got.Effective || got.Components.SealerReady || portOnly.cryptoCalls != 0 ||
		notReady.sealerCalls != 1 {
		t.Fatalf("port-only rebind = %+v, crypto %d, prior %d, err %v",
			got, portOnly.cryptoCalls, notReady.sealerCalls, err)
	}

	module.UseCommunicationContentSealer(nil)
	if module.communicationSealer != nil {
		t.Fatal("nil did not clear the bound sealer")
	}
	var typedNil *communicationReadinessStub
	module.UseCommunicationContentSealer(typedNil)
	if module.communicationSealer != nil {
		t.Fatal("typed nil did not canonicalize to an unbound sealer")
	}
	got, err = module.EvaluateCommunicationReadiness(context.Background())
	if err != nil || got.Effective || got.Components.SealerReady ||
		!reflect.DeepEqual(got.Missing, []CommunicationReadinessDependency{
			CommunicationReadinessSealer,
		}) {
		t.Fatalf("cleared readiness = %+v, err %v", got, err)
	}
	if module.CommunicationSessionCredentialsEnabled() {
		t.Fatal("sealer rebind enabled communication-session credentials")
	}
}

func TestCommunicationPermissionCatalogIsExact(t *testing.T) {
	t.Parallel()
	want := []auth.Permission{
		"sessions:channel:read", "sessions:channel:write", "sessions:channel:admin",
		"sessions:message:read", "sessions:message:write", "sessions:message:admin",
		auth.CommunicationSessionMessageSendWrite,
		auth.CommunicationSessionDeliveryRead, auth.CommunicationSessionDeliveryWrite,
		"sessions:delivery:admin",
		"sessions:decision-request:read", "sessions:decision-request:write",
		"sessions:decision-request:admin",
		"sessions:handoff:read", "sessions:handoff:write", "sessions:handoff:admin",
		auth.CommunicationSessionHandoffResponseWrite,
		"sessions:route:read", "sessions:route:write", "sessions:route:admin",
		"sessions:subscription:read", "sessions:subscription:write",
		"sessions:subscription:admin",
		"sessions:endpoint:read", "sessions:endpoint:write", "sessions:endpoint:admin",
	}
	if got := communicationPermissions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("communication permission catalog = %v, want %v", got, want)
	}

	declared := New().Permissions()
	if !communicationPermissionsReady(declared) {
		t.Fatal("exact module catalog is not permissions-ready")
	}
	for _, omitted := range want {
		without := make([]auth.Permission, 0, len(declared)-1)
		for _, permission := range declared {
			if permission != omitted {
				without = append(without, permission)
			}
		}
		if communicationPermissionsReady(without) {
			t.Fatalf("catalog remained ready without %q", omitted)
		}
	}
	if communicationPermissionsReady(append(append([]auth.Permission(nil), declared...), want[0])) {
		t.Fatal("catalog with a duplicate permission reported ready")
	}
	if communicationPermissionsReady(append(append([]auth.Permission(nil), declared...),
		auth.Permission("sessions:channel:delete"))) {
		t.Fatal("catalog with an invented K3 verb reported ready")
	}
	if !communicationPermissionsReady(append(append([]auth.Permission(nil), declared...),
		auth.Permission("sessions:protocol-binding:read"))) {
		t.Fatal("an additive K5 resource disabled K3 permission readiness")
	}
	if !communicationPermissionsReady(append(append([]auth.Permission(nil), declared...),
		auth.Permission("sessions:future:read"))) {
		t.Fatal("an unrelated sessions permission changed K3 readiness")
	}

	copyOfCatalog := communicationPermissions()
	copyOfCatalog[0] = "sessions:channel:corrupted"
	if reflect.DeepEqual(copyOfCatalog, communicationPermissions()) {
		t.Fatal("communicationPermissions returned mutable shared storage")
	}
}

// TestCommunicationPermissionsTermWatchesTheBinderAndNotTheTwoPorts is the defect
// that ALREADY HAPPENED, not a property imagined after the fact: until 2026-08-26
// PermissionsReady required communicationReadAuthorizer and
// communicationOperationAuthorizer -- two ports that receive a
// CommunicationPrincipal (identity alone: no role, no membership, no AAL, no
// CredID) and therefore CANNOT authorize without rebuilding a principal with no
// roles (denies everything) or inventing its attributes (grants too much) --
// while the direct binder, which resolves the real principal from its
// PrincipalRef, was not a readiness term at all. The gate certified the wrong
// machine.
//
// Both directions fail against the OLD conjunction, each for its own reason, and
// that is what makes them discriminating rather than decorative.
func TestCommunicationPermissionsTermWatchesTheBinderAndNotTheTwoPorts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newWired := func() *Module {
		stub := &communicationReadinessStub{storeReady: true, sealerReady: true, pumpReady: true}
		m := New()
		wireCommunicationReadiness(m, stub, true)
		return m
	}

	// Direction 1 -- binder bound, BOTH ports absent: the term is ready.
	// The old conjunction answered false here, because it demanded the ports.
	t.Run("binder bound and both ports absent is ready", func(t *testing.T) {
		t.Parallel()
		m := newWired()
		m.communicationReadAuthorizer = nil
		m.communicationOperationAuthorizer = nil
		got, err := m.EvaluateCommunicationReadiness(ctx)
		if err != nil {
			t.Fatalf("NO HE PODIDO MIRAR: %v", err)
		}
		if !got.Components.PermissionsReady || !got.Effective {
			t.Fatalf("PermissionsReady=%v Effective=%v missing=%v; the term must rest on "+
				"the binder, not on two ports that cannot authorize",
				got.Components.PermissionsReady, got.Effective, got.Missing)
		}
	})

	// Direction 2 -- both ports bound, binder absent: the term is NOT ready.
	// The old conjunction answered true here, which is exactly the false green.
	t.Run("both ports bound but binder absent is not ready", func(t *testing.T) {
		t.Parallel()
		m := newWired()
		m.communicationAuthoritySources = nil
		got, err := m.EvaluateCommunicationReadiness(ctx)
		if err != nil {
			t.Fatalf("NO HE PODIDO MIRAR: %v", err)
		}
		if got.Components.PermissionsReady || got.Effective {
			t.Fatalf("PermissionsReady=%v Effective=%v; with no binder there is no faithful "+
				"authority and the gate CANNOT say yes",
				got.Components.PermissionsReady, got.Effective)
		}
		want := []CommunicationReadinessDependency{CommunicationReadinessPermissions}
		if !reflect.DeepEqual(got.Missing, want) {
			t.Fatalf("missing = %v, want %v", got.Missing, want)
		}
	})

	// Negative control: half a bundle is NOT a bundle. The absent half stores nil
	// by construction, and this branch proves that instead of trusting the comment
	// which promises it.
	t.Run("half a bundle is not a bundle", func(t *testing.T) {
		t.Parallel()
		for _, half := range []struct {
			name     string
			resolver communicationPrincipalAuthorityResolver
			source   communicationAuthorizationEvidenceSource
		}{
			{"resolver only", &communicationAuthorityResolverRecorder{}, nil},
			{"source only", nil, &communicationAuthoritySourceRecorder{}},
		} {
			half := half
			t.Run(half.name, func(t *testing.T) {
				t.Parallel()
				m := newWired()
				m.useCommunicationRequestAuthoritySources(half.resolver, half.source)
				got, err := m.EvaluateCommunicationReadiness(ctx)
				if err != nil {
					t.Fatalf("NO HE PODIDO MIRAR: %v", err)
				}
				if got.Components.PermissionsReady {
					t.Fatalf("half a binding (%s) counted as bound: PermissionsReady=true", half.name)
				}
			})
		}
	})
}
