// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestLaunchReadiness_HasNoEffects is the purity row: the read is driven with
// every effectful seam WIRED and instrumented to fail on contact, and the WHOLE
// database is censused before and after.
func TestLaunchReadiness_HasNoEffects(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			runner := newInspectingRunner(t)
			m := New(
				WithRunner(runner),
				WithProgram(bins.present),
				// Every one of these fails the test if the read touches it. They are
				// WIRED rather than absent on purpose: an unwired seam proves nothing,
				// because the read would skip it for the wrong reason.
				WithCredentialSource(mintRefusingCredentialSource{t}),
				WithProviderDriver(NewCodexDriver()), WithDriverProgram("codex", bins.present),
				WithProviderDriver(NewOpenCodeDriver()), WithDriverProgram("opencode", bins.present),
				WithProviderCredentialSource("codex", mintRefusingProviderSource{t}),
				WithLaunchGate(refusingLaunchGate{t}),
				WithStopGate(refusingStopGate{t}),
				WithProviderApprovalGate(refusingApprovalGate{t}),
			)
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)
			cfgHome, userHome := readinessHomes(t)

			// A login file and an unreadable subdirectory INSIDE the config home. The
			// plane must never open the first or descend into the second: a home is
			// storage identity, and what is inside it is the provider's business.
			if err := os.WriteFile(filepath.Join(cfgHome, ".credentials.json"),
				[]byte(`{"access_token":"SENTINEL-LOGIN-VALUE"}`), 0o000); err != nil {
				t.Fatal(err)
			}
			sealed := filepath.Join(cfgHome, "sealed")
			if err := os.MkdirAll(sealed, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sealed, "token"), []byte("SENTINEL-LOGIN-VALUE"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(sealed, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(sealed, 0o700) })

			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceManagedInjection)
			codexRef := f.createProfile("codex", mustHome(t), mustHome(t), AuthSourceManagedInjection)
			openCodeRef := f.createProfile("opencode", mustHome(t), mustHome(t), AuthSourceAccountHome)

			before := tableCounts(t, be)
			for _, target := range []string{ref, codexRef, openCodeRef} {
				for _, query := range []string{"", "transport=remote-control", "isolation=container"} {
					doc, r := f.readiness(target, query)
					if r.code != http.StatusOK {
						t.Fatalf("readiness(%s, %q) = %d %s", target, query, r.code, r.raw)
					}
					if strings.Contains(r.raw, "SENTINEL-LOGIN-VALUE") {
						t.Fatalf("the response carries login material: %s", r.raw)
					}
					if doc.ProfileRef != target {
						t.Fatalf("readiness answered about %q when asked about %q", doc.ProfileRef, target)
					}
				}
			}
			after := tableCounts(t, be)
			if diff := diffCounts(before, after); len(diff) != 0 {
				t.Fatalf("the read changed the store: %v", diff)
			}
			if runner.inspections() == 0 {
				t.Fatal("the Runner was never inspected, so this purity run proved nothing about it")
			}
			// The unreadable login is still exactly as it was: an inspection that had
			// opened it would have had to widen its mode to succeed.
			if st, err := os.Stat(filepath.Join(cfgHome, ".credentials.json")); err != nil || st.Mode().Perm() != 0 {
				t.Fatalf("the login file changed: %v %v", st, err)
			}
			// ⛔ AND THE UNREADABLE HOME CONTENT DID NOT MAKE THE HOME UNKNOWN. The
			// home itself resolves, so homes is ready; a check that listed or read
			// inside it would have reported inspection_unavailable here.
			doc, _ := f.readiness(ref, "")
			doc.wantCheck(t, CheckHomes, ReadinessReady, codeHomesResolved)
		})
	}
}

// refusingApprovalGate fails if the read consults provider approval authority.
type refusingApprovalGate struct{ t *testing.T }

func (g refusingApprovalGate) Approve(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
	g.t.Error("launch readiness consulted the provider approval authority")
	return ProviderApprovalDecision{}, errors.New("readiness must not ask for approval")
}

