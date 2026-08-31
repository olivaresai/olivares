// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vault

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

// recordedCall captures one request the actuator emitted: method, path, the
// fully read body and the headers — enough to assert exact wire behavior.
type recordedCall struct {
	method, path, body string
	header             http.Header
}

// scriptDoer records every request and answers via the injected respond func.
type scriptDoer struct {
	t       *testing.T
	calls   []recordedCall
	respond func(method, path string) *http.Response
}

func (d *scriptDoer) Do(req *http.Request) (*http.Response, error) {
	d.t.Helper()
	var body string
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			d.t.Fatalf("read request body: %v", err)
		}
		body = string(b)
	}
	d.calls = append(d.calls, recordedCall{method: req.Method, path: req.URL.Path, body: body, header: req.Header.Clone()})
	return d.respond(req.Method, req.URL.Path), nil
}

// fatalDoer fails the test on ANY request: it proves an op refused before
// emitting traffic (unconfigured token, invalid target ref).
type fatalDoer struct{ t *testing.T }

func (d *fatalDoer) Do(req *http.Request) (*http.Response, error) {
	d.t.Fatalf("doer invoked (%s %s) — the op must refuse before any HTTP call", req.Method, req.URL.Path)
	return nil, nil
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// newTestActuator wires an actuator against a scriptDoer with the fixed clock.
func newTestActuator(t *testing.T, respond func(method, path string) *http.Response) (*Actuator, *scriptDoer) {
	t.Helper()
	doer := &scriptDoer{t: t, respond: respond}
	a := NewActuator("https://vault.example:8200", testToken, "team-a", doer)
	a.now = fixedClock
	return a, doer
}

func TestCapabilitiesHonest(t *testing.T) {
	caps := NewActuator("", testToken, "", nil).Capabilities()
	if len(caps) != 5 {
		t.Fatalf("capabilities = %d, want 5", len(caps))
	}
	for _, c := range caps {
		if c.TargetKind != "vault_entity" {
			t.Fatalf("op %s TargetKind = %q, want the Snapshot kind %q", c.Op, c.TargetKind, "vault_entity")
		}
		wantTargetRef := c.Op == identitysource.OpRotate || c.Op == identitysource.OpRetire
		if c.RequiresTargetRef != wantTargetRef {
			t.Fatalf("op %s RequiresTargetRef = %v, want %v", c.Op, c.RequiresTargetRef, wantTargetRef)
		}
	}
	// The honest caveats must be declared, not hidden.
	dis, ok := identitysource.FindCapability(caps, identitysource.OpDisable, "vault_entity")
	if !ok || !strings.Contains(dis.Detail, "NOT revoked") {
		t.Fatalf("disable capability must state the not-revoked caveat, got %+v", dis)
	}
	fin, ok := identitysource.FindCapability(caps, identitysource.OpFinalize, "vault_entity")
	if !ok || !strings.Contains(fin.Detail, "disabled") {
		t.Fatalf("finalize capability must state the keeps-disabled semantics, got %+v", fin)
	}
}

func TestDisableRestoreFinalizePostDisabledFlag(t *testing.T) {
	a, doer := newTestActuator(t, func(string, string) *http.Response {
		return jsonResp(http.StatusNoContent, "")
	})
	ctx := context.Background()
	req := identitysource.ActuationRequest{Ref: "ent-1234", Kind: "vault_entity"}

	cases := []struct {
		name     string
		call     func() (identitysource.ActuationReceipt, error)
		op       identitysource.LifecycleOp
		wantBody string
		inDetail string
	}{
		{"disable", func() (identitysource.ActuationReceipt, error) { return a.Disable(ctx, req) }, identitysource.OpDisable, `{"disabled":true}`, "NOT revoked"},
		{"restore", func() (identitysource.ActuationReceipt, error) { return a.Restore(ctx, req) }, identitysource.OpRestore, `{"disabled":false}`, "re-enables"},
		{"finalize", func() (identitysource.ActuationReceipt, error) { return a.Finalize(ctx, req) }, identitysource.OpFinalize, `{"disabled":true}`, "keeps the entity disabled"},
	}
	for i, tc := range cases {
		rec, err := tc.call()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		c := doer.calls[i]
		if c.method != http.MethodPost || c.path != "/v1/identity/entity/id/ent-1234" {
			t.Fatalf("%s request = %s %s", tc.name, c.method, c.path)
		}
		if c.body != tc.wantBody {
			t.Fatalf("%s body = %q, want exactly %q (minimal write)", tc.name, c.body, tc.wantBody)
		}
		if got := c.header.Get("X-Vault-Token"); got != testToken {
			t.Fatalf("%s X-Vault-Token = %q", tc.name, got)
		}
		if got := c.header.Get("X-Vault-Namespace"); got != "team-a" {
			t.Fatalf("%s X-Vault-Namespace = %q", tc.name, got)
		}
		if rec.Op != tc.op || rec.Ref != "ent-1234" || rec.Provider != identitysource.SourceVault {
			t.Fatalf("%s receipt = %+v", tc.name, rec)
		}
		if !strings.Contains(rec.Detail, tc.inDetail) {
			t.Fatalf("%s receipt detail %q must contain %q", tc.name, rec.Detail, tc.inDetail)
		}
		if !rec.OccurredAt.Equal(fixedClock()) {
			t.Fatalf("%s OccurredAt = %v", tc.name, rec.OccurredAt)
		}
	}

	// Empty ref refuses without traffic shape ambiguity.
	if _, err := a.Disable(ctx, identitysource.ActuationRequest{}); err == nil {
		t.Fatal("disable with empty ref must error")
	}
}

func TestRotateHappyPath(t *testing.T) {
	const secret = "s3cr3t-never-logged"
	a, doer := newTestActuator(t, func(method, path string) *http.Response {
		switch method {
		case "LIST":
			return jsonResp(200, `{"data":{"keys":["acc-old-1","acc-old-2"]}}`)
		case http.MethodPost:
			return jsonResp(200, `{"data":{"secret_id":"`+secret+`","secret_id_accessor":"acc-new","secret_id_ttl":600}}`)
		}
		t.Fatalf("unexpected %s %s", method, path)
		return nil
	})

	cred, err := a.Rotate(context.Background(), identitysource.ActuationRequest{
		Ref: "entity:billing-agent", Kind: "vault_entity", TargetRef: "approle:billing-agent",
	})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Wire shape: LIST first (pre-mint work list), then the mint POST with the
	// deliberate empty JSON body.
	if len(doer.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (LIST then POST)", len(doer.calls))
	}
	if doer.calls[0].method != "LIST" || doer.calls[0].path != "/v1/auth/approle/role/billing-agent/secret-id" {
		t.Fatalf("first call = %s %s", doer.calls[0].method, doer.calls[0].path)
	}
	if doer.calls[1].method != http.MethodPost || doer.calls[1].path != "/v1/auth/approle/role/billing-agent/secret-id" {
		t.Fatalf("second call = %s %s", doer.calls[1].method, doer.calls[1].path)
	}
	if doer.calls[1].body != "{}" {
		t.Fatalf("mint body = %q, want empty JSON object", doer.calls[1].body)
	}

	// Return-once: the secret rides ONLY RotatedCredential.Secret.
	if cred.Secret != secret {
		t.Fatalf("Secret = %q", cred.Secret)
	}
	r := cred.Receipt
	if r.NewCredentialRef != "acc-new" {
		t.Fatalf("NewCredentialRef = %q", r.NewCredentialRef)
	}
	if len(r.OldCredentialRefs) != 2 || r.OldCredentialRefs[0] != "acc-old-1" || r.OldCredentialRefs[1] != "acc-old-2" {
		t.Fatalf("OldCredentialRefs = %v", r.OldCredentialRefs)
	}
	if want := fixedClock().Add(600 * time.Second); !r.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", r.ExpiresAt, want)
	}
	if r.Op != identitysource.OpRotate || r.Provider != identitysource.SourceVault || r.Ref != "entity:billing-agent" {
		t.Fatalf("receipt = %+v", r.ActuationReceipt)
	}

	// The receipt is ledger-safe: serialize the WHOLE struct and prove the
	// secret is not in it (the MintedToken.Audit() rule).
	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("rotation receipt leaks the secret: %s", blob)
	}
}

