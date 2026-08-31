// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWriteStoreErrorAuditSpoolFull(t *testing.T) {
	w := httptest.NewRecorder()
	writeStoreError(w, fmt.Errorf("recording audit: %w", store.ErrAuditSpoolFull))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"message":"audit spool full"`) {
		t.Fatalf("body = %s, want audit spool full message", w.Body.String())
	}
}

func newDegradeRecorder(t *testing.T) (*Module, store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "recording-degrade.db")

	// Provision while the audit ledger is healthy. Tenant lifecycle mutations
	// require their own durable evidence; starving the spool before CreateOrg
	// would correctly test org.create refusal instead of the recording operation
	// each caller below is meant to exercise.
	bootstrapModule := New()
	bootstrap, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
	}, bootstrapModule.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := bootstrap.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		_ = bootstrap.Close()
		t.Fatal(err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}

	m := New()
	st, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	status, configured, err := st.(store.AuditSpoolStatuser).AuditSpoolStatus(ctx)
	if err != nil {
		t.Fatalf("degrade spool status: %v", err)
	}
	if !configured || status.MaxBytes != 1 || status.OnFull != store.AuditSpoolDegrade || !status.Engaged {
		t.Fatalf("degrade spool not engaged after reopen: configured=%v status=%+v", configured, status)
	}
	return m, st, tenant
}

func seedRecordingSession(t *testing.T, st store.Store, tenant model.TenantID, written, reserved int64) model.ID {
	t.Helper()
	ctx := context.Background()
	now := model.NewTimestamp(time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	var id model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colSubject: "user:u1", colSubjectKind: "user", colSubjectUser: "u1", colCred: "user:c1",
			colStatus: statusActive, colOpenedAt: now.String(), colLastAt: now.String(),
			colReserved: reserved, colWritten: written, colTipHash: "",
			colOpenSeq: int64(0), colAnchorSeq: int64(0), colSealSeq: int64(0),
			colConsentMode: "auto", colRetention: retentionClass, colOpenGuard: openGuard,
		})
		if err == nil {
			id = model.ID(rec.String(model.ColID))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRecordingOpenRefusesDegradeDrop(t *testing.T) {
	m, st, tenant := newDegradeRecorder(t)
	ctx := context.Background()
	p := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()}
	now := model.NewTimestamp(time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := m.openSession(ctx, sc, tenant, p, now, "auto")
		return err
	})
	if !errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("openSession error = %v, want ErrAuditSpoolFull", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err == nil && len(recs) != 0 {
			t.Fatalf("refused open committed %d session rows", len(recs))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingSealRefusesDegradeDrop(t *testing.T) {
	m, st, tenant := newDegradeRecorder(t)
	ctx := context.Background()
	id := seedRecordingSession(t, st, tenant, 0, 0)
	now := model.NewTimestamp(time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC))
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		return m.sealLocked(ctx, sc, rec, now, sealReasonClosed, "user:u1", "user")
	})
	if !errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("sealLocked error = %v, want ErrAuditSpoolFull", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err == nil && (rec.String(colStatus) != statusActive || rec.Int(colSealSeq) != 0) {
			t.Fatalf("refused seal committed status=%q seal_seq=%d", rec.String(colStatus), rec.Int(colSealSeq))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingPeriodicAnchorLogsDegradeDrop(t *testing.T) {
	m, st, tenant := newDegradeRecorder(t)
	var logs bytes.Buffer
	m.log = slog.New(slog.NewTextHandler(&logs, nil))
	ctx := context.Background()
	id := seedRecordingSession(t, st, tenant, anchorEvery-1, anchorEvery)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		call := api.RecordedCall{
			Tenant: tenant, Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()},
			Namespace: "governance", Method: http.MethodGet, Pattern: "/things", Permission: "governance:identity:read",
		}
		return m.appendFrame(ctx, sc, repo, rec, call, api.RecordedResult{Status: http.StatusOK},
			model.NewTimestamp(time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)))
	})
	if err != nil {
		t.Fatalf("appendFrame = %v, want best-effort success", err)
	}
	if !strings.Contains(logs.String(), "periodic anchor evidence dropped by the degrade spool policy (evidence gap)") {
		t.Fatalf("missing loud evidence-gap log: %s", logs.String())
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err == nil && (rec.Int(colWritten) != anchorEvery || rec.Int(colAnchorSeq) != 0) {
			t.Fatalf("session projection = written %d anchor_seq %d", rec.Int(colWritten), rec.Int(colAnchorSeq))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSemconvVersionPin pins the recording frame-schema semconv constants
//. semconvVersion is a documentation pin behind a mapping layer (never
// live coupling) and an unexported MIRROR of connectors/claude genAISemconvVersion
// and modules/observability otelGenAIVersion — all three must equal "1.41.1", the
// last VERSIONED GenAI vocabulary label. semconvUpstreamRepo/Ref mirror the
// semconv-genai authority re-verified 2026-07-05. Prior to this version had
// drifted to "1.41.0".
func TestSemconvVersionPin(t *testing.T) {
	const want = "1.41.1"
	if semconvVersion != want {
		t.Fatalf("semconvVersion = %q, want %q (mirror of genAISemconvVersion / otelGenAIVersion)", semconvVersion, want)
	}
	if semconvUpstreamRepo != "open-telemetry/semantic-conventions-genai" {
		t.Fatalf("semconvUpstreamRepo = %q", semconvUpstreamRepo)
	}
	if semconvUpstreamRef != "main@c321d7e, verified 2026-07-05" {
		t.Fatalf("semconvUpstreamRef = %q", semconvUpstreamRef)
	}
}

// The frame hash is deterministic, covers every field, and chains on prev.
func TestFrameHashDeterministicAndTotal(t *testing.T) {
	f := frameFields{
		Idx: 1, At: "2026-06-10T12:00:00Z", Actor: "user:u1", ActorKind: "user",
		Namespace: "governance", Method: "GET", Pattern: "/things",
		Perm: "governance:identity:read", Params: map[string]string{"id": "x"},
		QueryKeys: "limit", Status: 200, Outcome: "allowed", DurMS: 3,
	}
	h1 := frameHash("t1", "s1", f, zeroHash)
	h2 := frameHash("t1", "s1", f, zeroHash)
	if string(h1) != string(h2) {
		t.Fatal("hash must be deterministic")
	}
	g := f
	g.Outcome = "denied"
	if string(frameHash("t1", "s1", g, zeroHash)) == string(h1) {
		t.Fatal("hash must cover the outcome")
	}
	if string(frameHash("t1", "s1", f, h1)) == string(h1) {
		t.Fatal("hash must chain on prev")
	}
	if string(frameHash("t2", "s1", f, zeroHash)) == string(h1) {
		t.Fatal("hash must bind the tenant")
	}
}

// The redactor never lets an email, a credential shape, or an unbounded value
// through; ordinary identifiers pass.
func TestRedactParam(t *testing.T) {
	cases := map[string]string{
		"019eb239-59de-77ba-9c6d-a220819db969": "019eb239-59de-77ba-9c6d-a220819db969",
		"alice@example.com":                    redactedValue,
		"token=abc":                            redactedValue,
		"olvs_sel_secret":                      redactedValue,
		"eyJhbGciOiJIUzI1NiJ9":                 redactedValue,
		"Bearer abc":                           redactedValue,
		"-----BEGIN PRIVATE KEY-----":          redactedValue,
		"https://u:p@host/x":                   redactedValue,
	}
	for in, want := range cases {
		if got := redactParam(in); got != want {
			t.Fatalf("redactParam(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 300)
	if got := redactParam(long); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("overlong value must digest, got %q", got)
	}
}

// Query keys are bounded in count, key length and total size.
func TestBoundedQueryKeys(t *testing.T) {
	keys := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		keys = append(keys, strings.Repeat("k", 80))
	}
	out := boundedQueryKeys(keys)
	if len(out) > 600 || !strings.HasSuffix(out, ",…") {
		t.Fatalf("query keys must be bounded and marked truncated, got %d bytes", len(out))
	}
	if got := boundedQueryKeys([]string{"a", "b"}); got != "a,b" {
		t.Fatalf("simple keys = %q", got)
	}
}

// outcomeOf classifies statuses for the human-readable timeline.
func TestOutcomeOf(t *testing.T) {
	for status, want := range map[int]string{200: "allowed", 201: "allowed", 401: "denied", 403: "denied", 409: "rejected", 500: "error"} {
		if got := outcomeOf(status); got != want {
			t.Fatalf("outcomeOf(%d) = %q, want %q", status, got, want)
		}
	}
}
