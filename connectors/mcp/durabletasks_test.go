// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const durableTestTenant = "tenant-durable"

// The shared in-memory authority models the composed adapter for task-method
// tests that are not about the optional communication seam itself. Keeping the
// method here (rather than on the production port) makes those fixtures opt in
// explicitly while the negative test below can still wrap only
// DurableTaskStore and prove the connector's deny-closed type assertion.
func (s *memoryDurableTaskStore) PrepareInputResponses(
	ctx context.Context,
	owner TaskOwner,
	batch DurableTaskInputResponseBatch,
) error {
	if _, err := s.Get(ctx, owner, batch.TaskID, batch.Generation); err != nil {
		return err
	}
	if strings.TrimSpace(batch.OperationID) == "" || strings.TrimSpace(batch.EffectDigest) == "" ||
		len(batch.Responses) == 0 {
		return errors.New("invalid durable input-response batch")
	}
	return nil
}

func newDurableTaskRS(t *testing.T, jwks []byte, store DurableTaskStore, upstream Upstream) *ResourceServer {
	t.Helper()
	toolset, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer},
		Issuer: rsIssuer, IssuerJWKS: jwks, Tenant: durableTestTenant,
		Toolset: toolset, Upstream: upstream, Auditor: &taskAuditor{}, Clock: rsClock,
		DurableTaskStore: store, DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new durable task RS: %v", err)
	}
	return rs
}

func durableTestOwner() TaskOwner {
	return TaskOwner{Tenant: durableTestTenant, Issuer: rsIssuer, Subject: "agent:claude"}
}

func TestDurableTasksNilStoreHidesCapabilityAndRefusesCreation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "initialize":
			return json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{"tools":{},"tasks":{},"extensions":{"io.modelcontextprotocol/tasks":{},"com.example/custom":{}}}}`), nil
		case "tools/call":
			return json.RawMessage(`{"resultType":"task","taskId":"must-not-exist","status":"working"}`), nil
		default:
			return json.RawMessage(normativeCompleteResult), nil
		}
	}}
	rs := newDurableTaskRS(t, jwks, nil, up)

	discover := httptest.NewRecorder()
	rs.ServeHTTP(discover, taskReq(token, "initialize", `{}`))
	if discover.Code != http.StatusOK {
		t.Fatalf("initialize status = %d; body=%s", discover.Code, discover.Body.String())
	}
	var envelope struct {
		Result struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(discover.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if _, advertised := envelope.Result.Capabilities["tasks"]; advertised {
		t.Fatal("legacy Tasks capability was advertised without durable persistence")
	}
	var extensions map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Result.Capabilities["extensions"], &extensions); err != nil {
		t.Fatalf("decode extensions: %v", err)
	}
	if _, advertised := extensions[extensionTasks]; advertised {
		t.Fatal("Tasks extension was advertised without durable persistence")
	}
	if _, preserved := extensions["com.example/custom"]; !preserved {
		t.Fatal("projecting Tasks removed an unrelated extension")
	}

	created := httptest.NewRecorder()
	rs.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusServiceUnavailable {
		t.Fatalf("Tasks-declared tools/call status = %d, want 503; body=%s", created.Code, created.Body.String())
	}
	if got := up.count("tools/call"); got != 0 {
		t.Fatalf("Tasks-declared tools/call reached upstream %d times without persistence", got)
	}

	get := httptest.NewRecorder()
	rs.ServeHTTP(get, taskReq(token, methodTasksGet, `{"taskId":"unknown"}`))
	if get.Code != http.StatusServiceUnavailable {
		t.Fatalf("tasks/get status = %d, want 503 when Tasks is disabled; body=%s", get.Code, get.Body.String())
	}
}

func TestDurableTasksNilStoreKeepsSynchronousStandaloneCalls(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		return json.RawMessage(normativeCompleteResult), nil
	}}
	rs := newDurableTaskRS(t, jwks, nil, up)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, "tools/call", `{"name":"search","arguments":{}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("standalone synchronous tools/call status = %d; body=%s", w.Code, w.Body.String())
	}
	if got := up.count("tools/call"); got != 1 {
		t.Fatalf("standalone synchronous tools/call forwards = %d, want 1", got)
	}
}

