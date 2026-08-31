// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestDescriptor checks the descriptor's identity and that every credential field
// is declared Secret (so the engine masks it and never inlines it).
func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != "olivares.aws" || d.Version != "0.1.0" {
		t.Fatalf("descriptor identity: %q %q", d.Name, d.Version)
	}
	if d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor type/apiversion: %q %q", d.Type, d.APIVersion)
	}
	secretKeys := map[string]bool{cfgAccessKeyID: true, cfgSecretAccessKey: true, cfgSessionToken: true}
	for _, f := range d.ConfigFields {
		if secretKeys[f.Key] && !f.Secret {
			t.Fatalf("field %q must be Secret", f.Key)
		}
	}
}

// TestOpenMissingCredentials proves a config error surfaces in Open (not Gather)
// when a service is enabled but no credentials are available.
func TestOpenMissingCredentials(t *testing.T) {
	t.Setenv(envAccessKeyID, "")
	t.Setenv(envSecretAccessKey, "")
	t.Setenv(envSessionToken, "")
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err == nil {
		t.Fatal("expected missing-credentials error")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("error %v should mention credentials", err)
	}
}

// TestOpenDisabledNoCredentials proves a fully-disabled connector is a valid
// no-op configuration even with no credentials, and Gather emits nothing.
func TestOpenDisabledNoCredentials(t *testing.T) {
	t.Setenv(envAccessKeyID, "")
	t.Setenv(envSecretAccessKey, "")
	s := New()
	cfg := sdk.Config{Settings: map[string]string{cfgEnableIAM: "false", cfgEnableCloudTrail: "false"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open (all disabled) should succeed: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.edges())+len(sink.findings()) != 0 {
		t.Fatalf("disabled connector emitted observations: %+v", sink.obs)
	}
}

// TestGatherEndToEnd runs both services against live httptest servers and checks
// the combined edge set spans both signal sources.
func TestGatherEndToEnd(t *testing.T) {
	iamSrv := httptest.NewServer(&iamFixtureServer{})
	defer iamSrv.Close()

	ct := &ctFixtureServer{
		page1: []lookupEvent{
			{CloudTrailEvent: ctEventJSON("ec2.amazonaws.com", "DescribeInstances", "Management",
				boolPtr(true), "IAMUser", "arn:aws:iam::123456789012:user/alice", "")},
		},
	}
	ctSrv := httptest.NewServer(ct)
	defer ctSrv.Close()

	s := New()
	settings := map[string]string{
		cfgIAMEndpoint:        iamSrv.URL,
		cfgCloudTrailEndpoint: ctSrv.URL,
		cfgAccountID:          "123456789012",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}

	var sawAWS, sawCT bool
	for _, e := range sink.edges() {
		switch e.Source {
		case signalAWS:
			sawAWS = true
		case model.SignalCloudTrail:
			sawCT = true
		}
	}
	if !sawAWS || !sawCT {
		t.Fatalf("expected both signals; aws=%v cloudtrail=%v", sawAWS, sawCT)
	}
	// IAM topology edges all share the single per-pass timestamp.
	for _, e := range sink.edges() {
		if e.Source == signalAWS && e.ObservedAt.IsZero() {
			t.Fatal("IAM edge has zero ObservedAt")
		}
	}
}

// TestHealthFindingOnFailure proves an enabled-but-failing service yields exactly
// one health finding (with a hashed detail) and the OTHER service still runs.
func TestHealthFindingOnFailure(t *testing.T) {
	// IAM returns 500 (configured but failing ⇒ finding); CloudTrail succeeds.
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer iamSrv.Close()
	ct := &ctFixtureServer{
		page1: []lookupEvent{
			{CloudTrailEvent: ctEventJSON("ec2.amazonaws.com", "DescribeInstances", "Management",
				boolPtr(true), "IAMUser", "arn:aws:iam::123456789012:user/alice", "")},
		},
	}
	ctSrv := httptest.NewServer(ct)
	defer ctSrv.Close()

	s := New()
	settings := map[string]string{
		cfgIAMEndpoint:        iamSrv.URL,
		cfgCloudTrailEndpoint: ctSrv.URL,
		cfgAccountID:          "123456789012",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather should not fail on a service error: %v", err)
	}

	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("expected exactly 1 health finding, got %d: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Kind != "health" || f.SubjectKind != subjectIAM {
		t.Fatalf("wrong finding: %+v", f)
	}
	if f.Severity != model.SeverityMedium {
		t.Fatalf("wrong severity: %q", f.Severity)
	}
	// Detail must be a SHA-256 hash, never the raw error.
	if len(f.DetailHash) != 64 || strings.Contains(f.DetailHash, "boom") {
		t.Fatalf("detail must be hashed, got %q", f.DetailHash)
	}
	// The other service still produced edges.
	if len(sink.edges()) == 0 {
		t.Fatal("CloudTrail edges missing; a failing IAM aborted the pass")
	}
}

// TestAbsentServiceNoFinding proves a DISABLED (absent) service is skipped
// silently — no health finding — distinguishing "not present" from "failing".
func TestAbsentServiceNoFinding(t *testing.T) {
	iamSrv := httptest.NewServer(&iamFixtureServer{})
	defer iamSrv.Close()

	s := New()
	settings := map[string]string{
		cfgIAMEndpoint:      iamSrv.URL,
		cfgEnableCloudTrail: "false", // absent ⇒ silent skip
		cfgAccountID:        "123456789012",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("disabled service must not produce a finding: %+v", sink.findings())
	}
}

// TestGatherCtxCancel proves a canceled ctx makes Gather return ctx.Err()
// promptly. The IAM server blocks until the request's ctx is canceled, so the
// in-flight call observes the cancellation.
func TestGatherCtxCancel(t *testing.T) {
	iamSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never reply; wait for client cancellation
	}))
	defer iamSrv.Close()

	s := New()
	settings := map[string]string{
		cfgIAMEndpoint:      iamSrv.URL,
		cfgEnableCloudTrail: "false",
		cfgAccountID:        "123456789012",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, &fakeSink{}) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Gather returned nil on canceled ctx")
		}
		if !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Gather did not return promptly after cancellation")
	}
}

// TestRedactionScrubsSecretInPrincipal proves a secret embedded in a CloudTrail
// principal ARN (a worst case: a leaked access key inside the identity string) is
// scrubbed out of every emitted ref and replaced by the redaction marker — the
// raw secret never reaches an observation.
func TestRedactionScrubsSecretInPrincipal(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE" // an AWS access-key-shaped token
	tainted := "arn:aws:iam::123456789012:user/" + secret

	ct := &ctFixtureServer{
		page1: []lookupEvent{
			{CloudTrailEvent: ctEventJSON("ec2.amazonaws.com", "DescribeInstances", "Management",
				boolPtr(true), "IAMUser", tainted, "")},
		},
	}
	srv := httptest.NewServer(ct)
	defer srv.Close()

	s := openCloudTrailOnly(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	ref := edges[0].OriginRef
	if strings.Contains(ref, secret) {
		t.Fatalf("raw secret survived in ref %q", ref)
	}
	// The marker may be bare ("[REDACTED]") or labeled ("[REDACTED:aws-access-key]");
	// both begin with the placeholder's leading bracketed token.
	marker := strings.TrimSuffix(redact.Placeholder, "]")
	if !strings.Contains(ref, marker) {
		t.Fatalf("expected redaction marker in %q", ref)
	}
}
