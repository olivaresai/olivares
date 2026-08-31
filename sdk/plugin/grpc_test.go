// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/plugin"
)

// fakeSource is a SourceConnector served over gRPC by the test. Gather emits one
// edge and one cost observation, then returns (a batch source).
type fakeSource struct {
	mu        sync.Mutex
	openedAt  string // records the config it was opened with
	closed    bool
	gatherErr error
}

func (f *fakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name: "test.source", Version: "0.1.0", APIVersion: sdk.APIVersion,
		Type: sdk.TypeSource, Title: "Test Source",
		ConfigFields: []sdk.ConfigField{{Key: "dsn", Type: sdk.FieldString, Required: true}},
	}
}

func (f *fakeSource) Open(_ context.Context, cfg sdk.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openedAt = cfg.Get("dsn")
	return nil
}

func (f *fakeSource) Gather(ctx context.Context, sink sdk.Sink) error {
	if f.gatherErr != nil {
		return f.gatherErr
	}
	when := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if err := sink.Emit(ctx, model.EdgeObservation{
		OriginRef: "claude-1", ResourceRef: "public.customers", Mode: model.ModeRead,
		Source: model.SignalPGAudit, Confidence: model.ConfidenceAttributed, ObservedAt: when,
	}); err != nil {
		return err
	}
	return sink.Emit(ctx, model.CostSample{
		ProviderRef: "anthropic", ModelRef: "claude", CostMicroUSD: 42, OccurredAt: when,
	})
}

func (f *fakeSource) Close(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// collectSink records every observation it receives.
type collectSink struct {
	mu  sync.Mutex
	got []model.Observation
}

func (s *collectSink) Emit(_ context.Context, obs model.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, obs)
	return nil
}

func TestSourcePluginGRPCRoundTrip(t *testing.T) {
	fake := &fakeSource{}
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		plugin.SourcePluginName: &plugin.SourcePlugin{Impl: fake},
	})
	defer client.Close()

	raw, err := client.Dispense(plugin.SourcePluginName)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	sc, ok := raw.(sdk.SourceConnector)
	if !ok {
		t.Fatalf("dispensed %T does not satisfy sdk.SourceConnector", raw)
	}

	// Descriptor crossed the wire (fetched eagerly at dispense).
	d := sc.Descriptor()
	if d.Name != "test.source" || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor not round-tripped: %+v", d)
	}
	if len(d.ConfigFields) != 1 || d.ConfigFields[0].Key != "dsn" {
		t.Fatalf("descriptor config fields not round-tripped: %+v", d.ConfigFields)
	}

	ctx := context.Background()
	if err := sc.Open(ctx, sdk.Config{Settings: map[string]string{"dsn": "pg://x"}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	fake.mu.Lock()
	openedAt := fake.openedAt
	fake.mu.Unlock()
	if openedAt != "pg://x" {
		t.Errorf("config did not reach the connector: %q", openedAt)
	}

	sink := &collectSink{}
	if err := sc.Gather(ctx, sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.got) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(sink.got))
	}
	edge, ok := sink.got[0].(model.EdgeObservation)
	if !ok || edge.ResourceRef != "public.customers" || edge.Mode != model.ModeRead {
		t.Errorf("edge observation not round-tripped: %+v", sink.got[0])
	}
	cost, ok := sink.got[1].(model.CostSample)
	if !ok || cost.CostMicroUSD != 42 {
		t.Errorf("cost observation not round-tripped: %+v", sink.got[1])
	}

	if err := sc.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	fake.mu.Lock()
	closed := fake.closed
	fake.mu.Unlock()
	if !closed {
		t.Error("Close did not reach the connector across the wire")
	}
}

func TestSourcePluginGatherErrorPropagates(t *testing.T) {
	sentinel := errors.New("upstream unavailable")
	fake := &fakeSource{gatherErr: sentinel}
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		plugin.SourcePluginName: &plugin.SourcePlugin{Impl: fake},
	})
	defer client.Close()

	raw, err := client.Dispense(plugin.SourcePluginName)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	sc := raw.(sdk.SourceConnector)
	err = sc.Gather(context.Background(), &collectSink{})
	if err == nil {
		t.Fatal("expected Gather to surface the connector error across the wire")
	}
	// gRPC wraps the error message; assert it carried through.
	if got := err.Error(); !strings.Contains(got, "upstream unavailable") {
		t.Errorf("error message not propagated: %q", got)
	}
}

// fakeOutput is an OutputConnector served over gRPC by the test.
type fakeOutput struct {
	mu   sync.Mutex
	last sdk.Notification
}

