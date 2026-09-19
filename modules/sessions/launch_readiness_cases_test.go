// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLaunchReadiness_MeasuredOperableTrueMissingSource is the ORIGINAL fixture
// and the original cause, reproduced on a clean engine and then corrected.
//
// The measured defect: a LOCAL, ACTIVE Claude profile with real homes and NO
// inference credential source wired reports operable=true, the console renders
// "Launchable", and the launch it enables is refused. Both halves are asserted
// here at once — the legacy flag is still true (it is preserved, not redefined)
// and the new read says not_configured, naming the missing source.
func TestLaunchReadiness_MeasuredOperableTrueMissingSource(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			runner := newInspectingRunner(t)
			// Everything a launch needs EXCEPT the credential source: a wired runner,
			// a real pinned executable, real homes, an authorized managed source.
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithProgram(bins.present),
				WithLaunchGate(refusingLaunchGate{t}), WithStopGate(refusingStopGate{t}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceManagedInjection)

			if !f.operable(ref) {
				t.Fatal("the legacy operable flag changed: it must keep meaning exactly what it computed before")
			}
			doc, r := f.readiness(ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("readiness = %d %s", r.code, r.raw)
			}
			if doc.ConfigurationState != ReadinessNotConfigured {
				t.Fatalf("configuration_state = %q, want not_configured while no inference credential source is wired: %s",
					doc.ConfigurationState, r.raw)
			}
			doc.wantCheck(t, CheckCredentialSource, ReadinessNotConfigured, codeClaudeCredentialSourceNotConfigured)
			if got := doc.check(t, CheckCredentialSource).Remediation; got != remediationConfigureClaudeCredential {
				t.Fatalf("remediation = %q", got)
			}
			// Everything else was checked and is fine: the panel names ONE cause and
			// does not blame the profile, the driver, the runner or the binary.
			doc.wantCheck(t, CheckProfile, ReadinessReady, codeProfileActive)
			doc.wantCheck(t, CheckLocalEnvironment, ReadinessReady, codeEnvironmentLocal)
			doc.wantCheck(t, CheckDriver, ReadinessReady, codeDriverOperable)
			doc.wantCheck(t, CheckRunner, ReadinessReady, codeRunnerReady)
			doc.wantCheck(t, CheckProgram, ReadinessReady, codeProgramPresent)
			doc.wantCheck(t, CheckHomes, ReadinessReady, codeHomesResolved)
			doc.wantCheck(t, CheckAuthSource, ReadinessReady, codeAuthSourceManagedInjection)

			// ⛔ THE CAUSAL NEGATIVE OF THIS ROW. A readiness recomputed from
			// `operable` alone would answer ready here, because operable IS true. The
			// two disagreeing on the same profile, in the same request, is the proof
			// that the new read is not the old flag renamed.
			if doc.ConfigurationState == ReadinessReady {
				t.Fatal("readiness agreed with operable: the missing credential source was not detected")
			}
			// AFTER wiring the source — and nothing else — the same profile is ready.
			wired := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
				WithCredentialSource(mintRefusingCredentialSource{t}))
			wired.UseExecutionEnvironmentRef(testEnvRef)
			g := newReadinessFixture(t, freshEngine(t, be), wired)
			ref2 := g.createProfile("claude", cfgHome, userHome, AuthSourceManagedInjection)
			doc2, r2 := g.readiness(ref2, "")
			if r2.code != http.StatusOK || doc2.ConfigurationState != ReadinessReady {
				t.Fatalf("with the source wired: %d state=%q %s", r2.code, doc2.ConfigurationState, r2.raw)
			}
			doc2.wantCheck(t, CheckCredentialSource, ReadinessReady, codeClaudeCredentialSourceConfigured)
			// ⛔ AND READY IS STILL NOT AUTHENTICATED OR AUTHORIZED.
			if doc2.ProviderAuthentication.State != ReadinessUnknown ||
				doc2.ProviderAuthentication.Code != codeNotObservedForThisLaunch {
				t.Fatalf("provider_authentication = %+v, want unknown", doc2.ProviderAuthentication)
			}
			if doc2.LaunchAuthorization.State != ReadinessUnknown ||
				doc2.LaunchAuthorization.RequiredPermission != string(permRunWrite) {
				t.Fatalf("launch_authorization = %+v", doc2.LaunchAuthorization)
			}
		})
	}
}

