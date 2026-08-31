// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	azureactivity "github.com/olivaresai/olivares/connectors/azure-activity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	signalAzure    = model.SignalSource("azure")
	signalActivity = model.SignalSource("azure_activity")
	testTenant     = "tenant-xyz"
)

type capturingSink struct {
	mu       sync.Mutex
	edges    []model.EdgeObservation
	findings []model.FindingReport
}

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch o := obs.(type) {
	case model.EdgeObservation:
		c.edges = append(c.edges, o)
	case model.FindingReport:
		c.findings = append(c.findings, o)
	}
	return nil
}

func (c *capturingSink) edgeSnapshot() []model.EdgeObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.EdgeObservation(nil), c.edges...)
}

func (c *capturingSink) findingSnapshot() []model.FindingReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.FindingReport(nil), c.findings...)
}

type fixture struct {
	mu        sync.Mutex
	reqs      []string
	authSeen  bool
	failRG    bool
	failsAct  bool
	rgData    string
	events    string
	subsValue string // /subscriptions list response (auto-list test)
}

func newFixture() *fixture {
	return &fixture{rgData: defaultRGData, events: defaultEvents}
}

func (f *fixture) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		f.authSeen = true
	}
}

func (f *fixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/providers/Microsoft.ResourceGraph/resources", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.failRG {
			w.WriteHeader(500)
			return
		}
		writeJSON(w, f.rgData)
	})

	mux.HandleFunc("/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		writeJSON(w, f.subsValue)
	})

	mux.HandleFunc("/subscriptions/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if !strings.HasSuffix(r.URL.Path, "/providers/microsoft.insights/eventtypes/management/values") {
			writeJSON(w, `{"value":[]}`)
			return
		}
		if f.failsAct {
			w.WriteHeader(500)
			return
		}
		// Only sub-1 carries events (so two scoped subs don't double-count).
		if strings.Contains(r.URL.Path, "/subscriptions/sub-1/") {
			writeJSON(w, f.events)
			return
		}
		writeJSON(w, `{"value":[]}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

const defaultRGData = `{"data":[
{"id":"/subscriptions/sub-1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/VM-PROD","subscriptionId":"sub-1"},
{"id":"/subscriptions/sub-2/resourceGroups/rg2/providers/Microsoft.Storage/storageAccounts/STG","subscriptionId":"sub-2"}
]}`

// defaultEvents mixes Succeeded write/read/action/delete events with a Failed and
// a Started event (skipped: dedup + blocked) and a no-caller event (skipped). It
// carries http/authorization/localized fields the connector must NOT read.
const defaultEvents = `{"value":[
{"eventTimestamp":"2026-06-12T09:00:01Z","status":{"value":"Succeeded"},"operationName":{"value":"Microsoft.Compute/virtualMachines/write","localizedValue":"Create VM (SECRETDISPLAY)"},"resourceProviderName":{"value":"Microsoft.Compute"},"claims":{"http://schemas.microsoft.com/identity/claims/objectidentifier":"11111111-oid-admin","appid":"app-zzz","aud":"https://management.azure.com"},"httpRequest":{"uri":"https://x.example?sig=SECRETSAS"}},
{"eventTimestamp":"2026-06-12T09:00:02Z","status":{"value":"Succeeded"},"operationName":{"value":"Microsoft.Storage/storageAccounts/read"},"resourceProviderName":{"value":"Microsoft.Storage"},"claims":{"appid":"app-22222222"}},
{"eventTimestamp":"2026-06-12T09:00:03Z","status":{"value":"Succeeded"},"operationName":{"value":"Microsoft.Compute/virtualMachines/restart/action"},"caller":"user@contoso.onmicrosoft.com"},
{"eventTimestamp":"2026-06-12T09:00:04Z","status":{"value":"Succeeded"},"operationName":{"value":"Microsoft.Network/networkSecurityGroups/delete"},"resourceProviderName":{"value":"Microsoft.Network"},"claims":{"http://schemas.microsoft.com/identity/claims/objectidentifier":"33333333-oid"}},
{"eventTimestamp":"2026-06-12T09:00:05Z","status":{"value":"Failed"},"operationName":{"value":"Microsoft.Compute/virtualMachines/delete"},"claims":{"http://schemas.microsoft.com/identity/claims/objectidentifier":"intruder-oid"}},
{"eventTimestamp":"2026-06-12T09:00:06Z","status":{"value":"Started"},"operationName":{"value":"Microsoft.Compute/virtualMachines/write"},"claims":{"http://schemas.microsoft.com/identity/claims/objectidentifier":"11111111-oid-admin"}},
{"eventTimestamp":"2026-06-12T09:00:07Z","status":{"value":"Succeeded"},"operationName":{"value":"Microsoft.Resources/deployments/write"},"caller":""}
]}`

func openSource(t *testing.T, base string, extra map[string]string) *azureactivity.Source {
	t.Helper()
	settings := map[string]string{
		"access_token":        "test-token",
		"tenant_id":           testTenant,
		"subscriptions":       "sub-1,sub-2",
		"management_endpoint": base,
		"lookback":            "720h",
	}
	for k, v := range extra {
		settings[k] = v
	}
	s := azureactivity.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gather(t *testing.T, s *azureactivity.Source) *capturingSink {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

type edgeKey struct {
	originKind, originRef, resKind, resRef string
	mode                                   model.AccessMode
	source                                 model.SignalSource
	conf                                   model.Confidence
	tool                                   string
}

func keyOf(e model.EdgeObservation) edgeKey {
	return edgeKey{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, e.Mode, e.Source, e.Confidence, e.ToolRef}
}

func TestGatherInventoryGolden(t *testing.T) {
	srv := newFixture().server(t)
	s := openSource(t, srv.URL, map[string]string{"enable_activity": "false"})
	got := gather(t, s).edgeSnapshot()

	want := []edgeKey{
		{"azure.subscription", "sub-1", "azure.resource", "/subscriptions/sub-1/resourcegroups/rg1/providers/microsoft.compute/virtualmachines/vm-prod", model.ModeUnknown, signalAzure, model.ConfidenceAttributed, ""},
		{"azure.subscription", "sub-2", "azure.resource", "/subscriptions/sub-2/resourcegroups/rg2/providers/microsoft.storage/storageaccounts/stg", model.ModeUnknown, signalAzure, model.ConfidenceAttributed, ""},
		{"azure.tenant", testTenant, "azure.subscription", "sub-1", model.ModeUnknown, signalAzure, model.ConfidenceAttributed, ""},
		{"azure.tenant", testTenant, "azure.subscription", "sub-2", model.ModeUnknown, signalAzure, model.ConfidenceAttributed, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d inventory edges, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if k := keyOf(got[i]); k != want[i] {
			t.Errorf("edge[%d]\n got=%+v\nwant=%+v", i, k, want[i])
		}
		if got[i].ObservedAt.IsZero() {
			t.Errorf("edge[%d] ObservedAt is zero", i)
		}
	}
}

func TestGatherActivityGolden(t *testing.T) {
	srv := newFixture().server(t)
	s := openSource(t, srv.URL, map[string]string{
		"enable_inventory": "false",
		"shared_accounts":  "app-22222222",
	})
	got := gather(t, s).edgeSnapshot()

	want := []edgeKey{
		{"identity", "user@contoso.onmicrosoft.com", "azure.api", "Microsoft.Compute/virtualMachines/restart/action", model.ModeUnknown, signalActivity, model.ConfidenceAttributed, "Microsoft.Compute"},
		{"identity", "11111111-oid-admin", "azure.api", "Microsoft.Compute/virtualMachines/write", model.ModeWrite, signalActivity, model.ConfidenceAttributed, "Microsoft.Compute"},
		{"identity", "33333333-oid", "azure.api", "Microsoft.Network/networkSecurityGroups/delete", model.ModeWrite, signalActivity, model.ConfidenceAttributed, "Microsoft.Network"},
		{"identity", "app-22222222", "azure.api", "Microsoft.Storage/storageAccounts/read", model.ModeRead, signalActivity, model.ConfidenceApproximate, "Microsoft.Storage"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d activity edges, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if k := keyOf(got[i]); k != want[i] {
			t.Errorf("edge[%d]\n got=%+v\nwant=%+v", i, k, want[i])
		}
	}
	// The write edge must carry the event's own timestamp.
	for _, e := range got {
		if strings.HasSuffix(e.ResourceRef, "virtualMachines/write") {
			wantTS, _ := time.Parse(time.RFC3339, "2026-06-12T09:00:01Z")
			if !e.ObservedAt.Equal(wantTS) {
				t.Errorf("write ObservedAt = %v, want %v", e.ObservedAt, wantTS)
			}
		}
	}
}

func TestNoRawLeak(t *testing.T) {
	srv := newFixture().server(t)
	sink := gather(t, openSource(t, srv.URL, map[string]string{"enable_inventory": "false"}))
	forbidden := []string{
		"SECRETSAS", "SECRETDISPLAY", "httpRequest", "sig=", "intruder-oid",
		"https://management.azure.com", // the aud claim must never be emitted
	}
	for _, e := range sink.edgeSnapshot() {
		fields := []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
			string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef}
		for _, fld := range fields {
			for _, frag := range forbidden {
				if strings.Contains(fld, frag) {
					t.Errorf("edge field %q leaked forbidden fragment %q", fld, frag)
				}
			}
		}
	}
}

func TestSkippedEvents(t *testing.T) {
	srv := newFixture().server(t)
	sink := gather(t, openSource(t, srv.URL, map[string]string{"enable_inventory": "false"}))
	if n := len(sink.edgeSnapshot()); n != 4 {
		t.Fatalf("got %d activity edges, want 4 (Failed/Started/no-caller skipped)", n)
	}
}

func TestAutoListSubscriptions(t *testing.T) {
	f := newFixture()
	f.subsValue = `{"value":[{"subscriptionId":"sub-1","state":"Enabled"},{"subscriptionId":"sub-9","state":"Disabled"}]}`
	srv := f.server(t)
	// No explicit subscriptions ⇒ auto-list; sub-9 (Disabled) excluded, sub-1 used.
	s := openSource(t, srv.URL, map[string]string{"subscriptions": "", "enable_inventory": "false"})
	sink := gather(t, s)
	if len(sink.findingSnapshot()) != 0 {
		t.Fatalf("unexpected findings: %+v", sink.findingSnapshot())
	}
	// sub-1 carries the 4 events; sub-9 is excluded, so exactly 4 edges.
	if n := len(sink.edgeSnapshot()); n != 4 {
		t.Fatalf("auto-list produced %d edges, want 4 (only enabled sub-1)", n)
	}
}

func TestNoSubscriptionsFinding(t *testing.T) {
	f := newFixture()
	f.subsValue = `{"value":[]}`
	srv := f.server(t)
	s := openSource(t, srv.URL, map[string]string{"subscriptions": ""})
	sink := gather(t, s)
	findings := sink.findingSnapshot()
	if len(findings) != 1 || findings[0].SubjectKind != "azure.subscriptions" {
		t.Fatalf("want one azure.subscriptions finding, got %+v", findings)
	}
	if len(sink.edgeSnapshot()) != 0 {
		t.Error("no edges should be emitted when no subscriptions are visible")
	}
}

func TestOfflineNoOp(t *testing.T) {
	s := azureactivity.New()
	// Partial credential (tenant only) ⇒ offline.
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"tenant_id": testTenant}}); err != nil {
		t.Fatalf("offline Open should succeed: %v", err)
	}
	sink := gather(t, s)
	if n := len(sink.edgeSnapshot()) + len(sink.findingSnapshot()); n != 0 {
		t.Errorf("offline Gather emitted %d observations, want 0", n)
	}
}

func TestHealthFindingOnActivityFailure(t *testing.T) {
	f := newFixture()
	f.failsAct = true
	srv := f.server(t)
	sink := gather(t, openSource(t, srv.URL, nil))
	findings := sink.findingSnapshot()
	if len(findings) != 1 || findings[0].SubjectKind != "azure.activity" {
		t.Fatalf("want one azure.activity finding, got %+v", findings)
	}
	if findings[0].DetailHash == "" || strings.Contains(findings[0].Title, "500") {
		t.Errorf("finding must hash the detail, not embed it: %+v", findings[0])
	}
	if len(sink.edgeSnapshot()) == 0 {
		t.Error("inventory edges should still be emitted when only activity fails")
	}
}

// TestInventoryTruncationFinding proves that when Resource Graph stops at
// max_pages with a $skipToken still pending, the connector emits a low-severity
// coverage finding AND still emits the resources it collected.
func TestInventoryTruncationFinding(t *testing.T) {
	f := newFixture()
	// data + a $skipToken so page 1 (the only allowed page) leaves more data.
	f.rgData = `{"$skipToken":"more","data":[{"id":"/subscriptions/sub-1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/VM-A","subscriptionId":"sub-1"}]}`
	srv := f.server(t)
	sink := gather(t, openSource(t, srv.URL, map[string]string{
		"enable_activity": "false", "max_pages": "1",
	}))
	// tenant⊳sub (2) + sub⊳resource (1) = 3 inventory edges still emitted.
	if n := len(sink.edgeSnapshot()); n != 3 {
		t.Fatalf("got %d edges, want 3 (collected inventory)", n)
	}
	findings := sink.findingSnapshot()
	if len(findings) != 1 || findings[0].SubjectKind != "azure.inventory" || findings[0].Severity != model.SeverityLow {
		t.Fatalf("want one low-severity azure.inventory coverage finding, got %+v", findings)
	}
	if !strings.Contains(findings[0].Title, "max_pages") {
		t.Errorf("coverage finding should name max_pages: %q", findings[0].Title)
	}
}

func TestDescriptor(t *testing.T) {
	d := azureactivity.New().Descriptor()
	if d.Name != "olivares.azure-activity" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
	for _, fld := range d.ConfigFields {
		if (fld.Key == "client_secret" || fld.Key == "access_token") && !fld.Secret {
			t.Errorf("config field %q must be Secret", fld.Key)
		}
	}
}
