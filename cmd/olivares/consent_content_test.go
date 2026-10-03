// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestContentErasureNeedsItsDeclaredReader(t *testing.T) {
	leftover := unreadCounted(t, openCensus(t, nil))
	for _, tc := range []struct {
		name, reader            string
		read, duplicate, opaque bool
		wantReady               bool
	}{
		{name: "covered", reader: "fixture", read: true, wantReady: true},
		{name: "unread", reader: "fixture"},
		{name: "wrong reader", reader: "other", read: true},
		{name: "duplicate readers", reader: "fixture", read: true, duplicate: true},
		{name: "unknown payload", reader: "fixture", read: true, opaque: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := model.Ref(model.EncodeUserID, model.ClassEvidence)
			c.ContentErasure = &model.ContentErasure{Rule: "delete-subject-row: cmd/olivares/consent_content_test.go:1", Reader: tc.reader}
			fields := []model.FieldSpec{{Name: "owner_ref", Kind: model.KindText, Principal: c}}
			if tc.opaque {
				fields = append(fields, model.FieldSpec{Name: "payload", Kind: model.KindBytes})
			}
			st := openCensus(t, func(reg store.ExtensionRegistry) error {
				if err := reg.(store.CompositionContributionRegistry).DeclareCompositionContribution(store.CompositionContribution{Edition: "fixture", Modules: []string{"fixture"}, OutsideStores: []model.EntityDescriptor{{Kind: "fixture.personal", Table: "fixture_personal", Fields: fields}}}); err != nil {
					return err
				}
				cols := append([]string(nil), leftover...)
				if tc.read {
					cols = append(cols, "fixture.personal.owner_ref")
				}
				if len(cols) > 0 {
					if err := reg.(store.CompositionReaderRegistry).DeclareCompositionReader("fixture", cols); err != nil {
						return err
					}
				}
				if tc.duplicate {
					return reg.(store.CompositionReaderRegistry).DeclareCompositionReader("second", []string{"fixture.personal.owner_ref"})
				}
				return nil
			})
			r := st.(store.CompositionCensus).CompositionReadiness()
			if r.Ready() != tc.wantReady {
				t.Fatalf("content readiness=%v, want %v: %v", r.Ready(), tc.wantReady, r.Causes)
			}
			if !tc.wantReady && !strings.Contains(strings.Join(r.Causes, ";"), "fixture.personal") {
				t.Fatalf("unready omitted the actual store: %v", r.Causes)
			}
		})
	}
}

type contentReceiptStep struct {
	st   store.Store
	name string
	fail bool
}

func (s contentReceiptStep) Module() string { return s.name }
func (s contentReceiptStep) RetireUser(ctx context.Context, req auth.RetirementRequest) (out auth.RetirementOutcome, err error) {
	err = s.st.Mutate(ctx, req.Tenant, func(sc store.Scope) error { out.FactVersion, err = auth.PinRetirement(ctx, sc, req); return err })
	if err == nil && s.fail {
		out.Limits = []string{"fixture store unreadable"}
		err = errors.New("fixture reader failed")
	}
	return
}

func TestThirdPartyTextIsAnExplicitRetirementLimit(t *testing.T) {
	testThirdPartyTextReceipt(t, "fixture", "", false)
}

func TestThirdPartyTextLimitSurvivesIncompleteCompositionAndReaderNames(t *testing.T) {
	for _, tc := range []struct {
		name, reader, fault string
		wantIncomplete      bool
	}{
		{"missing step", "fixture", "missing-step", true},
		{"unknown payload", "fixture", "unknown-payload", true},
		{"reader named composition", "composition", "", false},
		{"failed reader named composition", "composition", "reader-error", true},
	} {
		t.Run(tc.name, func(t *testing.T) { testThirdPartyTextReceipt(t, tc.reader, tc.fault, tc.wantIncomplete) })
	}
}

