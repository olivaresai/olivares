// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuditFilteredListReportsScanCapHonestly(t *testing.T) {
	previousCap := auditScanCap
	auditScanCap = 2
	t.Cleanup(func() { auditScanCap = previousCap })

	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite,
		DSN:    ":memory:",
		Debug:  true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var firstSeq int64
	if err := st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		for i := 0; i < 3; i++ {
			event, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor:  "scan-cap",
				Action: "event." + strconv.Itoa(i),
			})
			if err != nil {
				return err
			}
			if i == 0 {
				firstSeq = event.Seq
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/?action=does-not-match", nil)
	filters, filtered, err := parseAuditFilters(req)
	if err != nil || !filtered {
		t.Fatalf("parse filters: filtered=%v err=%v", filtered, err)
	}
	rec := httptest.NewRecorder()
	server := &Server{st: st}
	server.auditFilteredListInto(
		rec,
		req,
		auth.Principal{},
		model.SystemTenantID,
		firstSeq,
		100,
		filters,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list = %d %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["scan_complete"] != false || body["has_more"] != true {
		t.Fatalf("honesty fields = %#v", body)
	}
	if got, ok := body["next_from"].(float64); !ok || int64(got) != firstSeq+2 {
		t.Fatalf("next_from = %#v, want %d", body["next_from"], firstSeq+2)
	}
}

// TestAuditHeadSeqReportsTheChainTip pins the two values the head_seq contract
// names by number: the tip after N appends, and 0 for an empty ledger.
//
// The empty case is measured HERE and not over HTTP for a reason worth writing
// down: every tenant reachable through the API is born with an event. CreateOrg
// appends org.create in the provisioning transaction and says so
// (core/internal/store/sqlstore/system.go: "a new tenant's audit chain still
// starts at seq 1 with org.create"), and EnsureSystemTenant seals system.genesis
// at seq 1 the same way — measured, not assumed. So no HTTP request can produce an
// empty chain, and an HTTP test of the empty case would be testing a state that
// cannot occur. A tenant nobody has written to yet is that state, and the store
// serves it, so the whole chain below is measured on one: 0, then N appends, then
// the tip.
func TestAuditHeadSeqReportsTheChainTip(t *testing.T) {
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite,
		DSN:    ":memory:",
		Debug:  true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A tenant nobody has written to. NOT the system tenant, which EnsureSystemTenant
	// above already sealed system.genesis into at seq 1 — starting there would measure
	// 1 for "empty" and the assertion below would have to be weakened to hide it.
	tenant := model.TenantID(model.NewID().String())

	headSeq := func() int64 {
		t.Helper()
		var seq int64
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			var herr error
			seq, herr = auditHeadSeq(ctx, sc)
			return herr
		}); err != nil {
			t.Fatalf("read head: %v", err)
		}
		return seq
	}

	if got := headSeq(); got != 0 {
		t.Fatalf("head_seq on an empty ledger = %d, want 0", got)
	}

	const appends = 4
	var lastSeq int64
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := range appends {
			event, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor:  "head-seq",
				Action: "event." + strconv.Itoa(i),
			})
			if err != nil {
				return err
			}
			lastSeq = event.Seq
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if lastSeq != appends {
		t.Fatalf("appended tip = %d, want %d (the chain must start at 1)", lastSeq, appends)
	}
	if got := headSeq(); got != lastSeq {
		t.Fatalf("head_seq after %d appends = %d, want the tip %d", appends, got, lastSeq)
	}
}

// auditOnlyScope is a store.Scope that answers exactly one question. The embedded
// interface is nil on purpose: auditHeadSeq is documented to touch nothing but the
// ledger, and any other call panics loudly instead of silently reading a zero value.
type auditOnlyScope struct {
	store.Scope
	log store.AuditLog
}

func (s auditOnlyScope) Audit() store.AuditLog { return s.log }

// visibleHeadAuditLog is an AuditLog that can report the tip it can SEE and
// nothing else — deliberately WITHOUT RecordedHead, which is what makes it the
// control for the fallback branch.
type visibleHeadAuditLog struct {
	head    store.HeadRef
	hasHead bool
}

func (l visibleHeadAuditLog) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	panic("auditHeadSeq must not append")
}

func (l visibleHeadAuditLog) Verify(context.Context, int64) (store.VerifyReport, error) {
	panic("auditHeadSeq must not verify")
}

func (l visibleHeadAuditLog) Walk(context.Context, int64, func(model.AuditEvent) error) error {
	panic("auditHeadSeq must not walk")
}

func (l visibleHeadAuditLog) Head(context.Context) (store.HeadRef, bool, error) {
	return l.head, l.hasHead, nil
}