// TestLaunchReadiness_AuthTransportMatrix is the driver × auth_source ×
// transport table of the contract, over real HTTP.
//
// The rows that matter most are the ones a single global rule would get wrong:
// an account-home profile needs NO Claude bearer, a managed Codex launch cannot
// borrow one, and Claude remote-control does not apply the managed injection it
// is authorized for.
func TestLaunchReadiness_AuthTransportMatrix(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			// Codex and Grok are registered INDEPENDENTLY, exactly as the composition
			// root registers them from two separate variables. Codex additionally has
			// its OWN governed adapter; Grok deliberately has none.
			m := New(WithSessionWorkspaceRoot(t.TempDir()),
				WithRunner(newInspectingRunner(t)),
				WithProgram(bins.present),
				WithCredentialSource(mintRefusingCredentialSource{t}),
				WithProviderDriver(NewCodexDriver()), WithDriverProgram("codex", bins.present),
				WithProviderDriver(NewGrokDriver()), WithDriverProgram("grok", bins.present),
				WithProviderDriver(NewOpenCodeDriver()), WithDriverProgram("opencode", bins.present),
				WithProviderCredentialSource("codex", mintRefusingProviderSource{t}),
				WithLaunchGate(refusingLaunchGate{t}), WithStopGate(refusingStopGate{t}),
			)
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)

			newProfile := func(driver, authSource string) string {
				cfgHome, userHome := readinessHomes(t)
				return f.createProfile(driver, cfgHome, userHome, authSource)
			}

			t.Run("claude account home needs no global credential source", func(t *testing.T) {
				doc, r := f.readiness(newProfile("claude", AuthSourceAccountHome), "transport=stream-json")
				if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
					t.Fatalf("= %d state=%q %s", r.code, doc.ConfigurationState, r.raw)
				}
				// ⛔ THE CAUSAL NEGATIVE: a rule that demanded a global WIF/token file
				// for every launch would make this row not_configured. Nothing is
				// injected here — the authorized home IS the credential.
				doc.wantCheck(t, CheckCredentialSource, ReadinessNotApplicable, codeCredentialSourceNotInjected)
				doc.wantCheck(t, CheckAuthSource, ReadinessReady, codeAuthSourceAccountHome)
			})

			t.Run("claude legacy and managed stream-json use the claude source", func(t *testing.T) {
				for _, source := range []string{"", AuthSourceManagedInjection} {
					doc, r := f.readiness(newProfile("claude", source), "transport=stream-json")
					if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
						t.Fatalf("source %q = %d state=%q %s", source, r.code, doc.ConfigurationState, r.raw)
					}
					doc.wantCheck(t, CheckCredentialSource, ReadinessReady, codeClaudeCredentialSourceConfigured)
					if doc.TransportCapabilities.Protocol != protocolClaudeStreamJSON ||
						doc.TransportCapabilities.IO != ioBidirectional ||
						doc.TransportCapabilities.Input != inputLine {
						t.Fatalf("source %q capabilities = %+v", source, doc.TransportCapabilities)
					}
				}
			})

			t.Run("claude remote-control managed injection is not certified", func(t *testing.T) {
				doc, r := f.readiness(newProfile("claude", AuthSourceManagedInjection), "transport=remote-control")
				if r.code != http.StatusOK {
					t.Fatalf("= %d %s", r.code, r.raw)
				}
				// The ratified precision: the selection is authorized and the injection
				// it names is NOT applied by this transport, so the read certifies
				// neither managed injection nor an authenticated account.
				doc.wantCheck(t, CheckAuthSource, ReadinessUnknown, codeManagedInjectionNotAppliedForTransport)
				doc.wantCheck(t, CheckCredentialSource, ReadinessNotApplicable, codeManagedInjectionNotAppliedForTransport)
				if doc.ConfigurationState != ReadinessUnknown {
					t.Fatalf("configuration_state = %q, want unknown: an unapplied injection is not a ready one", doc.ConfigurationState)
				}
				if doc.TransportCapabilities.Protocol != protocolClaudeRemoteControl ||
					doc.TransportCapabilities.IO != ioLifecycleOnly ||
					doc.TransportCapabilities.Input != inputUnavailable {
					t.Fatalf("capabilities = %+v, want lifecycle-only with no bridged input", doc.TransportCapabilities)
				}
			})

			t.Run("codex account home is ready without a claude bearer", func(t *testing.T) {
				doc, r := f.readiness(newProfile("codex", AuthSourceAccountHome), "")
				if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
					t.Fatalf("= %d state=%q %s", r.code, doc.ConfigurationState, r.raw)
				}
				doc.wantCheck(t, CheckDriver, ReadinessReady, codeDriverOperable)
				doc.wantCheck(t, CheckCredentialSource, ReadinessNotApplicable, codeCredentialSourceNotInjected)
				if doc.TransportCapabilities.Protocol != protocolCodexAppServer ||
					doc.TransportCapabilities.Input != inputText {
					t.Fatalf("capabilities = %+v, want the codex app-server contract", doc.TransportCapabilities)
				}
			})

			t.Run("codex managed injection uses its OWN adapter", func(t *testing.T) {
				doc, r := f.readiness(newProfile("codex", AuthSourceManagedInjection), "")
				if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
					t.Fatalf("= %d state=%q %s", r.code, doc.ConfigurationState, r.raw)
				}
				doc.wantCheck(t, CheckCredentialSource, ReadinessReady, codeProviderCredentialAdapterConfigured)
			})

			t.Run("grok managed injection blocks without its own adapter", func(t *testing.T) {
				doc, r := f.readiness(newProfile("grok", AuthSourceManagedInjection), "")
				if r.code != http.StatusOK {
					t.Fatalf("= %d %s", r.code, r.raw)
				}
				// ⛔ The Claude bearer IS wired and the Codex adapter IS wired. Neither
				// serves Grok: a managed launch resolves that driver's own governed
				// adapter or it is refused by name.
				doc.wantCheck(t, CheckCredentialSource, ReadinessNotConfigured, codeProviderAdapterNotConfigured)
				if doc.ConfigurationState != ReadinessNotConfigured {
					t.Fatalf("configuration_state = %q", doc.ConfigurationState)
				}
				if doc.TransportCapabilities.Protocol != protocolGrokACP {
					t.Fatalf("capabilities = %+v", doc.TransportCapabilities)
				}
			})

			t.Run("grok account home is ready", func(t *testing.T) {
				doc, r := f.readiness(newProfile("grok", AuthSourceAccountHome), "")
				if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
					t.Fatalf("= %d state=%q %s", r.code, doc.ConfigurationState, r.raw)
				}
			})

			t.Run("opencode managed injection blocks without its own adapter", func(t *testing.T) {
				doc, r := f.readiness(newProfile("opencode", AuthSourceManagedInjection), "")
				if r.code != http.StatusOK {
					t.Fatalf("= %d %s", r.code, r.raw)
				}
				doc.wantCheck(t, CheckCredentialSource, ReadinessNotConfigured, codeProviderAdapterNotConfigured)
				if doc.ConfigurationState != ReadinessNotConfigured {
					t.Fatalf("configuration_state = %q", doc.ConfigurationState)
				}
				if doc.TransportCapabilities.Protocol != protocolOpenCodeACP ||
					doc.TransportCapabilities.IO != ioBidirectional ||
					doc.TransportCapabilities.Input != inputText {
					t.Fatalf("capabilities = %+v", doc.TransportCapabilities)
				}
			})

			t.Run("opencode account home is ready", func(t *testing.T) {
				doc, r := f.readiness(newProfile("opencode", AuthSourceAccountHome), "")
				if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
					t.Fatalf("= %d state=%q %s", r.code, doc.ConfigurationState, r.raw)
				}
			})

			t.Run("codex and grok have no auth source at all", func(t *testing.T) {
				for _, driver := range []string{"codex", "grok", "opencode"} {
					doc, r := f.readiness(newProfile(driver, ""), "")
					if r.code != http.StatusOK {
						t.Fatalf("%s = %d %s", driver, r.code, r.raw)
					}
					doc.wantCheck(t, CheckAuthSource, ReadinessNotConfigured, codeAuthSourceRequired)
					// Which credential path applies is undecided, so it is uncertainty
					// about a requirement, never not_applicable.
					doc.wantCheck(t, CheckCredentialSource, ReadinessUnknown, codeAuthSourceRequired)
				}
			})

			t.Run("remote-control is unsupported for a non-claude driver", func(t *testing.T) {
				for _, driver := range []string{"codex", "grok"} {
					doc, r := f.readiness(newProfile(driver, AuthSourceAccountHome), "transport=remote-control")
					if r.code != http.StatusOK {
						t.Fatalf("%s = %d %s", driver, r.code, r.raw)
					}
					// Unsupported for THIS COMBINATION — not "the provider is not
					// integrated", which is what the console used to say.
					doc.wantCheck(t, CheckDriver, ReadinessUnsupported, codeTransportUnsupported)
					if doc.ConfigurationState != ReadinessUnsupported {
						t.Fatalf("%s configuration_state = %q", driver, doc.ConfigurationState)
					}
					if doc.TransportCapabilities.Protocol != protocolUnknown {
						t.Fatalf("%s capabilities = %+v", driver, doc.TransportCapabilities)
					}
				}
			})
		})
	}
}

