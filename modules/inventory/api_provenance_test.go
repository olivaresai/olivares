// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// D08-C3 first slice: GET /v1/m/inventory/entities/{kind}/{id}/observations.
//
// These tests drive the REAL router, authenticator, authorizer and tenant
// resolution over a real store (SQLite here; the PostgreSQL leg is
// provenance_api_pg_test.go), seed evidence through the C1 writer itself
// (Module.onEvent), and reach the private reader directly only for the two
// controls that HTTP cannot express: the repository-call ceiling and the refusal
// when the store lacks DistinctProjector.

// c3ObservationsPath is the route under test for one entity.
func c3ObservationsPath(kind string, id model.ID) string {
	return "/v1/m/inventory/entities/" + kind + "/" + id.String() + "/observations"
}

// c3Clock is a settable clock so a replay can be received LATER than the original
// and the page's first/last reception instants are provably different.
type c3Clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *c3Clock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.at)
}

func (c *c3Clock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// c3HTTP is the real API server with the inventory module mounted over the store
// the module already writes through. It is the same wiring api_test.go uses, kept
// inside the package so the tests can seed with the module's own writer.
type c3HTTP struct {
	t        *testing.T
	st       store.Store
	srv      *api.Server
	setupTok string
}

func newC3HTTP(t *testing.T, m *Module, st store.Store) *c3HTTP {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &c3HTTP{t: t, st: st, srv: srv, setupTok: plaintext}
}

type c3Resp struct {
	code int
	body map[string]any
	raw  string
}

func (h *c3HTTP) do(method, path, token string, body any, tenant model.TenantID) c3Resp {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := c3Resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func (h *c3HTTP) adminLogin() string {
	h.t.Helper()
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, ""); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, "")
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *c3HTTP) createOrg(admin, slug string) model.TenantID {
	h.t.Helper()
	r := h.do("POST", "/v1/system/orgs", admin, map[string]any{"name": slug, "slug": slug}, "")
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org %s = %d %s", slug, r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

// memberToken creates a user, grants it role in tenant (confined to workspace when
// ws is set) and returns its session token.
func (h *c3HTTP) memberToken(admin string, tenant model.TenantID, email, role string, ws model.ID) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, "")
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	grant := map[string]any{"user_id": r.body["id"], "tenant": tenant.String(), "role": role}
	if !ws.IsZero() {
		grant["workspace_id"] = ws.String()
	}
	if r := h.do("POST", "/v1/memberships", admin, grant, ""); r.code != http.StatusCreated {
		h.t.Fatalf("grant %s = %d %s", email, r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, "")
	if r.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *c3HTTP) createWorkspace(tenant model.TenantID, slug string) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
		id = ws.ID
		return err
	}); err != nil {
		h.t.Fatalf("create workspace %s: %v", slug, err)
	}
	return id
}

// c3Reg is a complete registration snapshot (registered_snapshot once recorded).
func c3Reg(sourceID string, revision int64, env string) *event.SourceRegistration {
	return &event.SourceRegistration{SourceID: sourceID, SourceRevision: revision, EnvironmentRef: env}
}

// c3Event wraps one observation as the event the C1 writer consumes, with the
// event id, ingestion label and registration snapshot under the test's control.
func c3Event(tenant model.TenantID, id, label string, reg *event.SourceRegistration, obs sdkmodel.Observation) event.Event {
	e := event.FromObservation(tenant.String(), label, obs)
	e.ID = id
	e.SourceRegistration = reg
	return e
}

// c3Deliver feeds one event to the C1 writer and fails the test on any error but
// the one the caller expects.
func c3Deliver(t *testing.T, m *Module, e event.Event, want error) {
	t.Helper()
	err := m.onEvent(context.Background(), e)
	if want == nil && err != nil {
		t.Fatalf("deliver %s: %v", e.ID, err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("deliver %s = %v, want %v", e.ID, err, want)
	}
}

// c3AgentEdge names agent `agent` reading file `file`: two members (agent, resource).
func c3AgentEdge(agent, file string, at time.Time) sdkmodel.EdgeObservation {
	return mkEdge("agent", agent, rkFile, file, sdkmodel.ModeRead, sdkmodel.SignalOTEL, "", at)
}

// c3EntityID resolves the catalog entity id of the entry named `name` of `kind`.
func c3EntityID(t *testing.T, st store.Store, tenant model.TenantID, kind, name string) model.ID {
	t.Helper()
	for _, row := range c1Rows(t, st, tenant, catalogEntryKind) {
		if row.String(colEntityKind) == kind && row.String(colName) == name {
			return model.ID(row.String(colEntityID))
		}
	}
	t.Fatalf("no catalog entry %s/%s", kind, name)
	return ""
}

// c3ReceiptID resolves the receipt id the writer allocated for eventID.
func c3ReceiptID(t *testing.T, st store.Store, tenant model.TenantID, eventID string) string {
	t.Helper()
	return c1Receipt(t, st, tenant, eventID).String(model.ColID)
}

// c3Items decodes the page envelope.
func c3Items(t *testing.T, r c3Resp) ([]map[string]any, string, bool) {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("page = %d %s", r.code, r.raw)
	}
	raw, ok := r.body["items"].([]any)
	if !ok {
		t.Fatalf("page has no items array: %s", r.raw)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		items = append(items, it.(map[string]any))
	}
	cursor, _ := r.body["cursor"].(string)
	hasMore, _ := r.body["has_more"].(bool)
	return items, cursor, hasMore
}

func c3ReceiptIDs(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it["receipt_id"].(string))
	}
	return out
}

// c3Update rewrites one row of kind through the real repository (optimistic
// concurrency and all), so a corruption case is a stored fact, not a mock.
func c3Update(t *testing.T, st store.Store, tenant model.TenantID, kind model.Kind, pick func(model.Record) bool, mutate func(model.Record)) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		if err != nil {
			return err
		}
		n := 0
		for _, row := range rows {
			if !pick(row) {
				continue
			}
			mutate(row)
			if _, err := repo.Update(context.Background(), row); err != nil {
				return err
			}
			n++
		}
		if n == 0 {
			return errors.New("fixture: corruption picked no row")
		}
		return nil
	}); err != nil {
		t.Fatalf("corrupt %s: %v", kind, err)
	}
}

// c3Reencode rewrites a receipt's facts with fn applied to the decoded V1 and a
// RECOMPUTED hash, so the digest holds and the named invariant is the one refused.
func c3Reencode(t *testing.T, st store.Store, tenant model.TenantID, receiptID string, fn func(map[string]any)) {
	t.Helper()
	c3Update(t, st, tenant, observationReceiptKind,
		func(r model.Record) bool { return r.String(model.ColID) == receiptID },
		func(r model.Record) {
			var generic map[string]any
			if err := json.Unmarshal([]byte(r.String(colFacts)), &generic); err != nil {
				t.Fatal(err)
			}
			fn(generic)
			encoded, err := json.Marshal(generic)
			if err != nil {
				t.Fatal(err)
			}
			r[colFacts] = string(encoded)
			r[colFactsHash] = digest(encoded)
		})
}