func (f *fakeOutput) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.output", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeOutput}
}
func (f *fakeOutput) Open(context.Context, sdk.Config) error { return nil }
func (f *fakeOutput) Notify(_ context.Context, n sdk.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = n
	return nil
}
func (f *fakeOutput) Close(context.Context) error { return nil }

func TestOutputPluginGRPCRoundTrip(t *testing.T) {
	fake := &fakeOutput{}
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		plugin.OutputPluginName: &plugin.OutputPlugin{Impl: fake},
	})
	defer client.Close()

	raw, err := client.Dispense(plugin.OutputPluginName)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	oc := raw.(sdk.OutputConnector)
	if oc.Descriptor().Name != "test.output" {
		t.Fatalf("descriptor not round-tripped: %+v", oc.Descriptor())
	}

	ctx := context.Background()
	n := sdk.Notification{
		Type: "finding.reported", Title: "drift detected", Severity: model.SeverityCritical,
		Tenant: "t1", Fields: map[string]string{"edge": "claude->customers"}, Time: time.Now().UTC(),
	}
	if err := oc.Notify(ctx, n); err != nil {
		t.Fatalf("notify: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.last.Title != "drift detected" || fake.last.Severity != model.SeverityCritical {
		t.Errorf("notification not round-tripped: %+v", fake.last)
	}
	if fake.last.Fields["edge"] != "claude->customers" {
		t.Errorf("notification fields not round-tripped: %+v", fake.last.Fields)
	}
}

type fakeContentSource struct {
	mu      sync.Mutex
	opened  string
	closed  bool
	fetches []string
	// listCursors records every cursor the SERVER handed this source, in order. It
	// replaces an unused `listNext` field (golangci-lint `unused`): the dead field
	// named an intent no test in THIS file carried out, and deleting it without
	// carrying the intent out would have dropped the name without answering it.
	//
	// CORRECTED 2026-08-17 by verification. This comment claimed "nothing proved the
	// CALLER's resume cursor reaches the plugin". That was false and was measured
	// false: with `cursor := req.GetCursor()` mutated to `cursor := ""` in
	// content_source.go:87, the PRE-EXISTING content_bounded_test.go:162
	// (TestContentWirePagesResumably, 250 docs over bounded pages) and
	// TestContentWireLargeCountPageNotTruncated both go red on the tree as it stood
	// before this field existed — a server that restarts at "" never terminates the
	// walk. The mechanism was covered; what was missing here was a DIRECT assertion
	// at the round-trip level, which is what this field and the resume block below
	// provide. Note also that under this two-page fixture the sibling shape assertion
	// (resumed == [doc-2] alone) is what fails first on that mutant, so this field's
	// own check reads as the diagnosis rather than the sole witness.
	listCursors []string
}

func (f *fakeContentSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       "test.content",
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeContentSource,
		Surfaces:   []string{"knowledge.document"},
	}
}

func (f *fakeContentSource) Open(_ context.Context, cfg sdk.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = cfg.Get("token")
	return nil
}

func (f *fakeContentSource) List(_ context.Context, cursor string) ([]sdk.DocRef, string, error) {
	f.mu.Lock()
	f.listCursors = append(f.listCursors, cursor)
	f.mu.Unlock()
	when := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	switch cursor {
	case "":
		return []sdk.DocRef{{DocID: "doc-1", Title: "One", ContentType: "text/markdown", ModifiedAt: when}}, "next", nil
	case "next":
		return []sdk.DocRef{{DocID: "doc-2", Title: "Two", ContentType: "text/plain", ModifiedAt: when.Add(time.Hour)}}, "", nil
	default:
		return nil, "", nil
	}
}

func (f *fakeContentSource) Fetch(_ context.Context, docID string) (sdk.Document, error) {
	f.mu.Lock()
	f.fetches = append(f.fetches, docID)
	f.mu.Unlock()
	return sdk.Document{
		Source:         "test",
		DocID:          docID,
		Title:          "Doc " + docID,
		Body:           []byte("body for " + docID),
		ContentType:    "text/plain",
		ACL:            []string{"group:eng"},
		Classification: "internal",
		SpaceRef:       "space:AI",
		ModifiedAt:     time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC),
		Attributes:     map[string]string{"uri": "test://" + docID},
		ExternalLabels: []string{"purview:internal"},
	}, nil
}

func (f *fakeContentSource) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

type fakeDeltaContentSource struct {
	*fakeContentSource
}

