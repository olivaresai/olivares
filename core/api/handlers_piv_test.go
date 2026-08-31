// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// PIV/CAC route tests: a real test CA, real leaf certificates
// (SAN rfc822 binding), a real OCSP responder over httptest, and the client
// chain injected as the TLS peer state — no mocked verification anywhere.

type pivCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newPIVCA(t *testing.T) *pivCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Agency CA", Organization: []string{"U.S. Government"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &pivCA{cert: cert, key: key, pool: pool}
}

// issue mints a client (smartcard) certificate bound to email, with the OCSP
// responder URL in its AIA when given.
func (ca *pivCA) issue(t *testing.T, cn, email, ocspURL string, serial int64) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: []string{"Agency"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if email != "" {
		tpl.EmailAddresses = []string{email}
	}
	if ocspURL != "" {
		tpl.OCSPServer = []string{ocspURL}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// ocspResponder serves real signed OCSP responses with the given status,
// issuer-signed and fresh (ThisUpdate -1m, NextUpdate +1h).
func (ca *pivCA) ocspResponder(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return ca.ocspResponderWith(t, status, time.Now().Add(-time.Minute), time.Now().Add(time.Hour), nil, nil)
}

// ocspResponderWith serves signed OCSP responses with full control over the
// validity window and (optionally) a delegated responder certificate/key.
func (ca *pivCA) ocspResponderWith(t *testing.T, status int, thisUpdate, nextUpdate time.Time, responderCert *x509.Certificate, responderKey *ecdsa.PrivateKey) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tpl := ocsp.Response{
			Status: status, SerialNumber: req.SerialNumber,
			ThisUpdate: thisUpdate, NextUpdate: nextUpdate,
		}
		if status == ocsp.Revoked {
			tpl.RevokedAt = time.Now().Add(-time.Minute)
			tpl.RevocationReason = ocsp.KeyCompromise
		}
		signCert, signKey := ca.cert, ca.key
		if responderCert != nil {
			tpl.Certificate = responderCert
			signCert, signKey = responderCert, responderKey
		}
		der, err := ocsp.CreateResponse(ca.cert, signCert, tpl, signKey)
		if err != nil {
			http.Error(w, "responder error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(der)
	}))
}

// issueResponder mints a delegated OCSP responder certificate under the CA,
// with or without the id-kp-OCSPSigning EKU.
func (ca *pivCA) issueResponder(t *testing.T, withEKU bool, serial int64) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "OCSP Responder"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if withEKU {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// newPIVHarness is newHarness with the PIV route configured.
func newPIVHarness(t *testing.T, cfg *auth.PIVConfig) *harness {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(context.Background()); return e }); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", PIV: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, authr: authr, signer: signer, setupTok: plaintext}
}