// c3AddMember appends a member row to receiptID copied from an existing member of
// that receipt, with the given ordinal.
func c3AddMember(t *testing.T, st store.Store, tenant model.TenantID, receiptID string, ordinal int64) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(observationMemberKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colReceiptID, receiptID)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.New("fixture: no member to copy")
		}
		src := rows[0]
		_, err = repo.Create(context.Background(), model.Record{
			colReceiptID: receiptID, colMemberOrdinal: ordinal, colObservationKey: src.String(colObservationKey),
			colSourceID: src.String(colSourceID), colEntityKind: src.String(colEntityKind),
			colEntityID: src.String(colEntityID), colFacts: src.String(colFacts),
		})
		return err
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}
}

// c3Plant writes, BY HAND but with the C1 encoding, a complete unattributed edge
// receipt whose single member names (kind, entityID), plus the catalog entry.
// It is how another tenant is given the SAME (kind, id) pair the legitimate writer
// could never allocate twice, so the isolation control has a real leak to catch.
func c3Plant(t *testing.T, m *Module, st store.Store, tenant model.TenantID, kind string, entityID model.ID, eventID string) string {
	t.Helper()
	edge := c3AgentEdge("planted", "/planted", baseTime)
	e := c3Event(tenant, eventID, "planted-label", nil, edge)
	facts := newInventoryFacts(e, &edge, nil)
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	member := observationMember{Kind: kind, EntityID: entityID, Native: nativeReference{Namespace: "agent", Ref: "planted"},
		Name: "planted", Ref: "planted", Signal: string(sdkmodel.SignalOTEL), OccurredAt: instant(edge.ObservedAt)}
	body, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	at := m.clock.Now().Time()
	var receiptID string
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		receipts, err := sc.Ext(observationReceiptKind)
		if err != nil {
			return err
		}
		keyBytes, _ := json.Marshal([]string{"event", eventID})
		receipt, err := receipts.Create(context.Background(), model.Record{colReceiptKey: digest(keyBytes), colEventID: eventID,
			colFactsHash: digest(encoded), colFacts: string(encoded), colFirstSeen: instant(at), colLastSeen: instant(at),
			colDeliveries: int64(1), colMemberCount: int64(1)})
		if err != nil {
			return err
		}
		receiptID = receipt.String(model.ColID)
		members, err := sc.Ext(observationMemberKind)
		if err != nil {
			return err
		}
		if _, err := members.Create(context.Background(), model.Record{colReceiptID: receiptID, colMemberOrdinal: int64(0),
			colObservationKey: "", colSourceID: "", colEntityKind: kind, colEntityID: entityID.String(), colFacts: string(body)}); err != nil {
			return err
		}
		return m.upsertCatalogEntry(context.Background(), sc, kind, entityID, "planted", "planted", string(sdkmodel.SignalOTEL), "", at, edge.ObservedAt)
	}); err != nil {
		t.Fatalf("plant receipt: %v", err)
	}
	return receiptID
}

// --- control 1: HTTP, authorization and tenant isolation -------------------------

func TestC3ObservationsHTTPAuthorityAndTenantIsolation(t *testing.T) {
	m, st, t1 := newInv(t)
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	t2 := h.createOrg(admin, "globex")

	// T1: agent "shared-external" seen twice by a registered source.
	c3Deliver(t, m, c3Event(t1, "t1-one", "src-label", c3Reg("source-a", 1, "env-a"), c3AgentEdge("shared-external", "/data/one", baseTime)), nil)
	c3Deliver(t, m, c3Event(t1, "t1-two", "src-label", c3Reg("source-a", 1, "env-a"), c3AgentEdge("shared-external", "/data/two", baseTime)), nil)
	agentT1 := c3EntityID(t, st, t1, kindAgent, "shared-external")
	wantT1 := []string{c3ReceiptID(t, st, t1, "t1-one"), c3ReceiptID(t, st, t1, "t1-two")}
	sort.Strings(wantT1)

	// T2: its own legitimate observation of the same external name (its own core
	// id), AND a planted receipt for EXACTLY T1's (kind, id) pair.
	c3Deliver(t, m, c3Event(t2, "t2-own", "src-label", nil, c3AgentEdge("shared-external", "/data/one", baseTime)), nil)
	agentT2 := c3EntityID(t, st, t2, kindAgent, "shared-external")
	if agentT2 == agentT1 {
		t.Fatal("fixture: the writer allocated one core id in two tenants")
	}
	planted := c3Plant(t, m, st, t2, kindAgent, agentT1, "t2-planted")

	viewer := h.memberToken(admin, t1, "v@acme.io", auth.RoleViewer, "")
	path := c3ObservationsPath(kindAgent, agentT1)

	// T1 viewer sees T1's receipts and nothing of T2's, even for the same pair.
	items, cursor, hasMore := c3Items(t, h.do("GET", path, viewer, nil, t1))
	got := c3ReceiptIDs(items)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(wantT1, ",") || cursor != "" || hasMore {
		t.Fatalf("T1 viewer page = %v cursor=%q has_more=%v, want exactly %v", got, cursor, hasMore, wantT1)
	}
	if strings.Contains(strings.Join(got, ","), planted) {
		t.Fatal("T2's planted receipt for the same (kind, id) leaked into T1")
	}
	// The superadmin sees the SAME projection of the selected tenant, not more.
	adminItems, _, _ := c3Items(t, h.do("GET", path, admin, nil, t1))
	adminGot := c3ReceiptIDs(adminItems)
	sort.Strings(adminGot)
	if strings.Join(adminGot, ",") != strings.Join(wantT1, ",") {
		t.Fatalf("superadmin page = %v, want %v", adminGot, wantT1)
	}
	// From T2, the same pair yields ONLY the planted receipt.
	t2Items, _, _ := c3Items(t, h.do("GET", path, admin, nil, t2))
	if ids := c3ReceiptIDs(t2Items); len(ids) != 1 || ids[0] != planted {
		t.Fatalf("T2 page for the shared pair = %v, want only %s", ids, planted)
	}

	// Denials: the T1 viewer selecting T2 (no membership), no credentials at all,
	// and a member of T2 only selecting T1 (authenticated, no permission in T1).
	if r := h.do("GET", path, viewer, nil, t2); r.code != http.StatusForbidden {
		t.Errorf("T1 viewer selecting T2 = %d %s, want 403", r.code, r.raw)
	}
	if r := h.do("GET", path, "", nil, t1); r.code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d %s, want 401", r.code, r.raw)
	}
	outsider := h.memberToken(admin, t2, "o@globex.io", auth.RoleViewer, "")
	if r := h.do("GET", path, outsider, nil, t1); r.code != http.StatusForbidden {
		t.Errorf("T2-only member selecting T1 = %d %s, want 403", r.code, r.raw)
	}
	// A workspace-confined principal is refused (403), not handed an empty list.
	ws := h.createWorkspace(t1, "wa")
	confined := h.memberToken(admin, t1, "c@acme.io", auth.RoleViewer, ws)
	if r := h.do("GET", path, confined, nil, t1); r.code != http.StatusForbidden || r.body["items"] != nil {
		t.Errorf("workspace-confined viewer = %d %s, want 403 with no items", r.code, r.raw)
	}
	// The existing catalog routes keep the same refusal for that principal.
	if r := h.do("GET", "/v1/m/inventory/entities/"+kindAgent+"/"+agentT1.String(), confined, nil, t1); r.code != http.StatusForbidden {
		t.Errorf("confined viewer on the detail route = %d %s, want 403", r.code, r.raw)
	}

	// 404: an id that is another tenant's, an exact-kind miss and no normalization.
	if r := h.do("GET", c3ObservationsPath(kindAgent, agentT2), viewer, nil, t1); r.code != http.StatusNotFound {
		t.Errorf("other tenant's entity id from T1 = %d %s, want 404", r.code, r.raw)
	}
	if r := h.do("GET", c3ObservationsPath("Agent", agentT1), viewer, nil, t1); r.code != http.StatusNotFound {
		t.Errorf("kind Agent (not normalized) = %d %s, want 404", r.code, r.raw)
	}
	if r := h.do("GET", c3ObservationsPath(kindResource, agentT1), viewer, nil, t1); r.code != http.StatusNotFound {
		t.Errorf("wrong kind for a real id = %d %s, want 404", r.code, r.raw)
	}
	if r := h.do("GET", c3ObservationsPath(kindAgent, model.NewID()), viewer, nil, t1); r.code != http.StatusNotFound {
		t.Errorf("unknown id = %d %s, want 404", r.code, r.raw)
	}
	// 400: path ids that are not canonical nonzero UUIDs, decided before any read.
	for _, bad := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000",
		strings.ToUpper(agentT1.String()), "{" + agentT1.String() + "}", strings.ReplaceAll(agentT1.String(), "-", "")} {
		if r := h.do("GET", "/v1/m/inventory/entities/agent/"+bad+"/observations", viewer, nil, t1); r.code != http.StatusBadRequest {
			t.Errorf("path id %q = %d %s, want 400", bad, r.code, r.raw)
		}
	}
}