func mustHome(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// TestLaunchReadiness_PermissionsAndConfidentiality is the authorization row: a
// principal without the profile read is refused BEFORE anything is inspected, a
// profile of another tenant is indistinguishable from an absent one, and a
// reader who cannot launch can still see what a launch would require.
func TestLaunchReadiness_PermissionsAndConfidentiality(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			runner := newInspectingRunner(t)
			m := New(WithRunner(runner), WithProgram(bins.present),
				WithCredentialSource(mintRefusingCredentialSource{t}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

			// Unauthenticated, and a principal with no standing in this tenant. Both
			// are refused by the router, so the filesystem is never touched.
			inspectionsBefore := runner.inspections()
			if _, r := f.readinessAs("", f.tenant, ref, ""); r.code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated = %d %s", r.code, r.raw)
			}
			if _, r := f.readinessAs(f.viewer, f.other, ref, ""); r.code != http.StatusForbidden {
				t.Fatalf("without profile:read in that tenant = %d %s", r.code, r.raw)
			}
			if runner.inspections() != inspectionsBefore {
				t.Fatal("a refused request still inspected the node: authorization must come first")
			}

			// A profile of another tenant is a 404 and says nothing about it. The
			// admin CAN act in both tenants, so this is the interesting case: the
			// scope, not the principal, is what makes the profile invisible.
			if _, r := f.readinessAs(f.admin, f.other, ref, ""); r.code != http.StatusNotFound {
				t.Fatalf("cross-tenant = %d %s", r.code, r.raw)
			}

			// The viewer holds profile:read and NOT run:write: it sees the
			// requirements and is told, in the document itself, what launching needs.
			doc, r := f.readiness(ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("viewer read = %d %s", r.code, r.raw)
			}
			if doc.LaunchAuthorization.RequiredPermission != string(permRunWrite) {
				t.Fatalf("required_permission = %q", doc.LaunchAuthorization.RequiredPermission)
			}
			// ⛔ AND THE OTHER STATEMENT MUST NOT CARRY IT AT ALL. The published
			// schema declares provider_authentication a closed object without that
			// property, so a body that sent one — an empty string included — would
			// violate the contract this route publishes. There is no permission that
			// would make a provider authenticated.
			if auth, ok := r.body["provider_authentication"].(map[string]any); !ok {
				t.Fatalf("no provider_authentication in the body: %s", r.raw)
			} else if _, leaked := auth["required_permission"]; leaked {
				t.Fatalf("provider_authentication carries required_permission: %s", r.raw)
			}
			if r := f.h.doJSON("POST", "/v1/m/sessions/runs", f.viewer,
				map[string]any{"provider_profile_ref": ref}, tenantHdr(f.tenant)); r.code != http.StatusForbidden {
				t.Fatalf("the viewer could launch after reading requirements: %d %s", r.code, r.raw)
			}
			if got := r.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store: a dated observation is not reusable", got)
			}
			// No path, no environment value, no argv anywhere in the document.
			for _, secret := range []string{cfgHome, userHome, bins.present, bins.dir, "CLAUDE_CONFIG_DIR", "ANTHROPIC"} {
				if strings.Contains(r.raw, secret) {
					t.Fatalf("the document carries %q: %s", secret, r.raw)
				}
			}
		})
	}
}

