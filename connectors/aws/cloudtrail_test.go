// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestMapCloudTrailEvent_UnknownWhenReadOnlyAbsent locks the honesty rule: a
// management event whose CloudTrailEvent omits the readOnly field is classified
// ModeUnknown, never a guessed read or write.
func TestMapCloudTrailEvent_UnknownWhenReadOnlyAbsent(t *testing.T) {
	ev := lookupEvent{CloudTrailEvent: ctEventJSON(
		"ec2.amazonaws.com", "SomeUnclassifiedCall", "Management",
		nil, "IAMUser", "arn:aws:iam::111122223333:user/alice", "")}
	edge, ok := mapCloudTrailEvent(ev, time.Unix(0, 0).UTC())
	if !ok {
		t.Fatal("expected the management event to map to an edge")
	}
	if edge.Mode != model.ModeUnknown {
		t.Fatalf("absent readOnly must yield ModeUnknown, got %q", edge.Mode)
	}
	if edge.Source != model.SignalCloudTrail {
		t.Fatalf("source = %q, want cloudtrail", edge.Source)
	}
}

// ctEventJSON builds a CloudTrailEvent JSON string (the inner string LookupEvents
// returns). Empty fields are omitted so each case can include only what it tests.
func ctEventJSON(source, name, category string, readOnly *bool, identType, arn, issuerARN string) string {
	m := map[string]any{
		"eventSource": source,
		"eventName":   name,
		"eventTime":   "2026-06-03T10:00:00Z",
	}
	if category != "" {
		m["eventCategory"] = category
	}
	if readOnly != nil {
		m["readOnly"] = *readOnly
	}
	ui := map[string]any{}
	if identType != "" {
		ui["type"] = identType
	}
	if arn != "" {
		ui["arn"] = arn
	}
	if issuerARN != "" {
		ui["sessionContext"] = map[string]any{
			"sessionIssuer": map[string]any{"arn": issuerARN},
		}
	}
	m["userIdentity"] = ui
	b, _ := json.Marshal(m)
	return string(b)
}

func boolPtr(b bool) *bool { return &b }

// ctFixtureServer serves a two-page LookupEvents response and records the
// X-Amz-Target and HTTP method of every request.
type ctFixtureServer struct {
	mu      sync.Mutex
	targets []string
	methods []string
	page1   []lookupEvent
	page2   []lookupEvent
}

func (h *ctFixtureServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.targets = append(h.targets, r.Header.Get("X-Amz-Target"))
	h.methods = append(h.methods, r.Method)
	h.mu.Unlock()

	var req lookupRequest
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	w.Header().Set("Content-Type", cloudTrailContentType)
	if req.NextToken == "" {
		writeJSON(w, lookupResponse{Events: h.page1, NextToken: "TOKEN2"})
		return
	}
	writeJSON(w, lookupResponse{Events: h.page2})
}

func writeJSON(w http.ResponseWriter, v lookupResponse) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}

