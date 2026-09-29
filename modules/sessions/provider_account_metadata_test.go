// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestProviderAccount_MetadataPreservesIdentityAndAuditsOnce(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "metadata-"+be.name)
			a := newAcctAPI(m, st, tenant)
			ref := acctProfiles(t, m, tenant, "claude", 1)[0].Ref
			if r := a.adopt(ref, "claude-b"); r.code != http.StatusOK {
				t.Fatalf("adopt = %d %s", r.code, r.raw)
			}
			before, digest := acctRow(t, m, tenant, ref), acctDigest(t, m, tenant, ref)
			path := "/provider-accounts/" + ref
			var firstUpdate model.Record
			for _, input := range []string{"  Research account  ", "Research account"} {
				r := a.call(http.MethodPatch, path, map[string]any{"display_name": input})
				if r.code != http.StatusOK || r.body["display_name"] != "Research account" || r.body["name"] != "claude-b" || r.body["account_ref"] != ref {
					t.Fatalf("patch = %d %s", r.code, r.raw)
				}
				if strings.Contains(r.raw, before.String(colPPConfigHome)) || strings.Contains(r.raw, before.String(colPPUserHome)) {
					t.Fatal("metadata response disclosed a home")
				}
				current := acctRow(t, m, tenant, ref)
				if firstUpdate == nil {
					firstUpdate = current
				} else if !reflect.DeepEqual(current, firstUpdate) {
					t.Fatal("normalized retry changed the row, version or timestamp")
				}
			}
			after := acctRow(t, m, tenant, ref)
			for key, value := range before {
				if key != colPPDisplayName && key != model.ColUpdatedAt && key != model.ColVersion && !reflect.DeepEqual(after[key], value) {
					t.Fatalf("metadata changed %s: %v -> %v", key, value, after[key])
				}
			}
			if got := acctDigest(t, m, tenant, ref); got != digest {
				t.Fatal("metadata changed the launch digest")
			}
			events := acctAuditsOf(t, m, tenant, "sessions.provider_account.metadata_updated")
			if len(events) != 1 || events[0].meta["display_name"] != "Research account" || events[0].targetID != model.ID(before.String(model.ColID)) {
				t.Fatalf("metadata audit = %+v", events)
			}
			if r := a.call(http.MethodGet, path, nil); r.body["display_name"] != "Research account" {
				t.Fatalf("get omits label: %s", r.raw)
			}
			items := a.list("").items()
			if len(items) != 1 || items[0]["display_name"] != "Research account" {
				t.Fatalf("list omits label: %v", items)
			}
			// Clearing is an explicit mutation; repeating it is a no-op.
			for range 2 {
				if r := a.call(http.MethodPatch, path, map[string]any{"display_name": ""}); r.code != http.StatusOK || r.body["display_name"] != nil {
					t.Fatalf("clear = %d %s", r.code, r.raw)
				}
			}
			if got := len(acctAuditsOf(t, m, tenant, "sessions.provider_account.metadata_updated")); got != 2 {
				t.Fatalf("audit count = %d, want 2", got)
			}
		})
	}
}

// Fail after the real mutation callback has updated the row and appended audit,
// while still inside the store transaction. Both writes must roll back together.
type metadataRollbackData struct{ api.ModuleData }

type metadataDroppedAudit struct{ store.AuditLog }

func (metadataDroppedAudit) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, nil
}

type metadataDroppedAuditScope struct{ store.Scope }

func (metadataDroppedAuditScope) Audit() store.AuditLog { return metadataDroppedAudit{} }

func TestProviderAccount_MetadataRefusesDroppedAudit(t *testing.T) {
	if err := appendAccountAudit(context.Background(), metadataDroppedAuditScope{}, testActor(), "metadata_updated", ProviderAccount{}); err == nil {
		t.Fatal("metadata accepted a dropped audit event")
	}
}

func (d metadataRollbackData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := fn(sc); err != nil {
			return err
		}
		return errors.New("metadata transaction refused")
	})
}