// --- control 2: the minimal, allowlisted projection --------------------------------

// c3AllowedKeys is the closed set of item keys; c3AllowedRegistrationKeys the
// closed set inside registration.
var (
	c3AllowedKeys             = map[string]bool{"receipt_id": true, "event_type": true, "registration": true, "source_occurred_at": true, "first_received_at": true, "last_received_at": true, "deliveries": true, "conflicting_redelivery": true}
	c3AllowedRegistrationKeys = map[string]bool{"registration_state": true, "source_id": true, "source_revision": true, "environment_ref": true}
)

func c3AssertAllowlist(t *testing.T, item map[string]any) {
	t.Helper()
	for k := range item {
		if !c3AllowedKeys[k] {
			t.Errorf("item publishes key %q outside the allowlist: %v", k, item)
		}
	}
	for _, k := range []string{"receipt_id", "event_type", "registration", "first_received_at", "last_received_at", "deliveries", "conflicting_redelivery"} {
		if _, ok := item[k]; !ok {
			t.Errorf("item lacks required key %q: %v", k, item)
		}
	}
	reg, ok := item["registration"].(map[string]any)
	if !ok {
		t.Fatalf("registration is not an object: %v", item)
	}
	for k := range reg {
		if !c3AllowedRegistrationKeys[k] {
			t.Errorf("registration publishes key %q outside the allowlist: %v", k, reg)
		}
	}
	if reg["registration_state"] != registrationRegistered {
		for _, k := range []string{"source_id", "source_revision", "environment_ref"} {
			if _, present := reg[k]; present {
				t.Errorf("registration_state %v publishes %s: %v", reg["registration_state"], k, reg)
			}
		}
	}
}

func TestC3ObservationsMinimalProjection(t *testing.T) {
	clock := &c3Clock{at: baseTime.Add(time.Hour)}
	m, st, tenant := newInv(t, WithClock(clock))
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	viewer := h.memberToken(admin, tenant, "v@acme.io", auth.RoleViewer, "")

	const label, binding, file = "editable-source-label", "historical-binding-ref", "/secret/path/config.yaml"
	reg := c3Reg("source-a", 3, "env-a")
	reg.BindingRef = binding
	// registered, with a source-declared instant
	c3Deliver(t, m, c3Event(tenant, "registered", label, reg, c3AgentEdge("proj", file, baseTime)), nil)
	// unattributed, with NO source-declared instant
	c3Deliver(t, m, c3Event(tenant, "unattributed", label, nil, c3AgentEdge("proj", file, time.Time{})), nil)
	// invalid snapshot (revision 0): components must stay withheld
	c3Deliver(t, m, c3Event(tenant, "invalid", label, c3Reg("source-a", 0, "env-a"), c3AgentEdge("proj", file, baseTime)), nil)
	// exact replay of the registered receipt, received one hour later
	clock.advance(time.Hour)
	c3Deliver(t, m, c3Event(tenant, "registered", label, reg, c3AgentEdge("proj", file, baseTime)), nil)
	// conflicting redelivery of the same event id, another hour later
	clock.advance(time.Hour)
	c3Deliver(t, m, c3Event(tenant, "registered", label, reg, c3AgentEdge("proj", "/other/file", baseTime)), ErrObservationReceiptConflict)
	// a cost observation names a model entity
	cost := sdkmodel.CostSample{ProviderRef: "anthropic", ModelRef: "claude-opus-5", OccurredAt: baseTime.Add(time.Minute), Gateway: "fixture-gateway"}
	c3Deliver(t, m, c3Event(tenant, "cost", label, c3Reg("source-b", 7, "env-b"), cost), nil)

	agent := c3EntityID(t, st, tenant, kindAgent, "proj")
	items, _, _ := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, agent), viewer, nil, tenant))
	if len(items) != 3 {
		t.Fatalf("agent history = %d items, want 3 distinct receipts: %v", len(items), items)
	}
	byEvent := map[string]map[string]any{}
	for _, it := range items {
		c3AssertAllowlist(t, it)
		for _, id := range []string{"registered", "unattributed", "invalid"} {
			if it["receipt_id"] == c3ReceiptID(t, st, tenant, id) {
				byEvent[id] = it
			}
		}
	}
	if len(byEvent) != 3 {
		t.Fatalf("items do not map onto the three receipts: %v", items)
	}
	reg1 := byEvent["registered"]["registration"].(map[string]any)
	if reg1["registration_state"] != registrationRegistered || reg1["source_id"] != "source-a" || reg1["source_revision"] != float64(3) || reg1["environment_ref"] != "env-a" {
		t.Errorf("registered snapshot = %v", reg1)
	}
	if byEvent["registered"]["event_type"] != string(event.TypeEdgeObserved) || byEvent["registered"]["source_occurred_at"] != instant(baseTime) {
		t.Errorf("registered item = %v", byEvent["registered"])
	}
	first, last := byEvent["registered"]["first_received_at"].(string), byEvent["registered"]["last_received_at"].(string)
	if first != instant(baseTime.Add(time.Hour)) || last != instant(baseTime.Add(2*time.Hour)) {
		t.Errorf("registered reception = %s..%s, want the original and the EQUAL-facts replay, not the conflict", first, last)
	}
	if byEvent["registered"]["deliveries"] != float64(2) || byEvent["registered"]["conflicting_redelivery"] != true {
		t.Errorf("registered counters = deliveries %v conflicting %v, want 2 and true", byEvent["registered"]["deliveries"], byEvent["registered"]["conflicting_redelivery"])
	}
	if reg := byEvent["unattributed"]["registration"].(map[string]any); reg["registration_state"] != registrationUnattributed || len(reg) != 1 {
		t.Errorf("unattributed registration = %v", reg)
	}
	if _, present := byEvent["unattributed"]["source_occurred_at"]; present {
		t.Errorf("unattributed item invents a source instant: %v", byEvent["unattributed"])
	}
	if byEvent["unattributed"]["deliveries"] != float64(1) || byEvent["unattributed"]["conflicting_redelivery"] != false {
		t.Errorf("unattributed counters = %v", byEvent["unattributed"])
	}
	if reg := byEvent["invalid"]["registration"].(map[string]any); reg["registration_state"] != registrationInvalid || len(reg) != 1 {
		t.Errorf("invalid registration must withhold its partial components: %v", reg)
	}

	// Nothing free-form or out of scope reaches the wire.
	raw := h.do("GET", c3ObservationsPath(kindAgent, agent), viewer, nil, tenant).raw
	for _, forbidden := range []string{label, binding, file, "proj", "source_label", "binding_ref", "native", "event_id", "receipt_key", "facts", "hash", "\"edge\"", "\"cost\"", "origin", "resource_kind", "\"name\"", "\"ref\"", "signal", "host", "envelope", "coverage", "owner", "health", "version\""} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("response carries %q: %s", forbidden, raw)
		}
	}

	// The cost receipt: model entity, cost.sampled, the cost's own instant.
	modelID := c3EntityID(t, st, tenant, kindModel, "claude-opus-5")
	costItems, _, _ := c3Items(t, h.do("GET", c3ObservationsPath(kindModel, modelID), viewer, nil, tenant))
	if len(costItems) != 1 {
		t.Fatalf("model history = %v", costItems)
	}
	c3AssertAllowlist(t, costItems[0])
	if costItems[0]["event_type"] != string(event.TypeCostSampled) || costItems[0]["source_occurred_at"] != instant(cost.OccurredAt) {
		t.Errorf("cost item = %v", costItems[0])
	}
	if reg := costItems[0]["registration"].(map[string]any); reg["source_revision"] != float64(7) || reg["environment_ref"] != "env-b" {
		t.Errorf("cost registration = %v", reg)
	}
	if strings.Contains(h.do("GET", c3ObservationsPath(kindModel, modelID), viewer, nil, tenant).raw, "fixture-gateway") {
		t.Error("cost item publishes the gateway")
	}
	// The catalog entry created by an OLD writer, without receipts, is an empty
	// success — not an error and not an absence claim.
	var legacy model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		legacy, err = foAgent(context.Background(), sc, "legacy-only")
		if err != nil {
			return err
		}
		return m.upsertCatalogEntry(context.Background(), sc, kindAgent, legacy, "legacy-only", "legacy-only", "legacy", "", baseTime, time.Time{})
	}); err != nil {
		t.Fatal(err)
	}
	if items, cursor, more := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, legacy), viewer, nil, tenant)); len(items) != 0 || cursor != "" || more {
		t.Errorf("legacy entry = %v %q %v, want an empty page", items, cursor, more)
	}
}

