// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

const mcpFinalRevision = "2026-07-28"

type mcpRestartWorkAuthority struct{}

func (mcpRestartWorkAuthority) ResolveParticipant(
	_ context.Context,
	_ model.TenantID,
	_ model.ID,
	kind, ref string,
) (sessions.Participant, error) {
	return sessions.Participant{
		Kind: kind, CanonicalRef: ref, Active: true, WorkspaceEligible: true,
	}, nil
}

func (mcpRestartWorkAuthority) SessionActsForAgent(
	context.Context,
	model.TenantID,
	string,
	string,
) (bool, error) {
	return true, nil
}

type mcpRestartContentGuard struct{}

func (mcpRestartContentGuard) Inspect(
	context.Context,
	model.TenantID,
	model.ID,
	string,
	[]byte,
) (sessions.ContentDecision, error) {
	return sessions.ContentDecision{Allowed: true, Code: "fixture_content_allowed"}, nil
}

type mcpRestartSpecValidator struct{}

func (mcpRestartSpecValidator) ValidateProtocolBindingSpec(
	_ context.Context,
	_ model.TenantID,
	_ sessions.ProtocolBindingSpecInput,
) (sessions.ProtocolBindingValidation, error) {
	return sessions.ProtocolBindingValidation{
		Verdict: sessions.ProtocolObservationClean, Code: "fixture_peer_validated",
		ObservedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
	}, nil
}

type mcpRestartUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  map[string]int
	err    error
}

func newMCPRestartUpstream(t *testing.T, taskID string, createdAt time.Time) *mcpRestartUpstream {
	t.Helper()
	createdAtJSON, err := json.Marshal(createdAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	updatedAtJSON, err := json.Marshal(createdAt.UTC().Add(time.Second).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	upstream := &mcpRestartUpstream{calls: make(map[string]int)}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				TaskID string `json:"taskId"`
				Meta   struct {
					Version      string `json:"io.modelcontextprotocol/protocolVersion"`
					Capabilities struct {
						Extensions map[string]json.RawMessage `json:"extensions"`
					} `json:"io.modelcontextprotocol/clientCapabilities"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			upstream.mu.Lock()
			upstream.err = err
			upstream.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		upstream.mu.Lock()
		upstream.calls[envelope.Method]++
		upstream.mu.Unlock()
		if strings.HasPrefix(envelope.Method, "tasks/") {
			_, declaresTasks := envelope.Params.Meta.Capabilities.Extensions["io.modelcontextprotocol/tasks"]
			if envelope.Params.TaskID != taskID || envelope.Params.Meta.Version != mcpFinalRevision ||
				!declaresTasks || r.Header.Get("MCP-Protocol-Version") != mcpFinalRevision ||
				r.Header.Get("Mcp-Method") != envelope.Method || r.Header.Get("Mcp-Name") != taskID {
				upstream.mu.Lock()
				upstream.err = fmt.Errorf("non-conforming final tasks request: method=%s params=%+v headers=%v",
					envelope.Method, envelope.Params, r.Header)
				upstream.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		id := envelope.ID
		if len(id) == 0 {
			id = json.RawMessage(`1`)
		}
		result := `{"resultType":"complete"}`
		switch envelope.Method {
		case "tools/call":
			result = `{"resultType":"task","taskId":"` + taskID + `","status":"working","ttlMs":60000,"pollIntervalMs":1000}`
		case "tasks/get":
			result = `{"resultType":"complete","taskId":"` + taskID + `","status":"working",` +
				`"createdAt":` + string(createdAtJSON) + `,"lastUpdatedAt":` + string(updatedAtJSON) + `,` +
				`"ttlMs":60000,"pollIntervalMs":1000}`
		case "tasks/cancel":
			result = `{}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result)
	}))
	return upstream
}

func (u *mcpRestartUpstream) count(method string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[method]
}

func (u *mcpRestartUpstream) failure() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.err
}

func newMCPRestartSessionsModule() *sessions.Module {
	module := sessions.New(
		sessions.WithWorkIdentityResolver(mcpRestartWorkAuthority{}),
		sessions.WithWorkContentGuard(mcpRestartContentGuard{}),
	)
	module.UseProtocolBindingSpecValidator(sessions.BindingProtocolMCP, mcpRestartSpecValidator{})
	return module
}