func TestProviderAccount_MetadataRollsBackWithItsAudit(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "metadata-rollback-"+be.name)
			a := newAcctAPI(m, st, tenant)
			ref := acctProfiles(t, m, tenant, "claude", 1)[0].Ref
			if r := a.adopt(ref, "claude-b"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			before := acctRow(t, m, tenant, ref)
			data := m.data
			m.data = metadataRollbackData{data}
			r := a.call(http.MethodPatch, "/provider-accounts/"+ref, map[string]any{"display_name": "must roll back", "accent": "blue"})
			m.data = data
			if r.code < 500 {
				t.Fatalf("failed transaction = %d %s", r.code, r.raw)
			}
			if !reflect.DeepEqual(acctRow(t, m, tenant, ref), before) {
				t.Fatal("failed transaction left metadata")
			}
			if len(acctAuditsOf(t, m, tenant, "sessions.provider_account.metadata_updated")) != 0 {
				t.Fatal("failed transaction left audit")
			}
		})
	}
}

func TestProviderAccount_MetadataRequiresUnconfinedAccountWrite(t *testing.T) {
	for _, be := range acctConfinedBackends(t) {
		for _, scoped := range []bool{false, true} {
			t.Run(be.name+map[bool]string{false: "/flat", true: "/scoped"}[scoped], func(t *testing.T) {
				f := newAccountHomeAuthorityFixture(t, be.config(t), scoped)
				ctx := context.Background()
				admin := f.adminLogin()
				tenant := f.createOrg(admin, "metadata-authority")
				var workspace model.ID
				if err := f.st.Mutate(ctx, tenant, func(sc store.Scope) error {
					ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Limited", Slug: "limited", Status: model.StatusActive})
					workspace = ws.ID
					return err
				}); err != nil {
					t.Fatal(err)
				}
				confined := f.member(t, admin, tenant, "confined@metadata.test", workspace)
				viewer := f.viewerToken(admin, tenant, "viewer@metadata.test")
				ref := acctProfiles(t, f.m, tenant, "claude", 1)[0].Ref
				base := "/v1/m/sessions/provider-accounts/"
				if r := f.doJSON(http.MethodPost, base+ref+"/adopt", admin, map[string]any{"name": "claude-b"}, tenantHdr(tenant)); r.code != http.StatusOK {
					t.Fatal(r.raw)
				}
				before := acctRow(t, f.m, tenant, ref)
				for _, token := range []string{confined, viewer} {
					for _, target := range []string{ref, newProfileRef()} {
						r := f.doJSON(http.MethodPatch, base+target, token, map[string]any{"display_name": "forbidden"}, tenantHdr(tenant))
						if r.code != http.StatusForbidden {
							t.Fatalf("unauthorized metadata = %d %s", r.code, r.raw)
						}
					}
				}
				if !reflect.DeepEqual(acctRow(t, f.m, tenant, ref), before) {
					t.Fatal("denied actor changed metadata")
				}
				if r := f.doJSON(http.MethodPatch, base+ref, admin, map[string]any{"display_name": "Allowed"}, tenantHdr(tenant)); r.code != http.StatusOK {
					t.Fatalf("authorized control = %d %s", r.code, r.raw)
				}
			})
		}
	}
}

