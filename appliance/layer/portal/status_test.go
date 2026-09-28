// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type sourceFile struct {
	path   string
	syntax *ast.File
}

// nonTestSources parses every non-test Go file of this package and its subdirectories.
func nonTestSources(t *testing.T) []sourceFile {
	t.Helper()
	var files []sourceFile
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		syntax, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{path: path, syntax: syntax})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no non-test sources found")
	}
	return files
}

func serve(t *testing.T, h http.Handler, method, path, peer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = peer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getJSON(t *testing.T, h http.Handler, peer string) map[string]any {
	t.Helper()
	rec := serve(t, h, http.MethodGet, "/status", peer)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET /status from %q: %d %q", peer, rec.Code, rec.Header().Get("Content-Type"))
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /status from %q is not JSON: %v", peer, err)
	}
	return got
}

type fixedFirewall FirewallMeasurement

func (f fixedFirewall) MeasureFirewall() FirewallMeasurement { return FirewallMeasurement(f) }

const localPeer = "127.0.0.1:40000"

func TestPortalStatus_RendersWithoutProduct(t *testing.T) {
	dir := t.TempDir()
	fingerprint := writeTLSPair(t, dir, 0o600)
	custody, _ := CheckTLSCustody(dir, dir)
	handler := NewHandler(Snapshot(Selection{}, custody, NoFirewallProbe{}))

	got := getJSON(t, handler, localPeer)
	for key, want := range map[string]string{
		"console":                        "Appliance Console",
		"first_boot":                     "unmeasured",
		"certificate_fingerprint_sha256": fingerprint,
		"firewall":                       "unmeasured",
		"product":                        "unmeasured",
		"channel":                        "unmeasured",
		"listen":                         "local-only",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %q", key, got[key], want)
		}
	}
	local, _ := got["local"].(map[string]any)
	if local == nil || local["firewall_policy"] != "unmeasured" {
		t.Errorf("a loopback caller should see the unmeasured firewall policy: %v", got["local"])
	}

	rec := serve(t, handler, http.MethodGet, "/", localPeer)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("GET /: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	page := rec.Body.String()
	if !strings.Contains(page, "Appliance Console") || !strings.Contains(page, fingerprint) {
		t.Errorf("page lacks the console name or the fingerprint:\n%s", page)
	}
	if n := strings.Count(page, "unmeasured"); n < 5 {
		t.Errorf("page shows %d unmeasured facts, want at least 5:\n%s", n, page)
	}
	for header, want := range map[string]string{
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	// Without verified TLS material the fingerprint is unmeasured, never a stale value.
	empty := t.TempDir()
	unverified, _ := CheckTLSCustody(empty, empty)
	got = getJSON(t, NewHandler(Snapshot(Selection{}, unverified, NoFirewallProbe{})), localPeer)
	if got["certificate_fingerprint_sha256"] != "unmeasured" || got["listen"] != "disabled" {
		t.Errorf("no custody: fingerprint %v, listen %v", got["certificate_fingerprint_sha256"], got["listen"])
	}

	var zero Fact
	if zero.String() != "unmeasured" || Measured("ready").String() != "ready" {
		t.Error("a fact is its measured value or unmeasured")
	}

	// Nothing of the product is imported, so the page renders while the product is absent.
	for _, file := range nonTestSources(t) {
		for _, spec := range file.syntax.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(path, "github.com/olivaresai/") && !strings.HasPrefix(path, "github.com/olivaresai/olivares/appliance/") {
				t.Errorf("%s imports %s", file.path, path)
			}
		}
	}
}

func TestPortalStatus_ExposesNoTenantAccountSupportOrNetworkDetailToRemote(t *testing.T) {
	firewall := FirewallMeasurement{Holds: true, Policy: Measured("tcp dport 9443 iifname eth0 ip saddr 198.51.100.0/24 accept")}
	handler := NewHandler(Snapshot(remoteSelection(), verifiedCustody(t), fixedFirewall(firewall)))
	detail := []string{"eth0", "198.51.100", "dport", "iifname"}

	// Control: a loopback caller sees the detail, so its absence below is a real refusal.
	local := serve(t, handler, http.MethodGet, "/status", localPeer).Body.String()
	for _, d := range detail {
		if !strings.Contains(local, d) {
			t.Fatalf("control: the local view lacks %q:\n%s", d, local)
		}
	}

	public := map[string]bool{
		"console": true, "first_boot": true, "certificate_fingerprint_sha256": true,
		"firewall": true, "product": true, "channel": true, "listen": true,
		// Every page states the sign-in mode, to every caller: a mode is a state word,
		// and an operator who cannot sign in has to be told which door is open.
		"sign_in": true, "sign_in_statement": true,
	}
	for _, peer := range []string{"192.0.2.50:5555", "[2001:db8::50]:5555", "10.0.0.8:1", "", "not-an-address"} {
		t.Run("peer "+peer, func(t *testing.T) {
			for _, path := range []string{"/", "/status"} {
				body := serve(t, handler, http.MethodGet, path, peer).Body.String()
				for _, d := range detail {
					if strings.Contains(body, d) {
						t.Errorf("%s from a remote peer exposes %q:\n%s", path, d, body)
					}
				}
			}
			got := getJSON(t, handler, peer)
			for key := range got {
				if !public[key] {
					t.Errorf("a remote peer sees %q", key)
				}
			}
			if got["firewall"] != "prerequisite held" || got["listen"] != "remote" {
				t.Errorf("a remote peer sees state words only: firewall %v, listen %v", got["firewall"], got["listen"])
			}
		})
	}

	for _, path := range []string{"/support", "/support-bundle", "/tenants", "/accounts", "/firewall", "/status/local"} {
		if code := serve(t, handler, http.MethodGet, path, "192.0.2.50:5555").Code; code != http.StatusNotFound {
			t.Errorf("GET %s answered %d, want 404", path, code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if code := serve(t, handler, method, "/status", localPeer).Code; code != http.StatusMethodNotAllowed {
			t.Errorf("%s /status answered %d, want 405", method, code)
		}
	}
}
