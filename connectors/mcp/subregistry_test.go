// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testSubRegistry(t *testing.T) *SubRegistry {
	t.Helper()
	reg, err := NewSubRegistry(SubRegistryConfig{
		DefaultTenant: "acme",
		Tenants: map[string]SubRegistryTenant{
			"acme": {
				OwnedNamespaces: []string{"corp.acme"},
				Servers: []ServedServer{
					{Name: "corp.acme/widgets", Description: "Internal widgets server.", Version: "1.0.0", UpdatedAt: "2026-01-01T00:00:00Z"},
					{Name: "corp.acme/widgets", Description: "Internal widgets server.", Version: "1.2.0", UpdatedAt: "2026-06-01T00:00:00Z"},
					{Name: "corp.acme/legacy", Description: "Retired tool.", Version: "0.9.0", Status: "deleted"},
					{Name: "corp.acme/reports", Description: "Reporting server.", Version: "2.0.0", Status: "deprecated", StatusMessage: "use widgets"},
				},
			},
			"globex": {
				Servers: []ServedServer{
					{Name: "corp.globex/secret-tool", Description: "Globex-only server.", Version: "3.0.0"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSubRegistry: %v", err)
	}
	return reg
}

func subRegistryGET(t *testing.T, reg *SubRegistry, path string) (*httptest.ResponseRecorder, servedListResponse) {
	t.Helper()
	rr := httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	var out servedListResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, rr.Body.String())
		}
	}
	return rr, out
}

func servedNames(list servedListResponse) []string {
	var out []string
	for _, s := range list.Servers {
		out = append(out, s.Server.Name+"@"+s.Server.Version)
	}
	return out
}

func TestSubRegistryServesApprovedSet(t *testing.T) {
	reg := testSubRegistry(t)
	rr, list := subRegistryGET(t, reg, "/v0.1/servers")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v0.1/servers: %d %s", rr.Code, rr.Body.String())
	}
	// The deleted entry is hidden by default; the rest serve name-ascending.
	want := []string{"corp.acme/reports@2.0.0", "corp.acme/widgets@1.0.0", "corp.acme/widgets@1.2.0"}
	if strings.Join(servedNames(list), ",") != strings.Join(want, ",") {
		t.Errorf("served set = %v, want %v", servedNames(list), want)
	}
	// Lifecycle rides in the spec's registry-managed _meta, not on the record.
	first := list.Servers[0]
	official, _ := first.Meta[officialMetaKey].(map[string]any)
	if official == nil || official["status"] != "deprecated" || official["statusMessage"] != "use widgets" {
		t.Errorf("registry-managed _meta missing/wrong: %+v", first.Meta)
	}
	// include_deleted surfaces the deleted entry (yank visibility for sync clients).
	_, withDeleted := subRegistryGET(t, reg, "/v0.1/servers?include_deleted=true")
	if len(withDeleted.Servers) != 4 {
		t.Errorf("include_deleted must surface the deleted entry, got %v", servedNames(withDeleted))
	}
}

func TestSubRegistryTenantIsolation(t *testing.T) {
	reg := testSubRegistry(t)
	// Tenant paths see ONLY their tenant's entries.
	_, acme := subRegistryGET(t, reg, "/t/acme/v0.1/servers")
	for _, n := range servedNames(acme) {
		if strings.Contains(n, "globex") {
			t.Errorf("tenant acme must not see globex entries: %v", servedNames(acme))
		}
	}
	_, globex := subRegistryGET(t, reg, "/t/globex/v0.1/servers")
	if len(globex.Servers) != 1 || globex.Servers[0].Server.Name != "corp.globex/secret-tool" {
		t.Errorf("tenant globex must see exactly its own entry, got %v", servedNames(globex))
	}
	// The default (bare) path serves the default tenant — and never leaks others.
	_, def := subRegistryGET(t, reg, "/v0.1/servers")
	for _, n := range servedNames(def) {
		if strings.Contains(n, "globex") {
			t.Errorf("the default tenant view must not leak another tenant: %v", servedNames(def))
		}
	}
	// An unknown tenant serves an EMPTY view, byte-identical to a provisioned-but-
	// empty tenant — the anonymous surface must not be a tenant-existence oracle.
	rr, unknown := subRegistryGET(t, reg, "/t/nope/v0.1/servers")
	if rr.Code != http.StatusOK || len(unknown.Servers) != 0 {
		t.Errorf("unknown tenant must serve an empty 200 view (no existence oracle), got %d %v", rr.Code, servedNames(unknown))
	}
	rr = httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/t/nope/v0.1/servers/corp.acme%2Fwidgets/versions/latest", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown tenant version lookup must answer the same generic 404, got %d", rr.Code)
	}
}