// --- control 3: distinct-receipt pagination, strict limits and cursors -----------

// c3PagerFixture seeds agent "pager" with 24 single-hit receipts, then ONE receipt
// whose two members both resolve to the agent (origin agent/pager + resource
// a2a.agent/pager), then six more — so the shared receipt's member rows are the
// 25th and 26th member rows of the entity, straddling a 25-row boundary — plus
// noise from another entity and another tenant. It returns the entity id, the
// ascending list of the entity's 31 distinct receipt ids and the shared one.
func c3PagerFixture(t *testing.T, m *Module, st store.Store, tenant, other model.TenantID) (model.ID, []string, string) {
	t.Helper()
	var events []string
	deliver := func(id string, edge sdkmodel.EdgeObservation) {
		c3Deliver(t, m, c3Event(tenant, id, "label", c3Reg("source-a", 1, "env-a"), edge), nil)
		events = append(events, id)
	}
	for i := 0; i < 24; i++ {
		deliver(fmt.Sprintf("pager-%02d", i), c3AgentEdge("pager", fmt.Sprintf("/f/%02d", i), baseTime))
	}
	deliver("pager-shared", mkEdge("agent", "pager", rkA2AAgent, "pager", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, "", baseTime))
	for i := 24; i < 30; i++ {
		deliver(fmt.Sprintf("pager-%02d", i), c3AgentEdge("pager", fmt.Sprintf("/f/%02d", i), baseTime))
	}
	// noise: another entity in the same tenant, and the same names in another tenant
	for i := 0; i < 5; i++ {
		c3Deliver(t, m, c3Event(tenant, fmt.Sprintf("noise-%d", i), "label", nil, c3AgentEdge("noise", fmt.Sprintf("/n/%d", i), baseTime)), nil)
		c3Deliver(t, m, c3Event(other, fmt.Sprintf("pager-%02d", i), "label", nil, c3AgentEdge("pager", fmt.Sprintf("/f/%02d", i), baseTime)), nil)
	}
	agent := c3EntityID(t, st, tenant, kindAgent, "pager")
	ids := make([]string, 0, len(events))
	for _, id := range events {
		ids = append(ids, c3ReceiptID(t, st, tenant, id))
	}
	sort.Strings(ids)
	shared := c3ReceiptID(t, st, tenant, "pager-shared")

	// Prove the straddle: the entity's member rows in id order have the shared
	// receipt at positions 25 and 26 (1-based).
	var rows []model.Record
	for _, row := range c1Rows(t, st, tenant, observationMemberKind) {
		if row.String(colEntityKind) == kindAgent && row.String(colEntityID) == agent.String() {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].String(model.ColID) < rows[j].String(model.ColID) })
	if len(rows) != 32 || rows[24].String(colReceiptID) != shared || rows[25].String(colReceiptID) != shared {
		t.Fatalf("fixture: %d member rows; rows 25/26 = %s/%s, want the shared receipt %s on both sides of the boundary",
			len(rows), rows[24].String(colReceiptID), rows[25].String(colReceiptID), shared)
	}
	return agent, ids, shared
}

// c3Walk pages the whole history with limit, returning every receipt id in order
// and the per-page envelopes.
func c3Walk(t *testing.T, h *c3HTTP, token string, tenant model.TenantID, path string, limit int) ([]string, [][]string) {
	t.Helper()
	var all []string
	var pages [][]string
	cursor := ""
	for {
		url := fmt.Sprintf("%s?limit=%d", path, limit)
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		items, next, more := c3Items(t, h.do("GET", url, token, nil, tenant))
		ids := c3ReceiptIDs(items)
		if len(ids) > limit {
			t.Fatalf("page of %d exceeds limit %d", len(ids), limit)
		}
		if more && (len(ids) != limit || next != ids[len(ids)-1]) {
			t.Fatalf("has_more page must be full and its cursor the last receipt: %d items, cursor %q, last %q", len(ids), next, ids[len(ids)-1])
		}
		if !more && next != "" {
			t.Fatalf("final page carries a cursor %q", next)
		}
		pages = append(pages, ids)
		all = append(all, ids...)
		if !more {
			return all, pages
		}
		cursor = next
	}
}

