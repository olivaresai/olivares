// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestProviderKeyChangesWriteOneSafeAuditEvent(t *testing.T) {
	vault := newFakeVault()
	m := New(WithProviderSecretVault(vault))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "key-audit")
	key := "sk-openai-SYNTHETIC-KEY-ORIGINAL-0123456789"
	replacement := "sk-openai-SYNTHETIC-KEY-REPLACEMENT-9876543210"
	r := h.doJSON("POST", "/v1/m/sessions/providers", admin, map[string]any{
		"kind": ProviderKindOpenAI, "display_name": "audit fixture", "api_key": key,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("add key: %d %s", r.code, r.raw)
	}
	ref := r.body["provider_ref"].(string)
	record, err := m.GetProviderRecord(t.Context(), model.TenantID(tenant), ref)
	if err != nil {
		t.Fatal(err)
	}
	assertEvent := func(verb string) {
		t.Helper()
		events := acctAuditsOf(t, m, model.TenantID(tenant), "sessions.provider_record."+verb)
		if len(events) != 1 || events[0].targetKind != providerRecordKind || events[0].targetID != record.ID {
			t.Fatalf("%s needs one provider audit: %+v", verb, events)
		}
		meta := events[0].meta
		if len(meta) != 2 || meta["provider_ref"] != ref || meta["kind"] != ProviderKindOpenAI {
			t.Fatalf("%s audit must contain only provider reference and kind: %+v", verb, meta)
		}
	}
	assertEvent("create")
	path := "/v1/m/sessions/providers/" + ref
	if r := h.doJSON("PATCH", path, admin, map[string]any{"display_name": "renamed"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("rename: %d %s", r.code, r.raw)
	}
	assertEvent("create")
	if r := h.doJSON("PATCH", path, admin, map[string]any{"api_key": replacement}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("replace key: %d %s", r.code, r.raw)
	}
	assertEvent("rotate")
	if r := h.doJSON("POST", path+"/revoke", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("remove key: %d %s", r.code, r.raw)
	}
	assertEvent("revoke")
	if r := h.doJSON("POST", path+"/revoke", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("repeat removal: %d %s", r.code, r.raw)
	}
	assertEvent("revoke")
	if err := m.Data.View(t.Context(), model.TenantID(tenant), func(sc store.Scope) error {
		walker := sc.Audit().(store.CanonicalWalker)
		seen := 0
		if err := walker.WalkCanonical(t.Context(), 1, func(ev model.AuditEvent, canonical string, _ []byte) error {
			if strings.Contains(canonical, key) || strings.Contains(canonical, replacement) || strings.Contains(canonical, record.SecretRef) {
				t.Fatal("key material or a private vault locator reached the audit ledger")
			}
			if strings.HasPrefix(ev.Action, "sessions.provider_record.") {
				seen++
				if ev.ActorKind != model.ActorUser || !strings.HasPrefix(ev.Actor, "user:") {
					t.Fatalf("key change has no authenticated actor: %s/%s", ev.ActorKind, ev.Actor)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if seen != 3 {
			t.Fatalf("key lifecycle has %d events, want exactly 3", seen)
		}
		report, err := sc.Audit().Verify(t.Context(), 0)
		if err == nil && !report.OK {
			t.Fatalf("key audit chain does not verify: %s", report.Reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

type providerKeyAuditFaultData struct {
	api.ModuleData
	dropped bool
}

type providerKeyAuditFaultScope struct {
	store.Scope
	dropped bool
}

func (s providerKeyAuditFaultScope) Audit() store.AuditLog {
	return providerKeyAuditFaultLog{AuditLog: s.Scope.Audit(), dropped: s.dropped}
}

type providerKeyAuditFaultLog struct {
	store.AuditLog
	dropped bool
}

func (a providerKeyAuditFaultLog) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if strings.HasPrefix(draft.Action, "sessions.provider_record.") {
		if a.dropped {
			return model.AuditEvent{}, nil
		}
		return model.AuditEvent{}, errors.New("synthetic provider audit failure")
	}
	return a.AuditLog.Append(ctx, draft)
}

func (d providerKeyAuditFaultData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(providerKeyAuditFaultScope{Scope: sc, dropped: d.dropped})
	})
}

func TestProviderKeyChangesRollBackWhenAuditFailsOrIsDropped(t *testing.T) {
	for _, dropped := range []bool{false, true} {
		for _, verb := range []string{"create", "rotate", "revoke"} {
			name := verb + "/failure"
			if dropped {
				name = verb + "/dropped"
			}
			t.Run(name, func(t *testing.T) {
				m, _, tenant, vault, _ := providerHarness(t)
				var before ProviderRecord
				if verb != "create" {
					before = mustCreateRecord(t, m, tenant, anthropicInput("rollback fixture"))
				}
				data := m.Data
				m.Data = providerKeyAuditFaultData{ModuleData: data, dropped: dropped}
				var err error
				switch verb {
				case "create":
					in := anthropicInput("must not persist")
					in.Actor = testActor()
					_, err = m.CreateProviderRecord(t.Context(), tenant, in)
				case "rotate":
					key := "sk-ant-SYNTHETIC-AUDIT-FAILURE-ROTATION-0123456789"
					_, err = m.PatchProviderRecord(t.Context(), tenant, before.Ref, ProviderRecordPatch{APIKey: &key, Actor: testActor()})
				case "revoke":
					_, err = m.RevokeProviderRecord(t.Context(), testActor(), tenant, before.Ref)
				}
				m.Data = data
				if err == nil {
					t.Fatal("key change succeeded without its persisted audit event")
				}
				if verb == "create" {
					rows, _, err := m.ListProviderRecords(t.Context(), tenant, "", "", model.Query{Limit: 10})
					if err != nil || len(rows) != 0 || vault.count() != 0 {
						t.Fatalf("failed audit left a provider row or sealed value: rows=%d vault=%d err=%v", len(rows), vault.count(), err)
					}
				} else {
					after, err := m.GetProviderRecord(t.Context(), tenant, before.Ref)
					if err != nil || after.State != before.State || after.SecretRef != before.SecretRef || after.Version != before.Version || vault.count() != 1 {
						t.Fatalf("failed audit changed the live credential tuple: before=%+v after=%+v vault=%d err=%v", before, after, vault.count(), err)
					}
					key, err := vault.Open(t.Context(), tenant, before.SecretRef)
					if err != nil || string(key) != testProviderKey {
						t.Fatal("failed audit withdrew the live key")
					}
				}
				if events := acctAuditsOf(t, m, tenant, "sessions.provider_record."+verb); len(events) != 0 {
					t.Fatalf("failed mutation left %d committed %s events", len(events), verb)
				}
			})
		}
	}
}
