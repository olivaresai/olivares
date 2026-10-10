// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestProviderDefaultModelSurvivesReadChangeAndRestart(t *testing.T) {
	h := newHarness(t, New(WithProviderSecretVault(newFakeVault())))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "default-model")
	r := h.doJSON("POST", "/v1/m/sessions/providers", admin, map[string]any{
		"kind": "openai", "display_name": "Coding", "api_key": testProviderKey,
		"default_model": "coding-model-a",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["default_model"] != "coding-model-a" {
		t.Fatalf("save default on a provider = %d %s, want 201 with coding-model-a", r.code, r.raw)
	}
	ref := r.body["provider_ref"].(string)
	r = h.doJSON("PATCH", "/v1/m/sessions/providers/"+ref, admin,
		map[string]any{"default_model": "coding-model-b"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["default_model"] != "coding-model-b" {
		t.Fatalf("change default = %d %s", r.code, r.raw)
	}
	// A new module instance reads the same durable state without a process-local cache.
	restarted := New()
	restarted.UseData(api.NewModuleData(h.st))
	rec, err := restarted.GetProviderRecord(context.Background(), tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(toProviderRecordDTO(rec))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil || body["default_model"] != "coding-model-b" {
		t.Fatalf("read default after module restart = %s, %v", data, err)
	}
	r = h.doJSON("PATCH", "/v1/m/sessions/providers/"+ref, admin,
		map[string]any{"default_model": ""}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["default_model"] != nil {
		t.Fatalf("clear default = %d %s, want explicit null", r.code, r.raw)
	}
}

func TestNewProviderInitialDefaultDoesNotOverwriteAChoice(t *testing.T) {
	for _, be := range providerDefaultBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := be.open(t, nil)
			defer st.Close()
			tenant := ensureTenant(t, st, "initial-default")
			probe := m.rt.ProviderProbe.(*fakeProbe)
			ctx := context.Background()
			probe.result.Models = []string{"coding-model-a"}
			in := CreateProviderRecordInput{Kind: ProviderKindOpenAICompatible, DisplayName: "Gateway",
				BaseURL: "https://gateway.example/v1", APIKey: testProviderKey}
			rec := mustCreateRecord(t, m, tenant, in)
			if rec.DefaultModel == nil || *rec.DefaultModel != "" {
				t.Fatalf("new omitted default = %v, want pending first test", rec.DefaultModel)
			}
			after, err := m.TestProviderRecord(ctx, tenant, rec.Ref)
			if err != nil || after.DefaultModel == nil || *after.DefaultModel != "coding-model-a" {
				t.Fatalf("successful sole-model test did not select its model: %+v %v", after, err)
			}
			// The test was in flight when an operator chose another model. Its observation
			// must still be recorded without replacing that explicit choice.
			chosen := "coding-model-b"
			if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DefaultModel: &chosen}); err != nil {
				t.Fatal(err)
			}
			after, err = m.recordProbeOutcome(ctx, tenant, rec, ProbeOK, "accepted", []string{"coding-model-a"}, time.Millisecond)
			if err != nil || after.DefaultModel == nil || *after.DefaultModel != chosen {
				t.Fatalf("in-flight test replaced operator choice: %+v %v", after, err)
			}
			clear := ""
			if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DefaultModel: &clear}); err != nil {
				t.Fatal(err)
			}
			after, err = m.TestProviderRecord(ctx, tenant, rec.Ref)
			if err != nil || after.DefaultModel != nil {
				t.Fatalf("a later probe filled an explicitly cleared NULL: %+v %v", after, err)
			}
		})
	}
}

func TestProviderDefaultModelReachesGateAndArgvAndNextStartOnly(t *testing.T) {
	m, tenant, runner, _, _, rec, prof := boundHarness(t)
	gate := &spyGate{}
	m.rt.LaunchGate = gate
	ctx := context.Background()
	if _, err := m.recordProbeOutcome(ctx, tenant, rec, ProbeOK, "accepted", []string{"coding-a", "coding-b"}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	launch := func(explicit string) runDTO {
		t.Helper()
		dto, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON,
			Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, ProviderProfileRef: prof.Ref, Model: explicit})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
			t.Fatal(err)
		}
		return dto
	}
	for _, chosen := range []string{"coding-a", "coding-b"} {
		if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DefaultModel: &chosen}); err != nil {
			t.Fatal(err)
		}
		dto := launch("")
		stored, err := m.loadRun(ctx, tenant, dto.RunRef)
		if err != nil || gate.last(t).Model != chosen || stored.String(colRunModelRef) != chosen ||
			!strings.Contains(strings.Join(runner.lastSpec().Args, " "), "--model "+chosen) {
			t.Fatalf("effective default diverged: gate=%q row=%q argv=%q err=%v", gate.last(t).Model, stored.String(colRunModelRef), runner.lastSpec().Args, err)
		}
	}
	launch("explicit-model")
	if gate.last(t).Model != "explicit-model" {
		t.Fatal("record default replaced an explicit session model")
	}
	unavailable := "retired-model"
	if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DefaultModel: &unavailable}); err != nil {
		t.Fatal(err)
	}
	before := launchCount(runner)
	_, err := launchBound(m, tenant, prof)
	var coded *codedRunErr
	if !errors.As(err, &coded) || statusOf(err) != http.StatusConflict || coded.code != "provider_default_model_unavailable" ||
		!strings.Contains(err.Error(), "--model") || launchCount(runner) != before {
		t.Fatalf("unavailable default did not refuse actionably before spawn: %v", err)
	}
}