type orderedDurableStore struct {
	*memoryDurableTaskStore
	events *[]string
}

func (s *orderedDurableStore) Register(ctx context.Context, intent DurableTaskIntent) (DurableTaskRef, error) {
	*s.events = append(*s.events, "register")
	return s.memoryDurableTaskStore.Register(ctx, intent)
}

type orderedWriter struct {
	*httptest.ResponseRecorder
	events *[]string
}

func (w *orderedWriter) Write(body []byte) (int, error) {
	*w.events = append(*w.events, "respond")
	return w.ResponseRecorder.Write(body)
}

type orderedInterruptStore struct {
	DurableTaskStore
	events   *[]string
	prepared []DurableTaskInputResponseBatch
	err      error
}

func (s *orderedInterruptStore) PrepareInputResponses(
	_ context.Context,
	_ TaskOwner,
	batch DurableTaskInputResponseBatch,
) error {
	*s.events = append(*s.events, "prepare")
	s.prepared = append(s.prepared, batch)
	return s.err
}

// durableTaskStoreWithoutInterrupt deliberately narrows the method set of its
// backing authority to the base persistence port. The dynamic backing value may
// implement the optional sub-port, but the ResourceServer sees only this
// wrapper and therefore must fail closed for non-empty inputResponses.
type durableTaskStoreWithoutInterrupt struct{ DurableTaskStore }

func TestDurableTaskRegistersBeforeHandleResponse(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	events := []string{}
	store := &orderedDurableStore{memoryDurableTaskStore: newMemoryDurableTaskStore(), events: &events}
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			events = append(events, "forward")
			return json.RawMessage(`{"resultType":"task","taskId":"task-ordered","status":"working"}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	rs := newDurableTaskRS(t, jwks, store, up)
	w := &orderedWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d; body=%s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(events, []string{"forward", "register", "respond"}) {
		t.Fatalf("effect order = %v, want forward → durable register → response", events)
	}
	view, err := store.Get(context.Background(), durableTestOwner(), "task-ordered", 0)
	if err != nil {
		t.Fatalf("durable task after response: %v", err)
	}
	if view.Ref.Generation <= 0 || view.Ref.BindingID == "" || view.Ref.WorkItemID == "" || view.Ref.SID == "" {
		t.Fatalf("durable ref incomplete: %+v", view.Ref)
	}
	cached, ok := rs.taskLedger.get("task-ordered")
	if !ok || cached.DurableRef != view.Ref || cached.Generation != durableGenerationToken(view.Ref.Generation) {
		t.Fatalf("task cache is not the durable projection: ok=%v cached=%+v view=%+v", ok, cached, view)
	}
}

func TestDurableTaskInputResponsesPrepareBeforeUpstream(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	events := []string{}
	store := &orderedInterruptStore{
		DurableTaskStore: newMemoryDurableTaskStore(), events: &events,
	}
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return json.RawMessage(`{"resultType":"task","taskId":"task-input-order","status":"working"}`), nil
		case methodTasksUpdate:
			events = append(events, "forward")
			return json.RawMessage(normativeCompleteResult), nil
		default:
			return json.RawMessage(`{}`), nil
		}
	}}
	rs := newDurableTaskRS(t, jwks, store, up)
	created := httptest.NewRecorder()
	rs.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusOK {
		t.Fatalf("task creation status = %d; body=%s", created.Code, created.Body.String())
	}

	events = events[:0]
	updated := httptest.NewRecorder()
	rs.ServeHTTP(updated, taskReq(token, methodTasksUpdate,
		`{"taskId":"task-input-order","inputResponses":{"need":{"answer":"ready"}},`+
			`"_meta":{"ai.olivares/operationId":"input-order-operation"}}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("tasks/update status = %d; body=%s", updated.Code, updated.Body.String())
	}
	if !reflect.DeepEqual(events, []string{"prepare", "forward"}) {
		t.Fatalf("input-response order = %v, want prepare → forward", events)
	}
	if len(store.prepared) != 1 {
		t.Fatalf("prepared batches = %d, want 1", len(store.prepared))
	}
	batch := store.prepared[0]
	if batch.TaskID != "task-input-order" || batch.Generation < 1 ||
		batch.OperationID == "" || batch.EffectDigest == "" || len(batch.Responses) != 1 ||
		len(batch.Responses[0].KeyDigest) != sha256.Size*2 ||
		len(batch.Responses[0].ContentDigest) != sha256.Size*2 {
		t.Fatalf("prepared input-response batch = %+v", batch)
	}
}