func TestRotateListNotFoundMeansEmpty(t *testing.T) {
	a, _ := newTestActuator(t, func(method, _ string) *http.Response {
		if method == "LIST" {
			// A role with no live secret-ids: Vault answers the list with 404.
			return jsonResp(http.StatusNotFound, `{"errors":[]}`)
		}
		return jsonResp(200, `{"data":{"secret_id":"fresh","secret_id_accessor":"acc-1"}}`)
	})
	cred, err := a.Rotate(context.Background(), identitysource.ActuationRequest{Ref: "entity:x", TargetRef: "approle:fresh-role"})
	if err != nil {
		t.Fatalf("Rotate on a fresh role must proceed: %v", err)
	}
	if len(cred.Receipt.OldCredentialRefs) != 0 {
		t.Fatalf("OldCredentialRefs = %v, want empty", cred.Receipt.OldCredentialRefs)
	}
	if !cred.Receipt.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want zero when the mint reply carries no ttl", cred.Receipt.ExpiresAt)
	}
}

func TestRotateTargetRefValidation(t *testing.T) {
	a := NewActuator("", testToken, "", &fatalDoer{t: t})
	ctx := context.Background()

	_, err := a.Rotate(ctx, identitysource.ActuationRequest{Ref: "entity:x"})
	if !errors.Is(err, identitysource.ErrTargetRefRequired) {
		t.Fatalf("no target ref: err = %v, want ErrTargetRefRequired", err)
	}
	for _, bad := range []string{"notapprole:x", "approle:", "approle: "} {
		if _, err := a.Rotate(ctx, identitysource.ActuationRequest{Ref: "entity:x", TargetRef: bad}); err == nil {
			t.Fatalf("malformed target ref %q must be rejected", bad)
		} else if errors.Is(err, identitysource.ErrTargetRefRequired) {
			t.Fatalf("malformed (not missing) ref %q must not map to ErrTargetRefRequired: %v", bad, err)
		}
	}
	// Retire shares the same gate.
	if _, err := a.Retire(ctx, identitysource.ActuationRequest{Ref: "entity:x", CredentialRefs: []string{"a"}}); !errors.Is(err, identitysource.ErrTargetRefRequired) {
		t.Fatalf("retire without target ref: err = %v, want ErrTargetRefRequired", err)
	}
}