// TestLaunchReadiness_UnregisteredDriverIsConfigurationNotAbsence proves the
// correction the contract demands of slice 3's prose: a fixture that did not set
// the driver's variable measured an UNCONFIGURED node, not missing code. The
// same profile, on a node that registered the driver, is operable.
func TestLaunchReadiness_UnregisteredDriverIsConfigurationNotAbsence(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			bare := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present))
			bare.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, bare)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("codex", cfgHome, userHome, AuthSourceAccountHome)

			doc, r := f.readiness(ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("= %d %s", r.code, r.raw)
			}
			doc.wantCheck(t, CheckDriver, ReadinessNotConfigured, codeDriverNotRegistered)
			if got := doc.check(t, CheckDriver).Remediation; got != remediationConfigureDriver {
				t.Fatalf("remediation = %q, want the operator action that registers the driver", got)
			}
			if f.operable(ref) {
				t.Fatal("operable must be false for a driver this node has not registered")
			}

			registered := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
				WithProviderDriver(NewCodexDriver()), WithDriverProgram("codex", bins.present))
			registered.UseExecutionEnvironmentRef(testEnvRef)
			g := newReadinessFixture(t, freshEngine(t, be), registered)
			ref2 := g.createProfile("codex", cfgHome, userHome, AuthSourceAccountHome)
			doc2, r2 := g.readiness(ref2, "")
			if r2.code != http.StatusOK || doc2.ConfigurationState != ReadinessReady {
				t.Fatalf("registered = %d state=%q %s", r2.code, doc2.ConfigurationState, r2.raw)
			}
		})
	}
}