// recordedHeadAuditLog adds the OPTIONAL capability on top, so the two branches of
// auditHeadSeq differ by exactly one method and by nothing else.
type recordedHeadAuditLog struct {
	visibleHeadAuditLog
	recorded store.HeadRef
	hasRec   bool
}

func (l recordedHeadAuditLog) RecordedHead(context.Context) (store.HeadRef, bool, error) {
	return l.recorded, l.hasRec, nil
}

// TestAuditHeadSeqPrefersTheRecordedHead proves the two things a reader of
// head_seq is entitled to assume, and the second is the one a passing test could
// otherwise fake: the value comes from audit_heads (store.RecordedHeadReader) when
// the store keeps one, and from the last surviving event when it does not.
//
// The two are indistinguishable on every healthy ledger, so the recorded-head case
// is measured on the one ledger where they disagree — events gone under a live
// head. A test built on agreeing values would pass with either source wired up and
// would therefore prove nothing about which one is read.
func TestAuditHeadSeqPrefersTheRecordedHead(t *testing.T) {
	ctx := context.Background()

	// Control on the fixtures themselves: the capability must be present on one and
	// ABSENT on the other, or both cases below exercise the same branch and the
	// whole test is vacuous.
	var withCap store.AuditLog = recordedHeadAuditLog{}
	var withoutCap store.AuditLog = visibleHeadAuditLog{}
	if _, ok := withCap.(store.RecordedHeadReader); !ok {
		t.Fatal("fixture control: recordedHeadAuditLog must satisfy store.RecordedHeadReader")
	}
	if _, ok := withoutCap.(store.RecordedHeadReader); ok {
		t.Fatal("fixture control: visibleHeadAuditLog must NOT satisfy store.RecordedHeadReader")
	}

	tests := []struct {
		name string
		log  store.AuditLog
		want int64
	}{
		{
			// A ledger emptied under a live head: Head sees nothing, audit_heads still
			// records 50. head_seq answers 50 — how far the chain has gone.
			name: "recorded head wins over the visible one",
			log: recordedHeadAuditLog{
				visibleHeadAuditLog: visibleHeadAuditLog{hasHead: false},
				recorded:            store.HeadRef{Seq: 50}, hasRec: true,
			},
			want: 50,
		},
		{
			name: "no recorded head means an empty ledger",
			log: recordedHeadAuditLog{
				visibleHeadAuditLog: visibleHeadAuditLog{head: store.HeadRef{Seq: 7}, hasHead: true},
				hasRec:              false,
			},
			want: 0,
		},
		{
			name: "a store without the capability falls back to the visible tip",
			log:  visibleHeadAuditLog{head: store.HeadRef{Seq: 7}, hasHead: true},
			want: 7,
		},
		{
			name: "no capability and no events is still 0",
			log:  visibleHeadAuditLog{hasHead: false},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := auditHeadSeq(ctx, auditOnlyScope{log: tt.log})
			if err != nil {
				t.Fatalf("auditHeadSeq: %v", err)
			}
			if got != tt.want {
				t.Fatalf("head_seq = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAuditListServesHeadSeqZeroOverTheWire is the one assertion that makes
// "head_seq is ALWAYS present" a measured property instead of a promise.
//
// Every other test in this file and in the HTTP suite reads a ledger with events,
// so every head_seq they see is positive — and `omitempty` on the struct tag would
// leave all of them green while silently dropping the field from exactly the
// response a client cannot interpret without it. An absent key and a key worth 0
// are the same value to a JavaScript reader (`data.head_seq ?? 0`) only by luck;
// to a typed client they are "required field missing".
//
// It goes through the handler rather than through auditHeadSeq because the claim
// is about the WIRE, and it uses a tenant nobody has written to because that is
// the only empty ledger the engine can produce (an HTTP-reachable tenant is born
// with an event — see TestAuditHeadSeqReportsTheChainTip).
func TestAuditListServesHeadSeqZeroOverTheWire(t *testing.T) {
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite,
		DSN:    ":memory:",
		Debug:  true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	tenant := model.TenantID(model.NewID().String())
	rec := httptest.NewRecorder()
	server := &Server{st: st}
	server.auditListInto(rec, httptest.NewRequest(http.MethodGet, "/?limit=10", nil), auth.Principal{}, tenant)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty-ledger list = %d %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	head, present := body["head_seq"]
	if !present {
		t.Fatalf("head_seq is ABSENT from an empty-ledger response — it is required and 0 is a value, not a reason to omit it: %s", rec.Body.String())
	}
	if got, ok := head.(float64); !ok || got != 0 {
		t.Fatalf("head_seq = %#v, want 0 on a ledger with no recorded head", head)
	}
	// The empty page is still the empty page: head_seq 0 must not have come from a
	// response that failed to read anything at all.
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("items = %#v, want an empty array beside head_seq 0", body["items"])
	}
}
