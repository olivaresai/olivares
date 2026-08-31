// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func runEventing(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newEventingCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestEventingSubscriptionsListTextAndJSON(t *testing.T) {
	// was a fabricated tenant id, which only listed cleanly because a tenant
	// with no org row used to be served an empty list. The subject here is the text
	// and JSON shapes of an EMPTY listing, so it needs a real tenant that has no
	// subscriptions — not a tenant that does not exist.
	dir, tenant := seededTenantDataDir(t)

	textOut, err := runEventing(t, "subscriptions", "ls", "--tenant", tenant, "--data-dir", dir)
	if err != nil {
		t.Fatalf("eventing subscriptions ls text: %v\n%s", err, textOut)
	}
	if textOut != "no subscriptions\n" {
		t.Fatalf("eventing subscriptions text output changed: %q", textOut)
	}

	jsonOut, err := runEventing(t, "subscriptions", "ls", "--tenant", tenant, "--data-dir", dir, "--format", "json")
	if err != nil {
		t.Fatalf("eventing subscriptions ls json: %v\n%s", err, jsonOut)
	}
	var items []eventingSubscriptionListItem
	if err := json.Unmarshal([]byte(jsonOut), &items); err != nil {
		t.Fatalf("eventing subscriptions JSON is invalid: %v\n%s", err, jsonOut)
	}
	if len(items) != 0 {
		t.Fatalf("eventing subscriptions JSON = %#v, want an empty list", items)
	}
}

func TestEventingTenantResolution(t *testing.T) {
	// this used a FABRICATED tenant id and asserted "no subscriptions". That
	// only worked because a request naming a tenant with no org row was served an
	// empty list — the hole the suspension guard closed ("served nothing" where the
	// honest answer is "not served"). The subject of this test is that
	// $OLIVARES_TENANT replaces the flag, so it now resolves a tenant that exists;
	// the assertion it makes is unchanged.
	tenantDir, tenant := seededTenantDataDir(t)

	t.Run("missing flag and environment returns resolver error", func(t *testing.T) {
		t.Setenv("OLIVARES_TENANT", "")
		t.Setenv("OLIVARES_HOOK_PEP_TENANT", "")

		_, err := runEventing(t, "subscriptions", "ls", "--data-dir", initialisedDataDir(t))
		const want = "tenant required: pass --tenant or set $OLIVARES_TENANT"
		if err == nil || err.Error() != want {
			t.Fatalf("eventing without tenant error = %v, want %q", err, want)
		}
	})

	t.Run("OLIVARES_TENANT replaces the omitted flag", func(t *testing.T) {
		t.Setenv("OLIVARES_TENANT", tenant)
		t.Setenv("OLIVARES_HOOK_PEP_TENANT", "")

		out, err := runEventing(t, "subscriptions", "ls", "--data-dir", tenantDir)
		if err != nil {
			t.Fatalf("eventing with OLIVARES_TENANT: %v\n%s", err, out)
		}
		if out != "no subscriptions\n" {
			t.Fatalf("eventing environment fallback output = %q, want no subscriptions", out)
		}
	})
}

// seededTenantDataDir boots a data dir and provisions ONE real business org in it,
// returning the dir and that org's tenant id. It exists because a tenant with no
// org row is no longer served an empty list: the suspension guard denies
// closed on a missing org, so a CLI test that wants a successful listing needs a
// tenant that actually exists.
func seededTenantDataDir(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("seed data dir %s: %v", dir, err)
	}
	var tenant string
	if serr := eng.store.System(context.Background(), func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(context.Background(), model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = o.TenantID.String()
		return nil
	}); serr != nil {
		t.Fatalf("seed org: %v", serr)
	}
	if cerr := eng.Close(); cerr != nil {
		t.Fatalf("close seeded engine: %v", cerr)
	}
	return dir, tenant
}

// TestEventingSubscriptionsGetShowsWhatListCannot is the witness for the verb C08-04
// named as an open gap: the API has served GET /v1/m/eventing/subscriptions/{id} since
// (modules/eventing/eventing.go:465) and the CLI could only LIST.
//
// `ls` truncates the endpoint to 50 columns and has no column at all for the retry
// policy, the auth shape or the secret hint. A `get` that showed the same fields would
// be `ls` with one row, so what this asserts is precisely the fields `ls` cannot show.
//
// The row is SEEDED rather than created through the CLI on purpose: `create` is behind
// the egress writer fence, which refuses an unauthorized destination — correctly. The
// subject here is `get`, and making the test authorize a destination would have made it
// a test of the fence with a `get` at the end.
func TestEventingSubscriptionsGetShowsWhatListCannot(t *testing.T) {
	dir, tenant := seededTenantDataDir(t)
	id := seedSubscriptionRow(t, dir, tenant, "https://example.invalid/hook", "sealed-secret-value")

	jsonOut, err := runEventing(t, "subscriptions", "get", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--format", "json")
	if err != nil {
		t.Fatalf("get json: %v\n%s", err, jsonOut)
	}
	var got eventingSubscriptionDetail
	if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
		t.Fatalf("get JSON is invalid: %v\n%s", err, jsonOut)
	}
	if got.ID != id {
		t.Fatalf("get returned %q, want the seeded %q", got.ID, id)
	}
	if got.Endpoint != "https://example.invalid/hook" {
		t.Fatalf("get must show the endpoint UNTRUNCATED, got %q", got.Endpoint)
	}

	// THE SECRET NEVER LEAVES. The record carries evtColSubSecret; this verb reads the
	// HINT and never the value. `create` already says the secret is printed once, so a
	// read verb that reprinted it would quietly undo that promise. Both renderings are
	// checked: a struct with no secret field says nothing about what the text writer does.
	const sealed = "sealed-secret-value"
	if strings.Contains(jsonOut, sealed) {
		t.Fatalf("get -o json leaked the delivery secret:\n%s", jsonOut)
	}
	textOut, err := runEventing(t, "subscriptions", "get", "--tenant", tenant, "--data-dir", dir, "--id", id)
	if err != nil {
		t.Fatalf("get text: %v\n%s", err, textOut)
	}
	if strings.Contains(textOut, sealed) {
		t.Fatalf("get text output leaked the delivery secret:\n%s", textOut)
	}
	// The fields that justify the verb existing at all.
	for _, want := range []string{"MAX_ATTEMPTS", "INITIAL_INTERVAL", "AUTH_TYPE", "SECRET_HINT", "SOURCES"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("get text output is missing %s, which is a field ls cannot show:\n%s", want, textOut)
		}
	}

	// Deny direction: an id that does not exist must FAIL, not print an empty shell that
	// a script would read as a subscription with blank fields.
	missingOut, err := runEventing(t, "subscriptions", "get", "--tenant", tenant,
		"--data-dir", dir, "--id", "sub-does-not-exist")
	if err == nil {
		t.Fatalf("get on a missing id must fail, got:\n%s", missingOut)
	}
}