func (f *fakeDeltaContentSource) DeltaList(_ context.Context, cursor string) (sdk.DeltaPage, error) {
	return sdk.DeltaPage{
		Changes: []sdk.Change{
			{ChangeKind: sdk.ChangeContent, DocRef: sdk.DocRef{DocID: "doc-1", Title: "One"}},
			{ChangeKind: sdk.ChangeACL, DocRef: sdk.DocRef{DocID: "doc-2", Title: "Two"}},
			{ChangeKind: sdk.ChangeDeleted, DocRef: sdk.DocRef{DocID: "doc-3", Title: "Three"}},
		},
		NextToken:   "",
		ResumeToken: cursor + ":resume",
	}, nil
}

func (f *fakeDeltaContentSource) FetchACL(_ context.Context, docID string) (sdk.ACLResult, error) {
	return sdk.ACLResult{
		ACL:            []string{"group:eng", "role:reader"},
		ExternalLabels: []string{"uc:restricted"},
		Classification: "confidential",
	}, nil
}

func TestContentSourcePluginGRPCRoundTrip(t *testing.T) {
	fake := &fakeDeltaContentSource{fakeContentSource: &fakeContentSource{}}
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		plugin.ContentSourcePluginName: &plugin.ContentSourcePlugin{Impl: fake},
	})
	defer client.Close()

	raw, err := client.Dispense(plugin.ContentSourcePluginName)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	cs, ok := raw.(sdk.ContentSource)
	if !ok {
		t.Fatalf("dispensed %T does not satisfy sdk.ContentSource", raw)
	}
	if cs.Descriptor().Type != sdk.TypeContentSource || len(cs.Descriptor().Surfaces) != 1 {
		t.Fatalf("descriptor not round-tripped: %+v", cs.Descriptor())
	}

	ctx := context.Background()
	if err := cs.Open(ctx, sdk.Config{Settings: map[string]string{"token": "resolved-token"}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	fake.mu.Lock()
	opened := fake.opened
	fake.mu.Unlock()
	if opened != "resolved-token" {
		t.Errorf("config did not reach content source: %q", opened)
	}

	refs, next, err := cs.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if next != "" || len(refs) != 2 || refs[0].DocID != "doc-1" || refs[1].DocID != "doc-2" {
		t.Fatalf("streamed list mismatch: refs=%+v next=%q", refs, next)
	}
	// RESUME: the caller's cursor must reach the source. The assertion above cannot
	// see a server that ignores req.Cursor and always restarts, because it would
	// still deliver doc-2 — just with doc-1 in front of it.
	fake.mu.Lock()
	fake.listCursors = nil
	fake.mu.Unlock()
	resumed, next2, err := cs.List(ctx, "next")
	if err != nil {
		t.Fatalf("list from cursor: %v", err)
	}
	if next2 != "" || len(resumed) != 1 || resumed[0].DocID != "doc-2" {
		t.Fatalf("resume from cursor mismatch: refs=%+v next=%q", resumed, next2)
	}
	fake.mu.Lock()
	seen := append([]string(nil), fake.listCursors...)
	fake.mu.Unlock()
	if len(seen) != 1 || seen[0] != "next" {
		t.Errorf("the caller's cursor did not reach the source: saw %q", seen)
	}

	doc, err := cs.Fetch(ctx, "doc-1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(doc.Body) != "body for doc-1" || doc.Attributes["uri"] != "test://doc-1" {
		t.Errorf("document not round-tripped: %+v", doc)
	}

	live, ok := raw.(sdk.DeltaContentSource)
	if !ok {
		t.Fatalf("delta-capable source was not dispensed as sdk.DeltaContentSource")
	}
	acl, err := live.FetchACL(ctx, "doc-1")
	if err != nil {
		t.Fatalf("fetch acl: %v", err)
	}
	if len(acl.ACL) != 2 || acl.Classification != "confidential" || acl.ExternalLabels[0] != "uc:restricted" {
		t.Errorf("acl not round-tripped: %+v", acl)
	}
	page, err := live.DeltaList(ctx, "cursor-1")
	if err != nil {
		t.Fatalf("delta list: %v", err)
	}
	if page.ResumeToken != "cursor-1:resume" || len(page.Changes) != 3 {
		t.Fatalf("delta page mismatch: %+v", page)
	}
	if page.Changes[0].ChangeKind != sdk.ChangeContent || page.Changes[1].ChangeKind != sdk.ChangeACL || page.Changes[2].ChangeKind != sdk.ChangeDeleted {
		t.Errorf("change kinds not preserved: %+v", page.Changes)
	}

	if err := cs.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	fake.mu.Lock()
	closed := fake.closed
	fake.mu.Unlock()
	if !closed {
		t.Error("Close did not reach content source across the wire")
	}
}

func TestContentSourcePluginCapabilityGating(t *testing.T) {
	fake := &fakeContentSource{}
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		plugin.ContentSourcePluginName: &plugin.ContentSourcePlugin{Impl: fake},
	})
	defer client.Close()

	raw, err := client.Dispense(plugin.ContentSourcePluginName)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	if _, ok := raw.(sdk.ContentSource); !ok {
		t.Fatalf("dispensed %T does not satisfy sdk.ContentSource", raw)
	}
	if _, ok := raw.(sdk.DeltaContentSource); ok {
		t.Fatal("non-delta content source must not be dispensed as sdk.DeltaContentSource")
	}
}