func TestProviderAccount_MetadataRefusesInvalidOrUnownedRows(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "metadata-refuse-"+be.name)
			other := ensureTenant(t, st, "metadata-other-"+be.name)
			a := newAcctAPI(m, st, tenant)
			profiles := acctProfiles(t, m, tenant, "claude", 2)
			ref := profiles[0].Ref
			if r := a.adopt(ref, "claude-b"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			path := "/provider-accounts/" + ref
			before := acctRow(t, m, tenant, ref)
			for _, body := range []any{nil, map[string]any{}, map[string]any{"display_name": nil}, map[string]any{"display_name": 7}, map[string]any{"display_name": "x", "name": "replace"}, map[string]any{"display_name": "a\x00b"}, map[string]any{"display_name": strings.Repeat("é", 101)}} {
				if r := a.call(http.MethodPatch, path, body); r.code != http.StatusBadRequest {
					t.Fatalf("invalid body %v = %d %s", body, r.code, r.raw)
				}
			}
			for _, target := range []string{profiles[1].Ref, newProfileRef(), "malformed"} {
				if r := a.call(http.MethodPatch, "/provider-accounts/"+target, map[string]any{"display_name": "x"}); r.code != http.StatusNotFound {
					t.Fatalf("unknown or unnamed = %d %s", r.code, r.raw)
				}
			}
			if r := newAcctAPI(m, st, other).call(http.MethodPatch, path, map[string]any{"display_name": "x"}); r.code != http.StatusNotFound {
				t.Fatalf("other tenant = %d %s", r.code, r.raw)
			}
			if got := acctRow(t, m, tenant, ref); !reflect.DeepEqual(got, before) {
				t.Fatal("refused request changed row")
			}
			if len(acctAuditsOf(t, m, tenant, "sessions.provider_account.metadata_updated")) != 0 {
				t.Fatal("refused request appended audit")
			}
			state := ProfileDisabled
			if _, err := m.PatchProfile(context.Background(), tenant, ref, ProfilePatch{State: &state}); err != nil {
				t.Fatal(err)
			}
			if r := a.call(http.MethodPatch, path, map[string]any{"display_name": strings.Repeat("é", 100)}); r.code != http.StatusOK {
				t.Fatalf("disabled and 200-byte label = %d %s", r.code, r.raw)
			}
			state = ProfileRetired
			if _, err := m.PatchProfile(context.Background(), tenant, ref, ProfilePatch{State: &state}); err != nil {
				t.Fatal(err)
			}
			if r := a.call(http.MethodPatch, path, map[string]any{"display_name": "x"}); r.code != http.StatusConflict {
				t.Fatalf("retired = %d %s", r.code, r.raw)
			}
		})
	}
}

func TestProviderAccount_MetadataPartialAccentAndConcurrentUpdates(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "metadata-accent-"+be.name)
			a := newAcctAPI(m, st, tenant)
			ref := acctProfiles(t, m, tenant, "claude", 1)[0].Ref
			if r := a.adopt(ref, "claude-b"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			path := "/provider-accounts/" + ref
			before, digest := acctRow(t, m, tenant, ref), acctDigest(t, m, tenant, ref)
			if before.String(colPPAccent) != "" {
				t.Fatal("new/legacy profile has an invented accent")
			}
			// Two concurrent partial writes must preserve each other's distinct field.
			var wg sync.WaitGroup
			replies := make(chan acctResp, 2)
			start := make(chan struct{})
			for _, body := range []map[string]any{{"display_name": "Research"}, {"accent": " blue "}} {
				wg.Add(1)
				go func(body map[string]any) { defer wg.Done(); <-start; replies <- a.call(http.MethodPatch, path, body) }(body)
			}
			close(start)
			wg.Wait()
			close(replies)
			for reply := range replies {
				if reply.code != http.StatusOK {
					t.Fatalf("partial patch = %d %s", reply.code, reply.raw)
				}
			}
			after := acctRow(t, m, tenant, ref)
			if after.String(colPPDisplayName) != "Research" || after.String(colPPAccent) != "blue" {
				t.Fatalf("partial update lost a field: %v", after)
			}
			if got := acctDigest(t, m, tenant, ref); got != digest {
				t.Fatal("accent changed K4")
			}
			for _, route := range []string{path, "/provider-profiles/" + ref} {
				if reply := a.call(http.MethodGet, route, nil); reply.code != http.StatusOK || reply.body["accent"] != "blue" || reply.body["display_name"] != "Research" {
					t.Fatalf("metadata read = %d %s", reply.code, reply.raw)
				}
			}
			for key, value := range before {
				if key != colPPDisplayName && key != colPPAccent && key != model.ColUpdatedAt && key != model.ColVersion && !reflect.DeepEqual(after[key], value) {
					t.Fatalf("accent changed %s", key)
				}
			}
			for _, body := range []map[string]any{{"accent": "blue"}, {"display_name": " Research ", "accent": "blue"}} {
				if reply := a.call(http.MethodPatch, path, body); reply.code != http.StatusOK {
					t.Fatal(reply.raw)
				}
				if !reflect.DeepEqual(acctRow(t, m, tenant, ref), after) {
					t.Fatal("normalized partial no-op changed row")
				}
			}
			events := acctAuditsOf(t, m, tenant, "sessions.provider_account.metadata_updated")
			if len(events) != 2 {
				t.Fatalf("audit count = %d", len(events))
			}
			if reply := a.call(http.MethodPatch, path, map[string]any{"accent": ""}); reply.code != http.StatusOK || reply.body["accent"] != nil || reply.body["display_name"] != "Research" {
				t.Fatalf("clear changed label: %d %s", reply.code, reply.raw)
			}
			for _, body := range []map[string]any{{"accent": nil}, {"accent": 7}, {"accent": "violet"}, {"accent": "var(--bad)"}, {"accent": nil, "display_name": "valid"}, {"display_name": nil, "accent": "green"}, {"accent": "green", "unknown": true}} {
				current := acctRow(t, m, tenant, ref)
				if reply := a.call(http.MethodPatch, path, body); reply.code != http.StatusBadRequest {
					t.Fatalf("invalid partial patch = %d %s", reply.code, reply.raw)
				}
				if !reflect.DeepEqual(acctRow(t, m, tenant, ref), current) {
					t.Fatal("invalid partial patch changed row")
				}
			}
			if len(acctAuditsOf(t, m, tenant, "sessions.provider_account.metadata_updated")) != 3 {
				t.Fatal("invalid patch appended audit")
			}
		})
	}
}