func testThirdPartyTextReceipt(t *testing.T, reader, fault string, wantIncomplete bool) {
	t.Helper()
	ctx := context.Background()
	leftover := unreadCounted(t, openCensus(t, nil))
	fields := []model.FieldSpec{{Name: "prose", Kind: model.KindText, Principal: model.Scan(model.ClassThirdPartyText)}}
	if fault == "unknown-payload" {
		fields = append(fields, model.FieldSpec{Name: "payload", Kind: model.KindBytes})
	}
	st := openCensus(t, func(reg store.ExtensionRegistry) error {
		if err := reg.(store.CompositionContributionRegistry).DeclareCompositionContribution(store.CompositionContribution{Edition: "fixture", OutsideStores: []model.EntityDescriptor{{Kind: "fixture.notes", Table: "fixture_notes", Fields: fields}}}); err != nil {
			return err
		}
		if len(leftover) > 0 {
			return reg.(store.CompositionReaderRegistry).DeclareCompositionReader(reader, leftover)
		}
		return nil
	})
	if r := st.(store.CompositionCensus).CompositionReadiness(); r.Ready() != (fault != "unknown-payload") {
		t.Fatalf("actual complete registry is unready: %v", r.Causes)
	}
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	t.Cleanup(func() {
		auth.SetTestHashParams(auth.DefaultArgonMemKiB, auth.DefaultArgonTime, auth.DefaultArgonThreads)
	})
	a := auth.NewAuthenticator(st, nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@content.test", "content-fixture-password"); err != nil {
		t.Fatal(err)
	}
	bearer, _, err := a.Login(ctx, "root@content.test", "content-fixture-password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := a.Authenticate(ctx, bearer)
	if err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(ctx, model.Org{Name: "Content", Slug: "content", Status: model.StatusActive})
		tenant = o.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var user model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Create(ctx, model.User{Email: "subject@content.test", Status: model.StatusActive})
		if err != nil {
			return err
		}
		user = u.ID
		if _, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: tenant, Role: auth.RoleViewer}); err != nil {
			return err
		}
		_, err = a.OffboardFromTenant(ctx, as, actor, user, tenant, "content fixture")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range st.(store.CompositionReaders).CompositionReaders() {
		names = append(names, name)
	}
	sort.Strings(names)
	var steps []auth.RetirementStep
	for _, name := range names {
		if fault == "missing-step" && name == reader {
			continue
		}
		steps = append(steps, contentReceiptStep{st: st, name: name, fail: fault == "reader-error" && name == reader})
	}
	pass, err := a.AdvanceRetirement(ctx, steps, user, tenant)
	if (err != nil) != wantIncomplete {
		t.Fatalf("retirement error=%v wantIncomplete=%v", err, wantIncomplete)
	}
	if wantIncomplete {
		// A failed pass preserves its prior active state and schedules a retry.
		// Both retiring and blocked retain the same unlifted exclusion fence.
		state := pass.Record.RetirementState
		if state != model.RetirementRetiring && state != model.RetirementBlocked {
			t.Fatalf("incomplete pass settled the retirement: %+v", pass.Record)
		}
		if excluded, err := a.SubjectExcluded(ctx, tenant, user); err != nil || !excluded {
			t.Fatalf("incomplete pass lost its exclusion fence: excluded=%v err=%v", excluded, err)
		}
	} else if pass.Record.RetirementState != model.RetirementRetired {
		t.Fatalf("nonblocking limit prevented authority retirement: %+v", pass.Record)
	}
	var results map[string]struct {
		State  string   `json:"state"`
		Limits []string `json:"limits"`
	}
	if err := json.Unmarshal([]byte(pass.Record.ModuleResults), &results); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range results {
		for _, limit := range result.Limits {
			if limit == "text by others not scanned: fixture.notes.prose" {
				found = true
				if result.State == "erased" {
					t.Fatal("third-party text falsely counted as erased")
				}
			}
		}
	}
	if fault == "reader-error" {
		ownLimit := false
		for _, limit := range results[reader].Limits {
			ownLimit = ownLimit || limit == "fixture store unreadable"
		}
		if !ownLimit {
			t.Fatalf("reader's own limit lost: %s", pass.Record.ModuleResults)
		}
	}
	if !found {
		t.Fatalf("retirement omitted third-party prose limit: %s", pass.Record.ModuleResults)
	}
}