func TestProviderDefaultModelUsesTheExistingWritePermission(t *testing.T) {
	h := newHarness(t, New(WithProviderSecretVault(newFakeVault())))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "model-choice")
	rec := mustCreateRecord(t, h.m, tenant, anthropicInput("Key"))
	viewer := h.viewerToken(admin, tenant, "default-model-viewer@example.test")
	if err := h.st.AuthMutate(t.Context(), func(sc store.AuthScope) error {
		_, err := sc.AuthPolicy().Create(t.Context(), model.AuthPolicy{AdminStepUp: auth.StepUpPasskey})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := "/v1/m/sessions/providers/" + rec.Ref
	if r := h.doJSON("PATCH", path, viewer, map[string]any{"default_model": "coding-model"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("read-only operator changed model: %d %s", r.code, r.raw)
	}
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_model": "coding-model"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("model-only write asked for credential step-up: %d %s", r.code, r.raw)
	}
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_model": "bad\nmodel"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("invalid identifier = %d %s", r.code, r.raw)
	}
}

// The upgrade and first-test transaction must also work when only the separate
// owner can migrate and the application role holds DML rights.
type providerDefaultBackend struct {
	name   string
	config store.Config
}

func providerDefaultBackends(t *testing.T) []providerDefaultBackend {
	t.Helper()
	out := []providerDefaultBackend{{name: "sqlite", config: store.Config{
		Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "defaults.db"), Debug: true,
	}}}
	if enginetest.PostgresAvailable(t) {
		pg := enginetest.IsolatedPostgresSplitOwner(t)
		out = append(out, providerDefaultBackend{name: "postgres", config: store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, Debug: true,
		}})
	} else {
		t.Log("PostgreSQL split-owner NOT exercised: no test runtime configured")
	}
	return out
}

func (be providerDefaultBackend) open(t *testing.T, register func(store.ExtensionRegistry) error) (*Module, store.Store) {
	t.Helper()
	m := New(WithProviderSecretVault(newFakeVault()), WithProviderProbe(&fakeProbe{}))
	m.clock = &testClock{now: baseTime}
	m.UseExecutionEnvironmentRef(testEnvRef)
	if register == nil {
		register = m.RegisterSchema
	}
	st, err := engine.Open(t.Context(), be.config, register)
	if err != nil {
		t.Fatalf("open %s: %v", be.name, err)
	}
	m.UseData(api.NewModuleData(st))
	bindStoreStanding(m, st)
	stopModuleAtCleanup(t, m)
	return m, st
}

type beforeProviderDefaultRegistry struct{ store.ExtensionRegistry }

func (r beforeProviderDefaultRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == providerRecordKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name != colPRDefaultModel {
				fields = append(fields, f)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func TestUsedProviderUpgradeNeverInfersADefault(t *testing.T) {
	for _, be := range providerDefaultBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := t.Context()
			old := New()
			_, st := be.open(t, func(reg store.ExtensionRegistry) error {
				return old.RegisterSchema(beforeProviderDefaultRegistry{reg})
			})
			tenant := ensureTenant(t, st, "default-model-upgrade")
			ref := newProviderRecordRef()
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(providerRecordKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(ctx, model.Record{colPRRef: ref, colPRKind: ProviderKindOpenAICompatible,
					colPRDisplayName: "Used key", colPRBaseURL: "https://gateway.example/v1", colPRSecretRef: "old-locator",
					colPRState: ProviderRecordActive, colPRNameSlot: activeProviderNameSlot(ProviderKindOpenAICompatible, "Used key"),
					colPRProbeState: ProbeOK, colPRModels: `["old-model"]`})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, upgraded := be.open(t, nil)
			defer upgraded.Close()
			rec, err := m.GetProviderRecord(ctx, tenant, ref)
			if err != nil || rec.DefaultModel != nil || rec.SecretRef != "old-locator" {
				t.Fatalf("upgrade changed an already-used key: %+v %v", rec, err)
			}
			after, err := m.recordProbeOutcome(ctx, tenant, rec, ProbeOK, "accepted", []string{"new-model"}, time.Millisecond)
			if err != nil || after.DefaultModel != nil || after.SecretRef != rec.SecretRef {
				t.Fatalf("probe inferred an upgrade default or rewrote the locator: %+v %v", after, err)
			}
			chosen := "new-model"
			after, err = m.PatchProviderRecord(ctx, tenant, ref, ProviderRecordPatch{DefaultModel: &chosen})
			if err != nil || after.SecretRef != rec.SecretRef || after.ProbeState != ProbeOK || len(after.Models) != 1 || after.Models[0] != chosen {
				t.Fatalf("default-only edit reset provider observations or credentials: %+v %v", after, err)
			}
		})
	}
}

func TestNewProviderCodingPreferenceRequiresBoundMembership(t *testing.T) {
	m, _, tenant, _, probe := providerHarness(t)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Coding"))
	probe.result.Models = []string{"image-model", "claude-opus-5-5"}
	after, err := m.TestProviderRecord(t.Context(), tenant, rec.Ref)
	if err != nil || after.DefaultModel == nil || *after.DefaultModel == "" || *after.DefaultModel == "image-model" {
		t.Fatalf("qualified coding preference was not selected from the bound catalog: %+v %v", after, err)
	}
	other := mustCreateRecord(t, m, tenant, anthropicInput("Different access"))
	probe.result.Models = []string{"image-model"}
	after, err = m.TestProviderRecord(t.Context(), tenant, other.Ref)
	if err != nil || after.DefaultModel == nil || *after.DefaultModel != "" {
		t.Fatalf("a preference absent from this credential's catalog was guessed: %+v %v", after, err)
	}
}
