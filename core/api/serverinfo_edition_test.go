// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// The unauthenticated server-info reports the BUILD edition, so the console offers only
// the controls this build can serve. It is a build fact: it never reads the license, and
// a license does not change it (LICENSING.md).
func TestServerInfoReportsTheBuildEdition(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.Edition = "community" })
	r := h.do("GET", "/v1/server-info", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("server-info = %d %s", r.code, r.raw)
	}
	if r.body["edition"] != "community" {
		t.Fatalf("edition = %v, want community", r.body["edition"])
	}
	lic, ok := r.body["license"].(map[string]any)
	if !ok || lic["status"] != "none" {
		t.Fatalf("license = %v; the edition must not come from or alter the license", r.body["license"])
	}
}

// An embedder that does not name its edition gets NO edition key, never a guessed one:
// the console then treats the edition as unknown.
func TestServerInfoOmitsAnUnnamedEdition(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/v1/server-info", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("server-info = %d %s", r.code, r.raw)
	}
	if v, present := r.body["edition"]; present {
		t.Fatalf("edition = %v, want the key omitted", v)
	}
}
