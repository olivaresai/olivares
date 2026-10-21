// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// fakeNotifyDestinations records what the handlers pass and returns canned outputs, so
// these tests exercise the transport and the superadmin/AAL3 gate without a runtime.
type fakeNotifyDestinations struct {
	list      api.NotifyDestinationList
	testErr   error
	putErr    error
	deleteErr error
	gotPut    api.NotifyDestinationInput
	gotTest   api.NotifyDestinationInput
	gotDelete string
}

func (f *fakeNotifyDestinations) List(context.Context) (api.NotifyDestinationList, error) {
	return f.list, nil
}

func (f *fakeNotifyDestinations) Test(_ context.Context, _ auth.Principal, in api.NotifyDestinationInput) error {
	f.gotTest = in
	return f.testErr
}

func (f *fakeNotifyDestinations) Put(_ context.Context, _ auth.Principal, in api.NotifyDestinationInput) (api.NotifyDestination, error) {
	f.gotPut = in
	if f.putErr != nil {
		return api.NotifyDestination{}, f.putErr
	}
	return api.NotifyDestination{Name: in.Name, Kind: in.Kind, Source: api.NotifyDestinationSourceConsole, Live: true, WebhookHost: "hooks.slack.com"}, nil
}

func (f *fakeNotifyDestinations) Delete(_ context.Context, _ auth.Principal, name string) error {
	f.gotDelete = name
	return f.deleteErr
}

func newNotifyDestinationsHarness(t *testing.T, fake *fakeNotifyDestinations) *harness {
	return newHarnessOpts(t, func(o *api.Options) { o.NotifyDestinations = fake })
}

func TestNotifyDestinationsListTestPutDelete(t *testing.T) {
	fake := &fakeNotifyDestinations{list: api.NotifyDestinationList{
		Destinations: []api.NotifyDestination{{Name: "oncall", Kind: "slack", Source: "console", Live: true, WebhookHost: "hooks.slack.com"}},
		Kinds:        []api.NotifyDestinationKind{{Kind: "slack", Title: "Slack"}, {Kind: "teams", Title: "Microsoft Teams"}},
	}}
	h := newNotifyDestinationsHarness(t, fake)
	h.requirePasskeyStepUp()
	admin := h.adminLogin()

	// A read needs a superadmin but no AAL3, and never carries a webhook URL.
	r := h.do("GET", "/v1/console/notify-destinations", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	if dests, _ := r.body["destinations"].([]any); len(dests) != 1 || dests[0].(map[string]any)["name"] != "oncall" {
		t.Fatalf("destinations = %v", r.body["destinations"])
	}
	if kinds, _ := r.body["kinds"].([]any); len(kinds) != 2 {
		t.Fatalf("kinds = %v, want slack and teams", r.body["kinds"])
	}

	put := map[string]any{"name": "oncall", "kind": "slack", "webhook_url": "https://hooks.slack.com/services/T0/B0/XXXX"}
	// Every write and the test send need the AAL3 step-up: they carry the secret.
	for _, c := range []struct{ method, path string }{
		{"PUT", "/v1/console/notify-destinations"},
		{"POST", "/v1/console/notify-destinations/test"},
		{"DELETE", "/v1/console/notify-destinations"},
	} {
		r := h.do(c.method, c.path, admin, put, nil)
		if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "step_up_required" {
			t.Fatalf("%s %s at AAL1 = %d %s, want 403 step_up_required", c.method, c.path, r.code, r.raw)
		}
	}

	h.elevate(admin)
	r = h.do("PUT", "/v1/console/notify-destinations", admin, put, nil)
	if r.code != http.StatusOK || r.body["live"] != true || r.body["webhook_host"] != "hooks.slack.com" {
		t.Fatalf("put = %d %s", r.code, r.raw)
	}
	if fake.gotPut.WebhookURL != put["webhook_url"] || fake.gotPut.Kind != "slack" {
		t.Fatalf("the service did not receive the typed input: %+v", fake.gotPut)
	}

	if r := h.do("POST", "/v1/console/notify-destinations/test", admin, put, nil); r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("test = %d %s", r.code, r.raw)
	}
	// A destination that refuses the message is a generic 422: the error would embed the URL.
	fake.testErr = api.ErrNotifyDestinationTestFailed
	r = h.do("POST", "/v1/console/notify-destinations/test", admin, put, nil)
	if r.code != http.StatusUnprocessableEntity || r.body["error"].(map[string]any)["code"] != "notify_destination_test_failed" {
		t.Fatalf("failed test = %d %s, want 422 notify_destination_test_failed", r.code, r.raw)
	}

	if r := h.do("DELETE", "/v1/console/notify-destinations", admin, map[string]any{"name": "oncall"}, nil); r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	if fake.gotDelete != "oncall" {
		t.Fatalf("delete got name %q", fake.gotDelete)
	}
}

func TestNotifyDestinationsErrorsMapToStatuses(t *testing.T) {
	fake := &fakeNotifyDestinations{}
	h := newNotifyDestinationsHarness(t, fake)
	admin := h.adminLogin()
	h.elevate(admin)
	put := map[string]any{"name": "oncall", "kind": "slack"}

	fake.putErr = api.ErrNotifyDestinationInvalid
	if r := h.do("PUT", "/v1/console/notify-destinations", admin, put, nil); r.code != http.StatusBadRequest {
		t.Fatalf("invalid destination = %d %s, want 400", r.code, r.raw)
	}
	fake.deleteErr = api.ErrNotifyDestinationNotFound
	if r := h.do("DELETE", "/v1/console/notify-destinations", admin, map[string]any{"name": "nope"}, nil); r.code != http.StatusNotFound {
		t.Fatalf("unknown destination = %d %s, want 404", r.code, r.raw)
	}
}

func TestNotifyDestinationsSuperadminOnly(t *testing.T) {
	h := newNotifyDestinationsHarness(t, &fakeNotifyDestinations{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A tenant admin cannot read or write a deployment-wide destination.
	adminTok := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)
	if r := h.do("GET", "/v1/console/notify-destinations", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin list = %d, want 403 (superadmin-only)", r.code)
	}
	h.elevate(adminTok)
	put := map[string]any{"name": "x", "kind": "slack", "webhook_url": "https://hooks.slack.com/services/T0/B0/XXXX"}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/v1/console/notify-destinations"},
		{"POST", "/v1/console/notify-destinations/test"},
		{"DELETE", "/v1/console/notify-destinations"},
	} {
		if r := h.do(c.method, c.path, adminTok, put, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Fatalf("tenant-admin %s %s = %d, want 403", c.method, c.path, r.code)
		}
	}
}

func TestNotifyDestinationsUnwiredIs501(t *testing.T) {
	h := newHarnessOpts(t, func(*api.Options) {})
	admin := h.adminLogin()
	h.elevate(admin)
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/console/notify-destinations"},
		{"PUT", "/v1/console/notify-destinations"},
		{"POST", "/v1/console/notify-destinations/test"},
		{"DELETE", "/v1/console/notify-destinations"},
	} {
		r := h.do(c.method, c.path, admin, map[string]any{"name": "x", "kind": "slack"}, nil)
		if r.code != http.StatusNotImplemented || r.body["error"].(map[string]any)["code"] != "notify_destinations_unavailable" {
			t.Fatalf("%s %s unwired = %d %s, want 501 notify_destinations_unavailable", c.method, c.path, r.code, r.raw)
		}
	}
}
