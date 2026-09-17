// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

type fixedContentFirewallState ContentFirewallState

func (s fixedContentFirewallState) ContentFirewallState() ContentFirewallState {
	return ContentFirewallState(s)
}

func TestContentFirewallRouteIsAConfigReadCollectionRoute(t *testing.T) {
	var routes recordingInferenceRoutes
	New().APIRoutes(&routes)
	var found []recordedInferenceRoute
	for _, route := range routes.routes {
		if route.pattern == "/content-firewall" {
			found = append(found, route)
		}
	}
	if len(found) != 1 || found[0].method != http.MethodGet || found[0].perm != permConfigRead {
		t.Fatalf("content-firewall routes = %+v, want one GET requiring %s", found, permConfigRead)
	}
	if len(routes.routes) != 7 {
		t.Fatalf("route count = %d, want 7", len(routes.routes))
	}
	want := []auth.Permission{permConfigRead, permConfigAdmin, permDLPRead, permDLPAdmin}
	if got := New().Permissions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}
}

func TestContentFirewallStatusServesOnlyThePublishedStates(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want ContentFirewallState
	}{
		{name: "no recorder", want: ContentFirewallUnobserved},
		{name: "nil recorder", opts: []Option{WithContentFirewallStatus(nil)}, want: ContentFirewallUnobserved},
		{name: "unobserved", opts: []Option{WithContentFirewallStatus(fixedContentFirewallState(ContentFirewallUnobserved))}, want: ContentFirewallUnobserved},
		{name: "pep not composed", opts: []Option{WithContentFirewallStatus(fixedContentFirewallState(ContentFirewallPEPNotComposed))}, want: ContentFirewallPEPNotComposed},
		{name: "inspector absent", opts: []Option{WithContentFirewallStatus(fixedContentFirewallState(ContentFirewallInspectorAbsent))}, want: ContentFirewallInspectorAbsent},
		{name: "inspector attached", opts: []Option{WithContentFirewallStatus(fixedContentFirewallState(ContentFirewallInspectorAttached))}, want: ContentFirewallInspectorAttached},
		{name: "outside the enum", opts: []Option{WithContentFirewallStatus(fixedContentFirewallState("attached"))}, want: ContentFirewallUnobserved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(tc.opts...)
			rec := httptest.NewRecorder()
			m.handleGetContentFirewall(rec, httptest.NewRequest(http.MethodGet, "/content-firewall", nil), api.ModuleContext{})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("content type = %q", ct)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			keys := make([]string, 0, len(body))
			for key := range body {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, []string{"note", "pep", "state"}) {
				t.Fatalf("keys = %v, want [note pep state]", keys)
			}
			if body["pep"] != "messages_proxy" || body["state"] != string(tc.want) || body["note"] != contentFirewallNote {
				t.Fatalf("body = %v, want pep messages_proxy, state %s and the constant note", body, tc.want)
			}
		})
	}
}