func activateMCPRestartSpec(
	t *testing.T,
	module *sessions.Module,
	tenant model.TenantID,
	workspace model.ID,
	peerAuthority string,
) sessions.ProtocolBindingSpec {
	t.Helper()
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: "mcp-final-restart", Generation: 1,
		Protocol: sessions.BindingProtocolMCP, ProtocolVersion: mcpFinalRevision,
		Direction: sessions.BindingOutbound, LocalKind: sessions.BindingLocalWorkItem,
		LocalSelector: json.RawMessage(`{"work_kind":"operations"}`),
		PeerAuthority: peerAuthority, RemoteResourceKind: "tasks",
		RemoteResourceRef: "resource-server:restart",
		MappingSchema:     sessions.ProtocolBindingMappingSchemaV1,
		Mapping: []sessions.ProtocolMappingRule{{
			Source: "task.summary", Target: "work.brief",
			Cardinality: sessions.ProtocolMappingOneToOne, Transform: sessions.ProtocolTransformText,
		}},
		KnownLosses: []sessions.ProtocolBindingLoss{}, RuleRefs: []string{"rule:mcp-task"},
		PermissionProfileRef: "permission:mcp-task", CurrencyPolicy: sessions.BindingCurrencyPinned,
		Validation: sessions.ProtocolBindingValidation{
			Verdict: sessions.ProtocolObservationClean, Code: "fixture_peer_validated",
			ObservedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		},
	}
	draft := sessions.ProtocolBindingSpecCommand{
		Operation: sessions.ProtocolBindingSpecCreateDraft, WorkspaceID: workspace,
		Input: &input, IdempotencyKey: model.NewID().String(),
	}
	plan, err := module.PlanProtocolBindingSpec(context.Background(), tenant, draft)
	if err != nil {
		t.Fatalf("plan restart MCP spec: %v", err)
	}
	draft.ExpectedPlanHash = plan.PlanHash
	created, err := module.ApplyProtocolBindingSpec(context.Background(), tenant, draft)
	if err != nil {
		t.Fatalf("create restart MCP spec: %v", err)
	}
	activate := sessions.ProtocolBindingSpecCommand{
		Operation: sessions.ProtocolBindingSpecActivate, WorkspaceID: workspace,
		SpecID: created.Spec.ID, ExpectedVersion: created.Spec.Version,
	}
	activationPlan, err := module.PlanProtocolBindingSpec(context.Background(), tenant, activate)
	if err != nil {
		t.Fatalf("plan restart MCP spec activation: %v", err)
	}
	activate.ExpectedPlanHash = activationPlan.PlanHash
	active, err := module.ApplyProtocolBindingSpec(context.Background(), tenant, activate)
	if err != nil {
		t.Fatalf("activate restart MCP spec: %v", err)
	}
	return active.Spec
}

func mcpFinalRequestMeta(operationID string) string {
	return `"_meta":{` +
		`"ai.olivares/operationId":"` + operationID + `",` +
		`"io.modelcontextprotocol/protocolVersion":"` + mcpFinalRevision + `",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"olivares-restart-fixture","version":"1"},` +
		`"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`
}