func TestRetireDestroysEachAccessor(t *testing.T) {
	a, doer := newTestActuator(t, func(string, string) *http.Response {
		return jsonResp(http.StatusNoContent, "")
	})
	rec, err := a.Retire(context.Background(), identitysource.ActuationRequest{
		Ref: "entity:billing-agent", TargetRef: "approle:billing-agent",
		CredentialRefs: []string{"acc-old-1", "acc-old-2"},
	})
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if len(doer.calls) != 2 {
		t.Fatalf("calls = %d, want one destroy per accessor", len(doer.calls))
	}
	for i, want := range []string{`{"secret_id_accessor":"acc-old-1"}`, `{"secret_id_accessor":"acc-old-2"}`} {
		c := doer.calls[i]
		if c.method != http.MethodPost || c.path != "/v1/auth/approle/role/billing-agent/secret-id-accessor/destroy" {
			t.Fatalf("call %d = %s %s", i, c.method, c.path)
		}
		if c.body != want {
			t.Fatalf("call %d body = %q, want %q", i, c.body, want)
		}
	}
	if rec.Op != identitysource.OpRetire || !strings.Contains(rec.Detail, "destroyed 2") {
		t.Fatalf("receipt = %+v", rec)
	}

	// No accessors = nothing to do is an error (a silent no-op would fake retirement).
	if _, err := a.Retire(context.Background(), identitysource.ActuationRequest{Ref: "e", TargetRef: "approle:r"}); err == nil {
		t.Fatal("retire without credential refs must error")
	}
}

func TestRetirePartialFailureReportsProgress(t *testing.T) {
	n := 0
	a, _ := newTestActuator(t, func(string, string) *http.Response {
		n++
		if n == 2 { // second destroy fails
			return jsonResp(http.StatusForbidden, `{"errors":["permission denied"]}`)
		}
		return jsonResp(http.StatusNoContent, "")
	})
	_, err := a.Retire(context.Background(), identitysource.ActuationRequest{
		Ref: "entity:x", TargetRef: "approle:r",
		CredentialRefs: []string{"acc-ok", "acc-fail", "acc-never-tried"},
	})
	if err == nil {
		t.Fatal("partial failure must surface an error")
	}
	for _, want := range []string{"destroyed 1 of 3", "accessor 2", "acc-fail", "403"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must contain %q", err, want)
		}
	}
}

func TestErrorsCarryStatusNeverToken(t *testing.T) {
	a, _ := newTestActuator(t, func(string, string) *http.Response {
		return jsonResp(http.StatusForbidden, `{"errors":["permission denied"]}`)
	})
	_, err := a.Disable(context.Background(), identitysource.ActuationRequest{Ref: "ent-1"})
	if err == nil {
		t.Fatal("non-2xx must error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "403") || !strings.Contains(msg, "permission denied") {
		t.Fatalf("error must carry status and Vault's error string, got %q", msg)
	}
	if strings.Contains(msg, testToken) {
		t.Fatalf("error leaks the token: %q", msg)
	}

	// The body excerpt is bounded even when Vault answers with junk.
	a2, _ := newTestActuator(t, func(string, string) *http.Response {
		return jsonResp(http.StatusInternalServerError, strings.Repeat("x", 10_000))
	})
	_, err = a2.Disable(context.Background(), identitysource.ActuationRequest{Ref: "ent-1"})
	if err == nil || len(err.Error()) > 512 {
		t.Fatalf("error excerpt must be bounded, got %d bytes", len(err.Error()))
	}
}

func TestUnconfiguredActuatorRefusesWithoutTraffic(t *testing.T) {
	a := NewActuator("", "", "team-a", &fatalDoer{t: t})
	ctx := context.Background()
	req := identitysource.ActuationRequest{Ref: "ent-1", TargetRef: "approle:r", CredentialRefs: []string{"acc"}}

	ops := map[string]func() error{
		"disable":  func() error { _, err := a.Disable(ctx, req); return err },
		"restore":  func() error { _, err := a.Restore(ctx, req); return err },
		"finalize": func() error { _, err := a.Finalize(ctx, req); return err },
		"rotate":   func() error { _, err := a.Rotate(ctx, req); return err },
		"retire":   func() error { _, err := a.Retire(ctx, req); return err },
	}
	for name, call := range ops {
		err := call()
		if !errors.Is(err, errNotConfigured) {
			t.Fatalf("%s unconfigured: err = %v, want errNotConfigured", name, err)
		}
	}
	// Capabilities stays static and credential-free per the contract.
	if got := len(a.Capabilities()); got != 5 {
		t.Fatalf("Capabilities() unconfigured = %d, want 5", got)
	}
}