func openCloudTrailOnly(t *testing.T, endpoint string, extra map[string]string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgCloudTrailEndpoint: endpoint,
		cfgEnableIAM:          "false",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	for k, v := range extra {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestCloudTrailGoldenAndMapping(t *testing.T) {
	h := &ctFixtureServer{
		page1: []lookupEvent{
			// readOnly Describe ⇒ ModeRead, distinct user ARN ⇒ Attributed.
			{CloudTrailEvent: ctEventJSON("ec2.amazonaws.com", "DescribeInstances", "Management",
				boolPtr(true), "IAMUser", "arn:aws:iam::123456789012:user/alice", "")},
			// mutating event ⇒ ModeWrite, distinct role ARN ⇒ Attributed.
			{CloudTrailEvent: ctEventJSON("ec2.amazonaws.com", "RunInstances", "Management",
				boolPtr(false), "Root", "arn:aws:iam::123456789012:root", "")},
		},
		page2: []lookupEvent{
			// assumed-role ⇒ ConfidenceApproximate even though an ARN is present.
			{CloudTrailEvent: ctEventJSON("s3.amazonaws.com", "ListBuckets", "Management",
				boolPtr(true), "AssumedRole", "arn:aws:sts::123456789012:assumed-role/deploy/session", "")},
			// Data-category event ⇒ MUST be skipped (owns the data plane).
			{CloudTrailEvent: ctEventJSON("s3.amazonaws.com", "GetObject", "Data",
				boolPtr(true), "IAMUser", "arn:aws:iam::123456789012:user/alice", "")},
		},
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openCloudTrailOnly(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}

	want := []edgeKey{
		{originIdentity, "arn:aws:iam::123456789012:user/alice", resAWSAPI, "ec2.amazonaws.com:DescribeInstances",
			model.ModeRead, model.SignalCloudTrail, model.ConfidenceAttributed, "ec2.amazonaws.com"},
		{originIdentity, "arn:aws:iam::123456789012:root", resAWSAPI, "ec2.amazonaws.com:RunInstances",
			model.ModeWrite, model.SignalCloudTrail, model.ConfidenceAttributed, "ec2.amazonaws.com"},
		{originIdentity, "arn:aws:sts::123456789012:assumed-role/deploy/session", resAWSAPI, "s3.amazonaws.com:ListBuckets",
			model.ModeRead, model.SignalCloudTrail, model.ConfidenceApproximate, "s3.amazonaws.com"},
	}
	assertEdgeSet(t, sink.edges(), want)

	// The Data-category GetObject must NOT appear in any emitted ref.
	for _, e := range sink.edges() {
		if strings.Contains(e.ResourceRef, "GetObject") {
			t.Fatalf("data-category event leaked into edges: %q", e.ResourceRef)
		}
	}

	// Pagination really happened (two requests), and every request hit the
	// LookupEvents target via POST (the documented read-only exception).
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.targets) != 2 {
		t.Fatalf("expected 2 LookupEvents requests (pagination), got %d", len(h.targets))
	}
	for i := range h.targets {
		if h.targets[i] != cloudTrailTarget {
			t.Fatalf("request %d hit unexpected target %q", i, h.targets[i])
		}
		if h.methods[i] != http.MethodPost {
			t.Fatalf("request %d used method %q, want POST", i, h.methods[i])
		}
	}
}

// TestCloudTrailSessionIssuerFallback covers the principal-attribution fallbacks:
// no top-level ARN but a session-issuer ARN ⇒ approximate using the issuer; and
// no ARN at all ⇒ approximate using the identity type.
func TestCloudTrailSessionIssuerFallback(t *testing.T) {
	h := &ctFixtureServer{
		page1: []lookupEvent{
			{CloudTrailEvent: ctEventJSON("kms.amazonaws.com", "ListKeys", "Management",
				boolPtr(true), "AssumedRole", "", "arn:aws:iam::123456789012:role/issuer")},
			{CloudTrailEvent: ctEventJSON("sts.amazonaws.com", "GetCallerIdentity", "Management",
				boolPtr(true), "AWSService", "", "")},
		},
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openCloudTrailOnly(t, srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	byRes := map[string]model.EdgeObservation{}
	for _, e := range sink.edges() {
		byRes[e.ResourceRef] = e
	}
	issuer := byRes["kms.amazonaws.com:ListKeys"]
	if issuer.OriginRef != "arn:aws:iam::123456789012:role/issuer" || issuer.Confidence != model.ConfidenceApproximate {
		t.Fatalf("session-issuer fallback wrong: %+v", issuer)
	}
	svc := byRes["sts.amazonaws.com:GetCallerIdentity"]
	if svc.OriginRef != "AWSService" || svc.Confidence != model.ConfidenceApproximate {
		t.Fatalf("type fallback wrong: %+v", svc)
	}
}
