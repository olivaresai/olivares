// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const auditFixture = "audit_log.jsonl"

// auditStubDoer serves the entity-resolution GETs: it queues programmed JSON
// responses per entity id (each GET pops the head; the LAST entry repeats, so
// single-programmed tests behave as before and a test can program 500-then-200
// to exercise the transient-retry path), records every request, and fails the
// test on anything that is not a GET /v1/identity/entity/id/{id} — the audit
// connector has no other legitimate network surface.
type auditEntityResp struct {
	status int
	body   string
}

type auditStubDoer struct {
	t         *testing.T
	responses map[string][]auditEntityResp
	reqs      []*http.Request
}

func newAuditStub(t *testing.T) *auditStubDoer {
	t.Helper()
	return &auditStubDoer{t: t, responses: map[string][]auditEntityResp{}}
}

func (d *auditStubDoer) onEntity(id string, status int, body string) {
	d.responses[id] = append(d.responses[id], auditEntityResp{status, body})
}

func (d *auditStubDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if req.Method != http.MethodGet || !strings.HasPrefix(req.URL.Path, "/v1/identity/entity/id/") {
		d.t.Fatalf("unexpected request: %s %s (audit source may only GET entity details)", req.Method, req.URL.String())
	}
	id := strings.TrimPrefix(req.URL.Path, "/v1/identity/entity/id/")
	queue, ok := d.responses[id]
	if !ok || len(queue) == 0 {
		d.t.Fatalf("no programmed response for entity %q", id)
	}
	r := queue[0]
	if len(queue) > 1 {
		d.responses[id] = queue[1:]
	}
	return &http.Response{
		StatusCode: r.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Request:    req,
	}, nil
}