func mcpFinalToolsCallEnvelope(operationID string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"name":"search","arguments":{"q":"restart"},` + mcpFinalRequestMeta(operationID) + `}}`)
}

func postMCPFinalRequest(
	t *testing.T,
	rs *mcpc.ResourceServer,
	token string,
	method, name string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, mcpReviewResource, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", mcpFinalRevision)
	request.Header.Set("Mcp-Method", method)
	request.Header.Set("Mcp-Name", name)
	recorder := httptest.NewRecorder()
	rs.ServeHTTP(recorder, request)
	return recorder
}

func TestMCPDurableTaskSQLiteRestartKeepsFinalLifecycleAndCursor(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcp-durable-restart.db")
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	open := func() (*sessions.Module, store.Store) {
		module := newMCPRestartSessionsModule()
		st, openErr := coreengine.Open(ctx, store.Config{
			Engine: store.EngineSQLite, DSN: dbPath, SignEvent: signer.SignEvent,
		}, module.RegisterSchema)
		if openErr != nil {
			t.Fatalf("open restart SQLite store: %v", openErr)
		}
		module.UseData(api.NewModuleData(st))
		return module, st
	}

	module, firstStore := open()
	firstClosed := false
	t.Cleanup(func() {
		if !firstClosed {
			_ = firstStore.Close()
		}
	})
	var tenant model.TenantID
	if err := firstStore.System(ctx, func(system store.SystemScope) error {
		if _, ensureErr := system.EnsureSystemTenant(ctx); ensureErr != nil {
			return ensureErr
		}
		org, createErr := system.CreateOrg(ctx, model.Org{
			Name: "mcp-restart", Slug: "mcp-restart", Status: model.StatusActive,
		})
		if createErr == nil {
			tenant = org.TenantID
		}
		return createErr
	}); err != nil {
		t.Fatalf("provision restart tenant: %v", err)
	}
	var workspace model.ID
	if err := firstStore.View(ctx, tenant, func(scope store.Scope) error {
		value, readErr := scope.DefaultWorkspace(ctx)
		workspace = value.ID
		return readErr
	}); err != nil {
		t.Fatalf("read restart workspace: %v", err)
	}

	const taskID = "task-final-restart-a"
	taskCreatedAt := time.Now().UTC()
	upstream := newMCPRestartUpstream(t, taskID, taskCreatedAt)
	defer upstream.server.Close()
	upstreamDescriptor := fmt.Sprintf(
		"https-forward:%s|cred-provider:%T", upstream.server.URL, newUpstreamCredentialProvider(""),
	)
	activeSpec := activateMCPRestartSpec(t, module, tenant, workspace, upstream.server.URL)
	interruptRoute := sessions.ProtocolInterruptRoute{
		ChannelID: model.NewID(), SenderUserID: model.NewID(), RecipientUserID: model.NewID(),
	}
	storeConfig := mcpDurableTaskStoreConfig{
		WorkspaceID: workspace, BindingSpecID: activeSpec.ID, Generation: activeSpec.Generation,
		OwnerKind: "agent", OwnerRef: "agent:operations", InterruptRoute: interruptRoute,
		Policy: mcpTaskRuntimePolicy,
	}
	durableStore, err := newMCPDurableTaskStore(tenant, module, storeConfig)
	if err != nil {
		t.Fatalf("compose first durable task store: %v", err)
	}
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	gatewayConfig := &mcpGatewayConfig{
		Resource: mcpReviewResource, AuthorizationServers: []string{"https://auth.review.example"},
		Issuer: "https://auth.review.example", IssuerJWKS: json.RawMessage(jwks),
		Tenant: tenant.String(), UpstreamURL: upstream.server.URL,
		UpstreamRevision: mcpFinalRevision, NextRevisionHeaders: true,
		Tools: []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
	}
	firstMux := newProtocolBindingReconcileMux()
	firstRS, _, err := buildMCPResourceServerWithDurableTaskStore(
		&engine{
			store: firstStore, sessionsMod: module, log: discardLogger(),
			protocolBindingReconciler: firstMux,
		}, gatewayConfig, discardLogger(), durableStore,
	)
	if err != nil {
		t.Fatalf("compose first MCP Resource Server: %v", err)
	}
	toolsCall := mcpFinalToolsCallEnvelope("restart-create-1")
	validateMCPFinalFixtureAgainstPinnedSchema(t, "CallToolRequest", toolsCall)
	if response := postMCPFinalRequest(
		t, firstRS, token, "tools/call", "search", toolsCall,
	); response.Code != http.StatusOK {
		t.Fatalf("tools/call before restart = %d; body=%s", response.Code, response.Body.String())
	}
	owner := mcpc.TaskOwner{
		Tenant: tenant.String(), Issuer: "https://auth.review.example", Subject: "agent:review",
	}
	original, err := durableStore.Get(ctx, owner, taskID, 0)
	if err != nil {
		t.Fatalf("read task before restart: %v", err)
	}
	if _, err := durableStore.Register(ctx, mcpc.DurableTaskIntent{
		Owner: owner, TaskID: "task-final-restart-z", Tool: "search", RequiredScope: "tools:read",
		CreatedAt: taskCreatedAt,
		TTLMs:     mcpRestartInt64(60_000), PollIntervalMs: mcpRestartInt64(1_000),
		InitialStatus: "working", UpstreamDescriptor: upstreamDescriptor,
		ProtocolVersion: mcpFinalRevision, OriginOperationID: "restart-direct-register",
		OriginEffectDigest: strings.Repeat("d", 64),
	}); err != nil {
		t.Fatalf("register second durable inventory row: %v", err)
	}

	if err := firstStore.Close(); err != nil {
		t.Fatalf("close SQLite before restart: %v", err)
	}
	firstClosed = true
	module, reopened := open()
	t.Cleanup(func() { _ = reopened.Close() })
	restartedStore, err := newMCPDurableTaskStore(tenant, module, storeConfig)
	if err != nil {
		t.Fatalf("compose restarted durable task store: %v", err)
	}
	restartedMux := newProtocolBindingReconcileMux()
	restartedRS, _, err := buildMCPResourceServerWithDurableTaskStore(
		&engine{
			store: reopened, sessionsMod: module, log: discardLogger(),
			protocolBindingReconciler: restartedMux,
		}, gatewayConfig, discardLogger(), restartedStore,
	)
	if err != nil {
		t.Fatalf("compose restarted MCP Resource Server: %v", err)
	}
	if adapter, routeErr := restartedMux.route(sessions.BindingProtocolMCP); routeErr != nil {
		t.Fatalf("restart MCP protocol binding reconcile route: %v", routeErr)
	} else if _, ok := adapter.(*mcpProtocolBindingReconciler); !ok {
		t.Fatalf("restart MCP protocol binding reconcile route = %T", adapter)
	}

	firstPage, err := restartedStore.List(ctx, mcpc.TaskOwner{Tenant: tenant.String()}, "", 1)
	if err != nil || len(firstPage.Tasks) != 1 || firstPage.Tasks[0].Ref.TaskID != taskID ||
		firstPage.NextCursor == "" {
		t.Fatalf("restart inventory first page = %#v, %v", firstPage, err)
	}
	secondPage, err := restartedStore.List(
		ctx, mcpc.TaskOwner{Tenant: tenant.String()}, firstPage.NextCursor, 1,
	)
	if err != nil || len(secondPage.Tasks) != 1 ||
		secondPage.Tasks[0].Ref.TaskID != "task-final-restart-z" || secondPage.NextCursor != "" {
		t.Fatalf("restart inventory second page = %#v, %v", secondPage, err)
	}
	rehydrated, err := restartedStore.Get(ctx, owner, taskID, original.Ref.Generation)
	if err != nil || rehydrated.Ref != original.Ref {
		t.Fatalf("exact generation after restart = %#v, %v; want %#v", rehydrated.Ref, err, original.Ref)
	}

	request := func(method, operationID string) []byte {
		return []byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{` +
			`"taskId":"` + taskID + `",` + mcpFinalRequestMeta(operationID) + `}}`)
	}
	for _, call := range []struct {
		method      string
		operationID string
	}{
		{method: "tasks/get", operationID: "restart-get-1"},
		{method: "tasks/update", operationID: "restart-update-1"},
		{method: "tasks/cancel", operationID: "restart-cancel-1"},
	} {
		response := postMCPFinalRequest(
			t, restartedRS, token, call.method, taskID, request(call.method, call.operationID),
		)
		if response.Code != http.StatusOK {
			t.Fatalf("%s after restart = %d; body=%s", call.method, response.Code, response.Body.String())
		}
	}
	final, err := restartedStore.Get(ctx, owner, taskID, original.Ref.Generation)
	if err != nil || final.Observation.Kind != mcpc.DurableTaskObservationCancel ||
		!final.Observation.Acknowledged || !final.Observation.CancelRequested {
		t.Fatalf("final durable task projection = %#v, %v", final, err)
	}
	if upstream.count("tools/call") != 1 || upstream.count("tasks/get") != 1 ||
		upstream.count("tasks/update") != 1 || upstream.count("tasks/cancel") != 1 {
		t.Fatalf("upstream lifecycle counts = %#v", upstream.calls)
	}
	if err := upstream.failure(); err != nil {
		t.Fatal(err)
	}
}

