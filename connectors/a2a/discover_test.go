// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func TestCardURL(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://a.example.com", "https://a.example.com/.well-known/agent-card.json"},
		{"https://a.example.com/", "https://a.example.com/.well-known/agent-card.json"},
		{"https://a.example.com/card.json", "https://a.example.com/card.json"},
		{"", ""},
	}
	for _, c := range cases {
		if got := cardURL(agentSpec{URL: c.url}, defaultWellKnownPath); got != c.want {
			t.Errorf("cardURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// TestFetchCardHTTP exercises the real HTTP+JSON discovery binding end-to-end
// against an httptest server serving the well-known card path.
func TestFetchCardHTTP(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", baseCard("researcher"))

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(card)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgAgents: `[{"name":"researcher","url":"` + srv.URL + `","trust_jwks":` + string(jwks) + `}]`,
	}}
	if err := s.Open(t.Context(), cfg); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	trust := sink.findingsOfKind(findingTrust)
	if len(trust) != 1 {
		t.Fatalf("want one trust finding from HTTP discovery, got %+v", sink.findings())
	}
	if trust[0].Title == "" || trust[0].Severity == "" {
		t.Errorf("unexpected trust finding: %+v", trust[0])
	}
}