// requireProgramInspectionDenied is the real os.Stat probe the sibling
// permission unit already applies to this exact fixture
// (launch_readiness_permission_test.go, "a link whose target cannot be examined
// stays unknown"), brought to the HTTP table for the ONE row that depends on it.
//
// ⛔ THE FIXTURE STATES A PLATFORM FACT, NOT A PRODUCT ONE. It hides the target
// behind a mode 0000 directory so stat answers EACCES, and
// classifyProgramCandidate answers unknown precisely BECAUSE its own os.Stat
// failed. An identity that bypasses those bits — euid 0, or a filesystem that
// does not apply them — stats straight through: the inspection succeeds, and the
// only honest verdict for a program that is then present and executable is
// ready / program_present. Demanding unknown there would demand that production
// invent an uncertainty it never observed, so the row reports itself NOT
// EXERCISED, exactly as its unit sibling does. The other three rows do not
// depend on any identity and keep running.
func requireProgramInspectionDenied(t *testing.T, program string) {
	t.Helper()
	_, err := os.Stat(program)
	if err == nil {
		t.Skipf("NOT exercised: euid %d can still stat through a 0000 directory, so this "+
			"run has no unexaminable program for the inspection to be unavailable about",
			os.Geteuid())
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("the unexaminable fixture must fail stat with a PERMISSION error — an "+
			"absence above all is a DIFFERENT verdict, and this row would then be "+
			"asserting the wrong one: %v", err)
	}
}