func TestDurableTaskInputResponsesRequireInterruptSubport(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	base := newMemoryDurableTaskStore()
	store := &durableTaskStoreWithoutInterrupt{DurableTaskStore: base}
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(`{"resultType":"task","taskId":"task-input-no-port","status":"working"}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	rs := newDurableTaskRS(t, jwks, store, up)
	created := httptest.NewRecorder()
	rs.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusOK {
		t.Fatalf("task creation status = %d; body=%s", created.Code, created.Body.String())
	}

	updated := httptest.NewRecorder()
	rs.ServeHTTP(updated, taskReq(token, methodTasksUpdate,
		`{"taskId":"task-input-no-port","inputResponses":{"need":{"answer":"ready"}}}`))
	if updated.Code != http.StatusServiceUnavailable {
		t.Fatalf("tasks/update status = %d, want 503; body=%s", updated.Code, updated.Body.String())
	}
	if got := up.count(methodTasksUpdate); got != 0 {
		t.Fatalf("tasks/update reached upstream %d times without interrupt sub-port", got)
	}
}

func TestDurableTaskRestartRehydratesAndReanchorsLifecycle(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	store := newMemoryDurableTaskStore()
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return json.RawMessage(`{"resultType":"task","taskId":"task-restart","status":"working","ttlMs":60000}`), nil
		case methodTasksGet:
			return json.RawMessage(`{"resultType":"complete","taskId":"task-restart","status":"working","createdAt":"2026-08-18T10:00:00Z","lastUpdatedAt":"2026-08-18T10:00:01Z","ttlMs":60000,"pollIntervalMs":2500}`), nil
		case methodTasksUpdate, methodTasksCancel:
			return json.RawMessage(normativeCompleteResult), nil
		default:
			return json.RawMessage(`{}`), nil
		}
	}}
	first := newDurableTaskRS(t, jwks, store, up)
	created := httptest.NewRecorder()
	first.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusOK {
		t.Fatalf("task creation status = %d; body=%s", created.Code, created.Body.String())
	}
	original, err := store.Get(context.Background(), durableTestOwner(), "task-restart", 0)
	if err != nil {
		t.Fatalf("get registered durable task: %v", err)
	}

	// A new ResourceServer has an empty process ledger before construction. Its
	// startup List must rebuild the exact durable generation and inventory.
	restarted := newDurableTaskRS(t, jwks, store, up)
	cached, ok := restarted.taskLedger.get("task-restart")
	if !ok || cached.DurableRef != original.Ref || !cached.HandleRelayed {
		t.Fatalf("restart did not conservatively rehydrate task: ok=%v cached=%+v", ok, cached)
	}

	get := httptest.NewRecorder()
	restarted.ServeHTTP(get, taskReq(token, methodTasksGet, `{"taskId":"task-restart"}`))
	if get.Code != http.StatusOK {
		t.Fatalf("rehydrated tasks/get status = %d; body=%s", get.Code, get.Body.String())
	}
	view, err := store.Get(context.Background(), durableTestOwner(), "task-restart", original.Ref.Generation)
	if err != nil {
		t.Fatalf("get post-observation durable task: %v", err)
	}
	if view.Observation.Kind != DurableTaskObservationGet || view.Observation.Verdict != DurableTaskVerdictClean ||
		view.Observation.Status != taskStatusWorking || view.Observation.ResultDigest == "" ||
		view.Observation.PollIntervalMs == nil || *view.Observation.PollIntervalMs != 2500 {
		t.Fatalf("tasks/get observation not persisted: %+v", view.Observation)
	}

	update := httptest.NewRecorder()
	restarted.ServeHTTP(update, taskReq(token, methodTasksUpdate, `{"taskId":"task-restart"}`))
	if update.Code != http.StatusOK {
		t.Fatalf("rehydrated tasks/update status = %d; body=%s", update.Code, update.Body.String())
	}
	view, _ = store.Get(context.Background(), durableTestOwner(), "task-restart", original.Ref.Generation)
	if view.Observation.Kind != DurableTaskObservationUpdate || !view.Observation.Acknowledged {
		t.Fatalf("tasks/update observation not persisted: %+v", view.Observation)
	}

	cancel := httptest.NewRecorder()
	restarted.ServeHTTP(cancel, taskReq(token, methodTasksCancel, `{"taskId":"task-restart"}`))
	if cancel.Code != http.StatusOK {
		t.Fatalf("rehydrated tasks/cancel status = %d; body=%s", cancel.Code, cancel.Body.String())
	}
	view, _ = store.Get(context.Background(), durableTestOwner(), "task-restart", original.Ref.Generation)
	if view.Observation.Kind != DurableTaskObservationCancel || !view.Observation.Acknowledged ||
		!view.Observation.CancelRequested || view.Observation.Status != taskCancelRequestedStatus {
		t.Fatalf("tasks/cancel observation not persisted: %+v", view.Observation)
	}
	if got := up.count("tools/call"); got != 1 {
		t.Fatalf("restart re-created task: tools/call forwards=%d, want 1", got)
	}
}

func TestDurableTaskAuthorityFailureNeverFallsBackToCache(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	store := newMemoryDurableTaskStore()
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(`{"resultType":"task","taskId":"task-no-fallback","status":"working"}`), nil
		}
		return json.RawMessage(`{"resultType":"complete","taskId":"task-no-fallback","status":"working","createdAt":"2026-08-18T10:00:00Z","lastUpdatedAt":"2026-08-18T10:00:01Z","ttlMs":null}`), nil
	}}
	rs := newDurableTaskRS(t, jwks, store, up)
	created := httptest.NewRecorder()
	rs.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusOK {
		t.Fatalf("task creation status = %d; body=%s", created.Code, created.Body.String())
	}
	store.getErr = errors.New("store unavailable")
	get := httptest.NewRecorder()
	rs.ServeHTTP(get, taskReq(token, methodTasksGet, `{"taskId":"task-no-fallback"}`))
	if get.Code != http.StatusServiceUnavailable {
		t.Fatalf("tasks/get status = %d, want 503 on durable Get failure; body=%s", get.Code, get.Body.String())
	}
	if got := up.count(methodTasksGet); got != 0 {
		t.Fatalf("durable Get failure fell back to cache and forwarded %d times", got)
	}
}

func TestDurableTaskObservationFailureWithholdsResultAndCacheMutation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	store := newMemoryDurableTaskStore()
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(`{"resultType":"task","taskId":"task-observation","status":"working"}`), nil
		}
		return json.RawMessage(`{"resultType":"complete","taskId":"task-observation","status":"completed","createdAt":"2026-08-18T10:00:00Z","lastUpdatedAt":"2026-08-18T10:00:01Z","ttlMs":null,"result":{}}`), nil
	}}
	rs := newDurableTaskRS(t, jwks, store, up)
	created := httptest.NewRecorder()
	rs.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusOK {
		t.Fatalf("task creation status = %d; body=%s", created.Code, created.Body.String())
	}
	store.updateErr = errors.New("observation unavailable")
	get := httptest.NewRecorder()
	rs.ServeHTTP(get, taskReq(token, methodTasksGet, `{"taskId":"task-observation"}`))
	if get.Code != http.StatusServiceUnavailable || strings.Contains(get.Body.String(), `"status":"completed"`) {
		t.Fatalf("tasks/get observation failure was released: status=%d body=%s", get.Code, get.Body.String())
	}
	cached, ok := rs.taskLedger.get("task-observation")
	if !ok || cached.Status != taskStatusWorking {
		t.Fatalf("cache mutated before durable observation: ok=%v record=%+v", ok, cached)
	}
}

func TestDurableTaskServerCancelPersistsBeforeCacheMutation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	store := newMemoryDurableTaskStore()
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return json.RawMessage(`{"resultType":"task","taskId":"task-sweep","status":"working"}`), nil
		case methodTasksCancel:
			return json.RawMessage(normativeCompleteResult), nil
		default:
			return json.RawMessage(`{}`), nil
		}
	}}
	rs := newDurableTaskRS(t, jwks, store, up)
	created := httptest.NewRecorder()
	rs.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusOK {
		t.Fatalf("task creation status = %d; body=%s", created.Code, created.Body.String())
	}

	cancelled, err := rs.CancelActiveTasks(context.Background(), nil, "operator stop")
	if err != nil || cancelled != 1 {
		t.Fatalf("server cancellation = (%d, %v), want (1, nil)", cancelled, err)
	}
	view, err := store.Get(context.Background(), durableTestOwner(), "task-sweep", 0)
	if err != nil {
		t.Fatalf("durable task after server cancellation: %v", err)
	}
	if view.Observation.Kind != DurableTaskObservationCancel || !view.Observation.Acknowledged ||
		!view.Observation.CancelRequested || view.Observation.Status != taskCancelRequestedStatus ||
		view.Observation.OperationID == "" || view.Observation.ResultDigest == "" {
		t.Fatalf("server cancellation observation not persisted: %+v", view.Observation)
	}
	cached, ok := rs.taskLedger.get("task-sweep")
	if !ok || cached.Status != taskCancelRequestedStatus {
		t.Fatalf("server cancellation cache not advanced after durable write: ok=%v record=%+v", ok, cached)
	}
}

func TestDurableTaskServerCancelObservationFailureIsConservative(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	store := newMemoryDurableTaskStore()
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(`{"resultType":"task","taskId":"task-sweep-failure","status":"working"}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	rs := newDurableTaskRS(t, jwks, store, up)
	created := httptest.NewRecorder()
	rs.ServeHTTP(created, toolsCallReq(token, "search", `{}`))
	if created.Code != http.StatusOK {
		t.Fatalf("task creation status = %d; body=%s", created.Code, created.Body.String())
	}
	store.updateErr = errors.New("observation unavailable")

	cancelled, err := rs.CancelActiveTasks(context.Background(), nil, "operator stop")
	if err == nil || cancelled != 0 {
		t.Fatalf("server cancellation = (%d, %v), want (0, error)", cancelled, err)
	}
	cached, ok := rs.taskLedger.get("task-sweep-failure")
	if !ok || cached.Status != taskStatusWorking || !cached.CancelUnconfirmed || !cached.Quarantined {
		t.Fatalf("failed durable observation was not retained conservatively: ok=%v record=%+v", ok, cached)
	}
}

func TestDurableTaskBootstrapFailureRefusesResourceServer(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	store := newMemoryDurableTaskStore()
	store.listErr = errors.New("inventory unavailable")
	toolset, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	_, err = NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer},
		Issuer: rsIssuer, IssuerJWKS: jwks, Tenant: durableTestTenant,
		Toolset: toolset, DurableTaskStore: store,
	})
	if err == nil || !strings.Contains(err.Error(), "rehydrate durable tasks") {
		t.Fatalf("bootstrap error = %v, want durable rehydration refusal", err)
	}
}
