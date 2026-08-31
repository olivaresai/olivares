// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// raiFixture serves the Cognitive Services accounts + raiPolicies + deployments reads
// and records request methods so a test can assert the reads are GETs.
type raiFixture struct {
	accountsSub1 string // accounts list for sub-1 (sub-2 returns empty)
	policies     string // raiPolicies list for the (only) AI account
	deployments  string // deployments list for the (only) AI account
	policyStatus int    // status to return for raiPolicies (0 ⇒ 200)

	mu      sync.Mutex
	methods []string
}

func (f *raiFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.methods = append(f.methods, r.Method)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/providers/Microsoft.CognitiveServices/accounts"):
			if strings.Contains(r.URL.Path, "/subscriptions/sub-1/") {
				_, _ = w.Write([]byte(f.accountsSub1))
				return
			}
			_, _ = w.Write([]byte(`{"value":[]}`)) // sub-2 has no AI accounts
		case strings.HasSuffix(r.URL.Path, "/raiPolicies"):
			if f.policyStatus != 0 {
				w.WriteHeader(f.policyStatus)
				return
			}
			_, _ = w.Write([]byte(f.policies))
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			_, _ = w.Write([]byte(f.deployments))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func (f *raiFixture) gets() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	gets, other := 0, 0
	for _, m := range f.methods {
		if m == http.MethodGet {
			gets++
		} else {
			other++
		}
	}
	return gets, other
}

// One OpenAI account (carries RAI) and one Speech account (must be skipped).
const raiAccounts = `{"value":[
{"id":"/subscriptions/sub-1/resourceGroups/rg1/providers/Microsoft.CognitiveServices/accounts/aoai","name":"aoai","kind":"OpenAI"},
{"id":"/subscriptions/sub-1/resourceGroups/rg1/providers/Microsoft.CognitiveServices/accounts/speech","name":"speech","kind":"SpeechServices"}
]}`

// A weakened UserManaged policy (Hate filtering disabled on the prompt side) and a
// healthy SystemManaged default.
const raiPolicies = `{"value":[
{"name":"weak","properties":{"type":"UserManaged","mode":"Blocking","basePolicyName":"Microsoft.Default","contentFilters":[
  {"name":"Hate","enabled":false,"blocking":false,"source":"Prompt"},
  {"name":"Hate","enabled":true,"blocking":true,"severityThreshold":"Medium","source":"Completion"},
  {"name":"Violence","enabled":true,"blocking":true,"severityThreshold":"Medium","source":"Prompt"}
]}},
{"name":"corp-default","properties":{"type":"SystemManaged","mode":"Blocking","basePolicyName":"Microsoft.Default","contentFilters":[
  {"name":"Hate","enabled":true,"blocking":true,"severityThreshold":"Medium","source":"Prompt"},
  {"name":"Sexual","enabled":true,"blocking":true,"severityThreshold":"Medium","source":"Prompt"}
]}}
]}`

// One deployment bound to a named policy, one on the implicit default.
const raiDeployments = `{"value":[
{"name":"gpt4o","properties":{"raiPolicyName":"weak"}},
{"name":"gpt4o-mini","properties":{}}
]}`

func findingsBySubject(fs []model.FindingReport, subjectKind string) []model.FindingReport {
	var out []model.FindingReport
	for _, f := range fs {
		if f.SubjectKind == subjectKind {
			out = append(out, f)
		}
	}
	return out
}

func TestRAI_PostureFindings(t *testing.T) {
	fx := &raiFixture{accountsSub1: raiAccounts, policies: raiPolicies, deployments: raiDeployments}
	srv := fx.server(t)
	s := openSource(t, srv.URL, map[string]string{
		"enable_rai":       "true",
		"enable_inventory": "false",
		"enable_activity":  "false",
	})
	sink := gather(t, s)
	fs := sink.findingSnapshot()

	// Every finding must be safety_posture (no health/edge leakage on the happy path).
	for _, f := range fs {
		if f.Kind != "safety_posture" {
			t.Fatalf("unexpected non-posture finding: %+v", f)
		}
	}

	// One honest content_filter note.
	if cf := findingsBySubject(fs, "azure.content_filter"); len(cf) != 1 || cf[0].Severity != model.SeverityInfo {
		t.Fatalf("content_filter honesty finding = %+v", cf)
	}

	// Two policy findings: weak ⇒ Medium (disables Hate/Prompt); default ⇒ Info.
	pols := findingsBySubject(fs, "azure.rai_policy")
	if len(pols) != 2 {
		t.Fatalf("rai_policy findings = %d, want 2: %+v", len(pols), pols)
	}
	var sawWeak bool
	for _, p := range pols {
		if strings.Contains(p.Title, "weak") {
			sawWeak = true
			if p.Severity != model.SeverityMedium || !strings.Contains(p.Title, "disables harm filtering") || !strings.Contains(p.Title, "Hate/Prompt") {
				t.Fatalf("weak policy finding = %+v", p)
			}
		}
	}
	if !sawWeak {
		t.Fatal("missing the weakened RAI policy finding")
	}

	// Two deployment bindings: one default (Microsoft.Default), one bound.
	deps := findingsBySubject(fs, "azure.deployment")
	if len(deps) != 2 {
		t.Fatalf("deployment findings = %d, want 2: %+v", len(deps), deps)
	}
	var sawDefault, sawBound bool
	for _, d := range deps {
		if d.Severity != model.SeverityInfo {
			t.Fatalf("deployment finding severity = %q, want info", d.Severity)
		}
		if strings.Contains(d.Title, "Microsoft.Default") {
			sawDefault = true
		}
		if strings.Contains(d.Title, "bound to RAI policy weak") {
			sawBound = true
		}
	}
	if !sawDefault || !sawBound {
		t.Fatalf("deployment binding coverage: default=%v bound=%v", sawDefault, sawBound)
	}

	// The non-AI (Speech) account must be skipped — no finding references it.
	for _, f := range fs {
		if strings.Contains(f.SubjectRef, "speech") {
			t.Fatalf("Speech account must be skipped, got %+v", f)
		}
	}

	// Read-only: every Azure call was a GET.
	if gets, other := fx.gets(); other != 0 || gets == 0 {
		t.Fatalf("non-GET RAI requests: gets=%d other=%d", gets, other)
	}
}

// TestRAI_UnreadableDegradesHonestly proves a 403 on raiPolicies yields a Medium
// "unreadable" posture finding (honest degradation), never a fabricated green.
func TestRAI_UnreadableDegradesHonestly(t *testing.T) {
	fx := &raiFixture{accountsSub1: raiAccounts, policyStatus: http.StatusForbidden, deployments: `{"value":[]}`}
	srv := fx.server(t)
	s := openSource(t, srv.URL, map[string]string{
		"enable_rai":       "true",
		"enable_inventory": "false",
		"enable_activity":  "false",
	})
	sink := gather(t, s)
	fs := sink.findingSnapshot()

	pols := findingsBySubject(fs, "azure.rai_policy")
	if len(pols) != 1 || pols[0].Severity != model.SeverityMedium || !strings.Contains(pols[0].Title, "unreadable") {
		t.Fatalf("expected one Medium 'unreadable' policy finding, got %+v", pols)
	}
}
