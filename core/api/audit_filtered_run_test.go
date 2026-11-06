// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// GET /v1/audit?tool= and ?folder= (#2750): the Ledger query's tool: and folder:
// tokens. An entry has neither; both are facts of the sessions.run it targets (or
// of the approval that run names), so the handler hands them to the store.

func TestParseAuditFiltersToolAndFolder(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?tool=codex&folder="+url.QueryEscape("/work/a"), nil)
	filters, filtered, err := parseAuditFilters(req)
	if err != nil || !filtered {
		t.Fatalf("parse: filtered=%v err=%v", filtered, err)
	}
	got := filters.storeFilter()
	if got.Tool != "codex" || got.Folder != "/work/a" {
		t.Fatalf("store filter = %+v, want tool codex and folder /work/a", got)
	}

	// A cleared form field is "no filter", never "match nothing": it must not flip
	// the request onto the filtered path (same rule as ?actor=).
	blank := httptest.NewRequest(http.MethodGet, "/?tool=&folder=%20", nil)
	if _, filtered, err := parseAuditFilters(blank); err != nil || filtered {
		t.Fatalf("blank tool/folder: filtered=%v err=%v, want unfiltered", filtered, err)
	}
}

type auditRunFixture struct {
	st     store.Store
	tenant model.TenantID
	// seq of each seeded entry, by name.
	seq map[string]int64
}

func registerAuditRunFacts(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  "sessions.run",
		Table: "sessions_run",
		Fields: []model.FieldSpec{
			{Name: "run_ref", Kind: model.KindText},
			{Name: "provider_driver", Kind: model.KindText, Nullable: true},
			{Name: "workspace_path", Kind: model.KindText, Nullable: true},
			{Name: "approval_ref", Kind: model.KindText, Nullable: true},
		},
	})
}

func newAuditRunFixture(t *testing.T, register func(store.ExtensionRegistry) error) auditRunFixture {
	t.Helper()
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, register)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := auditRunFixture{st: st, seq: map[string]int64{}}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "ar", Slug: "ar", Status: model.StatusActive})
		f.tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if register == nil {
		return f
	}
	approval := model.NewID().String()
	if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		runs, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		mk := func(ref, tool, folder, approvalRef string) (model.ID, error) {
			rec := model.Record{"run_ref": ref, "provider_driver": tool, "workspace_path": folder}
			if approvalRef != "" {
				rec["approval_ref"] = approvalRef
			}
			created, err := runs.Create(ctx, rec)
			return model.ID(created.String(model.ColID)), err
		}
		claude, err := mk("run-claude", "claude", "/work/a", "")
		if err != nil {
			return err
		}
		codexA, err := mk("run-codex-a", "codex", "/work/a", approval)
		if err != nil {
			return err
		}
		codexB, err := mk("run-codex-b", "codex", "/work/b", "")
		if err != nil {
			return err
		}
		for _, e := range []struct {
			name, action string
			kind         model.Kind
			target       model.ID
		}{
			{"claude", "sessions.run.launch", "sessions.run", claude},
			{"codex-a", "sessions.run.launch", "sessions.run", codexA},
			{"approval", "governance.approval.decision", "governance.approval", model.ID(approval)},
			// Between two matches of tool=codex, for the paging across a gap.
			{"mid", "policy.updated", "core.policy", ""},
			{"codex-b", "sessions.run.launch", "sessions.run", codexB},
			{"other", "policy.updated", "core.policy", ""},
		} {
			ev, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: "user:marc", Action: e.action, TargetKind: e.kind, TargetID: e.target})
			if err != nil {
				return err
			}
			f.seq[e.name] = ev.Seq
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f auditRunFixture) listPage(t *testing.T, query string, from int64, limit int) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit?"+query, nil)
	filters, filtered, err := parseAuditFilters(req)
	if err != nil || !filtered {
		t.Fatalf("parse %q: filtered=%v err=%v", query, filtered, err)
	}
	rec := httptest.NewRecorder()
	srv := &Server{st: f.st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.auditFilteredListInto(rec, req, auth.Principal{}, f.tenant, from, limit, filters)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v (%s)", query, err, rec.Body.String())
	}
	return rec.Code, body
}

