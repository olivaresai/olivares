// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// syncStub serves a paginated GET /v0/servers for the sync path. Unlike registryStub,
// it ACCEPTS include_deleted=true (the deliberate yank-detection request) and serves a
// second page when a cursor is present, so pagination is exercised.
func syncStub(t *testing.T, page1, page2 string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("include_deleted") != "true" {
			t.Errorf("sync must request include_deleted=true to see yanks")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(page1))
			return
		}
		_, _ = w.Write([]byte(page2))
	})
	return httptest.NewServer(mux)
}

func TestListNamespacePaginatesAndFilters(t *testing.T) {
	srv := syncStub(t,
		`{"servers":[`+rec("io.github.acme/a", "active")+`,`+rec("io.github.other/x", "active")+`],"metadata":{"nextCursor":"p2"}}`,
		`{"servers":[`+rec("io.github.acme/b", "deleted")+`],"metadata":{}}`)
	defer srv.Close()

	c := newRegistryClient(srv.URL, defaultTimeout)
	got, err := c.listNamespace(t.Context(), "io.github.acme")
	if err != nil {
		t.Fatalf("listNamespace: %v", err)
	}
	// Two pages merged; the foreign-namespace record filtered out; the deleted record kept.
	if len(got) != 2 {
		t.Fatalf("want 2 in-namespace records (incl deleted), got %d (%+v)", len(got), got)
	}
	names := got[0].Server.Name + "," + got[1].Server.Name
	if strings.Contains(names, "io.github.other") {
		t.Errorf("a foreign-namespace record must be filtered out: %s", names)
	}
}

func newSyncSource(t *testing.T, url string, owned []string, entries []internalEntry) *Source {
	t.Helper()
	return &Source{
		reg:      newRegistryClient(url, defaultTimeout),
		internal: mustInternal(t, owned, entries),
		cfg:      config{registrySync: true},
	}
}

func TestDiscoverFindingsClassifies(t *testing.T) {
	srv := syncStub(t,
		`{"servers":[`+rec("io.github.acme/approved", "active")+`,`+rec("io.github.acme/rogue", "active")+`,`+rec("io.github.acme/old", "deleted")+`],"metadata":{}}`, `{}`)
	defer srv.Close()

	s := newSyncSource(t, srv.URL, []string{"io.github.acme"},
		[]internalEntry{{Name: "approved", RegistryName: "io.github.acme/approved"}})

	fs := s.discoverFindings(t.Context(), fixedTime())
	if len(fs) != 3 {
		t.Fatalf("want 3 discovery findings, got %d (%s)", len(fs), titles(fs))
	}

	var sawYank, sawUnmanaged, sawDiscovered bool
	for _, f := range fs {
		switch {
		case strings.Contains(f.Title, "deleted in the registry"):
			sawYank = f.Severity == model.SeverityMedium && strings.Contains(f.Title, "[MCP04]")
		case strings.Contains(f.Title, "unmanaged publication"):
			sawUnmanaged = f.Severity == model.SeverityLow && strings.Contains(f.Title, "[MCP04]")
		case strings.Contains(f.Title, "discovered in owned namespace"):
			sawDiscovered = f.Severity == model.SeverityInfo
		}
	}
	if !sawYank {
		t.Errorf("a deleted server must raise a Medium [MCP04] yank, got %s", titles(fs))
	}
	if !sawUnmanaged {
		t.Errorf("an unapproved publication under an owned namespace must be flagged unmanaged: %s", titles(fs))
	}
	if !sawDiscovered {
		t.Errorf("an approved publication must be an Info discovery: %s", titles(fs))
	}
}

func TestDiscoverFindingsDisabledOrNoNamespaces(t *testing.T) {
	srv := syncStub(t, `{"servers":[]}`, `{}`)
	defer srv.Close()
	// Sync disabled.
	s1 := &Source{reg: newRegistryClient(srv.URL, defaultTimeout), internal: mustInternal(t, []string{"io.github.acme"}, nil), cfg: config{registrySync: false}}
	if fs := s1.discoverFindings(t.Context(), fixedTime()); fs != nil {
		t.Errorf("sync disabled must emit nothing, got %+v", fs)
	}
	// No owned namespaces.
	s2 := newSyncSource(t, srv.URL, nil, nil)
	if fs := s2.discoverFindings(t.Context(), fixedTime()); fs != nil {
		t.Errorf("no owned namespaces must emit nothing, got %+v", fs)
	}
}

func TestDiscoverFindingsRegistryUnavailable(t *testing.T) {
	// A registry that never connects → one Info sync-unavailable finding per namespace.
	s := newSyncSource(t, "http://127.0.0.1:0", []string{"io.github.acme"}, nil)
	fs := s.discoverFindings(t.Context(), fixedTime())
	if len(fs) != 1 || fs[0].Severity != model.SeverityInfo || !strings.Contains(fs[0].Title, "sync unavailable") {
		t.Errorf("registry-unavailable must surface one Info finding, got %+v", fs)
	}
}
