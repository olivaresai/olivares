// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package datasourceacl_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/datasourceacl"
)

type callbackWatch struct {
	change datasourceacl.ACLChange
	cfg    datasourceacl.WatchConfig
	calls  int
}

func (w *callbackWatch) StartWatch(
	ctx context.Context,
	_ contentsource.Source,
	cfg datasourceacl.WatchConfig,
	cb datasourceacl.Callback,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.calls++
	w.cfg = cfg
	cb(ctx, w.change)
	return nil
}

func (*callbackWatch) Stop() error { return nil }

var _ datasourceacl.LiveACLSyncer = (*callbackWatch)(nil)

type fixedClassifier struct {
	labels    []string
	listed    []datasourceacl.PurviewLabel
	lastDocID string
	calls     int
}

func (c *fixedClassifier) Classify(ctx context.Context, docID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	c.lastDocID = docID
	return c.labels, nil
}

func (c *fixedClassifier) ListLabels(ctx context.Context) ([]datasourceacl.PurviewLabel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	return c.listed, nil
}

var _ datasourceacl.PurviewClassifier = (*fixedClassifier)(nil)

func TestLiveACLSyncerMethodSetDoesNotWeaken(t *testing.T) {
	assertInterfaceMethods(t, reflect.TypeOf((*datasourceacl.LiveACLSyncer)(nil)).Elem(),
		"StartWatch", "Stop")
}

func TestPurviewClassifierMethodSetDoesNotWeaken(t *testing.T) {
	assertInterfaceMethods(t, reflect.TypeOf((*datasourceacl.PurviewClassifier)(nil)).Elem(),
		"Classify", "ListLabels")
}

// The package is an interface-and-DTO seam, not a production watcher. These
// fixtures exercise what an external implementation can observe through that
// seam; enforcement behavior belongs to the implementation that supplies it.
func TestLiveACLContractCarriesChangeAndContext(t *testing.T) {
	detectedAt := time.Date(2026, time.August, 22, 9, 15, 0, 0, time.UTC)
	want := datasourceacl.ACLChange{
		SourceKind:     contentsource.SourceSharePoint,
		DocID:          "site-7/doc-42",
		ACL:            []string{"group:finance", "user:alice"},
		ExternalLabels: []string{"purview:highly-confidential"},
		Classification: "confidential",
		DetectedAt:     detectedAt,
	}
	wantConfig := datasourceacl.WatchConfig{
		PollInterval:  30 * time.Second,
		MaxBackoff:    5 * time.Minute,
		CredentialRef: "secret://purview/watch",
	}
	watcher := &callbackWatch{change: want}
	ctx := context.WithValue(context.Background(), callbackContextKey{}, "watch-7")

	var got datasourceacl.ACLChange
	var gotContextValue string
	err := watcher.StartWatch(ctx, nil, wantConfig, func(callbackCtx context.Context, change datasourceacl.ACLChange) {
		gotContextValue, _ = callbackCtx.Value(callbackContextKey{}).(string)
		got = change
	})
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	if watcher.calls != 1 {
		t.Fatalf("callback dispatches = %d, want 1", watcher.calls)
	}
	if !reflect.DeepEqual(watcher.cfg, wantConfig) {
		t.Fatalf("watch config = %#v, want %#v", watcher.cfg, wantConfig)
	}
	if gotContextValue != "watch-7" {
		t.Fatalf("callback context value = %q, want watch-7", gotContextValue)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callback change = %#v, want %#v", got, want)
	}
}

func TestLiveACLContractCancelledContextDoesNotFire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	watcher := new(callbackWatch)
	callbackCalls := 0

	err := watcher.StartWatch(ctx, nil, datasourceacl.WatchConfig{}, func(context.Context, datasourceacl.ACLChange) {
		callbackCalls++
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartWatch error = %v, want context.Canceled", err)
	}
	if watcher.calls != 0 || callbackCalls != 0 {
		t.Fatalf("canceled watch fired: watcher=%d callback=%d", watcher.calls, callbackCalls)
	}
}

func TestACLChangeSourceKindKeepsContentSourceType(t *testing.T) {
	field, ok := reflect.TypeOf(datasourceacl.ACLChange{}).FieldByName("SourceKind")
	if !ok {
		t.Fatal("ACLChange.SourceKind is absent")
	}
	want := reflect.TypeOf(contentsource.SourceKind(""))
	if field.Type != want {
		t.Fatalf("ACLChange.SourceKind type = %v, want %v", field.Type, want)
	}
}

func TestPurviewLabelPriorityKeepsIntType(t *testing.T) {
	field, ok := reflect.TypeOf(datasourceacl.PurviewLabel{}).FieldByName("Priority")
	if !ok {
		t.Fatal("PurviewLabel.Priority is absent")
	}
	want := reflect.TypeOf(int(0))
	if field.Type != want {
		t.Fatalf("PurviewLabel.Priority type = %v, want %v", field.Type, want)
	}
}

func TestPurviewContractCarriesLabelsAndHonorsCancellation(t *testing.T) {
	classifier := &fixedClassifier{
		labels: []string{"purview:highly-confidential"},
		listed: []datasourceacl.PurviewLabel{{
			ID:       "label-1",
			Name:     "Highly Confidential",
			Tooltip:  "Restricted to the finance group",
			Priority: 1,
		}},
	}
	var contract datasourceacl.PurviewClassifier = classifier

	got, err := contract.Classify(context.Background(), "doc-42")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !reflect.DeepEqual(got, classifier.labels) {
		t.Fatalf("Classify labels = %v, want %v", got, classifier.labels)
	}
	if classifier.lastDocID != "doc-42" {
		t.Fatalf("Classify docID = %q, want doc-42", classifier.lastDocID)
	}
	listed, err := contract.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if !reflect.DeepEqual(listed, classifier.listed) {
		t.Fatalf("ListLabels = %#v, want %#v", listed, classifier.listed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	callsBefore := classifier.calls
	if labels, err := contract.ListLabels(ctx); !errors.Is(err, context.Canceled) || labels != nil {
		t.Fatalf("canceled ListLabels = %v, %v; want nil, context.Canceled", labels, err)
	}
	if classifier.calls != callsBefore {
		t.Fatalf("canceled ListLabels reached fixture behavior: calls %d -> %d", callsBefore, classifier.calls)
	}
}

func TestPurviewConfigCarriesTenantAndCredentialReference(t *testing.T) {
	want := datasourceacl.PurviewConfig{
		Endpoint:      "https://purview.example.test",
		TenantID:      "tenant-a",
		CredentialRef: "secret://tenant-a/purview",
	}
	got := datasourceacl.PurviewConfig{
		Endpoint:      want.Endpoint,
		TenantID:      want.TenantID,
		CredentialRef: want.CredentialRef,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PurviewConfig = %#v, want %#v", got, want)
	}
}

type callbackContextKey struct{}

func assertInterfaceMethods(t *testing.T, contract reflect.Type, want ...string) {
	t.Helper()
	if contract.NumMethod() != len(want) {
		t.Fatalf("%s method count = %d, want %d (%v)", contract, contract.NumMethod(), len(want), want)
	}
	for _, name := range want {
		if _, ok := contract.MethodByName(name); !ok {
			t.Errorf("%s is missing required method %s", contract, name)
		}
	}
}
