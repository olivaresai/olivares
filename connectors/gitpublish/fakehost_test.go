// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// routeDoer sends every request to an httptest server while keeping the
// adapter's own https URL, host and path. The adapter is never given a
// permissive client: the endpoint validation still sees the production-shaped
// base, and no request can leave the process.
type routeDoer struct {
	srv *httptest.Server
	mu  sync.Mutex
	log []*http.Request
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.log = append(d.log, req.Clone(req.Context()))
	d.mu.Unlock()
	u, _ := url.Parse(d.srv.URL)
	out := req.Clone(req.Context())
	out.URL.Scheme = u.Scheme
	out.URL.Host = u.Host
	out.Host = req.URL.Host
	out.RequestURI = ""
	return d.srv.Client().Transport.RoundTrip(out)
}

func (d *routeDoer) requests() []*http.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*http.Request(nil), d.log...)
}

func newFake(t *testing.T, h http.HandlerFunc) *routeDoer {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &routeDoer{srv: srv}
}

func testKey(t *testing.T) (*rsa.PrivateKey, Secret) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	return k, NewSecret(string(pemBytes))
}
