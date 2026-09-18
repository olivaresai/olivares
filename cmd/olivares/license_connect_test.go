// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

// ⚠ PROTOCOL-UNIT PROOF. connectStub below is an owned, in-process HTTP fixture that follows the
// connect-v1 request/response shapes this client implements (license_connect_wire.go, provisional
// per an internal design note (not shipped)). It is NOT the
// license Worker and NOT D1: its proofs, idempotent replay and fault injection model the protocol
// contract so the CLIENT's persistence, verification and preservation rules can be driven through
// the real command tree. The joint Worker/D1 client proof follows B's accepted delivery.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/license/connectv1"
)

type stubChallenge struct {
	nonce, op, target, digest, idem, kid, method, path string
	epoch, exp                                         int64
	used                                               bool
}

type stubReq struct {
	id, op, target, kid, approvalID string
	approved                        bool
}

type stubDep struct {
	id, kid string
	epoch   int64
	status  string
	seq     int
}

type stubStored struct {
	digest string
	status int
	body   []byte
}

type stubRefusal struct {
	status int
	code   string
}

type stubSeen struct {
	method, path, rawQuery string
	header                 http.Header
	body                   []byte
}

type connectStub struct {
	t       *testing.T
	srv     *httptest.Server
	licPub  ed25519.PublicKey
	licPriv ed25519.PrivateKey

	mu         sync.Mutex
	n          int
	challenges map[string]*stubChallenge
	requests   map[string]*stubReq
	deps       map[string]*stubDep
	stored     map[string]stubStored
	seen       []stubSeen
	issued     []string // every credential blob, OTA token and approval id minted

	lose              map[string]int
	refuse            map[string]stubRefusal // by step: challenge, request, complete, refresh, recover, reactivate, delete
	signWith          ed25519.PrivateKey
	expiredCredential bool
	dropNoStore       bool
	staleStored       bool   // the stored rotation result is no longer servable (403 credential_reissue_required)
	mixedAddon        bool   // credentials carry a refund-window add-on with a 72-hour lease beside a term base
	summary           string // credential_effective_until to answer instead of the default
	lastSummary       string // the credential_effective_until of the last credential answer
	transitions       map[string]int
	download          http.Handler
	downloadAuth      []string
}