// doTLS issues a request carrying a TLS peer chain (the mTLS client cert).
func (h *harness) doTLS(method, path, token string, peer []*x509.Certificate) resp {
	h.t.Helper()
	req := httptest.NewRequest(method, path, http.NoBody)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.TLS = &tls.ConnectionState{PeerCertificates: peer}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// TestPIVUnconfiguredSeam pins the honest 501: with no PIV CA configured the
// status endpoint reports piv_not_configured (the pending seam) and
// elevation is impossible.
func TestPIVUnconfiguredSeam(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	r := h.do("GET", "/v1/auth/piv/status", token, nil, nil)
	if r.code != http.StatusNotImplemented || r.body["error"].(map[string]any)["code"] != "piv_not_configured" {
		t.Fatalf("unconfigured piv status = %d %s, want 501 piv_not_configured", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/auth/piv/elevate", token, nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("unconfigured piv elevate = %d %s, want 501", r.code, r.raw)
	}
}

// TestPIVStatusAndElevate is the happy path: a chain-valid,
// OCSP-good certificate bound to the session user's email reports its
// subject / cert-to-role mapping / OCSP state and elevates the session to AAL3.
func TestPIVStatusAndElevate(t *testing.T) {
	ca := newPIVCA(t)
	responder := ca.ocspResponder(t, ocsp.Good)
	defer responder.Close()
	leaf := ca.issue(t, "Root Operator", "root@x.io", responder.URL, 100)

	cfg := &auth.PIVConfig{
		Roots:   ca.pool,
		RoleMap: []auth.PIVRoleRule{{Subject: regexp.MustCompile(`OU=Agency`), Role: "admin"}},
	}
	h := newPIVHarness(t, cfg)
	token := h.adminLogin()

	st := h.doTLS("GET", "/v1/auth/piv/status", token, []*x509.Certificate{leaf})
	if st.code != http.StatusOK {
		t.Fatalf("piv status = %d %s", st.code, st.raw)
	}
	if st.body["presented"] != true || st.body["ocsp"] != "good" || st.body["mapped_role"] != "admin" {
		t.Fatalf("piv status body = %s", st.raw)
	}
	if st.body["subject"] == "" || st.body["not_after"] == "" {
		t.Fatalf("piv status missing subject/not_after: %s", st.raw)
	}
	// Status is a pure read: no elevation happened.
	if aal, _ := whoamiAAL(t, h, token); aal != 1 {
		t.Fatalf("aal after status read = %d, want 1", aal)
	}

	r := h.doTLS("POST", "/v1/auth/piv/elevate", token, []*x509.Certificate{leaf})
	if r.code != http.StatusOK || r.body["aal"] != float64(3) {
		t.Fatalf("piv elevate = %d %s", r.code, r.raw)
	}
	aal, amr := whoamiAAL(t, h, token)
	if aal != 3 || len(amr) != 2 || amr[1] != "piv" {
		t.Fatalf("post-piv aal/amr = %d %v, want 3 [pwd piv]", aal, amr)
	}
}

// TestPIVDenyPaths pins the fail-closed branches: no certificate, an untrusted
// chain, a revoked certificate, an unknown OCSP state (without the explicit
// opt-out) and a certificate not bound to the calling user are ALL refused —
// and none of them elevates the session.
func TestPIVDenyPaths(t *testing.T) {
	ca := newPIVCA(t)
	good := ca.ocspResponder(t, ocsp.Good)
	defer good.Close()
	revoked := ca.ocspResponder(t, ocsp.Revoked)
	defer revoked.Close()

	cfg := &auth.PIVConfig{Roots: ca.pool}
	h := newPIVHarness(t, cfg)
	token := h.adminLogin()

	check := func(name string, peer []*x509.Certificate, wantCode int, wantErr string) {
		t.Helper()
		r := h.doTLS("POST", "/v1/auth/piv/elevate", token, peer)
		if r.code != wantCode {
			t.Fatalf("%s: elevate = %d %s, want %d", name, r.code, r.raw, wantCode)
		}
		if wantErr != "" && r.body["error"].(map[string]any)["code"] != wantErr {
			t.Fatalf("%s: error code = %s, want %s", name, r.raw, wantErr)
		}
		if aal, _ := whoamiAAL(t, h, token); aal != 1 {
			t.Fatalf("%s: aal = %d, want 1 (never elevated)", name, aal)
		}
	}

	check("no certificate", nil, http.StatusForbidden, "piv_verification_failed")

	otherCA := newPIVCA(t)
	foreign := otherCA.issue(t, "Mallory", "root@x.io", good.URL, 200)
	check("untrusted chain", []*x509.Certificate{foreign}, http.StatusForbidden, "piv_verification_failed")

	rev := ca.issue(t, "Revoked Op", "root@x.io", revoked.URL, 300)
	st := h.doTLS("GET", "/v1/auth/piv/status", token, []*x509.Certificate{rev})
	if st.body["ocsp"] != "revoked" {
		t.Fatalf("revoked status = %s, want ocsp revoked", st.raw)
	}
	check("revoked", []*x509.Certificate{rev}, http.StatusForbidden, "piv_verification_failed")

	noAIA := ca.issue(t, "No Responder", "root@x.io", "", 400)
	check("ocsp unknown", []*x509.Certificate{noAIA}, http.StatusForbidden, "piv_verification_failed")

	wrongUser := ca.issue(t, "Other Person", "other@x.io", good.URL, 500)
	check("subject mismatch", []*x509.Certificate{wrongUser}, http.StatusForbidden, "piv_verification_failed")
}

// TestPIVAllowOCSPUnknown pins the explicit lab opt-out: with
// allow_ocsp_unknown an AIA-less (unknown revocation) chain elevates; the
// default (tested above) refuses it.
func TestPIVAllowOCSPUnknown(t *testing.T) {
	ca := newPIVCA(t)
	cfg := &auth.PIVConfig{Roots: ca.pool, AllowOCSPUnknown: true}
	h := newPIVHarness(t, cfg)
	token := h.adminLogin()

	leaf := ca.issue(t, "Lab Operator", "root@x.io", "", 600)
	r := h.doTLS("POST", "/v1/auth/piv/elevate", token, []*x509.Certificate{leaf})
	if r.code != http.StatusOK || r.body["aal"] != float64(3) {
		t.Fatalf("piv elevate with allow_ocsp_unknown = %d %s", r.code, r.raw)
	}
}

// TestPIVOCSPFreshnessAndResponderAuthority pins the two checks the library
// leaves to the caller (RFC 6960): a stale/replayed "good" response and a
// response missing its NextUpdate are treated as unknown (refused), and a
// delegated responder is honored ONLY with the id-kp-OCSPSigning EKU — without
// it, any agency-issued end-entity cert could forge a "good" for someone
// else's revoked card.
func TestPIVOCSPFreshnessAndResponderAuthority(t *testing.T) {
	ca := newPIVCA(t)
	cfg := &auth.PIVConfig{Roots: ca.pool}

	cases := []struct {
		name      string
		responder *httptest.Server
		wantOK    bool
	}{
		{"stale window (replayed capture)",
			ca.ocspResponderWith(t, ocsp.Good, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour), nil, nil), false},
		{"no NextUpdate (unbounded validity)",
			ca.ocspResponderWith(t, ocsp.Good, time.Now().Add(-time.Minute), time.Time{}, nil, nil), false},
		{"delegated responder WITHOUT OCSPSigning EKU (forgeable)",
			func() *httptest.Server {
				rc, rk := ca.issueResponder(t, false, 700)
				return ca.ocspResponderWith(t, ocsp.Good, time.Now().Add(-time.Minute), time.Now().Add(time.Hour), rc, rk)
			}(), false},
		{"delegated responder WITH OCSPSigning EKU",
			func() *httptest.Server {
				rc, rk := ca.issueResponder(t, true, 701)
				return ca.ocspResponderWith(t, ocsp.Good, time.Now().Add(-time.Minute), time.Now().Add(time.Hour), rc, rk)
			}(), true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer tc.responder.Close()
			h := newPIVHarness(t, cfg)
			token := h.adminLogin()
			leaf := ca.issue(t, "Op", "root@x.io", tc.responder.URL, int64(800+i))
			r := h.doTLS("POST", "/v1/auth/piv/elevate", token, []*x509.Certificate{leaf})
			if tc.wantOK && (r.code != http.StatusOK || r.body["aal"] != float64(3)) {
				t.Fatalf("elevate = %d %s, want 200 aal 3", r.code, r.raw)
			}
			if !tc.wantOK {
				if r.code != http.StatusForbidden {
					t.Fatalf("elevate = %d %s, want 403", r.code, r.raw)
				}
				if aal, _ := whoamiAAL(t, h, token); aal != 1 {
					t.Fatalf("aal = %d, want 1 (never elevated)", aal)
				}
			}
		})
	}
}