func TestC3ObservationsPaginationIsPerDistinctReceipt(t *testing.T) {
	m, st, tenant := newInv(t)
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	other := h.createOrg(admin, "other")
	agent, want, shared := c3PagerFixture(t, m, st, tenant, other)
	viewer := h.memberToken(admin, tenant, "v@acme.io", auth.RoleViewer, "")
	path := c3ObservationsPath(kindAgent, agent)

	for _, limit := range []int{25, 1, 7} {
		all, pages := c3Walk(t, h, viewer, tenant, path, limit)
		if strings.Join(all, ",") != strings.Join(want, ",") {
			t.Fatalf("limit %d: pages are not disjoint, exhaustive and ascending:\n got %v\nwant %v", limit, all, want)
		}
		seen := 0
		for _, page := range pages {
			for _, id := range page {
				if id == shared {
					seen++
				}
			}
		}
		if seen != 1 {
			t.Fatalf("limit %d: the shared receipt appears %d times across pages, want once", limit, seen)
		}
		if limit == 25 && (len(pages) != 2 || len(pages[0]) != 25 || len(pages[1]) != 6 || pages[0][24] != shared) {
			t.Fatalf("limit 25: pages %v; want 25 + 6 with the shared receipt closing page one", pages)
		}
	}
	// Default limit is 25.
	if items, cursor, more := c3Items(t, h.do("GET", path, viewer, nil, tenant)); len(items) != 25 || !more || cursor != want[24] {
		t.Fatalf("default page = %d items, more=%v cursor=%q", len(items), more, cursor)
	}
	// A canonical cursor that no page emitted still anchors honestly: everything
	// strictly after it, in order.
	if items, _, _ := c3Items(t, h.do("GET", path+"?cursor="+want[27], viewer, nil, tenant)); strings.Join(c3ReceiptIDs(items), ",") != strings.Join(want[28:], ",") {
		t.Fatalf("arbitrary canonical cursor page = %v, want %v", c3ReceiptIDs(items), want[28:])
	}
	if items, _, more := c3Items(t, h.do("GET", path+"?cursor="+want[30], viewer, nil, tenant)); len(items) != 0 || more {
		t.Fatalf("cursor at the last receipt = %v more=%v, want an empty final page", items, more)
	}
	// Strict request validation, decided before any read.
	for _, bad := range []string{
		"limit=0", "limit=26", "limit=-1", "limit=abc", "limit=", "limit=05", "limit=+5", "limit=5&limit=5", "limit=1.0",
		"cursor=", "cursor=not-a-uuid", "cursor=00000000-0000-0000-0000-000000000000",
		"cursor=" + strings.ToUpper(want[0]), "cursor={" + want[0] + "}", "cursor=" + strings.ReplaceAll(want[0], "-", ""),
		"cursor=" + want[0] + "&cursor=" + want[1],
	} {
		if r := h.do("GET", path+"?"+bad, viewer, nil, tenant); r.code != http.StatusBadRequest {
			t.Errorf("?%s = %d %s, want 400", bad, r.code, r.raw)
		}
	}
	// Noise stays where it belongs: the other entity has its five, the other
	// tenant its five, and neither shares a receipt id with the entity under test.
	noise := c3EntityID(t, st, tenant, kindAgent, "noise")
	if items, _, _ := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, noise), viewer, nil, tenant)); len(items) != 5 {
		t.Fatalf("noise entity = %d items, want 5", len(items))
	}
	otherAgent := c3EntityID(t, st, other, kindAgent, "pager")
	if items, _, _ := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, otherAgent), admin, nil, other)); len(items) != 5 {
		t.Fatalf("other tenant's pager = %d items, want 5", len(items))
	}
}

// --- control 4: composition budget and the fail-closed projector requirement ------

// c3Calls counts repository reads per entity kind.
type c3Calls struct {
	mu          sync.Mutex
	gets, lists map[model.Kind]int
	projections int
}

type c3CountingScope struct {
	store.Scope
	calls *c3Calls
}

type c3CountingRepo struct {
	store.GenericRepo
	kind  model.Kind
	calls *c3Calls
}

func (s c3CountingScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil {
		return nil, err
	}
	return c3CountingRepo{GenericRepo: repo, kind: kind, calls: s.calls}, nil
}

func (r c3CountingRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	r.calls.mu.Lock()
	r.calls.gets[r.kind]++
	r.calls.mu.Unlock()
	return r.GenericRepo.Get(ctx, id)
}

func (r c3CountingRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.calls.mu.Lock()
	r.calls.lists[r.kind]++
	r.calls.mu.Unlock()
	return r.GenericRepo.List(ctx, q)
}

func (r c3CountingRepo) ProjectDistinct(ctx context.Context, p store.DistinctProjection) (store.DistinctPage, error) {
	r.calls.mu.Lock()
	r.calls.projections++
	r.calls.mu.Unlock()
	return r.GenericRepo.(store.DistinctProjector).ProjectDistinct(ctx, p)
}

func (c *c3Calls) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.projections
	for _, v := range c.gets {
		n += v
	}
	for _, v := range c.lists {
		n += v
	}
	return n
}

// c3NoProjectorScope hands out member repositories WITHOUT DistinctProjector.
type c3NoProjectorScope struct {
	store.Scope
	calls *c3Calls
}

type c3PlainRepo struct{ store.GenericRepo }

func (s c3NoProjectorScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil {
		return nil, err
	}
	counting := c3CountingRepo{GenericRepo: repo, kind: kind, calls: s.calls}
	if kind == observationMemberKind {
		return c3PlainRepo{counting}, nil
	}
	return counting, nil
}

func newC3Calls() *c3Calls { return &c3Calls{gets: map[model.Kind]int{}, lists: map[model.Kind]int{}} }

func TestC3ObservationsCompositionBudgetAndProjectorRefusal(t *testing.T) {
	m, st, tenant := newInv(t)
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	other := h.createOrg(admin, "other")
	agent, want, _ := c3PagerFixture(t, m, st, tenant, other)
	ctx := context.Background()

	// A full page of 25 costs exactly the fixed ceiling, and the shared receipt is
	// composed once (25 gets for 25 items, not 26 for 26 member rows).
	calls := newC3Calls()
	var page observationPage
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		page, err = listEntityObservations(ctx, c3CountingScope{sc, calls}, kindAgent, agent, observationQuery{Limit: 25})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 25 || !page.HasMore || page.Cursor != want[24] {
		t.Fatalf("page = %d items more=%v cursor=%q", len(page.Items), page.HasMore, page.Cursor)
	}
	if calls.projections != 1 || calls.lists[catalogEntryKind] != 1 || calls.gets[observationReceiptKind] != 25 ||
		calls.lists[observationMemberKind] != 25 || calls.lists[observationConflictKind] != 25 || calls.total() != observationPageMaxRepositoryCalls {
		t.Fatalf("full page repository calls: projections=%d catalog=%d gets=%v lists=%v total=%d, want %d",
			calls.projections, calls.lists[catalogEntryKind], calls.gets, calls.lists, calls.total(), observationPageMaxRepositoryCalls)
	}
	if observationPageMaxRepositoryCalls != 77 {
		t.Fatalf("ceiling constant = %d, want the accepted 77", observationPageMaxRepositoryCalls)
	}
	// The last page (6 items) costs 2 + 3*6.
	calls = newC3Calls()
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		page, err = listEntityObservations(ctx, c3CountingScope{sc, calls}, kindAgent, agent, observationQuery{Limit: 25, After: want[24]})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 6 || page.HasMore || page.Cursor != "" || calls.total() != 2+3*6 {
		t.Fatalf("last page = %d items more=%v cursor=%q calls=%d", len(page.Items), page.HasMore, page.Cursor, calls.total())
	}

	// Without DistinctProjector the reader refuses and never falls back to paging
	// member rows: no member List, no receipt Get, nothing composed.
	calls = newC3Calls()
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, err := listEntityObservations(ctx, c3NoProjectorScope{sc, calls}, kindAgent, agent, observationQuery{Limit: 25})
		return err
	})
	if !errors.Is(err, errObservationProjectorUnavailable) {
		t.Fatalf("reader without a projector = %v, want %v", err, errObservationProjectorUnavailable)
	}
	if calls.lists[observationMemberKind] != 0 || calls.gets[observationReceiptKind] != 0 || calls.lists[observationConflictKind] != 0 || calls.lists[catalogEntryKind] != 1 {
		t.Fatalf("refusal still read: gets=%v lists=%v (only the catalog precondition may run)", calls.gets, calls.lists)
	}
	// Reader-level request guards are server-side errors, never a page.
	if _, err := listEntityObservations(ctx, nil, kindAgent, agent, observationQuery{Limit: 26}); err == nil {
		t.Fatal("limit 26 accepted by the reader")
	}
	if _, err := listEntityObservations(ctx, nil, kindAgent, agent, observationQuery{Limit: 1, After: "BAD"}); err == nil {
		t.Fatal("non-canonical anchor accepted by the reader")
	}
}