func newConnectStub(t *testing.T) *connectStub {
	t.Helper()
	seed := bytes.Repeat([]byte{0x5C}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(seed)
	s := &connectStub{t: t, licPriv: priv, licPub: priv.Public().(ed25519.PublicKey),
		challenges: map[string]*stubChallenge{}, requests: map[string]*stubReq{}, deps: map[string]*stubDep{},
		stored: map[string]stubStored{}, lose: map[string]int{}, refuse: map[string]stubRefusal{}, transitions: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *connectStub) next(prefix string) string {
	s.n++
	return fmt.Sprintf("%s_%d", prefix, s.n)
}

func (s *connectStub) write(w http.ResponseWriter, status int, obj any) []byte {
	body, _ := json.Marshal(obj)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(connectv1.HeaderProtocol, connectv1.Protocol)
	if !s.dropNoStore {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return body
}

func (s *connectStub) refuseWith(w http.ResponseWriter, status int, code string) {
	w.Header().Set(connectv1.HeaderError, code)
	s.write(w, status, map[string]any{"error": code, "action": "server text the client must not echo"})
}

// dropAfterCommit closes the connection without an answer: the effect is committed and the
// response is lost.
func (s *connectStub) dropAfterCommit(w http.ResponseWriter, key string) bool {
	if s.lose[key] <= 0 {
		return false
	}
	s.lose[key]--
	hj, ok := w.(http.Hijacker)
	if !ok {
		s.t.Fatal("the stub cannot drop a response")
	}
	conn, _, err := hj.Hijack()
	if err == nil {
		_ = conn.Close()
	}
	return true
}

func kidOf(t *testing.T, pubB64 string) (string, ed25519.PublicKey) {
	pub, err := connectv1.DecodePublicKey(pubB64)
	if err != nil {
		return "", nil
	}
	kid, _ := connectv1.KID(pub)
	return kid, pub
}

// verifyProof checks the challenge binding and the primary proof by pubB64 for operation op.
func (s *connectStub) verifyProof(r *http.Request, body []byte, pubB64, op string) (*stubChallenge, connectv1.PopMessage, bool) {
	ch := s.challenges[r.Header.Get(connectv1.HeaderChallenge)]
	kid, pub := kidOf(s.t, pubB64)
	if ch == nil || ch.used || pub == nil || ch.op != op || ch.method != r.Method || ch.path != r.URL.Path ||
		ch.digest != connectv1.BodyDigest(body) || ch.idem != r.Header.Get(connectv1.HeaderIdempotencyKey) || ch.kid != kid {
		return nil, connectv1.PopMessage{}, false
	}
	msg := connectv1.PopMessage{BindingEpoch: ch.epoch, BodySHA256: ch.digest, Challenge: ch.nonce, Exp: ch.exp,
		IdempotencyKey: ch.idem, KID: ch.kid, Method: ch.method, Operation: ch.op, Origin: s.srv.URL, Path: ch.path, Target: ch.target}
	if !connectv1.Verify(pub, msg, r.Header.Get(connectv1.HeaderProof)) {
		return nil, connectv1.PopMessage{}, false
	}
	ch.used = true
	return ch, msg, true
}

// credential signs a v3 credential for deployment with the stub's license key (or signWith).
func (s *connectStub) credential(deployment string, seq int, expired bool, signer ed25519.PrivateKey) string {
	return s.credentialIssued(deployment, seq, expired, signer, time.Now().UTC().Truncate(time.Second))
}

// credentialIssued signs a credential issued at issued. With mixedAddon the base line is a 30-day term
// and an add-on line is in its refund window with a 72-hour lease: the shape connect-v1 B derives
// from a paid mixed renewal.
func (s *connectStub) credentialIssued(deployment string, seq int, expired bool, signer ed25519.PrivateKey, issued time.Time) string {
	if signer == nil {
		signer = s.licPriv
	}
	kid, _ := license.KeyID(signer.Public().(ed25519.PublicKey))
	paid := "2099-01-01T00:00:00Z"
	if expired {
		issued = issued.Add(-30 * 24 * time.Hour)
		paid = issued.Add(24 * time.Hour).Format(time.RFC3339)
	}
	addon := ""
	if s.mixedAddon && !expired {
		at := func(d time.Duration) string { return issued.Add(d).Format(time.RFC3339) }
		paid = at(30 * 24 * time.Hour)
		addon = fmt.Sprintf(`,{"grant_id":"gr_ids","order_line_id":"ol_ids","product_id":"pdt_identity_scale","kind":"addon",`+
			`"cadence":"month","paid_through":%q,"expires_at":%q,"issuance_phase":"refund_window","guarantee_deadline":%q,`+
			`"promotion_hold_deadline":%q,"lease_until":%q}`, paid, at(72*time.Hour), at(14*24*time.Hour), paid, at(72*time.Hour))
	}
	iat := issued.Format(time.RFC3339)
	payload := fmt.Sprintf(`{"schema":"olivares.commercial.credential.v3","serial":"conn_production_%s_%d","issue_seq":%d,`+
		`"key_id":%q,"key_epoch":1,"issued_at":%q,"not_before":%q,"entity_id":"cus_1","deployment_id":%q,`+
		`"purpose":"production","licensee":{"display_name":"ACME S.L."},"grants":[{"grant_id":"gr_base",`+
		`"order_line_id":"ol_base","product_id":"pdt_business","kind":"base","cadence":"year","paid_through":%q,`+
		`"expires_at":%q,"issuance_phase":"term","guarantee_deadline":null,"promotion_hold_deadline":null,"lease_until":null}%s]}`,
		deployment, seq, seq, kid, iat, iat, deployment, paid, paid, addon)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(ed25519.Sign(signer, []byte(payload)))
}

func (s *connectStub) credentialAnswer(d *stubDep) map[string]any {
	d.seq++
	issued := time.Now().UTC().Truncate(time.Second)
	blob := s.credentialIssued(d.id, d.seq, s.expiredCredential, s.signWith, issued)
	ota := fmt.Sprintf("ota-bearer-%s-%d-%d", d.id, d.seq, time.Now().UnixNano())
	s.issued = append(s.issued, blob, ota)
	effective := "2099-01-01T00:00:00Z"
	if s.expiredCredential {
		effective = time.Now().UTC().Add(-29 * 24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	}
	if s.mixedAddon && !s.expiredCredential {
		effective = issued.Add(30 * 24 * time.Hour).Format(time.RFC3339) // B: the base line's boundary
	}
	if s.summary != "" {
		effective = s.summary
	}
	s.lastSummary = effective
	return map[string]any{"deployment_id": d.id, "slot": 1, "purpose": "production", "parent": nil, "pop_kid": d.kid,
		"binding_epoch": d.epoch, "credential": blob, "credential_serial": fmt.Sprintf("conn_production_%s_%d", d.id, d.seq),
		"credential_issue_seq": d.seq, "phase": "term", "credential_effective_until": effective,
		"ota": ota, "exp": time.Now().Add(24 * time.Hour).Unix(), "version": "26.9.0"}
}

// replay answers a repeated operation from its stored result; ok is false when there is none.
func (s *connectStub) replay(w http.ResponseWriter, key, digest string) bool {
	st, ok := s.stored[key]
	if !ok {
		return false
	}
	if st.digest != digest {
		s.refuseWith(w, http.StatusConflict, "operation_conflict")
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(connectv1.HeaderProtocol, connectv1.Protocol)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(st.status)
	_, _ = w.Write(st.body)
	return true
}

func (s *connectStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body := make([]byte, 0)
	if r.Body != nil {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		body = b.Bytes()
	}
	s.seen = append(s.seen, stubSeen{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, header: r.Header.Clone(), body: body})
	if strings.HasPrefix(r.URL.Path, "/download/") {
		s.downloadAuth = append(s.downloadAuth, r.Header.Get("Authorization"))
		if s.download == nil {
			http.NotFound(w, r)
			return
		}
		r.Body = http.NoBody
		s.download.ServeHTTP(w, r)
		return
	}
	if r.Header.Get(connectv1.HeaderProtocol) != connectv1.Protocol {
		s.refuseWith(w, http.StatusUnprocessableEntity, "protocol_invalid")
		return
	}
	obj, err := connectv1.ParseStrictObject(body, connectv1.BodyMax)
	if err != nil {
		s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
		return
	}
	str := func(k string) string { v, _ := obj[k].(string); return v }
	// Field sets are B's published table (PROTOCOL.md "Operations"); anything else is 422.
	exact := func(required, optional []string) bool {
		if connectv1.RequireExactFields(obj, required, optional) != nil {
			s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
			return false
		}
		return true
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == connectv1.PathChallenges:
		if connectv1.RequireExactFields(obj, []string{"operation", "target", "body_sha256", "idempotency_key", "kid"}, []string{"binding_epoch"}) != nil {
			s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
			return
		}
		if rf, ok := s.refuse["challenge"]; ok {
			s.refuseWith(w, rf.status, rf.code)
			return
		}
		method, path, err := connectv1.IntendedRoute(str("operation"), str("target"))
		if err != nil {
			s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
			return
		}
		epoch, _ := obj["binding_epoch"].(int64)
		id := s.next("chl")
		exp := time.Now().Add(120 * time.Second).UTC().Truncate(time.Second)
		s.challenges[id] = &stubChallenge{nonce: s.next("n"), op: str("operation"), target: str("target"), digest: str("body_sha256"),
			idem: str("idempotency_key"), kid: str("kid"), method: method, path: path, epoch: epoch, exp: exp.Unix()}
		s.write(w, http.StatusOK, map[string]any{"challenge_id": id, "nonce": s.challenges[id].nonce, "expires_at": exp.Format(time.RFC3339),
			"origin": s.srv.URL, "method": method, "path": path, "domain": connectv1.Domain})
	case r.Method == http.MethodPost && r.URL.Path == connectv1.PathDeployments && obj["approval_id"] != nil:
		if exact([]string{"request_id", "approval_id", "public_key", "channel"}, nil) {
			s.completeBind(w, r, body, obj, str)
		}
	case r.Method == http.MethodPost && r.URL.Path == connectv1.PathDeployments:
		if exact([]string{"provider", "business_id", "holder_id", "license_id", "purpose", "public_key", "evidence"},
			[]string{"label", "parent", "operation", "target_deployment_id"}) {
			s.request(w, r, body, obj, str)
		}
	case r.Method == http.MethodPost && r.URL.Path == connectv1.PathRefresh:
		if exact([]string{"deployment_id", "channel", "public_key"}, nil) {
			s.refresh(w, r, body, str)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rotate-key"):
		if exact([]string{"deployment_id", "operation", "new_public_key", "channel"}, []string{"public_key", "approval_id"}) {
			s.rotate(w, r, body, obj, str)
		}
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, connectv1.PathDeployments+"/"):
		if exact([]string{"deployment_id", "public_key"}, []string{"approval_id"}) {
			s.remove(w, r, body, str)
		}
	default:
		s.refuseWith(w, http.StatusUnprocessableEntity, "protocol_invalid")
	}
}

func (s *connectStub) request(w http.ResponseWriter, r *http.Request, body []byte, obj map[string]any, str func(string) string) {
	ch, _, ok := s.verifyProof(r, body, str("public_key"), connectv1.OpBindPending)
	if !ok {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	if str("evidence") == "" {
		s.refuseWith(w, http.StatusForbidden, "authority_denied")
		return
	}
	key := ch.target + "|bind_pending|" + ch.idem
	if st, ok := s.stored[key]; ok {
		if st.digest != ch.digest {
			s.refuseWith(w, http.StatusConflict, "operation_conflict")
			return
		}
		var pending map[string]string
		_ = json.Unmarshal(st.body, &pending)
		req := s.requests[pending["request_id"]]
		if req.approved {
			if s.dropAfterCommit(w, "approved") {
				return
			}
			s.write(w, http.StatusOK, map[string]any{"status": "approved", "request_id": req.id, "approval_id": req.approvalID})
			return
		}
		s.replay(w, key, ch.digest)
		return
	}
	if rf, ok := s.refuse["request"]; ok {
		s.refuseWith(w, rf.status, rf.code)
		return
	}
	kid, _ := kidOf(s.t, str("public_key"))
	op := str("operation")
	if op == "" {
		op = "bind"
	}
	req := &stubReq{id: s.next("req"), op: op, target: str("target_deployment_id"), kid: kid}
	s.requests[req.id] = req
	var target any
	if req.target != "" {
		target = req.target
	}
	_, pub := kidOf(s.t, str("public_key"))
	fp, _ := connectv1.Fingerprint(pub)
	b := s.write(w, http.StatusAccepted, map[string]any{"status": "pending", "request_id": req.id, "operation": req.op,
		"target_deployment_id": target, "pop_fingerprint": fp, "approval_url": s.srv.URL + "/portal/connect/approvals/" + req.id})
	s.stored[key] = stubStored{digest: ch.digest, status: http.StatusAccepted, body: b}
}

func (s *connectStub) approve(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.requests[requestID]
	if req == nil {
		s.t.Fatalf("no request %s", requestID)
	}
	req.approved, req.approvalID = true, s.next("apr_one_use_reference")
	s.issued = append(s.issued, req.approvalID)
}

// commit runs a committed answer: store it for replay, then drop or write it.
func (s *connectStub) commit(w http.ResponseWriter, key, digest, fault string, answer map[string]any) {
	body, _ := json.Marshal(answer)
	s.stored[key] = stubStored{digest: digest, status: http.StatusOK, body: body}
	if s.dropAfterCommit(w, fault) {
		return
	}
	s.write(w, http.StatusOK, answer)
}

func (s *connectStub) completeBind(w http.ResponseWriter, r *http.Request, body []byte, obj map[string]any, str func(string) string) {
	ch, _, ok := s.verifyProof(r, body, str("public_key"), connectv1.OpBind)
	if !ok {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	key := ch.target + "|bind|" + ch.idem
	if s.replay(w, key, ch.digest) {
		return
	}
	req := s.requests[str("request_id")]
	kid, _ := kidOf(s.t, str("public_key"))
	if req == nil || !req.approved || req.approvalID != str("approval_id") || req.kid != kid || req.op != "bind" {
		s.refuseWith(w, http.StatusForbidden, "approval_required")
		return
	}
	if rf, ok := s.refuse["complete"]; ok {
		s.refuseWith(w, rf.status, rf.code)
		return
	}
	d := &stubDep{id: s.next("dep"), kid: kid, epoch: 1, status: "active"}
	s.deps[d.id] = d
	s.commit(w, key, ch.digest, "complete", s.credentialAnswer(d))
}

func (s *connectStub) refresh(w http.ResponseWriter, r *http.Request, body []byte, str func(string) string) {
	d := s.deps[str("deployment_id")]
	kid, _ := kidOf(s.t, str("public_key"))
	if d == nil || d.status != "active" || d.kid != kid {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	ch, _, ok := s.verifyProof(r, body, str("public_key"), connectv1.OpRefresh)
	if !ok {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	if ch.epoch != d.epoch {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	key := d.id + "|refresh|" + ch.idem
	if s.replay(w, key, ch.digest) {
		return
	}
	if rf, ok := s.refuse["refresh"]; ok {
		s.refuseWith(w, rf.status, rf.code)
		return
	}
	s.commit(w, key, ch.digest, "refresh", s.credentialAnswer(d))
}

func (s *connectStub) rotate(w http.ResponseWriter, r *http.Request, body []byte, obj map[string]any, str func(string) string) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, connectv1.PathDeployments+"/"), "/rotate-key")
	d := s.deps[id]
	if d == nil || str("deployment_id") != id {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	op, approval := str("operation"), str("approval_id")
	var signer string
	switch op {
	case connectv1.OpRotateKey:
		if approval != "" || str("public_key") == "" {
			s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
			return
		}
		signer = str("public_key")
	case connectv1.OpRecover, connectv1.OpReactivate:
		if approval == "" || obj["public_key"] != nil {
			s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
			return
		}
		signer = str("new_public_key")
	default:
		s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
		return
	}
	ch, msg, ok := s.verifyProof(r, body, signer, op)
	if !ok {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	newKID, newPub := kidOf(s.t, str("new_public_key"))
	if op == connectv1.OpRotateKey && (newPub == nil || !connectv1.Verify(newPub, msg, r.Header.Get(connectv1.HeaderNewKeyProof))) {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	// B's rule: proofs first, then the stored result BEFORE any binding check, keyed by target,
	// operation, Idempotency-Key and the primary signer.
	signerKID, _ := kidOf(s.t, signer)
	key := id + "|" + op + "|" + ch.idem + "|" + signerKID
	if st, ok := s.stored[key]; ok {
		if st.digest != ch.digest {
			s.refuseWith(w, http.StatusConflict, "operation_conflict")
			return
		}
		if s.staleStored {
			s.refuseWith(w, http.StatusForbidden, "credential_reissue_required")
			return
		}
		s.replay(w, key, ch.digest)
		return
	}
	if ch.epoch != d.epoch {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	if op == connectv1.OpRotateKey {
		if signerKID != d.kid {
			s.refuseWith(w, http.StatusForbidden, "binding_denied")
			return
		}
	} else {
		var req *stubReq
		for _, q := range s.requests {
			if q.approvalID == approval {
				req = q
			}
		}
		if req == nil || !req.approved || req.op != op || req.target != id || req.kid != newKID {
			s.refuseWith(w, http.StatusForbidden, "recovery_required")
			return
		}
		if rf, ok := s.refuse[op]; ok {
			s.refuseWith(w, rf.status, rf.code)
			return
		}
	}
	d.kid, d.status = newKID, "active"
	d.epoch++
	s.transitions[id]++
	fault := op
	if op == connectv1.OpRotateKey {
		fault = "rotate"
	}
	s.commit(w, key, ch.digest, fault, s.credentialAnswer(d))
}

func (s *connectStub) remove(w http.ResponseWriter, r *http.Request, body []byte, str func(string) string) {
	id := strings.TrimPrefix(r.URL.Path, connectv1.PathDeployments+"/")
	d := s.deps[id]
	ch, _, ok := s.verifyProof(r, body, str("public_key"), connectv1.OpDelete)
	if d == nil || !ok || str("deployment_id") != id {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	kid, _ := kidOf(s.t, str("public_key"))
	if approval := str("approval_id"); approval != "" {
		var req *stubReq
		for _, q := range s.requests {
			if q.approvalID == approval {
				req = q
			}
		}
		if req == nil || !req.approved || req.op != "delete" || req.target != id || req.kid != kid {
			s.refuseWith(w, http.StatusForbidden, "approval_required")
			return
		}
		if rf, ok := s.refuse["delete"]; ok {
			s.refuseWith(w, rf.status, rf.code)
			return
		}
	} else if kid != d.kid {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	d.status = "deleted"
	s.commit(w, id+"|delete|"+ch.idem, ch.digest, "delete", map[string]any{"deployment_id": id, "deleted": true})
}

func (s *connectStub) snapshot() (seen []stubSeen, issued []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubSeen(nil), s.seen...), append([]string(nil), s.issued...)
}

// ---- client-side helpers ----------------------------------------------------------------

type connectCLI struct {
	t       *testing.T
	stub    *connectStub
	dir     string
	outputs []string
	// purchase is the purchase credential the data directory started with, before bind replaced it.
	purchase string
	// lastError is the error text of the last run, "" when it succeeded.
	lastError string
}

// purchaseEvidence writes the purchase credential to a 0600 file and returns its path.
func (c *connectCLI) purchaseEvidence() string {
	c.t.Helper()
	p := filepath.Join(c.t.TempDir(), "purchase-credential.v3")
	if err := os.WriteFile(p, []byte(c.purchase+"\n"), 0o600); err != nil {
		c.t.Fatal(err)
	}
	return p
}

// newConnectCLI prepares a data directory that trusts the stub's license key and holds an EXPIRED
// v3 credential as its installed license (the reconnecting buyer's case).
func newConnectCLI(t *testing.T, stub *connectStub) *connectCLI {
	t.Helper()
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	dir := t.TempDir()
	if _, err := writeLicenseTrustDocument(dir, license.TrustDocument{Keys: []license.TrustDocumentKey{{PublicKey: stub.licPub, State: license.KeyStateCurrent}}}); err != nil {
		t.Fatal(err)
	}
	purchase := stub.credential("dep_evidence", 1, true, nil)
	if err := os.WriteFile(licenseDataDirPath(dir), []byte(purchase+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &connectCLI{t: t, stub: stub, dir: dir, purchase: purchase}
}

func (c *connectCLI) run(args ...string) (int, map[string]any) {
	c.t.Helper()
	argv := append([]string{"license", "connect"}, args...)
	argv = append(argv, "--data-dir", c.dir)
	code, stdout, stderr, err := runCLIExit(c.t, argv...)
	c.outputs = append(c.outputs, stdout, stderr)
	c.lastError = ""
	if err != nil {
		// In process the command's error is returned, not printed; keep it as output too, so the
		// no-secret scan covers the diagnostics an operator would see.
		c.lastError = err.Error()
		c.outputs = append(c.outputs, c.lastError)
	}
	var rep map[string]any
	_ = json.Unmarshal([]byte(stdout), &rep)
	c.t.Logf("connect %v -> %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	return code, rep
}

func (c *connectCLI) start() (int, map[string]any) {
	return c.run("start", "--endpoint", c.stub.srv.URL, "--business-id", "bus_1", "--holder-id", "sub_1", "--license-id", "lic_1")
}

func (c *connectCLI) state() *connectState {
	c.t.Helper()
	data, err := os.ReadFile(filepath.Join(c.dir, connectDirName, connectStateFileName))
	if err != nil {
		c.t.Fatal(err)
	}
	var st connectState
	if err := json.Unmarshal(data, &st); err != nil {
		c.t.Fatal(err)
	}
	return &st
}

func (c *connectCLI) file(name string) []byte {
	c.t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		c.t.Fatal(err)
	}
	return b
}

func (c *connectCLI) license() []byte { return c.file(licenseDataDirPath(c.dir)) }

func (c *connectCLI) token() []byte {
	b, _ := os.ReadFile(filepath.Join(c.dir, connectDirName, connectTokenFileName))
	return b
}

func requestIDOf(t *testing.T, rep map[string]any) string {
	t.Helper()
	req, _ := rep["request"].(map[string]any)
	id, _ := req["request_id"].(string)
	if id == "" {
		t.Fatalf("no request id in %v", rep)
	}
	return id
}

// bind drives a fresh data directory to an active binding.
func (c *connectCLI) bind() string {
	c.t.Helper()
	code, rep := c.start()
	if code != exitcode.OK || rep["status"] != "approval_pending" {
		c.t.Fatalf("start = %d %v", code, rep)
	}
	c.stub.approve(requestIDOf(c.t, rep))
	code, rep = c.start()
	if code != exitcode.OK || rep["status"] != "bound" {
		c.t.Fatalf("completion = %d %v", code, rep)
	}
	return rep["deployment_id"].(string)
}

// assertNoSecretsLeaked: no credential, token, approval reference, private seed or Idempotency-Key
// appears in any output, and no request carried a query string or an approval reference outside a
// request body.
func (c *connectCLI) assertNoSecretsLeaked() {
	c.t.Helper()
	seen, issued := c.stub.snapshot()
	secrets := append([]string(nil), issued...)
	for _, name := range []string{connectIdentityFileName, connectNextKeyFileName} {
		if b, err := os.ReadFile(filepath.Join(c.dir, connectDirName, name)); err == nil {
			var w connectIdentityWire
			_ = json.Unmarshal(b, &w)
			secrets = append(secrets, w.Seed)
		}
	}
	for _, s := range seen {
		if k := s.header.Get(connectv1.HeaderIdempotencyKey); k != "" {
			secrets = append(secrets, k)
		}
		// Connect routes carry nothing in the query. The release-v1 download route has its own
		// contract (non-secret selectors in the query, the bearer in Authorization), checked by the
		// release-v1 fixture itself.
		if strings.HasPrefix(s.path, "/connect/") && s.rawQuery != "" {
			c.t.Errorf("%s %s carried a query string %q", s.method, s.path, s.rawQuery)
		}
		if strings.HasPrefix(s.path, "/download/") && strings.Contains(s.rawQuery, "ota-bearer-") {
			c.t.Errorf("a download token reached a query string on %s", s.path)
		}
		for _, a := range issued {
			if strings.HasPrefix(a, "apr_") {
				if strings.Contains(s.path, a) {
					c.t.Errorf("an approval reference reached a request path: %s", s.path)
				}
				for h, vs := range s.header {
					for _, v := range vs {
						if strings.Contains(v, a) {
							c.t.Errorf("an approval reference reached header %s", h)
						}
					}
				}
			}
		}
	}
	for _, out := range c.outputs {
		for _, sec := range secrets {
			if sec != "" && strings.Contains(out, sec) {
				c.t.Errorf("a secret (%d bytes, prefix %.12q) appeared in command output", len(sec), sec)
			}
		}
	}
}

// ---- tests ------------------------------------------------------------------------------

// TestConnectBindCompletesAfterApprovalAcrossResponseLossAndRestart: the request is repeated with
// its persisted bytes and key until approved; the approved replay creates a separate completion
// step, persisted before sending; a lost completion answer is recovered by repeating that step in a
// new invocation, and the expired installed license is replaced only then.
func TestConnectBindCompletesAfterApprovalAcrossResponseLossAndRestart(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	evidence := c.license()

	code, rep := c.start()
	if code != exitcode.OK || rep["status"] != "approval_pending" {
		t.Fatalf("start = %d %v", code, rep)
	}
	reqID := requestIDOf(t, rep)
	first := c.state().Pending
	if first == nil || first.Phase != "request" || first.Attempts != 1 {
		t.Fatalf("the request step must stay persisted: %+v", first)
	}
	if code, rep = c.start(); code != exitcode.OK || rep["status"] != "approval_pending" || requestIDOf(t, rep) != reqID {
		t.Fatalf("an unapproved repeat = %d %v", code, rep)
	}
	again := c.state().Pending
	if again.IdempotencyKey != first.IdempotencyKey || again.Body != first.Body || again.Attempts != 2 {
		t.Fatalf("the repeat is not the same operation: %+v vs %+v", again, first)
	}

	stub.approve(reqID)
	stub.mu.Lock()
	stub.lose["complete"] = 1
	stub.mu.Unlock()
	code, _ = c.start()
	if code != exitcode.Indeterminate {
		t.Fatalf("a lost completion answer must be an unknown outcome, got exit %d", code)
	}
	completion := c.state().Pending
	if completion == nil || completion.Phase != "complete" || completion.IdempotencyKey == first.IdempotencyKey {
		t.Fatalf("the completion must be its own persisted step: %+v", completion)
	}
	if !bytes.Equal(c.license(), evidence) || len(c.token()) != 0 {
		t.Fatal("the installed license or token changed on an unknown outcome")
	}

	// A new invocation is a restart: nothing is carried in memory.
	code, rep = c.start()
	if code != exitcode.OK || rep["status"] != "bound" {
		t.Fatalf("recovery after the lost answer = %d %v", code, rep)
	}
	st := c.state()
	if st.Pending != nil || st.Request != nil || st.Binding == nil || st.Binding.Status != "active" || st.Binding.BindingEpoch != 1 {
		t.Fatalf("state after binding: %+v", st)
	}
	seen, _ := stub.snapshot()
	completions := map[string]int{}
	for _, s := range seen {
		if s.path == connectv1.PathDeployments && bytes.Contains(s.body, []byte(`"approval_id"`)) {
			completions[s.header.Get(connectv1.HeaderIdempotencyKey)+"|"+connectv1.BodyDigest(s.body)]++
		}
	}
	if len(completions) != 1 {
		t.Fatalf("the completion was sent as %d different operations, want one repeated: %v", len(completions), completions)
	}
	kr, _ := licenseKeyringForDataDir(c.dir)
	v, err := kr.Verify(strings.TrimSpace(string(c.license())), time.Now())
	if err != nil || v.Credential.Deployment != st.Binding.DeploymentID || v.Status(time.Now()) != license.StatusValid {
		t.Fatalf("the installed credential: %v %+v", err, v.Credential.Deployment)
	}
	if code, rep = c.start(); code != exitcode.OK || rep["status"] != "bound" {
		t.Fatalf("a repeat after binding must be a no-op: %d %v", code, rep)
	}
	c.assertNoSecretsLeaked()
}

// TestConnectApprovedReplayLossRepeatsTheSameRequest proves the other phase boundary: losing the
// approved replay leaves the request step in place, and the next run learns the approval again.
func TestConnectApprovedReplayLossRepeatsTheSameRequest(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	_, rep := c.start()
	reqID := requestIDOf(t, rep)
	key := c.state().Pending.IdempotencyKey
	stub.approve(reqID)
	stub.mu.Lock()
	stub.lose["approved"] = 1
	stub.mu.Unlock()
	if code, _ := c.start(); code != exitcode.Indeterminate {
		t.Fatalf("a lost approved replay must be unknown, got %d", code)
	}
	if p := c.state().Pending; p == nil || p.Phase != "request" || p.IdempotencyKey != key {
		t.Fatalf("the request step must be kept unchanged: %+v", p)
	}
	if code, rep := c.start(); code != exitcode.OK || rep["status"] != "bound" {
		t.Fatalf("after the lost replay = %d %v", code, rep)
	}
	c.assertNoSecretsLeaked()
}

// TestConnectRefreshRecoversAnExpiredLocalLicenseAndPreservesOnFailure covers acceptance §7.10.
func TestConnectRefreshRecoversAnExpiredLocalLicenseAndPreservesOnFailure(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	dep := c.bind()

	// The installed credential lapses locally; the purchase is still current on the service.
	if err := os.WriteFile(licenseDataDirPath(c.dir), []byte(stub.credential(dep, 99, true, nil)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
		t.Fatalf("refresh with an expired local license = %d %v", code, rep)
	}
	kr, _ := licenseKeyringForDataDir(c.dir)
	if v, err := kr.Verify(strings.TrimSpace(string(c.license())), time.Now()); err != nil || v.Status(time.Now()) != license.StatusValid {
		t.Fatalf("refresh did not install a current credential: %v", err)
	}

	type preserve struct {
		name     string
		arrange  func()
		wantCode int
		keep     bool
		// completes says whether repeating the kept step succeeds once the fault is gone. An
		// untrusted credential does not: the service replays the SAME stored answer, which only
		// a trust update resolves (TestConnectUnknownTrustIsResolvedByATrustUpdateNotAFetch).
		completes bool
	}
	cases := []preserve{
		{"no rights refusal", func() { stub.refuse["refresh"] = stubRefusal{http.StatusForbidden, "authority_denied"} }, exitcode.Auth, false, false},
		{"service unavailable", func() { stub.refuse["refresh"] = stubRefusal{http.StatusServiceUnavailable, "authority_unavailable"} }, exitcode.Server, true, true},
		{"credential from an untrusted key", func() { _, stub.signWith = keyFromSeedForTest(0x77) }, exitcode.Err, true, false},
		{"credential that confers no current right", func() { stub.expiredCredential = true }, exitcode.Auth, false, false},
		{"answer without no-store", func() { stub.dropNoStore = true }, exitcode.Indeterminate, true, true},
		{"lost answer", func() { stub.lose["refresh"] = 1 }, exitcode.Indeterminate, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub.mu.Lock()
			stub.refuse, stub.lose = map[string]stubRefusal{}, map[string]int{}
			stub.signWith, stub.expiredCredential, stub.dropNoStore = nil, false, false
			tc.arrange()
			stub.mu.Unlock()
			lic, tok := c.license(), c.token()
			code, _ := c.run("refresh")
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d", code, tc.wantCode)
			}
			if !bytes.Equal(c.license(), lic) || !bytes.Equal(c.token(), tok) {
				t.Fatal("the installed license or token changed")
			}
			if kept := c.state().Pending != nil; kept != tc.keep {
				t.Fatalf("pending kept = %v, want %v", kept, tc.keep)
			}
			stub.mu.Lock()
			stub.refuse, stub.lose = map[string]stubRefusal{}, map[string]int{}
			stub.signWith, stub.expiredCredential, stub.dropNoStore = nil, false, false
			stub.mu.Unlock()
			switch {
			case tc.keep && tc.completes:
				// The kept step is the same operation: once the fault is gone it completes.
				if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
					t.Fatalf("the kept step did not complete: %d %v", code, rep)
				}
			case tc.keep:
				if code, rep := c.run("refresh"); code != tc.wantCode {
					t.Fatalf("repeating the kept step must return the same stored answer and refusal: %d %v", code, rep)
				}
				if code, rep := c.run("abandon", "--yes"); code != exitcode.OK || rep["status"] != "abandoned" {
					t.Fatalf("abandon = %d %v", code, rep)
				}
			}
		})
	}
	c.assertNoSecretsLeaked()
}

func keyFromSeedForTest(b byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

// TestConnectUnknownTrustIsResolvedByATrustUpdateNotAFetch: a credential signed by a key this data
// directory does not trust keeps the step; the operator adds the key administratively and the SAME
// step completes from the service's stored result.
func TestConnectUnknownTrustIsResolvedByATrustUpdateNotAFetch(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	c.bind()
	otherPub, otherPriv := keyFromSeedForTest(0x42)
	stub.mu.Lock()
	stub.signWith = otherPriv
	stub.mu.Unlock()
	if code, _ := c.run("refresh"); code != exitcode.Err {
		t.Fatalf("untrusted credential exit %d", code)
	}
	key := c.state().Pending.IdempotencyKey
	code, _, stderr, _ := runCLIExit(t, "license", "trust", "set", "--data-dir", c.dir, "--public-key", base64.StdEncoding.EncodeToString(otherPub), "--state", "current")
	if code != exitcode.OK {
		t.Fatalf("trust set: %d %s", code, stderr)
	}
	stub.mu.Lock()
	stub.signWith = nil // the service's stored result is what the repeat returns
	stub.mu.Unlock()
	if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
		t.Fatalf("after the trust update: %d %v", code, rep)
	}
	seen, _ := stub.snapshot()
	n := 0
	for _, s := range seen {
		if s.path == connectv1.PathRefresh && s.header.Get(connectv1.HeaderIdempotencyKey) == key {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("the refresh was sent %d times under its key, want the same operation twice", n)
	}
}

func TestConnectDataDirIsolationAndFileModes(t *testing.T) {
	stub := newConnectStub(t)
	a := newConnectCLI(t, stub)
	b := newConnectCLI(t, stub)
	a.start()
	b.start()
	ida, _ := os.ReadFile(filepath.Join(a.dir, connectDirName, connectIdentityFileName))
	idb, _ := os.ReadFile(filepath.Join(b.dir, connectDirName, connectIdentityFileName))
	var wa, wb connectIdentityWire
	_ = json.Unmarshal(ida, &wa)
	_ = json.Unmarshal(idb, &wb)
	if wa.KID == "" || wa.KID == wb.KID {
		t.Fatalf("two data directories share or lack an identity: %q %q", wa.KID, wb.KID)
	}
	if a.state().Pending.IdempotencyKey == b.state().Pending.IdempotencyKey {
		t.Fatal("two data directories share an operation")
	}
	modes := map[string]os.FileMode{
		filepath.Join(a.dir, connectDirName):                          0o700,
		filepath.Join(a.dir, connectDirName, connectIdentityFileName): 0o600,
		filepath.Join(a.dir, connectDirName, connectStateFileName):    0o600,
		filepath.Join(a.dir, licenseTrustFileName):                    0o600,
	}
	for p, want := range modes {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v (%v), want %04o", p, fi.Mode().Perm(), err, want)
		}
	}
	// Loosened custody is refused, not silently used.
	idPath := filepath.Join(a.dir, connectDirName, connectIdentityFileName)
	if err := os.Chmod(idPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := a.start(); code == exitcode.OK {
		t.Fatal("a group-readable identity must be refused")
	}
	_ = os.Chmod(idPath, 0o600)
	// A second invocation while one holds the lease refuses immediately.
	lease, err := acquireConnectLease(filepath.Join(a.dir, connectDirName))
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := a.run("status"); code != exitcode.Conflict {
		t.Fatalf("a held lease must refuse with Conflict, got %d", code)
	}
	_ = lease.Close()
	if code, rep := a.run("status"); code != exitcode.OK || rep["status"] != "pending" {
		t.Fatalf("status after release = %d %v", code, rep)
	}
}

// TestConnectRotateUsesBothProofsAndResolvesALostAnswer: the proposed key's proof over the same
// message is required; a lost committed rotation is resolved with a refresh signed by the
// proposed key instead of a blind new rotation.
func TestConnectRotateUsesBothProofsAndReplaysALostAnswer(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	dep := c.bind()
	oldKID := c.state().Binding.PopKID

	stub.mu.Lock()
	stub.lose["rotate"] = 1
	stub.mu.Unlock()
	if code, _ := c.run("rotate-key"); code != exitcode.Indeterminate {
		t.Fatalf("a lost rotation answer must be unknown, got %d", code)
	}
	pending := c.state().Pending
	if c.state().Binding.PopKID != oldKID || pending == nil || pending.Intent != "rotate" || pending.BindingEpoch != 1 {
		t.Fatalf("an unknown outcome must keep the binding and the step: %+v", c.state())
	}
	nextBefore := c.file(filepath.Join(c.dir, connectDirName, connectNextKeyFileName))

	code, rep := c.run("rotate-key")
	if code != exitcode.OK || rep["status"] != "rotated" {
		t.Fatalf("replaying the lost rotation = %d %v", code, rep)
	}
	st := c.state()
	if st.Binding.PopKID == oldKID || st.Binding.BindingEpoch != 2 || st.Binding.DeploymentID != dep || st.Pending != nil {
		t.Fatalf("state after the replayed rotation: %+v", st)
	}
	if got := c.file(filepath.Join(c.dir, connectDirName, connectIdentityFileName)); !bytes.Equal(got, nextBefore) {
		t.Fatal("the promoted identity is not the proposed key the rotation was signed with")
	}

	stub.mu.Lock()
	transitions := stub.transitions[dep]
	var rotateAttempts []stubSeen
	for _, s := range stub.seen {
		if strings.HasSuffix(s.path, "/rotate-key") {
			rotateAttempts = append(rotateAttempts, s)
		}
	}
	var epochs []int64
	for _, ch := range stub.challenges {
		if ch.op == connectv1.OpRotateKey {
			epochs = append(epochs, ch.epoch)
		}
	}
	stub.mu.Unlock()
	if transitions != 1 {
		t.Fatalf("the rotation committed %d epoch transitions, want exactly one", transitions)
	}
	if len(rotateAttempts) != 2 {
		t.Fatalf("%d rotate-key requests, want the original and one replay", len(rotateAttempts))
	}
	for _, s := range rotateAttempts {
		if s.header.Get(connectv1.HeaderIdempotencyKey) != pending.IdempotencyKey || connectv1.BodyDigest(s.body) != pending.BodySHA256 ||
			s.header.Get(connectv1.HeaderNewKeyProof) == "" {
			t.Fatal("a replay was not the same operation with both proofs")
		}
	}
	for _, e := range epochs {
		if e != 1 {
			t.Fatalf("a rotation challenge used epoch %d; the replay must keep the original epoch 1", e)
		}
	}
	if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
		t.Fatalf("refresh with the rotated key = %d %v", code, rep)
	}
	c.assertNoSecretsLeaked()
}

// TestConnectRefusedRotationReplayKeepsTheStepAndBothKeys: when the service no longer serves a
// rotation that may have committed, the client does not discard its bytes or either key.
func TestConnectRefusedRotationReplayKeepsTheStepAndBothKeys(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	c.bind()
	current := c.file(filepath.Join(c.dir, connectDirName, connectIdentityFileName))
	stub.mu.Lock()
	stub.lose["rotate"] = 1
	stub.mu.Unlock()
	if code, _ := c.run("rotate-key"); code != exitcode.Indeterminate {
		t.Fatalf("lost answer exit %d", code)
	}
	next := c.file(filepath.Join(c.dir, connectDirName, connectNextKeyFileName))
	key := c.state().Pending.IdempotencyKey
	stub.mu.Lock()
	stub.staleStored = true
	stub.mu.Unlock()
	if code, _ := c.run("rotate-key"); code != exitcode.Auth {
		t.Fatalf("a refused replay exit %d, want Auth", code)
	}
	st := c.state()
	if st.Pending == nil || st.Pending.IdempotencyKey != key {
		t.Fatalf("the rotation step was not kept: %+v", st.Pending)
	}
	if !bytes.Equal(c.file(filepath.Join(c.dir, connectDirName, connectIdentityFileName)), current) ||
		!bytes.Equal(c.file(filepath.Join(c.dir, connectDirName, connectNextKeyFileName)), next) {
		t.Fatal("a key file changed after a refused replay")
	}
	c.assertNoSecretsLeaked()
}

// TestConnectRecoverAndReactivateNeedTheOwnerNotTheLostKey.
func TestConnectRecoverAndReactivateNeedTheOwnerNotTheLostKey(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	dep := c.bind()
	if err := os.Remove(filepath.Join(c.dir, connectDirName, connectIdentityFileName)); err != nil {
		t.Fatal(err)
	}
	if code, _ := c.run("refresh"); code == exitcode.OK {
		t.Fatal("refresh without the bound key must fail")
	}
	// The new request presents the purchase credential; the completion below repeats it without.
	code, rep := c.run("recover", "--deployment", dep, "--evidence", c.purchaseEvidence())
	if code != exitcode.OK || rep["status"] != "approval_pending" {
		t.Fatalf("recover request = %d %v", code, rep)
	}
	stub.approve(requestIDOf(t, rep))
	code, rep = c.run("recover", "--deployment", dep)
	if code != exitcode.OK || rep["status"] != "recovered" {
		t.Fatalf("recover completion = %d %v", code, rep)
	}
	if b := c.state().Binding; b.BindingEpoch != 2 || b.Status != "active" {
		t.Fatalf("binding after recovery: %+v", b)
	}
	if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
		t.Fatalf("refresh after recovery = %d %v", code, rep)
	}

	// Explicit deactivation is not undone by refresh; reactivation is its own approved operation.
	if code, rep := c.run("deactivate", "--yes"); code != exitcode.OK || rep["status"] != "deactivated" {
		t.Fatalf("deactivate = %d %v", code, rep)
	}
	if code, _ := c.run("refresh"); code == exitcode.OK {
		t.Fatal("a deactivated binding must not refresh")
	}
	stub.mu.Lock()
	stub.deps[dep].status = "deleted"
	stub.mu.Unlock()
	code, rep = c.run("reactivate", "--deployment", dep, "--binding-epoch", "2", "--evidence", c.purchaseEvidence())
	if code != exitcode.OK || rep["status"] != "approval_pending" {
		t.Fatalf("reactivate request = %d %v", code, rep)
	}
	stub.approve(requestIDOf(t, rep))
	code, rep = c.run("reactivate", "--deployment", dep, "--binding-epoch", "2")
	if code != exitcode.OK || rep["status"] != "reactivated" {
		t.Fatalf("reactivate completion = %d %v", code, rep)
	}
	if b := c.state().Binding; b.BindingEpoch != 3 || b.Status != "active" {
		t.Fatalf("binding after reactivation: %+v", b)
	}
	c.assertNoSecretsLeaked()
}

func TestConnectDeactivateByOwnerApproval(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	dep := c.bind()
	lic := c.license()
	code, rep := c.run("deactivate", "--owner-approval", "--evidence", c.purchaseEvidence(), "--yes")
	if code != exitcode.OK || rep["status"] != "approval_pending" {
		t.Fatalf("owner-approved deactivation request = %d %v", code, rep)
	}
	stub.approve(requestIDOf(t, rep))
	code, rep = c.run("deactivate", "--owner-approval", "--yes")
	if code != exitcode.OK || rep["status"] != "deactivated" {
		t.Fatalf("owner-approved deactivation = %d %v", code, rep)
	}
	stub.mu.Lock()
	serviceStatus := stub.deps[dep].status
	stub.mu.Unlock()
	if serviceStatus != "deleted" || c.state().Binding.Status != "deleted" {
		t.Fatal("the deployment was not deactivated")
	}
	if !bytes.Equal(c.license(), lic) {
		t.Fatal("deactivation must not remove or change the installed license")
	}
	c.assertNoSecretsLeaked()
}

// TestConnectRefusesAShadowedLicenseBeforeSending: a license override outranks the data directory,
// so a connected credential would change nothing the engine reads; nothing is sent.
func TestConnectRefusesAShadowedLicenseBeforeSending(t *testing.T) {
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	t.Setenv("OLIVARES_LICENSE", "some-inline-license")
	if code, _ := c.start(); code != exitcode.Usage {
		t.Fatalf("exit %d, want Usage", code)
	}
	if seen, _ := stub.snapshot(); len(seen) != 0 {
		t.Fatalf("%d requests were sent under a shadowing override", len(seen))
	}
}

// TestUpgradeConnectRefreshesThenDownloadsWithTheFreshBearer is acceptance §7.10 through the real
// upgrade command: an expired local credential, a PoP refresh, the verified credential installed,
// then the unchanged release-v1 gate (manifest signature, artifact digest, anti-rollback) using
// the refreshed bearer. A refused refresh leaves the binary and the license untouched and never
// reaches the download route. The connect service is the protocol-unit stub; the release gateway
// is the existing release-v1 fixture behind it.
func TestUpgradeConnectRefreshesThenDownloadsWithTheFreshBearer(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	dep := c.bind()
	f := newV1Fixture(t, "26.8.0", "biz", v2)
	gateway, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.download = httputil.NewSingleHostReverseProxy(gateway)
	stub.mu.Unlock()
	if err := os.WriteFile(licenseDataDirPath(c.dir), []byte(stub.credential(dep, 50, true, nil)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	upgrade := func(target string) int {
		code, stdout, stderr, err := runCLIExit(t, "upgrade", "--enterprise", "--connect", "--endpoint", stub.srv.URL,
			"--pubkey", f.pubB64, "--data-dir", c.dir, "--target", target, "--os", "linux", "--arch", "amd64", "--yes")
		c.outputs = append(c.outputs, stdout, stderr)
		t.Logf("upgrade -> %d %v\n%s\n%s", code, err, stdout, stderr)
		return code
	}

	t.Run("a refused refresh changes nothing and downloads nothing", func(t *testing.T) {
		stub.mu.Lock()
		stub.refuse["refresh"] = stubRefusal{http.StatusForbidden, "authority_denied"}
		stub.mu.Unlock()
		defer func() { stub.mu.Lock(); delete(stub.refuse, "refresh"); stub.mu.Unlock() }()
		target := writeTarget(t, v1)
		lic := c.license()
		if code := upgrade(target); code != exitcode.Auth {
			t.Fatalf("exit %d, want Auth", code)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
			t.Fatalf("the binary changed: %q", got)
		}
		if !bytes.Equal(c.license(), lic) {
			t.Fatal("the license changed")
		}
		stub.mu.Lock()
		n := len(stub.downloadAuth)
		stub.mu.Unlock()
		if n != 0 {
			t.Fatalf("%d download requests after a refused refresh", n)
		}
	})

	t.Run("an authorized refresh installs the credential and the release", func(t *testing.T) {
		target := writeTarget(t, v1)
		if code := upgrade(target); code != exitcode.OK {
			t.Fatalf("connected upgrade exit %d", code)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("target not upgraded: %q", got)
		}
		kr, _ := licenseKeyringForDataDir(c.dir)
		if v, err := kr.Verify(strings.TrimSpace(string(c.license())), time.Now()); err != nil || v.Status(time.Now()) != license.StatusValid {
			t.Fatalf("the refreshed credential was not installed: %v", err)
		}
		want := "Bearer " + strings.TrimSpace(string(c.token()))
		stub.mu.Lock()
		auths := append([]string(nil), stub.downloadAuth...)
		stub.mu.Unlock()
		if len(auths) < 3 {
			t.Fatalf("expected the manifest, signature and artifact requests, saw %d", len(auths))
		}
		for _, a := range auths {
			if a != want {
				t.Fatal("a download request did not carry the bearer the refresh returned")
			}
		}
	})
	c.assertNoSecretsLeaked()
}

func TestUpgradeConnectFlagCombinationsAndTimer(t *testing.T) {
	dir := t.TempDir()
	for name, args := range map[string][]string{
		"without enterprise": {"--connect"},
		"with a bundle":      {"--enterprise", "--connect", "--bundle", "x.tar.gz"},
		"with a token":       {"--enterprise", "--connect", "--token", "pasted-bearer"},
	} {
		code, _, _, err := runCLIExit(t, append([]string{"upgrade", "--data-dir", dir}, args...)...)
		if code != exitcode.Usage {
			t.Errorf("%s: exit %d (%v), want Usage", name, code, err)
		}
	}
	timerDir := t.TempDir()
	code, stdout, stderr, err := runCLIExit(t, "upgrade", "--enterprise", "--connect", "--install-timer", "--timer-dir", timerDir,
		"--data-dir", dir, "--license", filepath.Join(dir, "license.key"), "--pubkey", "@"+filepath.Join(dir, "ota.pub"), "-o", "json")
	if code != exitcode.OK {
		t.Fatalf("timer: %d %v %s %s", code, err, stdout, stderr)
	}
	unit, err := os.ReadFile(filepath.Join(timerDir, "olivares-upgrade.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--enterprise", "--connect", "--data-dir " + dir, "--license " + filepath.Join(dir, "license.key"), "--pubkey @" + filepath.Join(dir, "ota.pub")} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("the unit does not pin %q:\n%s", want, unit)
		}
	}
	for _, bad := range []string{"EnvironmentFile", "OLIVARES_UPGRADE_TOKEN", "--token"} {
		if strings.Contains(string(unit), bad) || strings.Contains(stdout, "OLIVARES_UPGRADE_TOKEN=") {
			t.Errorf("the connected unit must carry no secret or token source (%s):\n%s", bad, unit)
		}
	}
	// The connected timer states what it actually does: it refreshes when it fires, on its fixed
	// OnCalendar schedule, and the weekly default is sparser than a provisional lease allows.
	timerUnit, err := os.ReadFile(filepath.Join(timerDir, "olivares-upgrade.timer"))
	if err != nil {
		t.Fatal(err)
	}
	// Without --timer-schedule a connected unit runs daily, keeping the random delay and catch-up.
	for _, want := range []string{"OnCalendar=*-*-* 03:00:00\n", "RandomizedDelaySec=30m", "Persistent=true", "refreshes ONLY when this timer fires",
		"last.effective_until", "runs daily", "72 hours", "12 hours", "olivares license connect refresh", "does not\n# rewrite this unit"} {
		if !strings.Contains(string(timerUnit), want) {
			t.Errorf("the connected timer does not state %q:\n%s", want, timerUnit)
		}
	}
	if strings.Contains(string(timerUnit), "weekly default") || strings.Contains(string(timerUnit), "OnCalendar=Sun") {
		t.Errorf("the connected timer still carries the weekly default:\n%s", timerUnit)
	}
	var timerRes map[string]any
	if err := json.Unmarshal([]byte(stdout), &timerRes); err != nil || timerRes["schedule"] != "*-*-* 03:00:00" {
		t.Errorf("the connected timer JSON reports schedule %v (%v)", timerRes["schedule"], err)
	}
	// An explicit schedule is kept exactly as typed, also on a connected unit.
	explicitDir := t.TempDir()
	if code, _, stderr, err := runCLIExit(t, "upgrade", "--enterprise", "--connect", "--install-timer", "--timer-dir", explicitDir, "--timer-schedule", "Sat *-*-* 04:30:00",
		"--data-dir", dir, "-o", "json"); code != exitcode.OK {
		t.Fatalf("explicit connected timer: %d %v %s", code, err, stderr)
	}
	if explicit, err := os.ReadFile(filepath.Join(explicitDir, "olivares-upgrade.timer")); err != nil || !strings.Contains(string(explicit), "OnCalendar=Sat *-*-* 04:30:00\n") {
		t.Fatalf("an explicit --timer-schedule was not kept (%v):\n%s", err, explicit)
	}
	communityDir := t.TempDir()
	if code, _, stderr, err := runCLIExit(t, "upgrade", "--install-timer", "--timer-dir", communityDir, "--data-dir", dir, "-o", "json"); code != exitcode.OK {
		t.Fatalf("community timer: %d %v %s", code, err, stderr)
	}
	if community, err := os.ReadFile(filepath.Join(communityDir, "olivares-upgrade.timer")); err != nil || strings.Contains(string(community), "CONNECTED UNIT") ||
		!strings.Contains(string(community), "OnCalendar=Sun *-*-* 03:00:00\n") {
		t.Fatalf("a community timer must keep the weekly default and carry no connected note (%v):\n%s", err, community)
	}
}
