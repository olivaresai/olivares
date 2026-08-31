// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type fakeOrgVisibility struct {
	orgs          []model.Org
	authoritative bool
	err           error
	calls         int
}

func (f *fakeOrgVisibility) ListOrgsVisible(context.Context) ([]model.Org, bool, error) {
	f.calls++
	return f.orgs, f.authoritative, f.err
}

// A DEPLOYMENT THE DOCS CALL SUPPORTED MUST BE ABLE TO BOOT.
//
// `deploy/postgres/01-app-role.sql:69-72` says the BYPASSRLS admin role may be omitted
// for a single-tenant deployment and that "the engine then LOGS that cross-tenant reads
// are RLS-limited". Reading the inventory with ListOrgs made that a refusal instead: the
// read answers ErrEnumerationNotAuthoritative and the error aborted the leader election,
// so the node never promoted. Measured on 2026-08-24 in e2e-operator-kind (job
// 97315647128) -- 8 of 8 runs red on exactly that.
func TestAnRLSLimitedTenantInventoryIsToleratedAndSaidOutLoud(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	seen := model.Org{BaseFields: model.BaseFields{ID: model.NewID()}, Slug: "visible"}
	f := &fakeOrgVisibility{orgs: []model.Org{seen}, authoritative: false}

	orgs, err := pdpVisibleTenantInventory(context.Background(), f, log)
	if err != nil {
		t.Fatalf("an RLS-limited inventory REFUSED the promotion: %v\n"+
			"  a Postgres deployment with no --admin-dsn is documented as supported "+
			"(deploy/postgres/01-app-role.sql:69-72) and would never reach leadership.", err)
	}
	if len(orgs) != 1 || orgs[0].ID != seen.ID {
		t.Fatalf("the tenants this pool CAN see were dropped: %+v", orgs)
	}
	// Tolerating in silence would be the other half of the same defect: the operator
	// must learn that the tenants outside this pool keep their seams closed.
	out := logged.String()
	for _, want := range []string{"RLS-limited", "CLOSED", "--admin-dsn"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the tolerance was silent: the log does not mention %q.\n  log: %s", want, out)
		}
	}
}

// And the tolerance is NARROW: a read that actually failed still travels.
func TestARealInventoryReadErrorIsNotTolerated(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	boom := errors.New("connection refused")
	f := &fakeOrgVisibility{err: boom}

	_, err := pdpVisibleTenantInventory(context.Background(), f, log)
	if !errors.Is(err, boom) {
		t.Fatalf("a genuine read failure was swallowed: %v", err)
	}
}

// An AUTHORITATIVE inventory must not log the limitation -- a warning that fires on
// every healthy boot is a warning nobody reads.
func TestAnAuthoritativeInventoryDoesNotWarn(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := &fakeOrgVisibility{orgs: []model.Org{{BaseFields: model.BaseFields{ID: model.NewID()}}}, authoritative: true}

	if _, err := pdpVisibleTenantInventory(context.Background(), f, log); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logged.String(), "RLS-limited") {
		t.Fatalf("a complete inventory warned about being partial: %s", logged.String())
	}
	_ = store.ErrEnumerationNotAuthoritative
}