// --- control 5: corruption fails the whole page, isolated from a green baseline ----

// c3VictimFixture seeds agent "victim" with one registered two-member receipt
// ("victim-one") and one unattributed receipt ("victim-two"), and proves the page
// is green before the case corrupts it.
func c3VictimFixture(t *testing.T, m *Module, st store.Store, h *c3HTTP, admin string, tenant model.TenantID) (model.ID, string, string) {
	t.Helper()
	c3Deliver(t, m, c3Event(tenant, "victim-one", "label", c3Reg("source-a", 2, "env-a"), c3AgentEdge("victim", "/v/one", baseTime)), nil)
	c3Deliver(t, m, c3Event(tenant, "victim-two", "label", nil, c3AgentEdge("victim", "/v/two", time.Time{})), nil)
	agent := c3EntityID(t, st, tenant, kindAgent, "victim")
	items, _, _ := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, agent), admin, nil, tenant))
	if len(items) != 2 {
		t.Fatalf("baseline = %d items, want 2 before corrupting anything", len(items))
	}
	return agent, c3ReceiptID(t, st, tenant, "victim-one"), c3ReceiptID(t, st, tenant, "victim-two")
}

func TestC3ObservationsCorruptionFailsTheWholePage(t *testing.T) {
	m, st, _ := newInv(t)
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()

	isReceipt := func(id string) func(model.Record) bool {
		return func(r model.Record) bool { return r.String(model.ColID) == id }
	}
	isMember := func(receipt string, ordinal int64) func(model.Record) bool {
		return func(r model.Record) bool {
			return r.String(colReceiptID) == receipt && r.Int(colMemberOrdinal) == ordinal
		}
	}
	agentMember := func(receipt string) func(model.Record) bool {
		return func(r model.Record) bool {
			return r.String(colReceiptID) == receipt && r.String(colEntityKind) == kindAgent
		}
	}

	cases := []struct {
		name    string
		corrupt func(tenant model.TenantID, one, two string)
	}{
		{"member_count zero", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colMemberCount] = int64(0) })
		}},
		{"member_count five with five rows", func(tenant model.TenantID, one, _ string) {
			for _, ord := range []int64{2, 3, 4} {
				c3AddMember(t, st, tenant, one, ord)
			}
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colMemberCount] = int64(5) })
		}},
		{"extra member row beyond member_count", func(tenant model.TenantID, one, _ string) {
			c3AddMember(t, st, tenant, one, 2)
		}},
		{"six rows for a claimed four", func(tenant model.TenantID, one, _ string) {
			for _, ord := range []int64{2, 3, 4, 5} {
				c3AddMember(t, st, tenant, one, ord)
			}
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colMemberCount] = int64(4) })
		}},
		{"ordinal gap", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, isMember(one, 1), func(r model.Record) { r[colMemberOrdinal] = int64(3) })
		}},
		// A REPEATED ordinal is not representable: the C1 unique index on
		// (tenant_id, receipt_id, member_ordinal) refuses the write, so the reader's
		// repeat check is defense in depth behind the database's own guarantee. A
		// negative ordinal IS representable, and is refused by the same check.
		{"negative ordinal", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, isMember(one, 1), func(r model.Record) { r[colMemberOrdinal] = int64(-1) })
		}},
		{"facts digest mismatch", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colFactsHash] = digest([]byte("other")) })
		}},
		{"facts unreadable", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) {
				r[colFacts] = "{not json"
				r[colFactsHash] = digest([]byte("{not json"))
			})
		}},
		{"facts with an unknown field", func(tenant model.TenantID, one, _ string) {
			c3Reencode(t, st, tenant, one, func(f map[string]any) { f["extra"] = true })
		}},
		{"facts version 2", func(tenant model.TenantID, one, _ string) {
			c3Reencode(t, st, tenant, one, func(f map[string]any) { f["version"] = 2 })
		}},
		{"unsupported event type", func(tenant model.TenantID, one, _ string) {
			c3Reencode(t, st, tenant, one, func(f map[string]any) { f["type"] = "finding.raised" })
		}},
		{"edge type without an edge payload", func(tenant model.TenantID, one, _ string) {
			c3Reencode(t, st, tenant, one, func(f map[string]any) { delete(f, "edge") })
		}},
		{"edge type with a cost payload too", func(tenant model.TenantID, one, _ string) {
			c3Reencode(t, st, tenant, one, func(f map[string]any) {
				f["cost"] = map[string]any{"ProviderRef": "p", "ModelRef": "m", "Gateway": "", "OccurredAt": ""}
			})
		}},
		{"registration_state contradicts a nil snapshot", func(tenant model.TenantID, _, two string) {
			c3Reencode(t, st, tenant, two, func(f map[string]any) { f["registration_state"] = registrationRegistered })
		}},
		{"registration_state contradicts a complete snapshot", func(tenant model.TenantID, one, _ string) {
			c3Reencode(t, st, tenant, one, func(f map[string]any) { f["registration_state"] = registrationInvalid })
		}},
		{"unknown registration_state", func(tenant model.TenantID, one, _ string) {
			c3Reencode(t, st, tenant, one, func(f map[string]any) { f["registration_state"] = "verified" })
		}},
		{"member occurrence differs from the payload", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, agentMember(one), func(r model.Record) {
				var member map[string]any
				if err := json.Unmarshal([]byte(r.String(colFacts)), &member); err != nil {
					t.Fatal(err)
				}
				member["occurred_at"] = instant(baseTime.Add(time.Second))
				b, _ := json.Marshal(member)
				r[colFacts] = string(b)
			})
		}},
		// The group is validated WHOLE: the resource member is never selected by the
		// entity predicate, yet its contradiction still fails the agent's page.
		{"unselected member's entity_kind column contradicts its facts", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, func(r model.Record) bool {
				return r.String(colReceiptID) == one && r.String(colEntityKind) == kindResource
			}, func(r model.Record) { r[colEntityKind] = kindTool })
		}},
		{"selected member's facts contradict its entity columns", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, agentMember(one), func(r model.Record) {
				var member map[string]any
				if err := json.Unmarshal([]byte(r.String(colFacts)), &member); err != nil {
					t.Fatal(err)
				}
				member["catalog_entity_id"] = model.NewID().String()
				b, _ := json.Marshal(member)
				r[colFacts] = string(b)
			})
		}},
		{"member facts unreadable", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, agentMember(one), func(r model.Record) { r[colFacts] = "[]" })
		}},
		{"source_id blanked on a registered member", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, agentMember(one), func(r model.Record) { r[colSourceID] = "" })
		}},
		{"observation_key altered", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, agentMember(one), func(r model.Record) { r[colObservationKey] = digest([]byte("x")) })
		}},
		{"attribution on an unattributed member", func(tenant model.TenantID, _, two string) {
			c3Update(t, st, tenant, observationMemberKind, agentMember(two), func(r model.Record) { r[colSourceID] = "source-a" })
		}},
		{"missing receipt row is 500, not 404", func(tenant model.TenantID, one, _ string) {
			if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(observationReceiptKind)
				if err != nil {
					return err
				}
				return repo.Delete(context.Background(), model.ID(one))
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"unreadable first reception", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colFirstSeen] = "yesterday" })
		}},
		{"non-canonical last reception", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colLastSeen] = baseTime.Format(time.RFC3339) })
		}},
		{"last reception before first", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colLastSeen] = instant(baseTime.Add(-time.Hour)) })
		}},
		{"deliveries zero", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationReceiptKind, isReceipt(one), func(r model.Record) { r[colDeliveries] = int64(0) })
		}},
		{"non-canonical receipt id on the member", func(tenant model.TenantID, one, _ string) {
			c3Update(t, st, tenant, observationMemberKind, agentMember(one), func(r model.Record) { r[colReceiptID] = strings.ToUpper(one) })
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenant := h.createOrg(admin, fmt.Sprintf("corrupt-%02d", i))
			agent, one, two := c3VictimFixture(t, m, st, h, admin, tenant)
			tc.corrupt(tenant, one, two)
			r := h.do("GET", c3ObservationsPath(kindAgent, agent), admin, nil, tenant)
			if r.code != http.StatusInternalServerError {
				t.Fatalf("corrupted page = %d %s, want 500", r.code, r.raw)
			}
			if _, hasItems := r.body["items"]; hasItems || !strings.Contains(r.raw, "internal error") {
				t.Fatalf("corrupted page body = %s, want the generic error and no items", r.raw)
			}
			for _, leaked := range []string{one, two, "receipt", "member", "digest", "ordinal"} {
				if strings.Contains(r.raw, leaked) {
					t.Errorf("error body leaks %q: %s", leaked, r.raw)
				}
			}
		})
	}

	// Lookahead: a corrupt receipt PAST the page does not fail the page (has_more
	// only says another id exists), and the page that composes it fails whole.
	t.Run("lookahead corruption fails when composed", func(t *testing.T) {
		tenant := h.createOrg(admin, "lookahead")
		agent, _, _ := c3VictimFixture(t, m, st, h, admin, tenant)
		c3Deliver(t, m, c3Event(tenant, "victim-three", "label", nil, c3AgentEdge("victim", "/v/three", baseTime)), nil)
		three := c3ReceiptID(t, st, tenant, "victim-three")
		ids := []string{c3ReceiptID(t, st, tenant, "victim-one"), c3ReceiptID(t, st, tenant, "victim-two"), three}
		sort.Strings(ids)
		if ids[2] != three {
			t.Fatalf("fixture: the third receipt must sort last, got %v", ids)
		}
		c3Update(t, st, tenant, observationReceiptKind, isReceipt(three), func(r model.Record) { r[colMemberCount] = int64(0) })
		path := c3ObservationsPath(kindAgent, agent)
		items, cursor, more := c3Items(t, h.do("GET", path+"?limit=2", admin, nil, tenant))
		if len(items) != 2 || !more || cursor != ids[1] {
			t.Fatalf("page before the corrupt receipt = %d items more=%v cursor=%q", len(items), more, cursor)
		}
		if r := h.do("GET", path+"?limit=2&cursor="+cursor, admin, nil, tenant); r.code != http.StatusInternalServerError {
			t.Fatalf("page composing the corrupt receipt = %d %s, want 500", r.code, r.raw)
		}
		if r := h.do("GET", path, admin, nil, tenant); r.code != http.StatusInternalServerError || r.body["items"] != nil {
			t.Fatalf("whole page with the corrupt receipt = %d %s, want 500 and no partial list", r.code, r.raw)
		}
	})
}

