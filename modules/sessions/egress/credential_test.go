// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	ownKey   = "sk-ant-own-0123456789"
	otherKey = "sk-ant-attacker-9876543210"
)

type refusals struct {
	mu  sync.Mutex
	got []Refusal
}

func (r *refusals) add(f Refusal) error {
	r.mu.Lock()
	r.got = append(r.got, f)
	r.mu.Unlock()
	return nil
}
func (r *refusals) all() []Refusal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Refusal(nil), r.got...)
}

func keyedPolicy(providerURL string, seen *refusals) Policy {
	return Policy{Providers: []string{providerURL}, CredentialSHA256: sha256.Sum256([]byte(ownKey)), OnRefusal: seen.add}
}

// proxyClient reaches the proxy over its socket; a TLS provider is reached through
// CONNECT and the session CA, as a session's tool does.
func proxyClient(t *testing.T, p *proxy) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if len(p.caPEM) > 0 && !roots.AppendCertsFromPEM(p.caPEM) {
		t.Fatal("no session CA")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy"}), TLSClientConfig: &tls.Config{RootCAs: roots},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
		}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func TestProxyForwardsOnlyTheSessionKey(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			var reached atomic.Int32
			var firstHeaders atomic.Value
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if reached.Add(1) == 1 {
					firstHeaders.Store(r.Header.Clone())
				}
				w.WriteHeader(http.StatusOK)
			})
			provider := httptest.NewServer(handler)
			if scheme == "https" {
				provider = httptest.NewTLSServer(handler)
			}
			defer provider.Close()
			seen := &refusals{}
			policy := keyedPolicy(provider.URL, seen)
			ds, err := destinations(policy)
			if err != nil {
				t.Fatal(err)
			}
			p, err := startProxy(context.Background(), ds, withCredential(policy))
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			if scheme == "https" {
				roots := x509.NewCertPool()
				roots.AddCert(provider.Certificate())
				p.transport.TLSClientConfig.RootCAs = roots
			}
			client := proxyClient(t, p)
			host := strings.TrimPrefix(strings.TrimPrefix(provider.URL, "https://"), "http://")
			host, _, _ = net.SplitHostPort(host)
			told := map[string]bool{} // a repeat of a refused host and header is not told again
			for _, tc := range []struct {
				name    string
				headers [][2]string
				want    int
				header  string // the header the refusal names
			}{
				{"own bearer", [][2]string{{"Authorization", "Bearer " + ownKey}}, http.StatusOK, ""},
				{"own bearer, lower-case scheme", [][2]string{{"Authorization", "bearer " + ownKey}}, http.StatusOK, ""},
				{"own x-api-key", [][2]string{{"X-Api-Key", ownKey}}, http.StatusOK, ""},
				{"own api-key", [][2]string{{"Api-Key", ownKey}}, http.StatusOK, ""},
				{"no credential header", nil, http.StatusOK, ""},
				{"other bearer", [][2]string{{"Authorization", "Bearer " + otherKey}}, http.StatusForbidden, "Authorization"},
				{"other x-api-key", [][2]string{{"X-Api-Key", otherKey}}, http.StatusForbidden, "X-Api-Key"},
				{"other api-key", [][2]string{{"Api-Key", otherKey}}, http.StatusForbidden, "Api-Key"},
				{"prefix of the key", [][2]string{{"X-Api-Key", ownKey[:10]}}, http.StatusForbidden, "X-Api-Key"},
				{"non-bearer authorization", [][2]string{{"Authorization", "Basic " + otherKey}}, http.StatusForbidden, "Authorization"},
				{"own key beside another", [][2]string{{"X-Api-Key", ownKey}, {"Authorization", "Bearer " + otherKey}}, http.StatusForbidden, "Authorization"},
				{"own then other, same header", [][2]string{{"Api-Key", ownKey}, {"Api-Key", otherKey}}, http.StatusForbidden, "Api-Key"},
				{"other then own, same header", [][2]string{{"Api-Key", otherKey}, {"Api-Key", ownKey}}, http.StatusForbidden, "Api-Key"},
				{"empty key header", [][2]string{{"X-Api-Key", ""}}, http.StatusOK, ""},
			} {
				before, refusedBefore := reached.Load(), len(seen.all())
				req, _ := http.NewRequest(http.MethodPost, provider.URL+"/v1/files", strings.NewReader("workspace file"))
				for _, h := range tc.headers {
					req.Header.Add(h[0], h[1])
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != tc.want {
					t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
				}
				forwarded := reached.Load() - before
				if tc.want == http.StatusOK && forwarded != 1 {
					t.Errorf("%s: provider reached %d times, want 1", tc.name, forwarded)
				}
				if tc.want != http.StatusOK && forwarded != 0 {
					t.Errorf("%s: a refused request reached the provider", tc.name)
				}
				got := seen.all()[refusedBefore:]
				first := tc.header != "" && !told[tc.header]
				told[tc.header] = true
				switch {
				case !first && len(got) != 0:
					t.Errorf("%s: unexpected refusal %v", tc.name, got)
				case first && len(got) != 1:
					t.Errorf("%s: %d refusals, want 1", tc.name, len(got))
				case first && got[0] != (Refusal{Host: host, Header: tc.header}):
					t.Errorf("%s: refusal %v, want host %s header %s", tc.name, got[0], host, tc.header)
				}
			}
			// The forwarded request is the one the tool sent: nothing is rewritten.
			if h, _ := firstHeaders.Load().(http.Header); h.Get("Authorization") != "Bearer "+ownKey {
				t.Errorf("forwarded request changed: %v", h)
			}
			for _, f := range seen.all() {
				if text := fmt.Sprint(f); strings.Contains(text, ownKey[:10]) || strings.Contains(text, otherKey[:10]) {
					t.Errorf("refusal carries a header value: %q", text)
				}
			}
		})
	}
}