func openAudit(t *testing.T, doer *auditStubDoer, settings map[string]string) *AuditSource {
	t.Helper()
	if _, ok := settings["path"]; !ok {
		settings["path"] = filepath.Join("testdata", auditFixture)
	}
	s := NewAudit()
	if doer != nil {
		s.doer = doer
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func auditEdges(t *testing.T, sink *captureSink) []model.EdgeObservation {
	t.Helper()
	out := make([]model.EdgeObservation, 0, len(sink.obs))
	for _, o := range sink.obs {
		e, ok := o.(model.EdgeObservation)
		if !ok {
			t.Fatalf("non-edge observation emitted: %T", o)
		}
		out = append(out, e)
	}
	return out
}

// findEdge returns the first edge whose ResourceRef matches, or fails.
func findEdge(t *testing.T, es []model.EdgeObservation, resourceRef string) model.EdgeObservation {
	t.Helper()
	for _, e := range es {
		if e.ResourceRef == resourceRef {
			return e
		}
	}
	t.Fatalf("no edge for resource %q in %+v", resourceRef, es)
	return model.EdgeObservation{}
}

// ---------------------------------------------------------------------------
// Golden-fixture mapping (no resolution)
// ---------------------------------------------------------------------------

func TestAuditGatherGoldenFixture(t *testing.T) {
	s := openAudit(t, nil, map[string]string{}) // defaults: secret_mounts_only=true, follow=false
	sink := &captureSink{}
	// follow=false: Gather must end at EOF and return nil (this test would hang
	// otherwise).
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	es := auditEdges(t, sink)

	// 5 completed, secret-mount, attributable accesses survive the fixture:
	// read+update+list on secret/* (entity), kv/data/legacy via the prefix
	// fallback (entity), and the root-token read (display_name fallback).
	// Skipped: request entry, errored response, unknown op (renew), auth
	// mount_class, sys/ + auth/ prefix fallback, no-identity line, non-JSON
	// line, unparseable time, empty path.
	if len(es) != 5 {
		t.Fatalf("edges = %d, want 5: %+v", len(es), es)
	}

	read := findEdge(t, es, "secret/data/app")
	if read.Mode != model.ModeRead {
		// the first secret/data/app edge in fixture order is the read
		t.Errorf("read edge mode = %q, want read", read.Mode)
	}
	if read.OriginKind != "identity" || read.OriginRef != "entity:ent-uuid-1" {
		t.Errorf("read edge origin = %q/%q, want identity/entity:ent-uuid-1", read.OriginKind, read.OriginRef)
	}
	if read.Confidence != model.ConfidenceApproximate {
		t.Errorf("unresolved entity confidence = %q, want approximate", read.Confidence)
	}
	if read.Source != SignalVaultAudit {
		t.Errorf("source = %q, want vault_audit", read.Source)
	}
	if read.ResourceKind != "vault.path" {
		t.Errorf("resourceKind = %q, want vault.path (the SignalPolicy diff space)", read.ResourceKind)
	}
	// RFC3339 with fractional seconds (the real audit format) must parse.
	if want := time.Date(2026, 6, 11, 8, 0, 0, 123456000, time.UTC); !read.ObservedAt.Equal(want) {
		t.Errorf("observedAt = %v, want %v", read.ObservedAt, want)
	}

	// update → write (the second secret/data/app edge).
	var update model.EdgeObservation
	for _, e := range es {
		if e.ResourceRef == "secret/data/app" && e.Mode == model.ModeWrite {
			update = e
		}
	}
	if update.OriginRef != "entity:ent-uuid-1" {
		t.Errorf("update→write edge missing or mis-attributed: %+v", es)
	}

	// list → read.
	list := findEdge(t, es, "secret/metadata/")
	if list.Mode != model.ModeRead {
		t.Errorf("list edge mode = %q, want read", list.Mode)
	}

	// Older-Vault line without mount_class on a non-system path passes the
	// prefix fallback.
	legacy := findEdge(t, es, "kv/data/legacy")
	if legacy.OriginRef != "entity:ent-uuid-2" || legacy.Mode != model.ModeRead {
		t.Errorf("legacy edge = %+v", legacy)
	}

	// display_name fallback: no entity_id → token:<display_name>, approximate.
	root := findEdge(t, es, "secret/data/breakglass")
	if root.OriginRef != "token:root" || root.Confidence != model.ConfidenceApproximate {
		t.Errorf("root-token edge = %+v, want token:root approximate", root)
	}

	// Nothing from the skipped lines.
	for _, e := range es {
		for _, banned := range []string{"secret/data/forbidden", "auth/token/lookup-self", "sys/health", "auth/approle/login", "secret/data/anonymous", "secret/data/badtime"} {
			if e.ResourceRef == banned {
				t.Errorf("skipped line was emitted: %+v", e)
			}
		}
	}
}

func TestAuditSecretMountsOnlyOff(t *testing.T) {
	s := openAudit(t, nil, map[string]string{"secret_mounts_only": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	es := auditEdges(t, sink)
	// The 5 golden edges + auth/token/lookup-self (read), sys/health (read) and
	// auth/approle/login (update by token:approle) now pass.
	if len(es) != 8 {
		t.Fatalf("edges = %d, want 8 with the mount filter off: %+v", len(es), es)
	}
	login := findEdge(t, es, "auth/approle/login")
	if login.Mode != model.ModeWrite || login.OriginRef != "token:approle" {
		t.Errorf("login edge = %+v", login)
	}
	if e := findEdge(t, es, "sys/health"); e.Mode != model.ModeRead {
		t.Errorf("sys/health edge = %+v", e)
	}
}

// ---------------------------------------------------------------------------
// HMAC'd sensitive fields never surface
// ---------------------------------------------------------------------------

func TestAuditHMACValuesNeverEmitted(t *testing.T) {
	s := openAudit(t, nil, map[string]string{"secret_mounts_only": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) == 0 {
		t.Fatal("fixture emitted nothing; the assertion would be vacuous")
	}
	for _, e := range auditEdges(t, sink) {
		for _, f := range []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
			string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef} {
			if strings.Contains(f, "hmac-sha256") {
				t.Errorf("HMAC'd audit value leaked into emitted field %q", f)
			}
			if strings.Contains(f, "digest") { // every fixture HMAC carries "digest"
				t.Errorf("sensitive digest material leaked into emitted field %q", f)
			}
		}
		if e.Labels != nil {
			t.Errorf("audit edges carry no labels, got %v", e.Labels)
		}
	}
}

// ---------------------------------------------------------------------------
// Entity resolution (cached, best-effort)
// ---------------------------------------------------------------------------

func TestAuditEntityResolutionCachedOneGetPerID(t *testing.T) {
	d := newAuditStub(t)
	d.onEntity("ent-uuid-1", 200, `{"data":{"id":"ent-uuid-1","name":"deploy"}}`)
	d.onEntity("ent-uuid-2", 404, `{"errors":[]}`)
	s := openAudit(t, d, map[string]string{
		"resolve_entities": "true",
		"base_url":         "https://vault.example:8200",
		"token":            testToken,
		"namespace":        "team-a",
	})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	es := auditEdges(t, sink)
	if len(es) != 5 {
		t.Fatalf("edges = %d, want 5", len(es))
	}

	// ent-uuid-1 appears on 3 emitted lines but must cost exactly ONE GET;
	// ent-uuid-2's failed lookup is negative-cached: 2 GETs total.
	if len(d.reqs) != 2 {
		t.Fatalf("entity GETs = %d, want 2 (one per unique id, cached)", len(d.reqs))
	}
	for _, req := range d.reqs {
		if got := req.Header.Get(headerToken); got != testToken {
			t.Errorf("resolution GET token header = %q, want the configured token", got)
		}
		if got := req.Header.Get(headerNamespace); got != "team-a" {
			t.Errorf("resolution GET namespace header = %q", got)
		}
	}

	// Resolved → roster-converging ref, attributed.
	read := findEdge(t, es, "secret/data/app")
	if read.OriginRef != "entity:deploy" || read.Confidence != model.ConfidenceAttributed {
		t.Errorf("resolved edge = %q/%q, want entity:deploy attributed", read.OriginRef, read.Confidence)
	}
	// Failed resolution → honest fallback to the id, approximate, no abort.
	legacy := findEdge(t, es, "kv/data/legacy")
	if legacy.OriginRef != "entity:ent-uuid-2" || legacy.Confidence != model.ConfidenceApproximate {
		t.Errorf("unresolvable edge = %q/%q, want entity:ent-uuid-2 approximate", legacy.OriginRef, legacy.Confidence)
	}
	// The token-only line is untouched by resolution.
	root := findEdge(t, es, "secret/data/breakglass")
	if root.OriginRef != "token:root" {
		t.Errorf("root edge = %q", root.OriginRef)
	}
}

func TestAuditResolutionOffMakesNoCalls(t *testing.T) {
	d := newAuditStub(t) // any request would t.Fatal (nothing programmed)
	s := openAudit(t, d, map[string]string{"token": testToken})
	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(d.reqs) != 0 {
		t.Fatalf("resolution off must make no network calls, made %d", len(d.reqs))
	}
}

func TestAuditResolutionWithoutTokenFallsBack(t *testing.T) {
	d := newAuditStub(t) // any request would t.Fatal
	s := openAudit(t, d, map[string]string{"resolve_entities": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(d.reqs) != 0 {
		t.Fatalf("no token => no resolution calls, made %d", len(d.reqs))
	}
	read := findEdge(t, auditEdges(t, sink), "secret/data/app")
	if read.OriginRef != "entity:ent-uuid-1" || read.Confidence != model.ConfidenceApproximate {
		t.Errorf("tokenless resolution must fall back to the id: %+v", read)
	}
}

// ---------------------------------------------------------------------------
// Config + descriptor
// ---------------------------------------------------------------------------

func TestAuditOpenRequiresPath(t *testing.T) {
	s := NewAudit()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err == nil {
		t.Fatal("Open without path must fail (a silent no-op audit source is a misconfiguration)")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("error should name the missing setting: %v", err)
	}
}

func TestAuditDescriptor(t *testing.T) {
	d := NewAudit().Descriptor()
	if d.Name != AuditName || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion || d.Version != "0.1.0" {
		t.Errorf("descriptor header wrong: %+v", d)
	}
	var pathRequired, tokenSecret bool
	for _, f := range d.ConfigFields {
		if f.Key == "path" {
			pathRequired = f.Required
		}
		if f.Key == "token" {
			tokenSecret = f.Secret
		}
	}
	if !pathRequired {
		t.Error("path must be declared Required")
	}
	if !tokenSecret {
		t.Error("token must be declared Secret")
	}
}

func TestAuditFollowFalseEndsAtEOF(t *testing.T) {
	s := openAudit(t, nil, map[string]string{"follow": "false"})
	done := make(chan error, 1)
	go func() { done <- s.Gather(context.Background(), &captureSink{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("batch Gather: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow=false Gather did not return at EOF")
	}
}

// TestAuditTransientResolutionRetries: a 5xx on the entity GET is NOT
// negative-cached — the next line carrying the same id retries and self-heals
// (only a definitive 404/403 is cached). Three lines for one entity cost two
// GETs: the 500, then the 200 whose positive result is cached.
func TestAuditTransientResolutionRetries(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "audit.log")
	line := `{"type":"response","time":"2026-06-10T08:0%d:00Z","auth":{"entity_id":"ent-uuid-9"},"request":{"operation":"read","path":"secret/data/web","mount_class":"secret"}}` + "\n"
	content := fmt.Sprintf(line, 1) + fmt.Sprintf(line, 2) + fmt.Sprintf(line, 3)
	if err := os.WriteFile(log, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newAuditStub(t)
	d.onEntity("ent-uuid-9", 500, `{"errors":["internal"]}`)
	d.onEntity("ent-uuid-9", 200, `{"data":{"id":"ent-uuid-9","name":"web"}}`)
	s := openAudit(t, d, map[string]string{
		"path":             log,
		"resolve_entities": "true",
		"base_url":         "https://vault.example:8200",
		"token":            testToken,
	})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	es := auditEdges(t, sink)
	if len(es) != 3 {
		t.Fatalf("edges = %d, want 3", len(es))
	}
	// Line 1: transient failure → honest approximate fallback, not cached.
	if es[0].OriginRef != "entity:ent-uuid-9" || es[0].Confidence != model.ConfidenceApproximate {
		t.Errorf("edge after 500 = %q/%q, want entity:ent-uuid-9 approximate", es[0].OriginRef, es[0].Confidence)
	}
	// Lines 2-3: retried, resolved, positively cached.
	for _, e := range es[1:] {
		if e.OriginRef != "entity:web" || e.Confidence != model.ConfidenceAttributed {
			t.Errorf("edge after retry = %q/%q, want entity:web attributed", e.OriginRef, e.Confidence)
		}
	}
	if len(d.reqs) != 2 {
		t.Errorf("entity GETs = %d, want 2 (500 retried once, 200 cached)", len(d.reqs))
	}
}

// TestAuditFollowTailsAppendedLines: follow=true blocks tailing — a line
// appended AFTER Gather starts is emitted, and cancelation ends the tail.
func TestAuditFollowTailsAppendedLines(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(log, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openAudit(t, nil, map[string]string{"path": log, "follow": "true"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := make(chan model.EdgeObservation, 4)
	done := make(chan error, 1)
	go func() {
		done <- s.Gather(ctx, sinkChan(emitted))
	}()

	// Append a complete line after the tail started.
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"response","time":"2026-06-10T09:00:00Z","auth":{"entity_id":"","display_name":"root"},"request":{"operation":"delete","path":"secret/data/tail","mount_class":"secret"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	select {
	case e := <-emitted:
		if e.ResourceRef != "secret/data/tail" || e.OriginRef != "token:root" || e.Mode != model.ModeWrite {
			t.Errorf("tailed edge = %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("appended line was not emitted by the follow tail")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("follow Gather after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow Gather did not return after ctx cancel")
	}
}

// sinkChan adapts a channel to sdk.Sink for cross-goroutine assertions
// (captureSink is not safe for concurrent reads while Gather appends).
type sinkChan chan model.EdgeObservation

func (c sinkChan) Emit(_ context.Context, o model.Observation) error {
	if e, ok := o.(model.EdgeObservation); ok {
		c <- e
	}
	return nil
}