// --- B2: the operator log names the invariant, never the corrupt value -----------

// c3LogSink is the module logger under test: a slog text handler over a buffer. The
// handler writes from the request goroutine, which httptest runs on the test's own
// goroutine, but the sink locks anyway so the race detector has nothing to say.
type c3LogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *c3LogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *c3LogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *c3LogSink) Reset() {
	s.mu.Lock()
	s.buf.Reset()
	s.mu.Unlock()
}

// TestC3ObservationsRefusalLogNamesInvariantNotValue plants a distinctive canary
// INSIDE the corrupt stored value and proves that the refusal is a generic 500 to the
// client, that the operator log carries the static invariant name (and the receipt
// id when it is canonical), and that the canary reaches neither the log nor the
// response. time.ParseError and JSON decoder messages echo their input; this is the
// control that they are no longer concatenated into what gets logged.
func TestC3ObservationsRefusalLogNamesInvariantNotValue(t *testing.T) {
	m, st, _ := newInv(t)
	sink := &c3LogSink{}
	m.log = slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()

	cases := []struct {
		name         string
		canary       string
		reason       evidenceReason
		receiptInLog bool // the canonical receipt id is expected in the log line
		corrupt      func(tenant model.TenantID, one, two string, canary string)
	}{
		{"first reception instant carries the canary", "CANARY-first-seen-7c1e4a", reasonFirstReception, true,
			func(tenant model.TenantID, one, _ string, canary string) {
				c3Update(t, st, tenant, observationReceiptKind,
					func(r model.Record) bool { return r.String(model.ColID) == one },
					func(r model.Record) { r[colFirstSeen] = canary })
			}},
		{"receipt facts carry the canary", "CANARY-facts-0b3d92", reasonFactsNotV1, true,
			func(tenant model.TenantID, one, _ string, canary string) {
				c3Update(t, st, tenant, observationReceiptKind,
					func(r model.Record) bool { return r.String(model.ColID) == one },
					func(r model.Record) {
						text := `{"version":1,"` + canary + `":true}`
						r[colFacts] = text
						r[colFactsHash] = digest([]byte(text))
					})
			}},
		{"member facts carry the canary", "CANARY-member-5e71c0", reasonMemberNotV1, true,
			func(tenant model.TenantID, one, _ string, canary string) {
				c3Update(t, st, tenant, observationMemberKind,
					func(r model.Record) bool {
						return r.String(colReceiptID) == one && r.String(colEntityKind) == kindAgent
					},
					func(r model.Record) { r[colFacts] = `{"kind":"agent","` + canary + `":1}` })
			}},
		{"member receipt_id column carries the canary", "CANARY-receipt-id-9d2f11", reasonReceiptIDNotCanonical, false,
			func(tenant model.TenantID, _, two string, canary string) {
				c3Update(t, st, tenant, observationMemberKind,
					func(r model.Record) bool {
						return r.String(colReceiptID) == two && r.String(colEntityKind) == kindAgent
					},
					func(r model.Record) { r[colReceiptID] = canary })
			}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenant := h.createOrg(admin, fmt.Sprintf("canary-%02d", i))
			agent, one, two := c3VictimFixture(t, m, st, h, admin, tenant)
			tc.corrupt(tenant, one, two, tc.canary)
			sink.Reset()
			r := h.do("GET", c3ObservationsPath(kindAgent, agent), admin, nil, tenant)
			if r.code != http.StatusInternalServerError || !strings.Contains(r.raw, "internal error") || r.body["items"] != nil {
				t.Fatalf("response = %d %s, want the generic 500", r.code, r.raw)
			}
			logged := sink.String()
			if !strings.Contains(logged, "observation history refused") || !strings.Contains(logged, string(tc.reason)) {
				t.Fatalf("log lacks the refusal or the static invariant %q:\n%s", tc.reason, logged)
			}
			if !strings.Contains(logged, "tenant="+tenant.String()) || !strings.Contains(logged, "entity_id="+agent.String()) {
				t.Fatalf("log lacks tenant/entity context:\n%s", logged)
			}
			if tc.receiptInLog && !strings.Contains(logged, one) {
				t.Fatalf("log lacks the canonical receipt id %s:\n%s", one, logged)
			}
			if !tc.receiptInLog && !strings.Contains(logged, receiptUnnamed) {
				t.Fatalf("log does not mark the receipt as unnamed:\n%s", logged)
			}
			for _, where := range map[string]string{"log": logged, "response": r.raw} {
				if strings.Contains(where, tc.canary) || strings.Contains(where, "CANARY") {
					t.Fatalf("the corrupt value reached the %s:\n%s", map[bool]string{true: "log", false: "response"}[where == logged], where)
				}
			}
			// The positive control: a fresh, uncorrupted entity in the same tenant
			// logs nothing at all and answers 200.
			sink.Reset()
			c3Deliver(t, m, c3Event(tenant, "clean-"+tc.canary, "label", nil, c3AgentEdge("clean", "/clean", baseTime)), nil)
			clean := c3EntityID(t, st, tenant, kindAgent, "clean")
			if items, _, _ := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, clean), admin, nil, tenant)); len(items) != 1 || sink.String() != "" {
				t.Fatalf("clean entity = %d items, log %q; want 1 item and an empty log", len(items), sink.String())
			}
		})
	}
}