func mcpRestartInt64(value int64) *int64 { return &value }

type mcpPinnedSchema struct {
	Defs map[string]json.RawMessage `json:"$defs"`
}

func validateMCPFinalFixtureAgainstPinnedSchema(t *testing.T, definition string, fixture []byte) {
	t.Helper()
	// ⛔ EL ESQUEMA PINADO SE LEE DE `testdata/`, NO DE `design/`, y no es una preferencia de
	// estilo: `design/` NO VIAJA en el export (scripts/export-public.sh lo excluye entero, junto
	// a `.claude`). Este test SI viaja, asi que sobre el arbol publicado abria un fichero que no
	// existe y moria con «read pinned MCP FINAL schema: open ../../design/…: no such file».
	//
	// Medido: fue el PRIMER fallo de test real de la race-full sobre el export — 23 min de job,
	// 0 DATA RACE, sin timeout. Un test que pasa en el hub y muere en el publicado no es un
	// flake: es una dependencia que se quedo del lado que no se publica.
	//
	// `testdata/` es el sitio canonico de Go para esto y viaja por construccion. La copia de
	// `an internal design note (not shipped)…` sigue siendo el original curado; que las dos no deriven lo comprueba
	// `scripts/check-mcp-pinned-schema.sh`, que es hub-only porque compara con algo que no viaja.
	schemaPath := filepath.Join("testdata", "mcp-final-2026-07-28", "schema.json")
	rawSchema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read pinned MCP FINAL schema: %v", err)
	}
	var schema mcpPinnedSchema
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		t.Fatalf("decode pinned MCP FINAL schema: %v", err)
	}
	var value any
	if err := json.Unmarshal(fixture, &value); err != nil {
		t.Fatalf("decode MCP FINAL fixture: %v", err)
	}
	node, exists := schema.Defs[definition]
	if !exists {
		t.Fatalf("pinned MCP FINAL schema has no $defs/%s", definition)
	}
	if err := validateMCPPinnedSchemaNode(schema, node, value); err != nil {
		t.Fatalf("MCP FINAL fixture does not satisfy pinned schema $defs/%s: %v\n%s",
			definition, err, fixture)
	}
}

