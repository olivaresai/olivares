// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

const testASIssuer = "https://as.example"

func TestNewClientAssertion(t *testing.T) {
	ctx := context.Background()

	t.Run("audience is exactly the AS issuer, no extras", func(t *testing.T) {
		svid := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{testASIssuer})
		f := &fakeJWTSource2{svid: svid}
		got, err := NewClientAssertion(ctx, f, testASIssuer)
		if err != nil {
			t.Fatalf("NewClientAssertion: %v", err)
		}
		// The draft (§4): aud MUST contain only the AS issuer identifier — so the
		// fetch must be bound to the issuer and nothing else.
		if f.lastAudience != testASIssuer {
			t.Errorf("audience requested = %q, want exactly %q", f.lastAudience, testASIssuer)
		}
		if got.Type != ClientAssertionTypeJWTSVID {
			t.Errorf("assertion type = %q, want %q", got.Type, ClientAssertionTypeJWTSVID)
		}
		if got.Assertion != svid.Marshal() {
			t.Error("must carry the raw JWT-SVID as the client_assertion")
		}
		if got.SpiffeID != "spiffe://corp.example/wl" {
			t.Errorf("SpiffeID = %q", got.SpiffeID)
		}
	})

	t.Run("Apply stamps the RFC 7523 form params and never client_id", func(t *testing.T) {
		svid := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{testASIssuer})
		a, err := NewClientAssertion(ctx, &fakeJWTSource2{svid: svid}, testASIssuer)
		if err != nil {
			t.Fatalf("NewClientAssertion: %v", err)
		}
		form := url.Values{"grant_type": {"client_credentials"}}
		a.Apply(form)
		if form.Get("client_assertion_type") != ClientAssertionTypeJWTSVID {
			t.Errorf("client_assertion_type = %q", form.Get("client_assertion_type"))
		}
		if form.Get("client_assertion") != svid.Marshal() {
			t.Error("client_assertion must be the raw JWT-SVID")
		}
		if _, present := form["client_id"]; present {
			t.Error("Apply must never invent a client_id (optional for spiffe_jwt; the host owns it)")
		}
	})

	t.Run("empty issuer fails before any fetch", func(t *testing.T) {
		f := &fakeJWTSource2{svid: mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{testASIssuer})}
		if _, err := NewClientAssertion(ctx, f, "  "); err == nil {
			t.Fatal("an empty AS issuer must be rejected")
		}
		if f.calls != 0 {
			t.Error("must not fetch a JWT-SVID for an empty issuer")
		}
	})

	t.Run("wrong-audience SVID is rejected by the pre-flight", func(t *testing.T) {
		bad := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{"https://other.example"})
		if _, err := NewClientAssertion(ctx, &fakeJWTSource2{svid: bad}, testASIssuer); err == nil {
			t.Fatal("a JWT-SVID whose aud lacks the AS issuer must be rejected locally")
		}
	})

	t.Run("multi-audience SVID is rejected — aud must contain ONLY the issuer", func(t *testing.T) {
		// Draft §4 exclusivity: containment is not enough.
		multi := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{testASIssuer, "https://extra.example"})
		if _, err := NewClientAssertion(ctx, &fakeJWTSource2{svid: multi}, testASIssuer); err == nil {
			t.Fatal("a JWT-SVID with extra audiences must be rejected (aud MUST contain only the AS issuer)")
		}
	})

	t.Run("fetch failure is wrapped, never swallowed", func(t *testing.T) {
		f := &failingJWTSource{err: errors.New("agent down")}
		if _, err := NewClientAssertion(ctx, f, testASIssuer); err == nil {
			t.Fatal("a fetch failure must surface")
		}
	})
}

func TestX509ClientID(t *testing.T) {
	// Injected-source workload (the workload_test.go fakes): the client_id must be
	// exactly the current X.509-SVID's SPIFFE ID string, reflecting rotation.
	const id = "spiffe://corp.example/workload/api"
	xsrc := &fakeX509Source{svid: &x509svid.SVID{ID: spiffeid.RequireFromString(id)}}
	w := newWorkload(xsrc, xsrc, &fakeJWTSource{})
	got, err := X509ClientID(w)
	if err != nil {
		t.Fatalf("X509ClientID: %v", err)
	}
	if got != id {
		t.Errorf("client_id = %q, want %q", got, id)
	}
	// Rotation is reflected: the NEXT call reads the live source.
	const rotated = "spiffe://corp.example/workload/api-v2"
	xsrc.svid = &x509svid.SVID{ID: spiffeid.RequireFromString(rotated)}
	got, err = X509ClientID(w)
	if err != nil {
		t.Fatalf("X509ClientID after rotation: %v", err)
	}
	if got != rotated {
		t.Errorf("client_id after rotation = %q, want %q", got, rotated)
	}
}

// failingJWTSource is a jwtFetcher whose fetch always errors.
type failingJWTSource struct{ err error }

func (f *failingJWTSource) FetchJWTSVID(context.Context, string, ...string) (*jwtsvid.SVID, error) {
	return nil, f.err
}
