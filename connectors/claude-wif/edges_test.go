// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestGatherPermittedEdges(t *testing.T) {
	s, _ := newLive(t, testFederation)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	byOrigin := map[string]model.EdgeObservation{}
	for _, e := range sink.edges() {
		if e.Source != model.SignalPolicy {
			t.Errorf("edge %s->%s is %q, want SignalPolicy", e.OriginRef, e.ResourceRef, e.Source)
		}
		byOrigin[e.OriginRef] = e
	}

	// API-key grant: active key -> its workspace, read-write, developer scope.
	key, ok := byOrigin["apikey_1"]
	if !ok {
		t.Fatal("missing api-key permitted edge")
	}
	if key.ResourceKind != resWorkspaceAPI || key.ResourceRef != "wrkspc_1" ||
		key.Mode != model.ModeReadWrite || key.ToolRef != scopeWorkspaceDeveloper {
		t.Errorf("api-key edge = %+v", key)
	}

	// Inactive key grants no live edge.
	if _, ok := byOrigin["apikey_2"]; ok {
		t.Error("inactive api key must not produce a permitted edge")
	}

	// Service-account grant from the declared federation rule.
	sa, ok := byOrigin["svac_1"]
	if !ok {
		t.Fatal("missing service-account permitted edge")
	}
	if sa.ResourceRef != "wrkspc_1" || sa.Mode != model.ModeReadWrite || sa.ToolRef != scopeWorkspaceDeveloper {
		t.Errorf("service-account edge = %+v", sa)
	}

	// Convergence: every permitted edge's origin must be a roster identity (same
	// external_id), or module III could never diff it against an observed access.
	g, _ := s.Snapshot(context.Background())
	for origin := range byOrigin {
		if _, found := g.FindIdentity(origin); !found {
			t.Errorf("permitted edge origin %q has no roster identity (would be a no-op grant)", origin)
		}
	}
}

func TestFootgunFinding(t *testing.T) {
	mkEnv := func(vals map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := vals[k]; return v, ok }
	}

	cases := []struct {
		name       string
		federation string
		env        map[string]string
		wantFind   bool
		wantSubj   string
	}{
		{"key shadows declared federation", testFederation, map[string]string{envAPIKey: "sk-ant-real"}, true, envAPIKey},
		{"empty key still shadows", testFederation, map[string]string{envAPIKey: ""}, true, envAPIKey},
		{"auth token shadows", testFederation, map[string]string{envAuthToken: "tok"}, true, envAuthToken},
		{"token-file signals federation in use", "", map[string]string{envAPIKey: "k", envIdentityTokenFile: "/run/token"}, true, envAPIKey},
		{"no static key, no finding", testFederation, map[string]string{}, false, ""},
		{"static key but no federation, no finding", "", map[string]string{envAPIKey: "k"}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New()
			s.now = fixedClock
			s.lookEnv = mkEnv(c.env)
			settings := map[string]string{}
			if c.federation != "" {
				settings["federation"] = c.federation
			}
			if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
				t.Fatalf("Open: %v", err)
			}
			sink := &captureSink{}
			if err := s.Gather(context.Background(), sink); err != nil {
				t.Fatalf("Gather: %v", err)
			}
			fs := sink.findings()
			if c.wantFind {
				if len(fs) != 1 {
					t.Fatalf("want 1 finding, got %d", len(fs))
				}
				f := fs[0]
				if f.Severity != model.SeverityHigh || f.SubjectKind != subjectFederation || f.SubjectRef != c.wantSubj {
					t.Errorf("finding = %+v", f)
				}
				if f.DetailHash == "" {
					t.Error("finding must carry a redacted detail hash")
				}
			} else if len(fs) != 0 {
				t.Errorf("want no finding, got %+v", fs)
			}
		})
	}
}
