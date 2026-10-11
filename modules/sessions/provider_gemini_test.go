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

	"github.com/olivaresai/olivares/core/model"
)

func TestGeminiAccountHomeCanLaunch(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "gemini-home-"+be.name)
			acctHomeRoot(t, m)
			a := newAcctAPI(m, st, tenant)
			r := a.create("gemini-cli", "gemini-personal")
			if r.code != http.StatusCreated {
				t.Fatalf("create: %d %s", r.code, r.raw)
			}
			ref := r.body["account_ref"].(string)
			p, err := m.GetProfile(context.Background(), tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(p.ConfigHome) != ".gemini" {
				t.Fatalf("Gemini config_home = %s", p.ConfigHome)
			}
			if info, err := os.Stat(p.ConfigHome); err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("private Gemini home: %v %v", info, err)
			}
			if err := geminiHomeRefusal(&ProviderHomeSnapshot{ConfigHome: p.ConfigHome, UserHome: p.UserHome}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Contributions reach the real launch path, including the native runner's final
// duplicate-key check. A different credential never reaches an executable.
func TestGeminiBoundKeyRuntimeRefusesGateAndSecretOverrides(t *testing.T) {
	for _, source := range []string{"gate", "secret"} {
		t.Run(source, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "launched")
			program := filepath.Join(t.TempDir(), "gemini")
			if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf launched > '"+marker+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			vault := newFakeVault()
			gate := &spyGate{inner: LaunchDecision{Allowed: true}}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(NewProcRunner()), WithProviderDriver(NewGeminiDriver()), WithDriverProgram(providerDriverGemini, program), WithProviderSecretVault(vault), WithLaunchGate(gate))
			m.UseExecutionEnvironmentRef(testEnvRef)
			record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindGemini, DisplayName: "selected Gemini", APIKey: testProviderKey})
			home := geminiTestHome(t, nil)
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverGemini, ConfigHome: home.ConfigHome, UserHome: home.UserHome, AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref})
			params := CreateRunParams{ProviderProfileRef: profile.Ref, Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: model.ActorUser}
			want := "another Google credential"
			if source == "gate" {
				gate.inner.InjectEnv = []EnvVar{{Name: "GOOGLE_API_KEY", Value: "alternate-fixture-key"}}
			} else {
				vault.values[tenant.String()+"|env/alternate"] = "alternate-fixture-key"
				params.SecretEnv = []SecretEnvRef{{Env: "GEMINI_API_KEY", Secret: "env/alternate"}}
				params.MayUseSecretEnv = true
				want = "tool process launch failed; the session was not started"
			}
			_, err := m.createRun(t.Context(), tenant, params)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s override = %v, want refusal naming %s", source, err, want)
			}
			if len(gate.seen) != 1 {
				t.Fatalf("override did not reach the launch gate: %d", len(gate.seen))
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("credential override started a child: %v", err)
			}
		})
	}
}

func TestGeminiProviderKeyBinding(t *testing.T) {
	kind, err := normalizeProviderKind("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if !recordServesDriver(kind, "", "gemini-cli") {
		t.Fatal("a Gemini key cannot bind to its own CLI")
	}
	if recordServesDriver(kind, "https://another.example", "gemini-cli") {
		t.Fatal("Gemini must not silently ignore a bound endpoint")
	}
	want := []EnvVar{{Name: "GEMINI_API_KEY", Value: "fixture-key"}}
	if got := providerRecordEnv(kind, "", "fixture-key"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Gemini credential env = %+v", got)
	}
}

func TestGeminiBoundKeyRefusesConflictingNativeAuth(t *testing.T) {
	for _, value := range []string{`{"security":{"auth":{"selectedType":"oauth-personal"}}}`, `{"security":{"auth":{"selectedType":"vertex-ai"}}}`, `{"security":{"auth":{"useExternal":true}}}`, `/* unreadable as strict JSON */`} {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := geminiKeySettingsRefusal(path); err == nil {
			t.Fatalf("conflicting native settings accepted: %s", value)
		}
	}
	for _, value := range []string{`{}`, `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`} {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := geminiKeySettingsRefusal(path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := validProviderBaseURL(ProviderKindGemini, "https://another.example"); err == nil {
		t.Fatal("Gemini endpoint override was accepted")
	}
}

func TestGeminiAccountRefusesOccupiedConfigWithoutWriting(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "gemini-occupied")
			root := acctHomeRoot(t, m)
			m.accountHomeCheckpoint = func(phase string) error {
				if phase == "reserved" {
					return errors.New("park reservation")
				}
				return nil
			}
			a := newAcctAPI(m, st, tenant)
			body := map[string]any{"driver": "gemini-cli", "idempotency_key": "gemini-occupied"}
			if r := a.call("POST", "/provider-accounts", body); r.code != 503 {
				t.Fatalf("reservation = %d %s", r.code, r.raw)
			}
			op := accountRecoveryOperation(t, m, tenant, "gemini-occupied")
			config := filepath.Join(acctCustodyDir(root, tenant, op.String(colHORef)), "config")
			if err := os.MkdirAll(config, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(config, "sentinel")
			if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			m.accountHomeCheckpoint = nil
			if r := a.call("POST", "/provider-accounts", body); r.code != 409 {
				t.Fatalf("occupied = %d %s", r.code, r.raw)
			}
			entries, err := os.ReadDir(config)
			if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
				t.Fatalf("unowned config changed: %v %v", entries, err)
			}
			if b, err := os.ReadFile(sentinel); err != nil || string(b) != "preserve" {
				t.Fatalf("sentinel changed: %q %v", b, err)
			}
		})
	}
}

func TestGeminiBoundKeyRefusesAlternateEnvironment(t *testing.T) {
	home := t.TempDir()
	clean := func() *LaunchSpec {
		return &LaunchSpec{BoundProvider: BoundProvider{Kind: ProviderKindGemini}, Env: []EnvVar{{Name: "GEMINI_CLI_HOME", Value: home}, {Name: "GEMINI_API_KEY", Value: "selected-fixture-key"}}}
	}
	if err := checkGeminiBoundProviderConfig(clean()); err != nil {
		t.Fatalf("clean bound launch: %v", err)
	}
	for _, name := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_GEMINI_BASE_URL"} {
		spec := clean()
		spec.EnvAllow = []string{name}
		if err := checkGeminiBoundProviderConfig(spec); err == nil {
			t.Fatalf("bound key inherited %s", name)
		}
		spec = clean()
		spec.Env = append(spec.Env, EnvVar{Name: name, Value: "alternate-fixture"})
		if name == "GEMINI_API_KEY" {
			if err := validateExplicitEnv(spec.Env); err == nil {
				t.Fatal("duplicate key could shadow selected identity")
			}
		} else if err := checkGeminiBoundProviderConfig(spec); err == nil {
			t.Fatalf("bound key accepted %s", name)
		}
	}
}