// TestLaunchReadiness_NativeProgramObservation exercises the native Runner's
// inspection against REAL files in the four states an operator's binary can be
// in, over HTTP, and proves that not one of them was executed.
func TestLaunchReadiness_NativeProgramObservation(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			cases := []struct {
				name    string
				program string
				state   ReadinessState
				code    string
				// require names the platform fact ONE row rests on and reports that
				// row NOT EXERCISED when the running identity cannot express it. The
				// other three hold for every identity and carry none.
				require func(*testing.T, string)
			}{
				{"present and executable", bins.present, ReadinessReady, codeProgramPresent, nil},
				// ⛔ THE CAUSAL NEGATIVE of the program dimension: omit this check and a
				// node with no CLI installed reports ready.
				{"absent", bins.missing, ReadinessNotConfigured, codeProgramMissing, nil},
				{"present without the executable bit", bins.notExecutable, ReadinessNotConfigured, codeProgramNotExecutable, nil},
				// A link whose target cannot be examined is uncertainty, NEVER
				// "not installed": missing and permission-denied are different facts.
				{"link target that cannot be examined", bins.unexaminable, ReadinessUnknown, codeInspectionUnavailable,
					requireProgramInspectionDenied},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					if tc.require != nil {
						tc.require(t, tc.program)
					}
					runner := newInspectingRunner(t)
					m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithProgram(tc.program),
						WithCredentialSource(mintRefusingCredentialSource{t}))
					m.UseExecutionEnvironmentRef(testEnvRef)
					f := newReadinessFixture(t, freshEngine(t, be), m)
					cfgHome, userHome := readinessHomes(t)
					ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
					doc, r := f.readiness(ref, "")
					if r.code != http.StatusOK {
						t.Fatalf("= %d %s", r.code, r.raw)
					}
					doc.wantCheck(t, CheckProgram, tc.state, tc.code)
					if runner.inspections() == 0 {
						t.Fatal("the Runner was never asked: this observation came from somewhere else")
					}
					if doc.ConfigurationState == ReadinessReady && tc.state != ReadinessReady {
						t.Fatalf("configuration_state = ready while the program is %s", tc.state)
					}
					// The path never leaves the server, even when it IS the cause.
					if strings.Contains(r.raw, bins.dir) {
						t.Fatalf("the response carries a filesystem path: %s", r.raw)
					}
				})
			}
		})
	}
}

// TestLaunchReadiness_ProgramResolvedThroughPATH proves the bare-name branch:
// the module resolves the program the way exec.Command would, and a name that
// exists on PATH without the executable bit is NOT reported as missing.
func TestLaunchReadiness_ProgramResolvedThroughPATH(t *testing.T) {
	be := readinessEngines(t)[0]
	dir := t.TempDir()
	exe := filepath.Join(dir, "pathcli")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plaincli")
	if err := os.WriteFile(plain, []byte("not a program\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, tc := range []struct {
		name, program string
		state         ReadinessState
		code          string
	}{
		{"executable on PATH", "pathcli", ReadinessReady, codeProgramPresent},
		{"on PATH without the executable bit", "plaincli", ReadinessNotConfigured, codeProgramNotExecutable},
		{"nowhere on PATH", "absentcli", ReadinessNotConfigured, codeProgramMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(tc.program),
				WithCredentialSource(mintRefusingCredentialSource{t}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, freshEngine(t, be), m)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
			doc, r := f.readiness(ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("= %d %s", r.code, r.raw)
			}
			doc.wantCheck(t, CheckProgram, tc.state, tc.code)
		})
	}
}