// --- control 6: SQLite reopen adds the two read indexes and uses them -------------

// c3PreC3Registry registers the descriptors WITHOUT the two C3 read indexes: the
// schema an existing deployment has on disk before this change.
type c3PreC3Registry struct{ store.ExtensionRegistry }

func (r c3PreC3Registry) Register(d model.EntityDescriptor) error {
	kept := make([]model.IndexSpec, 0, len(d.Indexes))
	for _, ix := range d.Indexes {
		if ix.Name == "inventory_member_entity_receipt" || ix.Name == "inventory_conflict_receipt_page" {
			continue
		}
		kept = append(kept, ix)
	}
	d.Indexes = kept
	return r.ExtensionRegistry.Register(d)
}

func c3SQLiteIndexes(t *testing.T, path, table string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ?", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	return out
}

func c3SQLiteExplain(t *testing.T, path, statement string, args ...any) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("ANALYZE"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, statement)
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		out.WriteString(detail + "\n")
	}
	return out.String()
}

func TestC3ObservationsSQLiteReopenAddsReadIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c3.db")
	ctx := context.Background()
	// 1. A deployment on the pre-C3 schema, with evidence written by the C1 writer.
	old := New(WithClock(pinnedClock{at: baseTime.Add(time.Hour)}))
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, func(reg store.ExtensionRegistry) error {
		return old.RegisterSchema(c3PreC3Registry{reg})
	})
	if err != nil {
		t.Fatal(err)
	}
	old.UseData(api.NewModuleData(st))
	tenant := c1Tenant(t, st, "reopen")
	for i := 0; i < 3; i++ {
		c3Deliver(t, old, c3Event(tenant, fmt.Sprintf("re-%d", i), "label", c3Reg("source-a", 1, "env-a"), c3AgentEdge("reopen", fmt.Sprintf("/r/%d", i), baseTime)), nil)
	}
	c3Deliver(t, old, c3Event(tenant, "re-0", "label", c3Reg("source-a", 1, "env-a"), c3AgentEdge("reopen", "/conflict", baseTime)), ErrObservationReceiptConflict)
	agent := c3EntityID(t, st, tenant, kindAgent, "reopen")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before := c3SQLiteIndexes(t, path, "inventory_observation_member")
	if before["inventory_member_entity_receipt"] || !before["inventory_member_uniq"] {
		t.Fatalf("pre-C3 member indexes = %v", before)
	}
	if c3SQLiteIndexes(t, path, "inventory_observation_conflict")["inventory_conflict_receipt_page"] {
		t.Fatal("pre-C3 conflict table already had the C3 index")
	}

	// 2. Reopen with the current schema: the reconciler adds exactly the two indexes.
	m := New(WithClock(pinnedClock{at: baseTime.Add(time.Hour)}))
	st, err = engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m.UseData(api.NewModuleData(st))
	members := c3SQLiteIndexes(t, path, "inventory_observation_member")
	conflicts := c3SQLiteIndexes(t, path, "inventory_observation_conflict")
	if !members["inventory_member_entity_receipt"] || !members["inventory_member_uniq"] || !conflicts["inventory_conflict_receipt_page"] || !conflicts["inventory_conflict_uniq"] {
		t.Fatalf("after reopen: member indexes %v, conflict indexes %v", members, conflicts)
	}
	t.Logf("C3_SQLITE_INDEXES member=%v conflict=%v", members, conflicts)

	// 3. The page reads the pre-existing evidence correctly through HTTP.
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	items, _, _ := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, agent), admin, nil, tenant))
	if len(items) != 3 {
		t.Fatalf("reopened history = %d items, want 3", len(items))
	}
	conflicting := 0
	for _, it := range items {
		c3AssertAllowlist(t, it)
		if it["conflicting_redelivery"] == true {
			conflicting++
		}
	}
	if conflicting != 1 {
		t.Fatalf("conflicting_redelivery flagged on %d items, want exactly 1", conflicting)
	}

	// 4. The focal statements use the indexes (SEARCH, not SCAN of the table).
	projection := c3SQLiteExplain(t, path, "SELECT DISTINCT receipt_id FROM inventory_observation_member WHERE tenant_id = ? AND receipt_id IS NOT NULL AND entity_kind = ? AND entity_id = ? ORDER BY receipt_id ASC LIMIT 26",
		tenant.String(), kindAgent, agent.String())
	conflict := c3SQLiteExplain(t, path, "SELECT id FROM inventory_observation_conflict WHERE tenant_id = ? AND receipt_id = ? ORDER BY id ASC LIMIT 2",
		tenant.String(), c3ReceiptID(t, st, tenant, "re-0"))
	t.Logf("C3_SQLITE_PLAN projection:\n%s", projection)
	t.Logf("C3_SQLITE_PLAN conflict:\n%s", conflict)
	if !strings.Contains(projection, "inventory_member_entity_receipt") || strings.Contains(projection, "SCAN inventory_observation_member") {
		t.Fatalf("projection does not use the member read index:\n%s", projection)
	}
	if !strings.Contains(conflict, "inventory_conflict_receipt_page") || strings.Contains(conflict, "SCAN inventory_observation_conflict") {
		t.Fatalf("conflict lookup does not use the conflict read index:\n%s", conflict)
	}
}