func TestSubRegistryNoDefaultTenant(t *testing.T) {
	reg, err := NewSubRegistry(SubRegistryConfig{Tenants: map[string]SubRegistryTenant{
		"acme": {Servers: []ServedServer{{Name: "corp.acme/x", Description: "d", Version: "1.0.0"}}},
	}})
	if err != nil {
		t.Fatalf("NewSubRegistry: %v", err)
	}
	rr, _ := subRegistryGET(t, reg, "/v0.1/servers")
	if rr.Code != http.StatusNotFound {
		t.Errorf("without a default tenant the bare path must 404, got %d", rr.Code)
	}
	if rr2, _ := subRegistryGET(t, reg, "/t/acme/v0.1/servers"); rr2.Code != http.StatusOK {
		t.Errorf("the tenant path must still serve, got %d", rr2.Code)
	}
}

func TestSubRegistryVersionEndpoints(t *testing.T) {
	reg := testSubRegistry(t)

	// GitHub BYO-registry contract: /versions/latest with the %2F-encoded name.
	rr := httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0.1/servers/corp.acme%2Fwidgets/versions/latest", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET versions/latest (encoded): %d %s", rr.Code, rr.Body.String())
	}
	var one servedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if one.Server.Version != "1.2.0" {
		t.Errorf("latest must be 1.2.0, got %q", one.Server.Version)
	}
	official, _ := one.Meta[officialMetaKey].(map[string]any)
	if official == nil || official["isLatest"] != true {
		t.Errorf("latest must carry isLatest=true in _meta: %+v", one.Meta)
	}

	// Raw-slash name + exact version also resolves.
	rr = httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0.1/servers/corp.acme/widgets/versions/1.0.0", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET versions/1.0.0 (raw slash): %d", rr.Code)
	}

	// The versions list serves newest first.
	_, versions := subRegistryGET(t, reg, "/v0.1/servers/corp.acme%2Fwidgets/versions")
	if strings.Join(servedNames(versions), ",") != "corp.acme/widgets@1.2.0,corp.acme/widgets@1.0.0" {
		t.Errorf("versions must list newest first, got %v", servedNames(versions))
	}

	// Unknown server/version → the spec's 404 error shape.
	rr = httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0.1/servers/corp.acme%2Fnope/versions/latest", nil))
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"error"`) {
		t.Errorf("unknown server must 404 with the error shape, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestSubRegistryListFilters(t *testing.T) {
	reg := testSubRegistry(t)
	// search= substring on the name.
	_, found := subRegistryGET(t, reg, "/v0.1/servers?search=widg")
	if len(found.Servers) != 2 {
		t.Errorf("search=widg must match both widget versions, got %v", servedNames(found))
	}
	// version=latest serves one record per name.
	_, latest := subRegistryGET(t, reg, "/v0.1/servers?version=latest")
	if strings.Join(servedNames(latest), ",") != "corp.acme/reports@2.0.0,corp.acme/widgets@1.2.0" {
		t.Errorf("version=latest = %v", servedNames(latest))
	}
	// updated_since: dated entries filter; undated entries are ALWAYS included
	// (superset semantics — a sync client must never silently miss them), and
	// include_deleted is implied.
	_, since := subRegistryGET(t, reg, "/v0.1/servers?updated_since=2026-03-01T00:00:00Z")
	names := strings.Join(servedNames(since), ",")
	if !strings.Contains(names, "widgets@1.2.0") || strings.Contains(names, "widgets@1.0.0") {
		t.Errorf("updated_since must keep newer + undated entries only, got %v", servedNames(since))
	}
	if !strings.Contains(names, "legacy@0.9.0") {
		t.Errorf("updated_since implies include_deleted (sync clients must see deletions), got %v", servedNames(since))
	}
	// Pagination: limit=1 walks the set via cursors without overlap.
	var (
		cursor string
		seen   []string
	)
	for i := 0; i < 10; i++ {
		path := "/v0.1/servers?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		_, page := subRegistryGET(t, reg, path)
		seen = append(seen, servedNames(page)...)
		if page.Metadata.NextCursor == "" {
			break
		}
		cursor = page.Metadata.NextCursor
	}
	if len(seen) != 3 {
		t.Errorf("cursor walk must visit each record exactly once, got %v", seen)
	}
	// A stale/garbled cursor must refuse (400), never answer an empty 200 page a
	// sync client cannot distinguish from a completed walk.
	rr, _ := subRegistryGET(t, reg, "/v0.1/servers?cursor=no.such%2Fserver:9.9.9")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("a stale cursor must answer 400, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestSubRegistryListLimitValidation(t *testing.T) {
	reg := testSubRegistry(t)
	for _, bad := range []string{"0", "-5", "abc"} {
		rr, _ := subRegistryGET(t, reg, "/v0.1/servers?limit="+bad)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "positive integer") {
			t.Errorf("limit=%s must answer 400, got %d %s", bad, rr.Code, rr.Body.String())
		}
	}
	// An oversize limit is clamped (accepted), not rejected.
	rr, list := subRegistryGET(t, reg, "/v0.1/servers?limit=9999")
	if rr.Code != http.StatusOK || len(list.Servers) != 3 || list.Metadata.NextCursor != "" {
		t.Errorf("an oversize limit must clamp and serve the full set, got %d %v", rr.Code, servedNames(list))
	}
}

func TestParseServerPath(t *testing.T) {
	cases := []struct {
		in            string
		name, version string
		ok            bool
	}{
		{"corp.acme%2Fwidgets/versions", "corp.acme/widgets", "", true},
		{"corp.acme/widgets/versions", "corp.acme/widgets", "", true},
		{"corp.acme%2Fwidgets/versions/latest", "corp.acme/widgets", "latest", true},
		{"corp.acme/widgets/versions/1.0.0", "corp.acme/widgets", "1.0.0", true},
		// A literal "versions" name segment in the middle still resolves.
		{"a/versions/b/versions/1.0.0", "a/versions/b", "1.0.0", true},
		// Trailing slash pins the TrimSuffix quirk: routes to the versions LIST.
		{"corp.acme%2Fwidgets/versions/", "corp.acme/widgets", "", true},
		// Deny-closed refusals.
		{"versions", "", "", false},                   // no name
		{"/versions", "", "", false},                  // empty name segment
		{"co%zz/versions", "", "", false},             // invalid escape in the name
		{"corp.acme%2Fw/versions/%zz", "", "", false}, // invalid escape in the version
		{"corp.acme%2Fwidgets", "", "", false},        // no versions marker
	}
	for _, c := range cases {
		name, version, ok := parseServerPath(c.in)
		if name != c.name || version != c.version || ok != c.ok {
			t.Errorf("parseServerPath(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, name, version, ok, c.name, c.version, c.ok)
		}
	}
}

func TestSubRegistryAllDeletedLatest(t *testing.T) {
	reg := testSubRegistry(t)
	// corp.acme/legacy has ONLY a deleted version. Without include_deleted it stays
	// hidden; the sync view (include_deleted) must still resolve its latest — yank
	// visibility is the point of that view.
	rr := httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0.1/servers/corp.acme%2Flegacy/versions/latest", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("a fully-deleted server must 404 on the default surface, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0.1/servers/corp.acme%2Flegacy/versions/latest?include_deleted=true", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("the sync view must resolve a fully-deleted server's latest, got %d %s", rr.Code, rr.Body.String())
	}
	var one servedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil || one.Server.Version != "0.9.0" {
		t.Errorf("latest of the fully-deleted name must be its highest version, got %+v (%v)", one.Server, err)
	}
}

func TestVersionLessTotalOrder(t *testing.T) {
	// Mixed semver/non-semver sets previously cycled ("9.0.0"<"10.0.0"<... while
	// lexicographic pairs flipped); the class partition makes the order total:
	// non-semver < semver, semver pairs numeric, non-semver pairs lexicographic.
	vs := []string{"9.0.0", "10.0.0", "5", "1.2.3-rc.1", "1.2.3", "abc"}
	for _, a := range vs {
		for _, b := range vs {
			if a != b && versionLess(a, b) == versionLess(b, a) && versionLess(a, b) {
				t.Errorf("versionLess(%q,%q) and its inverse both true (not antisymmetric)", a, b)
			}
			for _, c := range vs {
				if versionLess(a, b) && versionLess(b, c) && !versionLess(a, c) {
					t.Errorf("versionLess intransitive: %q<%q and %q<%q but not %q<%q", a, b, b, c, a, c)
				}
			}
		}
	}
	// A genuine semver always wins "latest" over a non-parseable version,
	// regardless of provisioning order.
	for _, order := range [][]string{{"9.0.0", "10.0.0", "5"}, {"5", "10.0.0", "9.0.0"}, {"10.0.0", "5", "9.0.0"}} {
		var servers []ServedServer
		for _, v := range order {
			servers = append(servers, ServedServer{Name: "corp.acme/x", Description: "d", Version: v})
		}
		reg, err := NewSubRegistry(SubRegistryConfig{DefaultTenant: "acme", Tenants: map[string]SubRegistryTenant{"acme": {Servers: servers}}})
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		reg.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0.1/servers/corp.acme%2Fx/versions/latest", nil))
		var one servedResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil || one.Server.Version != "10.0.0" {
			t.Errorf("order %v: latest = %q, want the highest semver 10.0.0", order, one.Server.Version)
		}
	}
}

func TestSubRegistryReadOnly(t *testing.T) {
	reg := testSubRegistry(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v0.1/publish"},
		{http.MethodPut, "/v0.1/servers/corp.acme%2Fwidgets/versions/1.2.0"},
		{http.MethodDelete, "/v0.1/servers/corp.acme%2Fwidgets/versions/1.2.0"},
		{http.MethodPost, "/v0.1/servers"},
	} {
		rr := httptest.NewRecorder()
		reg.ServeHTTP(rr, httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		if rr.Code != http.StatusNotImplemented {
			t.Errorf("%s %s must answer 501 (read-only registry), got %d", c.method, c.path, rr.Code)
		}
	}
}

func TestSubRegistryCORS(t *testing.T) {
	reg := testSubRegistry(t)
	// Preflight.
	rr := httptest.NewRecorder()
	reg.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/v0.1/servers", nil))
	if rr.Code != http.StatusNoContent || rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("OPTIONS preflight must answer 204 with CORS *, got %d %q", rr.Code, rr.Header().Get("Access-Control-Allow-Origin"))
	}
	// Every GET carries the CORS header (the GitHub BYO-registry requirement).
	rr, _ = subRegistryGET(t, reg, "/v0.1/servers")
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("GET responses must carry Access-Control-Allow-Origin: *")
	}
}

func TestSubRegistryValidation(t *testing.T) {
	base := func(s ServedServer) SubRegistryConfig {
		return SubRegistryConfig{Tenants: map[string]SubRegistryTenant{"acme": {Servers: []ServedServer{s}}}}
	}
	cases := []struct {
		name string
		cfg  SubRegistryConfig
	}{
		{"bad name (no slash)", base(ServedServer{Name: "widgets", Description: "d", Version: "1.0.0"})},
		{"bad name (two slashes)", base(ServedServer{Name: "corp.acme/a/b", Description: "d", Version: "1.0.0"})},
		{"missing description", base(ServedServer{Name: "corp.acme/x", Version: "1.0.0"})},
		{"version literal latest", base(ServedServer{Name: "corp.acme/x", Description: "d", Version: "latest"})},
		{"version literal versions (reserved by the URL grammar)", base(ServedServer{Name: "corp.acme/x", Description: "d", Version: "versions"})},
		{"bad status", base(ServedServer{Name: "corp.acme/x", Description: "d", Version: "1.0.0", Status: "yanked"})},
		{"inline secret", base(ServedServer{Name: "corp.acme/x", Description: "d", Version: "1.0.0",
			Remotes: json.RawMessage(`[{"type":"streamable-http","url":"https://x","headers":[{"name":"Authorization","value":"Bearer sk-ant-abc"}]}]`)})},
		{"no tenants", SubRegistryConfig{}},
		{"default tenant unknown", SubRegistryConfig{DefaultTenant: "nope", Tenants: map[string]SubRegistryTenant{"acme": {}}}},
		{"duplicate name@version", SubRegistryConfig{Tenants: map[string]SubRegistryTenant{"acme": {Servers: []ServedServer{
			{Name: "corp.acme/x", Description: "d", Version: "1.0.0"},
			{Name: "corp.acme/x", Description: "d", Version: "1.0.0"},
		}}}}},
		{"outside owned namespace", SubRegistryConfig{Tenants: map[string]SubRegistryTenant{"acme": {
			OwnedNamespaces: []string{"corp.acme"},
			Servers:         []ServedServer{{Name: "corp.evil/x", Description: "d", Version: "1.0.0"}},
		}}}},
	}
	for _, c := range cases {
		if _, err := NewSubRegistry(c.cfg); err == nil {
			t.Errorf("%s: construction must fail (deny-closed)", c.name)
		}
	}
}

// TestSubRegistryConsumableByPinnedClient: the served surface must be readable by
// the connector's OWN pinned /v0.1 registryClient — the same OpenAPI GitHub's
// clients and other subregistries consume (federation interop, end to end).
func TestSubRegistryConsumableByPinnedClient(t *testing.T) {
	reg := testSubRegistry(t)
	srv := httptest.NewServer(reg)
	defer srv.Close()

	c := newRegistryClient(srv.URL, defaultTimeout)
	prov, err := c.lookup(t.Context(), serverSpec{Name: "widgets", RegistryName: "corp.acme/widgets"})
	if err != nil {
		t.Fatalf("pinned client lookup against the sub-registry: %v", err)
	}
	if !prov.found || prov.namespace != "corp.acme" {
		t.Errorf("the pinned client must resolve provenance from the sub-registry, got %+v", prov)
	}
	// The sync path (include_deleted) sees the yanked entry too.
	records, err := c.listNamespace(t.Context(), "corp.acme")
	if err != nil {
		t.Fatalf("listNamespace: %v", err)
	}
	sawDeleted := false
	for _, r := range records {
		if r.Server.Name == "corp.acme/legacy" && r.status() == "deleted" {
			sawDeleted = true
		}
	}
	if !sawDeleted {
		t.Errorf("the sync path must see the deleted entry via include_deleted, got %+v", records)
	}
}