// TestPIVUPNBinding pins the FIPS 201-3 reality: a CAC auth certificate often
// carries the principal as a UPN otherName SAN with NO rfc822 email — that
// UPN binds to the session user's email like an email SAN does.
func TestPIVUPNBinding(t *testing.T) {
	ca := newPIVCA(t)
	responder := ca.ocspResponder(t, ocsp.Good)
	defer responder.Close()
	cfg := &auth.PIVConfig{Roots: ca.pool}
	h := newPIVHarness(t, cfg)
	token := h.adminLogin()

	leaf := ca.issueUPN(t, "CAC Holder", "root@x.io", responder.URL, 900)
	if len(leaf.EmailAddresses) != 0 {
		t.Fatalf("test premise broken: UPN leaf has an email SAN %v", leaf.EmailAddresses)
	}
	r := h.doTLS("POST", "/v1/auth/piv/elevate", token, []*x509.Certificate{leaf})
	if r.code != http.StatusOK || r.body["aal"] != float64(3) {
		t.Fatalf("piv elevate with UPN binding = %d %s", r.code, r.raw)
	}

	// A UPN naming someone else still refuses (the binding is the point).
	other := ca.issueUPN(t, "Other Holder", "other@x.io", responder.URL, 901)
	r = h.doTLS("POST", "/v1/auth/piv/elevate", token, []*x509.Certificate{other})
	if r.code != http.StatusForbidden {
		t.Fatalf("piv elevate with foreign UPN = %d %s, want 403", r.code, r.raw)
	}
}

// issueUPN mints a client certificate whose ONLY subject alt name is a UPN
// otherName (no rfc822 email), as real PIV/CAC auth certs are issued.
func (ca *pivCA) issueUPN(t *testing.T, cn, upn, ocspURL string, serial int64) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// otherName ::= [0] SEQUENCE { type-id OID, value [0] EXPLICIT UTF8String }
	upnValue, err := asn1.MarshalWithParams(upn, "utf8")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := asn1.MarshalWithParams(asn1.RawValue{
		Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: upnValue,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	oidDER, err := asn1.Marshal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 20, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	otherName, err := asn1.MarshalWithParams(asn1.RawValue{
		Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true,
		Bytes: append(oidDER, wrapped...),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	sanDER, err := asn1.Marshal(asn1.RawValue{
		Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: otherName,
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: []string{"Agency"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{{
			Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Value: sanDER,
		}},
	}
	if ocspURL != "" {
		tpl.OCSPServer = []string{ocspURL}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