func itemSeqs(body map[string]any) []int64 {
	out := []int64{}
	items, _ := body["items"].([]any)
	for _, it := range items {
		out = append(out, int64(it.(map[string]any)["seq"].(float64)))
	}
	return out
}

func TestAuditListByToolAndFolder(t *testing.T) {
	f := newAuditRunFixture(t, registerAuditRunFacts)
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"tool=codex", []string{"codex-a", "approval", "codex-b"}},
		{"tool=claude", []string{"claude"}},
		{"folder=" + url.QueryEscape("/work/a"), []string{"claude", "codex-a", "approval"}},
		{"tool=codex&folder=" + url.QueryEscape("/work/b"), []string{"codex-b"}},
		{"tool=gemini", nil},
	} {
		code, body := f.listPage(t, c.query, 1, 100)
		if code != http.StatusOK {
			t.Fatalf("%s = %d %v", c.query, code, body)
		}
		want := []int64{}
		for _, n := range c.want {
			want = append(want, f.seq[n])
		}
		if got := itemSeqs(body); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s items = %v, want %v", c.query, got, want)
		}
		if body["scan_complete"] != true {
			t.Fatalf("%s: a finished scan must say so: %v", c.query, body)
		}
	}
}

// The keyset paging and next_from scan are the ones every other filter uses.
func TestAuditListByToolPagesLikeTheOtherFilters(t *testing.T) {
	f := newAuditRunFixture(t, registerAuditRunFacts)
	code, page := f.listPage(t, "tool=codex", 1, 2)
	if code != http.StatusOK {
		t.Fatalf("list = %d %v", code, page)
	}
	if got, want := itemSeqs(page), []int64{f.seq["codex-a"], f.seq["approval"]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first page = %v, want %v", got, want)
	}
	next := int64(page["next_from"].(float64))
	if page["has_more"] != true || next != f.seq["approval"]+1 {
		t.Fatalf("first page must continue after its last match: %v", page)
	}
	// The continuation is what a client sends back as ?from.
	code, page = f.listPage(t, "tool=codex", next, 2)
	if code != http.StatusOK {
		t.Fatalf("second page = %d %v", code, page)
	}
	if got, want := itemSeqs(page), []int64{f.seq["codex-b"]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second page = %v, want %v", got, want)
	}
	if page["has_more"] != false || page["scan_complete"] != true {
		t.Fatalf("the last page must say the scan is complete: %v", page)
	}
}