// TestLaunchReadiness_SafeErrorsFromAHostileInspector proves the failure path is
// redacted at the seam: a Runner whose error carries a path, an argv and a token
// yields `unknown` and a response with none of them.
func TestLaunchReadiness_SafeErrorsFromAHostileInspector(t *testing.T) {
	be := readinessEngines(t)[0]
	leak := "/very/secret/path --token sk-ant-LEAKED"
	m := New(WithRunner(errorInspector{err: errors.New("inspect " + leak)}),
		WithProgram("claude"), WithCredentialSource(mintRefusingCredentialSource{t}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	f := newReadinessFixture(t, be, m)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

	doc, r := f.readiness(ref, "")
	if r.code != http.StatusOK {
		t.Fatalf("= %d %s", r.code, r.raw)
	}
	doc.wantCheck(t, CheckRunner, ReadinessUnknown, codeInspectionUnavailable)
	doc.wantCheck(t, CheckProgram, ReadinessUnknown, codeInspectionUnavailable)
	if doc.ConfigurationState != ReadinessUnknown {
		t.Fatalf("configuration_state = %q, want unknown", doc.ConfigurationState)
	}
	for _, fragment := range []string{"/very/secret/path", "sk-ant-LEAKED", "--token"} {
		if strings.Contains(r.raw, fragment) {
			t.Fatalf("the raw inspector error reached the response: %s", r.raw)
		}
	}
}

type errorInspector struct{ err error }

func (errorInspector) Launch(context.Context, LaunchSpec) (Process, error) {
	return nil, errors.New("not used")
}

func (e errorInspector) InspectLaunch(context.Context, RunnerInspection) (RunnerObservation, error) {
	return RunnerObservation{}, e.err
}

// TestLaunchReadiness_RunnerWithoutTheOptionalSeam is the extensibility row: a
// Runner that never heard of the inspection seam keeps working, is not required
// to change, and yields unknown — never "not installed" and never ready.
func TestLaunchReadiness_RunnerWithoutTheOptionalSeam(t *testing.T) {
	be := readinessEngines(t)[0]
	m := New(WithRunner(plainRunner{}), WithProgram("claude"),
		WithCredentialSource(mintRefusingCredentialSource{t}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	f := newReadinessFixture(t, be, m)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

	doc, r := f.readiness(ref, "")
	if r.code != http.StatusOK {
		t.Fatalf("= %d %s", r.code, r.raw)
	}
	doc.wantCheck(t, CheckRunner, ReadinessUnknown, codeRunnerInspectionUnavailable)
	doc.wantCheck(t, CheckProgram, ReadinessUnknown, codeInspectionUnavailable)
	if doc.ConfigurationState != ReadinessUnknown {
		t.Fatalf("configuration_state = %q: an uninspectable Runner is uncertainty, not a verdict", doc.ConfigurationState)
	}
	// Everything the node CAN answer is still answered, so the console can show a
	// valid Runner as incompletely checked rather than as unusable.
	doc.wantCheck(t, CheckDriver, ReadinessReady, codeDriverOperable)
	doc.wantCheck(t, CheckHomes, ReadinessReady, codeHomesResolved)
}

// plainRunner implements ONLY Runner — the shape every external runner has.
type plainRunner struct{}

func (plainRunner) Launch(context.Context, LaunchSpec) (Process, error) {
	return nil, errors.New("not used")
}

// TestLaunchReadiness_NoRunnerWired distinguishes an absent Runner from an
// uninspectable one: the first is a known wiring gap, the second is uncertainty.
func TestLaunchReadiness_NoRunnerWired(t *testing.T) {
	be := readinessEngines(t)[0]
	m := New(WithProgram("claude"), WithCredentialSource(mintRefusingCredentialSource{t}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	f := newReadinessFixture(t, be, m)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

	doc, r := f.readiness(ref, "")
	if r.code != http.StatusOK {
		t.Fatalf("= %d %s", r.code, r.raw)
	}
	doc.wantCheck(t, CheckRunner, ReadinessNotConfigured, codeRunnerNotConfigured)
	// The binary is not blamed for a wiring absence nobody asked it about.
	doc.wantCheck(t, CheckProgram, ReadinessUnknown, codeNotCheckedInThisEnvironment)
	if doc.ConfigurationState != ReadinessNotConfigured {
		t.Fatalf("configuration_state = %q", doc.ConfigurationState)
	}
}

// TestLaunchReadiness_IsolationUnsupported is the reserved-value row: container
// and sandbox are KNOWN values, so they are a 200 with an unsupported check
// rather than a 400, and the native runner never pretends to honor them.
func TestLaunchReadiness_IsolationUnsupported(t *testing.T) {
	be := readinessEngines(t)[0]
	bins := newReadinessProgramFixtures(t)
	m := New(WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
		WithCredentialSource(mintRefusingCredentialSource{t}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	f := newReadinessFixture(t, be, m)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

	for _, isolation := range []string{"container", "sandbox"} {
		doc, r := f.readiness(ref, "isolation="+isolation)
		if r.code != http.StatusOK {
			t.Fatalf("%s = %d %s", isolation, r.code, r.raw)
		}
		doc.wantCheck(t, CheckRunner, ReadinessUnsupported, codeIsolationUnsupported)
		if doc.ConfigurationState != ReadinessUnsupported || doc.Selection.Isolation != isolation {
			t.Fatalf("%s: state=%q selection=%+v", isolation, doc.ConfigurationState, doc.Selection)
		}
		// The other options stay visible with their own verdicts, so the console can
		// explain the limitation instead of hiding the choice.
		doc.wantCheck(t, CheckProgram, ReadinessReady, codeProgramPresent)
	}
	if doc, _ := f.readiness(ref, "isolation=native"); doc.ConfigurationState != ReadinessReady {
		t.Fatalf("native = %q", doc.ConfigurationState)
	}
}

// TestLaunchReadiness_QueryRejection is the closed-grammar row. An unknown or
// repeated parameter is a 400 and NOT a silently ignored one: ignoring it would
// answer a different question with the same document.
func TestLaunchReadiness_QueryRejection(t *testing.T) {
	be := readinessEngines(t)[0]
	bins := newReadinessProgramFixtures(t)
	m := New(WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
		WithCredentialSource(mintRefusingCredentialSource{t}))
	m.UseExecutionEnvironmentRef(testEnvRef)
	f := newReadinessFixture(t, be, m)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

	for _, query := range []string{
		"workspace_ref=ws_1",
		"driver=codex",
		"auth_source=managed_injection",
		"tenant=other",
		"transport=stream-json&transport=remote-control",
		"isolation=native&isolation=container",
		"transport=grpc",
		"isolation=vm",
		"transport=",
		"%zz=1",
		"transport=stream-json&unknown=1",
	} {
		if _, r := f.readiness(ref, query); r.code != http.StatusBadRequest {
			t.Fatalf("query %q = %d %s, want 400", query, r.code, r.raw)
		}
	}
	// A malformed reference is a 400 shaped like every other one, with no hint
	// about whether such a profile could exist.
	if _, r := f.readinessAs(f.viewer, f.tenant, "not-a-profile-ref", ""); r.code != http.StatusNotFound {
		t.Fatalf("malformed ref = %d %s", r.code, r.raw)
	}
	// The defaults are the documented ones and are echoed back, so a console can
	// never mistake one selection's answer for another's.
	doc, r := f.readiness(ref, "")
	if r.code != http.StatusOK ||
		doc.Selection != (LaunchReadinessSelection{Transport: string(TransportStreamJSON), Isolation: string(IsolationNative)}) {
		t.Fatalf("defaults = %+v (%d)", doc.Selection, r.code)
	}
}

// TestLaunchReadiness_ProfileChangedDuringInspection is the coherence row: the
// profile is mutated FROM INSIDE the inspection, so the filesystem checks and
// the profile they describe belong to two different states. The straddled
// observation is discarded with 409 instead of published as one document.
func TestLaunchReadiness_ProfileChangedDuringInspection(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			mutator := &mutatingInspector{native: NewProcRunner()}
			m := New(WithRunner(mutator), WithProgram(bins.present),
				WithCredentialSource(mintRefusingCredentialSource{t}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

			// Before arming the mutation, the same read is a plain 200.
			if _, r := f.readiness(ref, ""); r.code != http.StatusOK {
				t.Fatalf("baseline = %d %s", r.code, r.raw)
			}
			disabled := ProfileDisabled
			mutator.arm(func() {
				if _, err := m.PatchProfile(context.Background(), f.tenant, ref, ProfilePatch{State: &disabled}); err != nil {
					t.Errorf("concurrent patch: %v", err)
				}
			})
			_, r := f.readiness(ref, "")
			if r.code != http.StatusConflict {
				t.Fatalf("changed during inspection = %d %s, want 409", r.code, r.raw)
			}
			// ⛔ THE ASSERTION IS THE CODE, NOT THE PROSE. This test used to accept
			// any 409 whose message happened to contain the word "changed", and that
			// is exactly why it passed while the ratified identifier was absent from
			// the body: a console cannot detect a conflict by matching English.
			body, _ := r.body["error"].(map[string]any)
			if code, _ := body["code"].(string); code != codeProfileChanged {
				t.Fatalf("409 error.code = %q, want %q: %s", code, codeProfileChanged, r.raw)
			}
			if got := r.header.Get("Cache-Control"); got != "no-store" {
				t.Fatalf("409 Cache-Control = %q, want no-store: the one answer that says "+
					"\"this observation was incoherent\" must not be the one a cache may keep", got)
			}
			// Re-reading now returns the NEW state coherently — the conflict is a
			// refusal to publish a mixture, not a permanent failure.
			doc, r := f.readiness(ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("re-read = %d %s", r.code, r.raw)
			}
			doc.wantCheck(t, CheckProfile, ReadinessNotConfigured, codeProfileDisabled)
		})
	}
}

// mutatingInspector runs a one-shot mutation from INSIDE the inspection window,
// which is the only place a race can be made deterministic.
type mutatingInspector struct {
	native Runner
	fn     func()
}

func (mi *mutatingInspector) arm(fn func()) { mi.fn = fn }

func (mi *mutatingInspector) Launch(context.Context, LaunchSpec) (Process, error) {
	return nil, errors.New("readiness must not launch")
}

func (mi *mutatingInspector) InspectLaunch(ctx context.Context, in RunnerInspection) (RunnerObservation, error) {
	if fn := mi.fn; fn != nil {
		mi.fn = nil
		fn()
	}
	return mi.native.(RunnerInspector).InspectLaunch(ctx, in)
}

// TestLaunchReadiness_RuntimeCredentialWiring is the work/K3 row: two issuers
// are required rather than one flag, an effective composition is ready, and the
// read never activates or repairs the kernel to reach an answer.
func TestLaunchReadiness_RuntimeCredentialWiring(t *testing.T) {
	be := readinessEngines(t)[0]
	bins := newReadinessProgramFixtures(t)
	cfgHome, userHome := readinessHomes(t)

	readOne := func(t *testing.T, prepare func(m *Module)) (SessionLaunchReadiness, *Module) {
		t.Helper()
		m := New(WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
			WithCredentialSource(mintRefusingCredentialSource{t}))
		m.UseExecutionEnvironmentRef(testEnvRef)
		prepare(m)
		f := newReadinessFixture(t, freshEngine(t, be), m)
		ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
		doc, r := f.readiness(ref, "")
		if r.code != http.StatusOK {
			t.Fatalf("= %d %s", r.code, r.raw)
		}
		return doc, m
	}

	t.Run("standalone module that does not ask for them", func(t *testing.T) {
		doc, _ := readOne(t, func(*Module) {})
		// Its absence is not a universal defect: this composition never requested it.
		doc.wantCheck(t, CheckRuntimeCredentials, ReadinessNotApplicable, codeRuntimeCredentialsNotRequested)
		if doc.ConfigurationState != ReadinessReady {
			t.Fatalf("configuration_state = %q", doc.ConfigurationState)
		}
	})

	t.Run("enabled with only one issuer", func(t *testing.T) {
		doc, m := readOne(t, func(m *Module) {
			// The work issuer alone. The dual posture is INDIVISIBLE, so this is a
			// known incomplete composition rather than a working one.
			m.UseWorkSessionCredentialSource(nil)
			m.EnableCommunicationSessionCredentials()
		})
		doc.wantCheck(t, CheckRuntimeCredentials, ReadinessNotConfigured, codeRuntimeCredentialWiringPartial)
		if doc.ConfigurationState != ReadinessNotConfigured {
			t.Fatalf("configuration_state = %q", doc.ConfigurationState)
		}
		if !m.CommunicationSessionCredentialsEnabled() {
			t.Fatal("the read changed the K3 activation state")
		}
	})

	t.Run("enabled with a composed witness that is not effective", func(t *testing.T) {
		stub := &communicationReadinessStub{storeReady: true, sealerReady: true}
		doc, m := readOne(t, func(m *Module) {
			m.UseWorkSessionCredentialSource(stubWorkCredentialSource{})
			wireCommunicationReadiness(m, stub, false)
			m.EnableCommunicationSessionCredentials()
		})
		// The pump witness is absent, so the effective posture is OFF: a known
		// missing dependency, not an unavailable observation.
		doc.wantCheck(t, CheckRuntimeCredentials, ReadinessNotConfigured, codeRuntimeCredentialWiringPartial)
		readiness, err := m.EvaluateCommunicationReadiness(context.Background())
		if err != nil || readiness.Effective {
			t.Fatalf("the read must not have made K3 effective: %+v %v", readiness, err)
		}
	})

	t.Run("enabled with an unavailable witness", func(t *testing.T) {
		stub := &communicationReadinessStub{storeErr: errors.New("store witness unavailable")}
		doc, _ := readOne(t, func(m *Module) {
			m.UseWorkSessionCredentialSource(stubWorkCredentialSource{})
			wireCommunicationReadiness(m, stub, true)
			m.EnableCommunicationSessionCredentials()
		})
		// ⛔ A witness that could not answer is uncertainty, never an absence.
		doc.wantCheck(t, CheckRuntimeCredentials, ReadinessUnknown, codeRuntimeReadinessUnavailable)
	})

	t.Run("enabled and effective", func(t *testing.T) {
		stub := &communicationReadinessStub{storeReady: true, sealerReady: true, pumpReady: true}
		doc, _ := readOne(t, func(m *Module) {
			m.UseWorkSessionCredentialSource(stubWorkCredentialSource{})
			wireCommunicationReadiness(m, stub, true)
			m.EnableCommunicationSessionCredentials()
		})
		doc.wantCheck(t, CheckRuntimeCredentials, ReadinessReady, codeRuntimeCredentialsWired)
		if doc.ConfigurationState != ReadinessReady {
			t.Fatalf("configuration_state = %q", doc.ConfigurationState)
		}
	})
}

// stubWorkCredentialSource satisfies the work issuer seam and fails loudly if
// the read ever mints through it.
type stubWorkCredentialSource struct{}

func (stubWorkCredentialSource) Mint(context.Context, WorkSessionCredentialRequest) (WorkSessionCredential, error) {
	panic("launch readiness minted a work-session credential")
}

func (stubWorkCredentialSource) Renew(context.Context, model.ID, WorkSessionCredentialRequest) (time.Time, error) {
	panic("launch readiness renewed a work-session credential")
}

func (stubWorkCredentialSource) Revoke(context.Context, model.ID, WorkSessionCredentialRequest) error {
	panic("launch readiness revoked a work-session credential")
}

// TestLaunchReadinessAggregationPrecedence pins the explicit precedence of §4
// against the three mutants that would each look reasonable in isolation.
func TestLaunchReadinessAggregationPrecedence(t *testing.T) {
	t.Parallel()
	row := func(state ReadinessState) LaunchReadinessCheck {
		return LaunchReadinessCheck{Check: CheckProfile, State: state}
	}
	cases := []struct {
		name   string
		in     []LaunchReadinessCheck
		want   ReadinessState
		mutant string
	}{
		{"all ready", []LaunchReadinessCheck{row(ReadinessReady), row(ReadinessReady)}, ReadinessReady, ""},
		{"not_applicable is neutral", []LaunchReadinessCheck{row(ReadinessReady), row(ReadinessNotApplicable)}, ReadinessReady,
			"a mutant that ranked not_applicable above ready would report an absent requirement as a fault"},
		{"unknown beats ready", []LaunchReadinessCheck{row(ReadinessReady), row(ReadinessUnknown)}, ReadinessUnknown,
			"a mutant that ignored unknown would publish an unchecked node as ready — the whole defect this read exists to fix"},
		{"not_configured beats unknown", []LaunchReadinessCheck{row(ReadinessUnknown), row(ReadinessNotConfigured)}, ReadinessNotConfigured,
			"a mutant that preferred unknown would hide a nameable, fixable cause behind uncertainty"},
		{"unsupported beats not_configured", []LaunchReadinessCheck{row(ReadinessNotConfigured), row(ReadinessUnsupported)}, ReadinessUnsupported,
			"a mutant that preferred not_configured would tell an operator to configure something that cannot run here"},
	}
	for _, tc := range cases {
		if got := aggregateReadiness(tc.in); got != tc.want {
			t.Errorf("%s: aggregate = %q, want %q (%s)", tc.name, got, tc.want, tc.mutant)
		}
	}
}

// TestLaunchReadinessPanelIsComplete proves the panel never shrinks: every
// declared dimension appears exactly once, in the declared order.
func TestLaunchReadinessPanelIsComplete(t *testing.T) {
	be := readinessEngines(t)[0]
	bins := newReadinessProgramFixtures(t)
	m := New(WithRunner(newInspectingRunner(t)), WithProgram(bins.present))
	m.UseExecutionEnvironmentRef(testEnvRef)
	f := newReadinessFixture(t, be, m)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceManagedInjection)

	doc, r := f.readiness(ref, "")
	if r.code != http.StatusOK {
		t.Fatalf("= %d %s", r.code, r.raw)
	}
	var got []ReadinessCheck
	for _, c := range doc.Checks {
		got = append(got, c.Check)
	}
	if !reflect.DeepEqual(got, readinessCheckOrder[:]) {
		t.Fatalf("panel = %v, want %v", got, readinessCheckOrder)
	}
	// A dominant known cause does not hide the rest: the credential source is
	// missing AND every other dimension still carries its own verdict.
	doc.wantCheck(t, CheckCredentialSource, ReadinessNotConfigured, codeClaudeCredentialSourceNotConfigured)
	for _, c := range doc.Checks {
		if c.State == "" || c.Code == "" {
			t.Fatalf("check %q has no verdict: %+v", c.Check, c)
		}
	}
	if want := launchRemainingChecks(); !reflect.DeepEqual(doc.RemainingChecks, want) {
		t.Fatalf("remaining_checks = %v, want %v", doc.RemainingChecks, want)
	}
}