// Without a digest the proxy checks no key: unbound launches and kinds whose
// header is not measured keep forwarding whatever the tool sends.
func TestProxyWithoutDigestForwardsAnyKey(t *testing.T) {
	var reached atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer provider.Close()
	policy := Policy{Providers: []string{provider.URL}}
	ds, err := destinations(policy)
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds, withCredential(policy))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	req, _ := http.NewRequest(http.MethodGet, provider.URL, nil)
	req.Header.Set("Authorization", "Bearer "+otherKey)
	resp, err := proxyClient(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || reached.Load() != 1 {
		t.Fatalf("status %d, provider reached %d times", resp.StatusCode, reached.Load())
	}
}

// The engine's own control relay carries the PEP bearer, which is not the
// session's provider key and must not be compared to it.
func TestProxyControlRelayIgnoresTheKeyDigest(t *testing.T) {
	var reached atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer provider.Close()
	control := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer control.Close()
	policy := Policy{Providers: []string{provider.URL}, Controls: []string{control.URL + "/session/mcp"},
		CredentialSHA256: sha256.Sum256([]byte(ownKey))}
	ds, err := destinations(policy)
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds, withCredential(policy))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	req, _ := http.NewRequest(http.MethodPost, control.URL+"/session/mcp", nil)
	req.Header.Set("Authorization", "Bearer pep-token")
	resp, err := proxyClient(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || reached.Load() != 1 {
		t.Fatalf("control relay: status %d, reached %d", resp.StatusCode, reached.Load())
	}
}

// Only the bound endpoint is checked: a package registry in the same policy takes
// its own token, which is not the session's key.
func TestProxyChecksOnlyTheBoundEndpoint(t *testing.T) {
	var providerReached, registryReached atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerReached.Add(1) }))
	defer provider.Close()
	registry := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { registryReached.Add(1) }))
	defer registry.Close()
	seen := &refusals{}
	policy := keyedPolicy(provider.URL, seen)
	policy.Providers = append(policy.Providers, registry.URL)
	ds, err := destinations(policy)
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds, withCredential(policy))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	client := proxyClient(t, p)
	for _, tc := range []struct {
		url  string
		want int
	}{{registry.URL, http.StatusOK}, {provider.URL, http.StatusForbidden}} {
		req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		req.Header.Set("Authorization", "Bearer registry-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.url, resp.StatusCode, tc.want)
		}
	}
	if registryReached.Load() != 1 || providerReached.Load() != 0 || len(seen.all()) != 1 {
		t.Fatalf("registry reached %d, provider reached %d, refusals %v", registryReached.Load(), providerReached.Load(), seen.all())
	}
}

// A refusal whose report was not kept is reported again; one that was kept is not.
func TestRefusalIsReportedAgainUntilItIsKept(t *testing.T) {
	var calls int
	g := &credentialGuard{refused: func(Refusal) error {
		calls++
		if calls == 1 {
			return errors.New("store unavailable")
		}
		return nil
	}, told: map[Refusal]bool{}}
	for i := 0; i < 4; i++ {
		g.refuse("api.example", "X-Api-Key")
	}
	if calls != 2 {
		t.Fatalf("reported %d times, want 2 (one lost, one kept)", calls)
	}
}