func TestAuditPagesAcrossNonMatchingEntries(t *testing.T) {
	f := newAuditRunFixture(t, registerAuditRunFacts)
	// One match per page, with a non-matching entry between two matches: no match
	// may be skipped or repeated, and every page continues where the last one ended.
	var got []int64
	from := int64(1)
	for page := 0; page < 6; page++ {
		code, body := f.listPage(t, "tool=codex", from, 1)
		if code != http.StatusOK {
			t.Fatalf("page %d = %d %v", page, code, body)
		}
		got = append(got, itemSeqs(body)...)
		if body["has_more"] != true {
			break
		}
		from = int64(body["next_from"].(float64))
	}
	want := []int64{f.seq["codex-a"], f.seq["approval"], f.seq["codex-b"]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
}

// A value given twice has no single meaning: taking the first would let
// ?tool=&tool=codex read as "cleared" and answer with the whole ledger, and
// ?tool=claude&tool=codex answer for claude alone. Both are refused by name.
func TestAuditListRefusesARepeatedToolOrFolder(t *testing.T) {
	f := newAuditRunFixture(t, registerAuditRunFacts)
	for _, q := range []string{
		"tool=claude&tool=codex",
		"tool=&tool=codex",
		"folder=" + url.QueryEscape("/work/a") + "&folder=" + url.QueryEscape("/work/b"),
		"folder=&folder=" + url.QueryEscape("/work/a"),
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/audit?"+q, nil)
		rec := httptest.NewRecorder()
		srv := &Server{st: f.st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		srv.auditListInto(rec, req, auth.Principal{}, f.tenant)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s, want 400", q, rec.Code, rec.Body.String())
		}
	}
}

// The system ledger records no sessions. A tool or folder asked of it would come
// back as an empty page that reads "nothing happened": it is refused instead.
func TestAuditListByToolRefusesTheSystemLedger(t *testing.T) {
	f := newAuditRunFixture(t, registerAuditRunFacts)
	for _, q := range []string{"tool=codex", "folder=" + url.QueryEscape("/work/a")} {
		req := httptest.NewRequest(http.MethodGet, "/v1/audit/system?"+q, nil)
		rec := httptest.NewRecorder()
		srv := &Server{st: f.st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		srv.auditListInto(rec, req, auth.Principal{}, model.SystemTenantID)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("system %s = %d %s, want 400", q, rec.Code, rec.Body.String())
		}
	}
}

// An AuditLog without the filtered walk cannot join the run a tool or folder
// belongs to. The legacy walk must refuse rather than return every entry as if it
// matched. The sqlstore always offers the filtered walk, so the legacy path is
// called directly here, on the real store.
func TestAuditLegacyWalkRefusesToolAndFolder(t *testing.T) {
	f := newAuditRunFixture(t, registerAuditRunFacts)
	for _, q := range []string{"tool=codex", "folder=" + url.QueryEscape("/work/a")} {
		req := httptest.NewRequest(http.MethodGet, "/v1/audit?"+q, nil)
		filters, _, err := parseAuditFilters(req)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		srv := &Server{st: f.st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		srv.auditFilteredListLegacy(rec, req, auth.Principal{}, f.tenant, 1, 100, filters)
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "audit_filter_unavailable") {
			t.Fatalf("%s = %d %s, want 501 audit_filter_unavailable", q, rec.Code, rec.Body.String())
		}
	}
}

// What the reader asked for is part of the evidence of the read.
func TestAuditListByToolIsRecordedInTheSelfAudit(t *testing.T) {
	f := newAuditRunFixture(t, registerAuditRunFacts)
	if code, body := f.listPage(t, "tool=codex&folder="+url.QueryEscape("/work/a"), 1, 100); code != http.StatusOK {
		t.Fatalf("list = %d %v", code, body)
	}
	var filters map[string]any
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		// Walk drops Meta by design; the canonical walk carries the stored bytes.
		return sc.Audit().(store.CanonicalWalker).WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != "audit.read" {
				return nil
			}
			var decoded struct {
				Filters map[string]any `json:"filters"`
			}
			if err := json.Unmarshal([]byte(meta), &decoded); err != nil {
				return err
			}
			filters = decoded.Filters
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if filters["tool"] != "codex" || filters["folder"] != "/work/a" {
		t.Fatalf("audit.read filters = %v, want tool and folder", filters)
	}
}

// A build without the sessions module cannot resolve a tool. It says so (501),
// rather than answering "no entries" or ignoring the filter.
func TestAuditListByToolRefusesWhereSessionsAreNotRegistered(t *testing.T) {
	f := newAuditRunFixture(t, nil)
	for _, q := range []string{"tool=codex", "folder=" + url.QueryEscape("/work/a")} {
		code, body := f.listPage(t, q, 1, 100)
		if code != http.StatusNotImplemented {
			t.Fatalf("%s = %d %v, want 501", q, code, body)
		}
		if e, _ := body["error"].(map[string]any); e["code"] != "audit_filter_unavailable" {
			t.Fatalf("%s error = %v, want audit_filter_unavailable", q, body)
		}
	}
}