// Model the deployed descriptor before accent, then exercise the real reconciler.
type metadataBeforeAccentRegistry struct{ store.ExtensionRegistry }

func (r metadataBeforeAccentRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == providerProfileKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, field := range d.Fields {
			if field.Name != colPPAccent {
				fields = append(fields, field)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}
func TestProviderAccount_MetadataAccentAdditiveUpgrade(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			old, st := openProfileModule(t, be, func(reg store.ExtensionRegistry) error {
				return New().RegisterSchema(metadataBeforeAccentRegistry{reg})
			})
			tenant := ensureTenant(t, st, "accent-upgrade-"+be.name)
			ref := acctProfiles(t, old, tenant, "claude", 1)[0].Ref
			if reply := newAcctAPI(old, st, tenant).adopt(ref, "claude-b"); reply.code != http.StatusOK {
				t.Fatal(reply.raw)
			}
			before, digest := acctRow(t, old, tenant, ref), acctDigest(t, old, tenant, ref)
			if acctProfileColumns(t, be)[colPPAccent] {
				t.Fatal("old descriptor already has accent")
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			for boot := 0; boot < 2; boot++ {
				m, upgraded := openProfileModule(t, be, nil)
				if !acctProfileColumns(t, be)[colPPAccent] {
					t.Fatal("upgrade omitted accent column")
				}
				row := acctRow(t, m, tenant, ref)
				if !row.IsNull(colPPAccent) {
					t.Fatal("upgrade invented a stored accent")
				}
				for key, value := range before {
					if !reflect.DeepEqual(row[key], value) {
						t.Fatalf("upgrade changed %s", key)
					}
				}
				if acctDigest(t, m, tenant, ref) != digest {
					t.Fatal("upgrade changed K4")
				}
				if dto := m.toProfileDTO(profileFromRecord(row)); dto.Accent != "" {
					t.Fatal("profile DTO invented accent")
				}
				if reply := newAcctAPI(m, upgraded, tenant).call(http.MethodGet, "/provider-accounts/"+ref, nil); reply.body["accent"] != nil {
					t.Fatal("legacy account invented accent")
				}
				if err := upgraded.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestProviderAccount_MetadataAccentPalette(t *testing.T) {
	for _, value := range []string{"", "orange", "green", "amber", "red", "blue"} {
		got, err := normalizeAccountAccent(value)
		if err != nil || got != value {
			t.Fatalf("valid palette value %q = %q %v", value, got, err)
		}
	}
	for _, value := range []string{"BLUE", "#fff", "url(x)", "red\x00"} {
		if _, err := normalizeAccountAccent(value); err == nil {
			t.Fatalf("invalid palette value %q accepted", value)
		}
	}
}