// verdictOutput is a plugin that reports a fixed delivery verdict.
type verdictOutput struct{ err error }

func (v *verdictOutput) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.verdict", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeOutput}
}
func (v *verdictOutput) Open(context.Context, sdk.Config) error         { return nil }
func (v *verdictOutput) Notify(context.Context, sdk.Notification) error { return v.err }
func (v *verdictOutput) Close(context.Context) error                    { return nil }

// dispenseVerdictOutput wires one verdictOutput over a real gRPC plugin connection.
func dispenseVerdictOutput(t *testing.T, impl sdk.OutputConnector) (sdk.OutputConnector, func()) {
	t.Helper()
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		plugin.OutputPluginName: &plugin.OutputPlugin{Impl: impl},
	})
	raw, err := client.Dispense(plugin.OutputPluginName)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	return raw.(sdk.OutputConnector), func() { _ = client.Close() }
}

// TestNotifyVerdictSurvivesTheProcessBoundary pins the reason NotifyResponse
// exists at all.
//
// A gRPC error is only a string once it crosses, so a plugin that classified a
// deterministic refusal could not say so and the host retried it as though it were
// a temporary outage — which for OTLP the specification forbids outright. The
// verdict therefore travels in the RESPONSE, because gRPC DISCARDS the response
// message when a handler returns an error: a report returned beside an error would
// be lost at exactly the boundary it exists to cross.
func TestNotifyVerdictSurvivesTheProcessBoundary(t *testing.T) {
	refusal := sdk.NewDeliveryError(sdk.DeliveryReport{
		Outcome: sdk.OutcomeRejected, Sent: 1, Rejected: 1,
		Locator: sdk.LocatorPrefixBoundary, FirstRejected: 0, Code: 7,
	}, errors.New("HEC code 7: Incorrect index"))

	oc, cleanup := dispenseVerdictOutput(t, &verdictOutput{err: refusal})
	defer cleanup()

	err := oc.Notify(context.Background(), sdk.Notification{Title: "t"})
	if err == nil {
		t.Fatal("the refusal must cross the boundary")
	}
	report := sdk.ReportFor(err)
	if report.Outcome != sdk.OutcomeRejected {
		t.Fatalf("outcome = %s, want rejected: an out-of-process plugin must be able to state a deterministic refusal, or the host retries it through the whole ladder", report.Outcome)
	}
	if report.Outcome.Retryable() {
		t.Fatal("a deterministic refusal must not be retryable after crossing the boundary")
	}
	if report.Code != 7 || report.Rejected != 1 || report.FirstRejected != 0 {
		t.Fatalf("the report lost detail crossing the boundary: %+v", report)
	}
}

// TestAPluginSuccessCrossesAsNil keeps the ordinary path honest: an outcome with
// no error message is a clean delivery, not an empty failure.
func TestAPluginSuccessCrossesAsNil(t *testing.T) {
	oc, cleanup := dispenseVerdictOutput(t, &verdictOutput{})
	defer cleanup()
	if err := oc.Notify(context.Background(), sdk.Notification{Title: "t"}); err != nil {
		t.Fatalf("a successful notify must cross as nil, got %v", err)
	}
}

// TestAPlainPluginErrorStaysIndeterminate is what keeps an EXISTING plugin
// working: one that never heard of this contract returns a plain error, which
// reads as "I do not know" and stays retryable, exactly as before.
func TestAPlainPluginErrorStaysIndeterminate(t *testing.T) {
	oc, cleanup := dispenseVerdictOutput(t, &verdictOutput{err: errors.New("connection refused")})
	defer cleanup()
	err := oc.Notify(context.Background(), sdk.Notification{Title: "t"})
	if err == nil {
		t.Fatal("the failure must still cross")
	}
	report := sdk.ReportFor(err)
	if report.Outcome != sdk.OutcomeIndeterminate {
		t.Fatalf("outcome = %s, want indeterminate for an unclassified plugin error", report.Outcome)
	}
	if !report.Outcome.Retryable() {
		t.Fatal("an unclassified plugin failure must stay retryable, or every transient outage dead-letters on the first attempt")
	}
}
