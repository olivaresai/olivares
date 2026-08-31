// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"errors"
	"testing"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
)

func TestAssertNoStaticKeyShadowing(t *testing.T) {
	// Presence (set), not a non-empty value, is the trigger: even ANTHROPIC_API_KEY=""
	// wins its precedence slot and shadows federation.
	cases := []struct {
		name string
		env  map[string]string
		want bool // want error (fail-closed)
	}{
		{"none set", map[string]string{}, false},
		{"api key empty string", map[string]string{"ANTHROPIC_API_KEY": ""}, true},
		{"api key set", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-..."}, true},
		{"auth token set", map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok"}, true},
		{"unrelated set", map[string]string{"ANTHROPIC_BASE_URL": "https://x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }
			err := AssertNoStaticKeyShadowing(lookup)
			if (err != nil) != tc.want {
				t.Fatalf("AssertNoStaticKeyShadowing err=%v, want error=%v", err, tc.want)
			}
		})
	}
}

func TestFetchAnthropicAssertion(t *testing.T) {
	ctx := context.Background()
	noEnv := func(string) (string, bool) { return "", false }

	t.Run("happy path returns the raw JWT-SVID", func(t *testing.T) {
		svid := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{AnthropicAudience})
		f := &fakeJWTSource2{svid: svid}
		got, err := FetchAnthropicAssertion(ctx, f, noEnv)
		if err != nil {
			t.Fatalf("FetchAnthropicAssertion: %v", err)
		}
		if got != svid.Marshal() {
			t.Error("must return the raw JWT-SVID token to present to the WIF exchange")
		}
		if f.lastAudience != AnthropicAudience {
			t.Errorf("audience requested = %q, want exactly %q", f.lastAudience, AnthropicAudience)
		}
	})

	t.Run("static key fails closed BEFORE any fetch", func(t *testing.T) {
		f := &fakeJWTSource2{svid: mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{AnthropicAudience})}
		withKey := func(k string) (string, bool) {
			if k == "ANTHROPIC_API_KEY" {
				return "", true // present-but-empty still shadows
			}
			return "", false
		}
		if _, err := FetchAnthropicAssertion(ctx, f, withKey); err == nil {
			t.Fatal("a present ANTHROPIC_API_KEY must fail the federated egress closed")
		}
		if f.calls != 0 {
			t.Error("must not fetch a JWT-SVID when the footgun guard trips (fail-closed before fetch)")
		}
	})

	t.Run("wrong audience SVID is rejected by the harness", func(t *testing.T) {
		// A source that mints with the wrong audience: the pre-flight inspection catches
		// it locally instead of surfacing an opaque server-side invalid_grant.
		bad := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{"https://wrong.example"})
		f := &fakeJWTSource2{svid: bad}
		if _, err := FetchAnthropicAssertion(ctx, f, noEnv); err == nil {
			t.Fatal("a JWT-SVID whose aud lacks the Anthropic audience must be rejected")
		}
	})
}

func TestInspectAnthropicAssertion(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		svid := mintAnthropicSVID(t, "spiffe://corp.example/workload/api", []string{AnthropicAudience})
		c, err := InspectAnthropicAssertion(svid.Marshal())
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if c.SpiffeID != "spiffe://corp.example/workload/api" {
			t.Errorf("sub = %q", c.SpiffeID)
		}
		if c.Issuer != "https://oidc.spire.example" {
			t.Errorf("iss = %q", c.Issuer)
		}
		found := false
		for _, a := range c.Audience {
			if a == AnthropicAudience {
				found = true
			}
		}
		if !found {
			t.Errorf("aud %v must contain %q", c.Audience, AnthropicAudience)
		}
	})
	t.Run("missing audience rejected", func(t *testing.T) {
		svid := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{"https://other"})
		if _, err := InspectAnthropicAssertion(svid.Marshal()); err == nil {
			t.Fatal("a token without the Anthropic audience must be rejected")
		}
	})
	t.Run("garbage token rejected", func(t *testing.T) {
		if _, err := InspectAnthropicAssertion("not-a-jwt"); err == nil {
			t.Fatal("a non-JWT must be rejected")
		}
	})
}

func TestEmergentSeamDenyClosed(t *testing.T) {
	// Even with the flag "enabled", there is no in-tree backend, so the seam stays
	// deny-closed — we never pretend a pre-RFC draft is implemented.
	for _, enabled := range []bool{false, true} {
		seam := EmergentIdentity(enabled)
		_, err := seam.PresentWorkloadIdentity(context.Background(), AnthropicAudience)
		if !errors.Is(err, ErrEmergentIdentityDisabled) {
			t.Errorf("enabled=%v: emergent seam must be deny-closed, got err=%v", enabled, err)
		}
	}
}

// fakeJWTSource2 is the minimal jwtFetcher the egress path needs (distinct from the
// jwtsvid.Source fake in workload_test.go, which speaks the go-spiffe interface).
type fakeJWTSource2 struct {
	svid         *jwtsvid.SVID
	calls        int
	lastAudience string
}

func (f *fakeJWTSource2) FetchJWTSVID(_ context.Context, audience string, _ ...string) (*jwtsvid.SVID, error) {
	f.calls++
	f.lastAudience = audience
	return f.svid, nil
}
