// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// TestCloudTrailMaxEventsCap proves max_events bounds a pass: with a server that
// always returns one event and a NextToken, the connector still stops at the cap
// rather than paginating forever.
func TestCloudTrailMaxEventsCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", cloudTrailContentType)
		// Always one event and always a NextToken ⇒ unbounded without the cap.
		resp := lookupResponse{
			Events: []lookupEvent{
				{CloudTrailEvent: ctEventJSON("ec2.amazonaws.com", "DescribeInstances", "Management",
					boolPtr(true), "IAMUser", "arn:aws:iam::123456789012:user/alice", "")},
			},
			NextToken: "always-more",
		}
		writeJSON(w, resp)
	}))
	defer srv.Close()

	s := openCloudTrailOnly(t, srv.URL, map[string]string{cfgMaxEvents: "3"})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got := len(sink.edges()); got != 3 {
		t.Fatalf("max_events cap not honored: got %d edges, want 3", got)
	}
}

// TestEmitErrorIsFatal proves an Emit error is returned by Gather (treated as
// fatal to the pass), per the SDK contract.
func TestEmitErrorIsFatal(t *testing.T) {
	iamSrv := httptest.NewServer(&iamFixtureServer{})
	defer iamSrv.Close()

	s := openIAMOnly(t, iamSrv.URL, "123456789012")
	sentinel := errors.New("sink closed")
	sink := &fakeSink{emitErr: sentinel}
	err := s.Gather(context.Background(), sink)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected Emit error to propagate, got %v", err)
	}
}

// TestCloudTrailHealthFinding proves a failing (500) CloudTrail service yields one
// health finding under the cloudtrail subject (the symmetric case to the IAM
// health test), confirming the per-service subject mapping.
func TestCloudTrailHealthFinding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "throttled", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := openCloudTrailOnly(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectCloudTrail {
		t.Fatalf("expected one cloudtrail health finding, got %+v", fs)
	}
}

// TestCloudTrailEndpointDerivedFromRegion proves the CloudTrail endpoint is
// derived from the region when not explicitly set.
func TestCloudTrailEndpointDerivedFromRegion(t *testing.T) {
	cfg := sdk.Config{Settings: map[string]string{
		cfgRegion:          "eu-west-1",
		cfgAccessKeyID:     testCreds[cfgAccessKeyID],
		cfgSecretAccessKey: testCreds[cfgSecretAccessKey],
		cfgEnableIAM:       "false",
	}}
	c, err := loadConfig(cfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if c.cloudTrailEndpoint != "https://cloudtrail.eu-west-1.amazonaws.com" {
		t.Fatalf("derived endpoint wrong: %q", c.cloudTrailEndpoint)
	}
}

// TestPolicyScopeNormalization proves policy_scope coerces to exactly "Local" or
// "All", so a typo never silently widens discovery to AWS-managed policies.
func TestPolicyScopeNormalization(t *testing.T) {
	cases := map[string]string{"": "Local", "Local": "Local", "local": "Local", "all": "All", "All": "All", "bogus": "Local"}
	for in, want := range cases {
		if got := normalizePolicyScope(in); got != want {
			t.Fatalf("normalizePolicyScope(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseEventTimeFallbacks covers the eventTime resolution order:
// RFC3339 string ⇒ envelope unix ⇒ per-pass fallback.
func TestParseEventTimeFallbacks(t *testing.T) {
	fallback := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	if got := parseEventTime("2026-01-02T03:04:05Z", 0, fallback); !got.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("rfc3339 parse wrong: %v", got)
	}
	unix := float64(time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC).Unix())
	if got := parseEventTime("", unix, fallback); got.Year() != 2025 {
		t.Fatalf("unix fallback wrong: %v", got)
	}
	if got := parseEventTime("", 0, fallback); !got.Equal(fallback) {
		t.Fatalf("fallback wrong: %v", got)
	}
}
