// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// registryStub serves a canned GET /v0.1/servers response for the given query term
// (the PINNED stable API path — a request to any other version path fails the test).
func registryStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, r *http.Request) {
		// A read-only client must never request deleted servers.
		if r.URL.Query().Get("include_deleted") == "true" {
			t.Errorf("read-only client must not request include_deleted")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("client requested %s — the registry API version is pinned to %s", r.URL.Path, registryAPIPath)
		http.NotFound(w, r)
	})
	return httptest.NewServer(mux)
}

// rec renders one /v0.1 ServerResponse: the server.json record nested under
// "server" with the registry-managed lifecycle status in the response `_meta`.
func rec(name, status string) string {
	return `{"server":{"name":"` + name + `","version":"1.2.0"},` +
		`"_meta":{"io.modelcontextprotocol.registry/official":{"status":"` + status + `"}}}`
}

func TestRegistryLookupResolvesNamespace(t *testing.T) {
	srv := registryStub(t, `{"servers":[`+
		rec("io.github.acme/widgets", "active")+`,`+
		rec("io.github.other/thing", "active")+
		`],"metadata":{"count":2}}`)
	defer srv.Close()

	c := newRegistryClient(srv.URL, defaultTimeout)
	prov, err := c.lookup(t.Context(), serverSpec{Name: "widgets", RegistryName: "io.github.acme/widgets"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !prov.found || prov.namespace != "io.github.acme" {
		t.Fatalf("want found in namespace io.github.acme, got %+v", prov)
	}
}

func TestRegistryLookupDeletedIsNotFound(t *testing.T) {
	srv := registryStub(t, `{"servers":[`+rec("io.github.acme/widgets", "deleted")+`],"metadata":{"count":1}}`)
	defer srv.Close()

	c := newRegistryClient(srv.URL, defaultTimeout)
	prov, err := c.lookup(t.Context(), serverSpec{Name: "widgets", RegistryName: "io.github.acme/widgets"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if prov.found {
		t.Errorf("a deleted server must not count as a verified namespace: %+v", prov)
	}
}

func TestRegistryLookupNoMatchIsShadow(t *testing.T) {
	srv := registryStub(t, `{"servers":[`+rec("io.github.other/thing", "active")+`],"metadata":{"count":1}}`)
	defer srv.Close()

	c := newRegistryClient(srv.URL, defaultTimeout)
	prov, err := c.lookup(t.Context(), serverSpec{Name: "internal-server"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if prov.found {
		t.Errorf("an unmatched server must be unresolved (shadow candidate): %+v", prov)
	}
}

func TestRegistryFindingsShadowAndProvenance(t *testing.T) {
	// Provenance path.
	srv := registryStub(t, `{"servers":[`+rec("io.github.acme/widgets", "active")+`],"metadata":{"count":1}}`)
	defer srv.Close()
	s := &Source{reg: newRegistryClient(srv.URL, defaultTimeout)}

	prov := s.registryFindings(t.Context(), serverSpec{Name: "widgets", RegistryName: "io.github.acme/widgets"}, fixedTime())
	if len(prov) != 1 || prov[0].Kind != findingProvenance {
		t.Fatalf("want one provenance finding, got %+v", prov)
	}
	if prov[0].Severity != model.SeverityInfo {
		t.Errorf("provenance should be Info, got %q", prov[0].Severity)
	}

	// Shadow path (server not in registry).
	shadow := s.registryFindings(t.Context(), serverSpec{Name: "internal"}, fixedTime())
	if len(shadow) != 1 || shadow[0].Kind != findingShadow {
		t.Fatalf("want one shadow finding, got %+v", shadow)
	}
	if shadow[0].Severity != model.SeverityLow {
		t.Errorf("shadow candidate should be Low, got %q", shadow[0].Severity)
	}
	if shadow[0].Title[:7] != "[MCP09]" {
		t.Errorf("shadow finding must be tagged [MCP09]: %q", shadow[0].Title)
	}
}

func TestRegistryDisabledEmitsNothing(t *testing.T) {
	s := &Source{} // reg == nil
	if fs := s.registryFindings(t.Context(), serverSpec{Name: "x"}, fixedTime()); fs != nil {
		t.Errorf("registry disabled must emit nothing, got %+v", fs)
	}
}

func TestRegistryUnavailableIsInfoNotShadow(t *testing.T) {
	// A registry that errors must NOT be read as a shadow flag (absence of data is
	// not evidence of a shadow server) — it surfaces an Info gap instead.
	s := &Source{reg: newRegistryClient("http://127.0.0.1:0", 50_000_000)}
	fs := s.registryFindings(t.Context(), serverSpec{Name: "x"}, fixedTime())
	if len(fs) != 1 || fs[0].Kind != findingProvenance || fs[0].Severity != model.SeverityInfo {
		t.Errorf("registry error should surface a single Info provenance-unavailable finding, got %+v", fs)
	}
}

func TestRegistryShapeDriftDegradesNotShadow(t *testing.T) {
	// The registry is PREVIEW: a future breaking change that flattens/renames the
	// record shape (like the pre-freeze flat shape parsed) must degrade to the
	// Info "unavailable" finding — NEVER be misread as "server not found" and turn
	// every configured server into a fabricated [MCP09] shadow flag.
	srv := registryStub(t, `{"servers":[
		{"name":"io.github.acme/widgets","status":"active","version":"1.2.0"}
	],"metadata":{"count":1}}`) // the LEGACY flat shape: no nested "server" record
	defer srv.Close()

	s := &Source{reg: newRegistryClient(srv.URL, defaultTimeout)}
	fs := s.registryFindings(t.Context(), serverSpec{Name: "widgets", RegistryName: "io.github.acme/widgets"}, fixedTime())
	if len(fs) != 1 || fs[0].Kind != findingProvenance || fs[0].Severity != model.SeverityInfo {
		t.Fatalf("shape drift must surface one Info provenance-unavailable finding, got %+v", fs)
	}
	if fs[0].Kind == findingShadow {
		t.Errorf("shape drift must never fabricate a shadow flag")
	}
}