func validateMCPPinnedSchemaNode(schema mcpPinnedSchema, raw json.RawMessage, value any) error {
	var node map[string]any
	if err := json.Unmarshal(raw, &node); err != nil {
		return err
	}
	if ref, ok := node["$ref"].(string); ok {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(ref, prefix) {
			return fmt.Errorf("unsupported schema reference %q", ref)
		}
		target, exists := schema.Defs[strings.TrimPrefix(ref, prefix)]
		if !exists {
			return fmt.Errorf("missing schema reference %q", ref)
		}
		return validateMCPPinnedSchemaNode(schema, target, value)
	}
	if constant, exists := node["const"]; exists && !reflect.DeepEqual(constant, value) {
		return fmt.Errorf("value %v does not equal const %v", value, constant)
	}
	if alternatives, ok := node["anyOf"].([]any); ok {
		var alternativeErrors []string
		for _, alternative := range alternatives {
			rawAlternative, _ := json.Marshal(alternative)
			if err := validateMCPPinnedSchemaNode(schema, rawAlternative, value); err == nil {
				return nil
			} else {
				alternativeErrors = append(alternativeErrors, err.Error())
			}
		}
		return fmt.Errorf("no anyOf alternative matched: %s", strings.Join(alternativeErrors, "; "))
	}
	if rawType, exists := node["type"]; exists && !mcpPinnedTypeMatches(rawType, value) {
		return fmt.Errorf("value %T does not match type %v", value, rawType)
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return nil
	}
	if required, ok := node["required"].([]any); ok {
		for _, member := range required {
			name, _ := member.(string)
			if _, exists := object[name]; !exists {
				return fmt.Errorf("required member %q is absent", name)
			}
		}
	}
	properties, _ := node["properties"].(map[string]any)
	for name, property := range properties {
		member, exists := object[name]
		if !exists {
			continue
		}
		rawProperty, _ := json.Marshal(property)
		if err := validateMCPPinnedSchemaNode(schema, rawProperty, member); err != nil {
			return fmt.Errorf("member %q: %w", name, err)
		}
	}
	if additional, ok := node["additionalProperties"].(map[string]any); ok {
		rawAdditional, _ := json.Marshal(additional)
		for name, member := range object {
			if _, declared := properties[name]; declared {
				continue
			}
			if err := validateMCPPinnedSchemaNode(schema, rawAdditional, member); err != nil {
				return fmt.Errorf("additional member %q: %w", name, err)
			}
		}
	}
	return nil
}

func mcpPinnedTypeMatches(rawType any, value any) bool {
	types := []string{}
	switch typed := rawType.(type) {
	case string:
		types = append(types, typed)
	case []any:
		for _, item := range typed {
			if value, ok := item.(string); ok {
				types = append(types, value)
			}
		}
	}
	for _, expected := range types {
		switch expected {
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "number":
			if _, ok := value.(float64); ok {
				return true
			}
		case "integer":
			if number, ok := value.(float64); ok && math.Trunc(number) == number {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "null":
			if value == nil {
				return true
			}
		}
	}
	return false
}
