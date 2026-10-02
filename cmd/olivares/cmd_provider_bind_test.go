// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestProviderBindMakesTheProfileUseTheKey is FH 025: `provider bind` set only the
// profile's provider record, so the profile stayed on the tool's own login and the
// engine never opened the key ("Not logged in"). Binding also moves the profile to the
// managed credential; --unbind returns it to the tool's own login.
func TestProviderBindMakesTheProfileUseTheKey(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
		paths  []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies, paths = append(bodies, body), append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"profile_ref": "ppf_1"})
	}))
	defer srv.Close()

	if _, errb, err := execSessionCLI(t, nil, append([]string{"provider", "bind", "prv_1", "--profile", "ppf_1"}, sessionCreds(srv.URL)...)...); err != nil {
		t.Fatalf("bind: %v\n%s", err, errb)
	}
	if _, errb, err := execSessionCLI(t, nil, append([]string{"provider", "bind", "--unbind", "--profile", "ppf_1"}, sessionCreds(srv.URL)...)...); err != nil {
		t.Fatalf("unbind: %v\n%s", err, errb)
	}
	if len(bodies) != 2 || paths[0] != "PATCH /v1/m/sessions/provider-profiles/ppf_1" {
		t.Fatalf("requests = %v", paths)
	}
	for i, want := range []map[string]any{
		{"provider_record_ref": "prv_1", "auth_source": "managed_injection"},
		{"provider_record_ref": "", "auth_source": "provider_account_home"},
	} {
		for k, v := range want {
			if bodies[i][k] != v {
				t.Fatalf("request %d sent %v, want %s=%q", i, bodies[i], k, v)
			}
		}
	}
}

// TestProviderBindSaysWhatChanged is J7 on refresh 08b: `provider bind` and `--unbind`
// printed nothing, in text and with -o json, so the change could only be seen with
// `agent profile get`. Text is one line that says what new sessions on the profile use;
// -o json is the profile the engine returned.
func TestProviderBindSaysWhatChanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"profile_ref": "ppf_1",
			"provider_record_ref": body["provider_record_ref"], "auth_source": body["auth_source"]})
	}))
	defer srv.Close()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"provider", "bind", "prv_1", "--profile", "ppf_1", "-o", "text"}, "Bound prv_1 to ppf_1. New sessions on this profile use its credential.\n"},
		{[]string{"provider", "bind", "--unbind", "--profile", "ppf_1", "-o", "text"}, "Unbound ppf_1. New sessions on this profile use the tool's own sign-in.\n"},
	} {
		out, errb, err := execSessionCLI(t, nil, append(c.args, sessionCreds(srv.URL)...)...)
		if err != nil || out != c.want {
			t.Fatalf("%v: out=%q err=%v %s\nwant %q", c.args, out, err, errb, c.want)
		}
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"provider", "bind", "prv_1", "--profile", "ppf_1", "-o", "json"}, sessionCreds(srv.URL)...)...)
	var got map[string]any
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got["profile_ref"] != "ppf_1" || got["auth_source"] != "managed_injection" {
		t.Fatalf("-o json = %q (%v)", out, err)
	}
}