// TestLaunchReadiness_HomesAndForeignEnvironment covers the home states and the
// two localities, and proves the dependent dimensions of a foreign profile stay
// unknown-with-a-reason instead of being reported as absences.
func TestLaunchReadiness_HomesAndForeignEnvironment(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)

			t.Run("a home that was removed", func(t *testing.T) {
				m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
					WithCredentialSource(mintRefusingCredentialSource{t}))
				m.UseExecutionEnvironmentRef(testEnvRef)
				f := newReadinessFixture(t, freshEngine(t, be), m)
				cfgHome, userHome := readinessHomes(t)
				ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
				if doc, _ := f.readiness(ref, ""); doc.ConfigurationState != ReadinessReady {
					t.Fatalf("before removal: %q", doc.ConfigurationState)
				}
				if err := os.RemoveAll(cfgHome); err != nil {
					t.Fatal(err)
				}
				doc, r := f.readiness(ref, "")
				if r.code != http.StatusOK {
					t.Fatalf("= %d %s", r.code, r.raw)
				}
				doc.wantCheck(t, CheckHomes, ReadinessNotConfigured, codeHomeUnavailable)
				if strings.Contains(r.raw, cfgHome) {
					t.Fatalf("the response carries the home path: %s", r.raw)
				}
			})

			t.Run("a home whose alias now points elsewhere", func(t *testing.T) {
				m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
					WithCredentialSource(mintRefusingCredentialSource{t}))
				m.UseExecutionEnvironmentRef(testEnvRef)
				f := newReadinessFixture(t, freshEngine(t, be), m)
				root := t.TempDir()
				real := filepath.Join(root, "real")
				moved := filepath.Join(root, "moved")
				alias := filepath.Join(root, "alias")
				for _, d := range []string{real, moved} {
					if err := os.MkdirAll(d, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(real, alias); err != nil {
					t.Fatal(err)
				}
				_, userHome := readinessHomes(t)
				// The profile stores the RESOLVED location; repointing the alias
				// afterwards leaves the stored path resolving to something else.
				ref := f.createProfile("claude", alias, userHome, AuthSourceAccountHome)
				if err := os.Remove(real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, real); err != nil {
					t.Fatal(err)
				}
				doc, r := f.readiness(ref, "")
				if r.code != http.StatusOK {
					t.Fatalf("= %d %s", r.code, r.raw)
				}
				got := doc.check(t, CheckHomes)
				if got.State != ReadinessNotConfigured ||
					(got.Code != codeHomeChanged && got.Code != codeHomeUnavailable) {
					t.Fatalf("homes = %+v, want a named home fault", got)
				}
			})

			t.Run("a profile of another execution environment", func(t *testing.T) {
				m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
					WithCredentialSource(mintRefusingCredentialSource{t}))
				m.UseExecutionEnvironmentRef(testEnvRef)
				f := newReadinessFixture(t, freshEngine(t, be), m)
				cfgHome, userHome := readinessHomes(t)
				ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
				// The node's identity moves; the profile now belongs to another
				// environment, which is exactly the shape of a multi-node estate.
				m.UseExecutionEnvironmentRef("env-test-node-b")
				doc, r := f.readiness(ref, "")
				if r.code != http.StatusOK {
					t.Fatalf("= %d %s", r.code, r.raw)
				}
				doc.wantCheck(t, CheckLocalEnvironment, ReadinessUnsupported, codeOtherEnvironment)
				if doc.ConfigurationState != ReadinessUnsupported {
					t.Fatalf("configuration_state = %q", doc.ConfigurationState)
				}
				// ⛔ THE DEPENDENT DIMENSIONS ARE UNKNOWN, NOT ABSENT. The homes exist
				// and are perfectly fine; this node simply has no business examining
				// another environment's paths, and saying "home_unavailable" would be
				// a false statement about somebody else's filesystem.
				for _, kind := range []ReadinessCheck{CheckRunner, CheckProgram, CheckHomes, CheckRuntimeCredentials} {
					doc.wantCheck(t, kind, ReadinessUnknown, codeNotCheckedInThisEnvironment)
				}
				if doc.EvaluatedEnvironmentRef != "env-test-node-b" || doc.EnvironmentRef != testEnvRef {
					t.Fatalf("environments = %q / %q", doc.EnvironmentRef, doc.EvaluatedEnvironmentRef)
				}
			})

			t.Run("a node with no execution environment identity", func(t *testing.T) {
				m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
					WithCredentialSource(mintRefusingCredentialSource{t}))
				m.UseExecutionEnvironmentRef(testEnvRef)
				f := newReadinessFixture(t, freshEngine(t, be), m)
				cfgHome, userHome := readinessHomes(t)
				ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
				m.UseExecutionEnvironmentRef("")
				doc, r := f.readiness(ref, "")
				if r.code != http.StatusOK {
					t.Fatalf("= %d %s", r.code, r.raw)
				}
				doc.wantCheck(t, CheckLocalEnvironment, ReadinessNotConfigured, codeEnvironmentUnavailable)
				if doc.EvaluatedEnvironmentRef != "" {
					t.Fatalf("evaluated_environment_ref = %q, want absent", doc.EvaluatedEnvironmentRef)
				}
			})
		})
	}
}

// TestLaunchReadiness_ProfileLifecycle covers disabled and retired.
func TestLaunchReadiness_ProfileLifecycle(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(newInspectingRunner(t)), WithProgram(bins.present),
				WithCredentialSource(mintRefusingCredentialSource{t}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

			if r := f.h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+ref, f.admin,
				map[string]any{"state": "disabled"}, tenantHdr(f.tenant)); r.code != http.StatusOK {
				t.Fatalf("disable = %d %s", r.code, r.raw)
			}
			doc, _ := f.readiness(ref, "")
			doc.wantCheck(t, CheckProfile, ReadinessNotConfigured, codeProfileDisabled)
			if doc.ProfileState != ProfileDisabled || doc.ConfigurationState != ReadinessNotConfigured {
				t.Fatalf("disabled: %q / %q", doc.ProfileState, doc.ConfigurationState)
			}

			if r := f.h.do("POST", "/v1/m/sessions/provider-profiles/"+ref+"/retire", f.admin,
				tenantHdr(f.tenant)); r.code != http.StatusOK {
				t.Fatalf("retire = %d %s", r.code, r.raw)
			}
			doc, _ = f.readiness(ref, "")
			doc.wantCheck(t, CheckProfile, ReadinessNotConfigured, codeProfileRetired)
		})
	}
}
