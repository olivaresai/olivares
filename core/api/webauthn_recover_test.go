// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Someone who lost their only passkey under the passkey policy gets a way back
// from the host. RecoverPasskeys removes their passkeys and ends their sessions in one
// audited transaction, reports their active API tokens and changes nothing else; they
// then sign in with their password, register a first passkey and step up with it.
func TestRecoverPasskeysGivesTheirOwnerAWayBack(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.requirePasskeyStepUp()
	token := h.adminLogin()
	tenant := h.createOrg(token, "acme")
	registerOK(t, h, token, newSoftAuthenticator(t))

	var owner, other model.ID
	var activeTok, revokedTok, otherKey model.ID
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		us, _, err := as.Users().List(ctx, model.Query{Filters: []model.Filter{{Column: "email", Op: model.OpEq, Value: "root@x.io"}}, Limit: 1})
		if err != nil || len(us) != 1 {
			t.Fatalf("owner = %v %v", us, err)
		}
		owner = us[0].ID
		o, err := as.Users().Create(ctx, model.User{Email: "other@x.io", Status: model.StatusActive})
		if err != nil {
			return err
		}
		other = o.ID
		k, err := as.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{UserID: other, CredentialID: "b3RoZXIta2V5", Credential: []byte(`{}`)})
		if err != nil {
			return err
		}
		otherKey = k.ID
		for i, revoked := range []bool{false, true} {
			c, err := auth.NewCredential(auth.PrefixToken)
			if err != nil {
				return err
			}
			tok, err := as.Tokens().Create(ctx, model.APIToken{Name: "ci", UserID: owner, Selector: c.Selector,
				SecretHash: c.SecretHash, BoundTenantID: tenant, Role: auth.RoleViewer, Revoked: revoked})
			if err != nil {
				return err
			}
			if i == 0 {
				activeTok = tok.ID
			} else {
				revokedTok = tok.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	op, err := auth.NewLocalOperator(auth.LocalOperator{Subject: "ops-oncall", Via: "cli:admin-recover", Reason: "lost passkey"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.authr.RecoverPasskeys(ctx, op, owner)
	if err != nil || got.Passkeys != 1 || got.Sessions < 1 || len(got.Tokens) != 1 || got.Tokens[0] != activeTok {
		t.Fatalf("recover = %+v %v, want 1 passkey, the session(s) and the one active token", got, err)
	}
	if r := h.do("GET", "/v1/auth/webauthn/credentials", token, nil, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("the recovered person's old session = %d, want 401", r.code)
	}

	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		if _, err := as.WebAuthnCredentials().Get(ctx, otherKey); err != nil {
			t.Errorf("another person's passkey after the recovery: %v", err)
		}
		for _, id := range []model.ID{activeTok, revokedTok} {
			tok, err := as.Tokens().Get(ctx, id)
			if err != nil || tok.Revoked != (id == revokedTok) {
				t.Errorf("token %s after the recovery = %+v %v, want unchanged", id, tok, err)
			}
		}
		var rows []model.AuditEvent
		err := as.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action == "auth.webauthn.recover" {
				rows = append(rows, ev)
			}
			return nil
		})
		if err != nil || len(rows) != 1 || rows[0].TargetID != owner {
			t.Errorf("recovery audit rows = %+v %v, want one for the owner", rows, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("password sign-in after the recovery = %d %s", r.code, r.raw)
	}
	again := r.body["token"].(string)
	fresh := newSoftAuthenticator(t)
	registerOK(t, h, again, fresh)
	passkeyStepUp(t, h, again, fresh)
}

// pausedBody holds a request after the engine authenticated it: the first read of
// the body waits until the test releases it.
type pausedBody struct {
	r        *bytes.Reader
	once     sync.Once
	started  chan struct{}
	released chan struct{}
}

func (b *pausedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.released })
	return b.r.Read(p)
}

// A passkey enrollment already authenticated and in flight when
// its owner's passkeys are recovered must not commit a new passkey afterwards. The
// registration writer rechecks its session under the lock recovery also takes.
func TestAnEnrollmentInFlightDuringRecoveryEndsUnauthorized(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.requirePasskeyStepUp()
	token := h.adminLogin()
	soft := newSoftAuthenticator(t)
	opts := h.do("POST", "/v1/auth/webauthn/register/options", token, nil, nil)
	if opts.code != http.StatusOK {
		t.Fatalf("register options = %d %s", opts.code, opts.raw)
	}
	body, _ := json.Marshal(map[string]any{"credential": soft.register(t, opts, flagUP|flagUV|flagAT, testOrigin)})
	paused := &pausedBody{r: bytes.NewReader(body), started: make(chan struct{}), released: make(chan struct{})}
	req := httptest.NewRequest("POST", "/v1/auth/webauthn/register", paused)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.srv.Handler().ServeHTTP(rec, req) }()
	<-paused.started

	var owner model.ID
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		us, _, err := as.Users().List(ctx, model.Query{Filters: []model.Filter{{Column: "email", Op: model.OpEq, Value: "root@x.io"}}, Limit: 1})
		if err == nil && len(us) == 1 {
			owner = us[0].ID
		}
		return err
	}); err != nil || owner == "" {
		t.Fatalf("owner = %q %v", owner, err)
	}
	op, err := auth.NewLocalOperator(auth.LocalOperator{Subject: "ops-oncall", Via: "cli:admin-recover", Reason: "lost passkey"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authr.RecoverPasskeys(ctx, op, owner); err != nil {
		t.Fatal(err)
	}
	close(paused.released)
	<-done

	var left int
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.WebAuthnCredentials().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: owner.String()}}, Limit: 5})
		left = len(rows)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnauthorized || left != 0 {
		t.Fatalf("enrollment in flight during recovery = %d %s with %d passkeys, want 401 and none", rec.Code, rec.Body.String(), left)
	}
}
