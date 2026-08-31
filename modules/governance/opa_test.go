// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func opaReq() auth.Request {
	return auth.Request{
		Principal:  auth.Principal{Kind: auth.KindToken, CredID: "tok-1", Superadmin: false},
		Permission: "agent:write",
		Tenant:     model.TenantID("t-1"),
		Resource:   auth.ResourceAttrs{Kind: "agent", ID: "a-1", Sensitivity: "high"},
	}
}

// opaServer returns a test OPA Data API: it records the last request and replies with
// the given status and body.
func opaServer(t *testing.T, status int, body string, capture *[]byte, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			*capture, _ = io.ReadAll(r.Body)
		}
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/data/") {
			t.Errorf("unexpected OPA path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestOPAAllowDenyUndefined(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantAllow bool
	}{
		{"true permits", `{"result":true,"decision_id":"d1"}`, true},
		{"false restricts", `{"result":false,"decision_id":"d2"}`, false},
		{"absent restricts", `{}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := opaServer(t, 200, c.body, nil, nil)
			defer srv.Close()
			oe, err := NewOPAEvaluator(srv.URL, "authz.allow", "", nil)
			if err != nil {
				t.Fatalf("NewOPAEvaluator: %v", err)
			}
			dec, err := oe.Evaluate(context.Background(), opaReq())
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if dec.Allow != c.wantAllow {
				t.Errorf("Allow = %v, want %v (%+v)", dec.Allow, c.wantAllow, dec)
			}
		})
	}
}

func TestOPAFailsClosedOnError(t *testing.T) {
	srv := opaServer(t, 500, `boom`, nil, nil)
	defer srv.Close()
	oe, _ := NewOPAEvaluator(srv.URL, "authz/allow", "", nil)
	if _, err := oe.Evaluate(context.Background(), opaReq()); err == nil {
		t.Fatal("a non-2xx OPA response must return an error (Authorizer fails closed)")
	}
}

func TestOPARequestShape(t *testing.T) {
	var body []byte
	var gotAuth string
	srv := opaServer(t, 200, `{"result":true}`, &body, &gotAuth)
	defer srv.Close()
	oe, _ := NewOPAEvaluator(srv.URL, "authz.allow", "secret-token", nil)
	if _, err := oe.Evaluate(context.Background(), opaReq()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// The decision path maps to /v1/data/authz/allow and the bearer token is sent.
	if oe.dataURL != srv.URL+"/v1/data/authz/allow" {
		t.Errorf("dataURL = %q", oe.dataURL)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("authorization header = %q", gotAuth)
	}
	// The input document carries the principal/permission/resource.
	var sent struct {
		Input opaInput `json:"input"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode sent input: %v", err)
	}
	if sent.Input.Permission != "agent:write" || sent.Input.Resource.Sensitivity != "high" ||
		sent.Input.Principal.CredID != "tok-1" || sent.Input.Tenant != "t-1" {
		t.Errorf("opa input = %+v", sent.Input)
	}
}

func TestOPAConfigErrors(t *testing.T) {
	if _, err := NewOPAEvaluator("", "authz/allow", "", nil); err == nil {
		t.Error("empty base url must error")
	}
	if _, err := NewOPAEvaluator("http://opa", "", "", nil); err == nil {
		t.Error("empty decision path must error")
	}
}
