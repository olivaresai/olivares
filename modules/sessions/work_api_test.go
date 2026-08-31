// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type permissionSetWorkAuthorizer map[auth.Permission]bool

func (a permissionSetWorkAuthorizer) Authorize(_ context.Context, req auth.Request) auth.Decision {
	return auth.Decision{Allow: a[req.Permission]}
}

func TestWorkStreamFailureUsesThirdOutcomeControlFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	writeWorkStreamFailure(&out, store.ErrStoreUnavailable)
	got := out.String()
	if !strings.Contains(got, "event: olivares.error") ||
		!strings.Contains(got, `"verdict":"NO_HE_PODIDO_MIRAR"`) ||
		!strings.Contains(got, `"code":"observation_unavailable"`) {
		t.Fatalf("stream failure frame = %q", got)
	}
}

func TestLeaseWorkCommandJSONAcceptsDocumentedFieldsAndRejectsUnknown(t *testing.T) {
	t.Parallel()

	decisionID := model.NewID()
	req := httptest.NewRequest(http.MethodPost, "/lease", strings.NewReader(fmt.Sprintf(`{
		"holder_sid":"osn_%s",
		"holder_run_ref":"run-1",
		"holder_agent_ref":"agent-1",
		"ttl_seconds":60,
		"fence":3,
		"force":true,
		"unblock":true,
		"changes_requested":true,
		"reason":"operator-approved takeover",
		"decision_id":%q,
		"evidence_ref":"audit:42",
		"plan_hash":"%s"
	}`, model.NewID(), decisionID, strings.Repeat("a", 64))))
	var body leaseWorkCommandRequest
	if ok := decodeWorkJSON(httptest.NewRecorder(), req, &body); !ok {
		t.Fatal("documented lease WorkCommand fields were rejected by the decoder")
	}
	cmd := body.workCommand()
	if cmd.Reason != "operator-approved takeover" || cmd.DecisionID != decisionID ||
		cmd.EvidenceRef != "audit:42" || cmd.PlanHash != strings.Repeat("a", 64) {
		t.Fatalf("documented lease fields were not decoded: %#v", cmd)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/lease", strings.NewReader(`{"fence":3,"sql_column":"secret"}`))
	rec := httptest.NewRecorder()
	if ok := decodeWorkJSON(rec, unknown, &leaseWorkCommandRequest{}); ok {
		t.Fatal("unknown lease WorkCommand field was accepted")
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid_command"`) {
		t.Fatalf("unknown lease field response = %d %s", rec.Code, rec.Body.String())
	}
}

func newWorkAPIHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, New(
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	))
}

func workAPIWorkspace(t *testing.T, h *harness, tenant model.TenantID) model.ID {
	t.Helper()
	var id model.ID
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(context.Background())
		if err == nil {
			id = workspace.ID
		}
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	return id
}

func workAPICreateBody(workspace model.ID, owner, title string) map[string]any {
	return map[string]any{
		"workspace_id":    workspace.String(),
		"work_kind":       "implementation",
		"title":           title,
		"brief_md":        "Implement the HTTP contract and retain its evidence.",
		"context_refs":    []any{},
		"priority":        "p1",
		"owner_kind":      "user",
		"owner_ref":       owner,
		"provenance_kind": "human",
		"provenance_ref":  "test:work-api-k1",
		"acceptance": []any{map[string]any{
			"criterion_key": "http-contract",
			"ordinal":       0,
			"statement":     "The authenticated HTTP test is green.",
			"required":      true,
		}},
	}
}

func workAPIHeaders(tenant model.TenantID, values map[string]string) map[string]string {
	out := tenantHdr(tenant)
	for key, value := range values {
		out[key] = value
	}
	return out
}

func workAPIErrorCode(r resp) string {
	if code, _ := r.body["code"].(string); code != "" {
		return code
	}
	if body, _ := r.body["error"].(map[string]any); body != nil {
		code, _ := body["code"].(string)
		return code
	}
	return ""
}

func workAPIRoleToken(t *testing.T, h *harness, admin string, tenant model.TenantID, role, email string) string {
	return workAPIRoleTokenIn(t, h, admin, tenant, role, email, "")
}

func workAPIRoleTokenIn(t *testing.T, h *harness, admin string, tenant model.TenantID, role, email string, workspace model.ID) string {
	t.Helper()
	const password = "work-api-pass1"
	created := h.doJSON(http.MethodPost, "/v1/users", admin, map[string]any{
		"email": email, "password": password,
	}, nil)
	if created.code != http.StatusCreated {
		t.Fatalf("create %s user = %d %s", role, created.code, created.raw)
	}
	uid, _ := created.body["id"].(string)
	membership := map[string]any{
		"user_id": uid, "tenant": tenant.String(), "role": role,
	}
	if !workspace.IsZero() {
		membership["workspace_id"] = workspace.String()
	}
	granted := h.doJSON(http.MethodPost, "/v1/memberships", admin, membership, nil)
	if granted.code != http.StatusCreated {
		t.Fatalf("grant %s = %d %s", role, granted.code, granted.raw)
	}
	login := h.doJSON(http.MethodPost, "/v1/auth/login", "", map[string]any{
		"email": email, "password": password,
	}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login %s = %d %s", role, login.code, login.raw)
	}
	token, _ := login.body["token"].(string)
	return token
}

func workAPICreateWorkspace(t *testing.T, h *harness, tenant model.TenantID, slug string) model.ID {
	t.Helper()
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		created, err := sc.Workspaces().Create(context.Background(), model.Workspace{
			Name: strings.ToUpper(slug), Slug: slug, Status: model.StatusActive,
		})
		if err == nil {
			id = created.ID
		}
		return err
	}); err != nil {
		t.Fatalf("create workspace %s: %v", slug, err)
	}
	return id
}

func workAPIApplyAtVersion(
	h *harness,
	method, path, token string,
	tenant model.TenantID,
	version int64,
	body any,
) resp {
	headers := map[string]string{"Idempotency-Key": model.NewID().String()}
	if version > 0 {
		headers["If-Match"] = fmt.Sprintf(`"v%d"`, version)
	}
	return h.doJSON(method, path+"?mode=apply", token, body, workAPIHeaders(tenant, headers))
}

func workAPICollectChildren(
	t *testing.T,
	h *harness,
	token string,
	tenant model.TenantID,
	path string,
	limit int,
) []map[string]any {
	t.Helper()
	var out []map[string]any
	cursor := ""
	seen := map[string]bool{}
	sawContinuation := false
	for pageNo := 0; pageNo < 100; pageNo++ {
		url := path + "?limit=" + strconv.Itoa(limit)
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		response := h.do(http.MethodGet, url, token, tenantHdr(tenant))
		if response.code != http.StatusOK {
			t.Fatalf("child page %d = %d %s", pageNo, response.code, response.raw)
		}
		items, ok := response.body["items"].([]any)
		if !ok {
			t.Fatalf("child page %d items are not an array: %s", pageNo, response.raw)
		}
		for _, value := range items {
			row, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("child page %d row is not an object: %#v", pageNo, value)
			}
			out = append(out, row)
		}
		hasMore, _ := response.body["has_more"].(bool)
		next, _ := response.body["next_cursor"].(string)
		if !hasMore {
			if next != "" {
				t.Fatalf("terminal child page returned cursor %q", next)
			}
			if len(out) > limit && !sawContinuation {
				t.Fatalf("limit %d was not enforced for %d child rows", limit, len(out))
			}
			return out
		}
		if next == "" || !validWorkCursor(next) || seen[next] {
			t.Fatalf("non-terminal child page returned invalid cursor %q", next)
		}
		seen[next] = true
		sawContinuation = true
		cursor = next
	}
	t.Fatal("child pagination did not terminate")
	return nil
}

func TestWorkAPIValidatePlanApplyAndPreconditions(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-http-contract")
	workspace := workAPIWorkspace(t, h, tenant)
	body := workAPICreateBody(workspace, "owner:http", "K1 HTTP contract")
	path := "/v1/m/sessions/work-items"

	missingMode := h.doJSON(http.MethodPost, path, admin, body, tenantHdr(tenant))
	if missingMode.code != http.StatusBadRequest || workAPIErrorCode(missingMode) != "mode_required" {
		t.Fatalf("missing mode = %d %s", missingMode.code, missingMode.raw)
	}
	validated := h.doJSON(http.MethodPost, path+"?mode=validate", admin, body, tenantHdr(tenant))
	if validated.code != http.StatusOK || validated.body["verdict"] != string(VerdictClean) || validated.body["plan_hash"] != "" {
		t.Fatalf("validate = %d %s", validated.code, validated.raw)
	}
	planned := h.doJSON(http.MethodPost, path+"?mode=plan", admin, body, tenantHdr(tenant))
	planHash, _ := planned.body["plan_hash"].(string)
	if planned.code != http.StatusOK || planned.body["verdict"] != string(VerdictClean) || len(planHash) != 64 {
		t.Fatalf("plan = %d %s", planned.code, planned.raw)
	}
	listed := h.do(http.MethodGet, path, admin, tenantHdr(tenant))
	if items, _ := listed.body["items"].([]any); listed.code != http.StatusOK || len(items) != 0 {
		t.Fatalf("validate/plan wrote state: %d %s", listed.code, listed.raw)
	}

	missingKey := h.doJSON(http.MethodPost, path+"?mode=apply", admin, body, tenantHdr(tenant))
	if missingKey.code != http.StatusBadRequest || workAPIErrorCode(missingKey) != "idempotency_key_required" {
		t.Fatalf("missing idempotency key = %d %s", missingKey.code, missingKey.raw)
	}
	badPlanHeader := h.doJSON(http.MethodPost, path+"?mode=apply", admin, body,
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Plan-Hash": "not-a-hash"}))
	if badPlanHeader.code != http.StatusBadRequest || workAPIErrorCode(badPlanHeader) != "invalid_command" {
		t.Fatalf("bad If-Plan-Hash = %d %s", badPlanHeader.code, badPlanHeader.raw)
	}
	stalePlan := h.doJSON(http.MethodPost, path+"?mode=apply", admin, body,
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Plan-Hash": strings.Repeat("0", 64)}))
	if stalePlan.code != http.StatusPreconditionFailed || workAPIErrorCode(stalePlan) != "plan_changed" {
		t.Fatalf("stale If-Plan-Hash = %d %s", stalePlan.code, stalePlan.raw)
	}

	key := model.NewID().String()
	applyHeaders := workAPIHeaders(tenant, map[string]string{"Idempotency-Key": key, "If-Plan-Hash": planHash})
	applied := h.doJSON(http.MethodPost, path+"?mode=apply", admin, body, applyHeaders)
	itemID, _ := applied.body["result_id"].(string)
	if applied.code != http.StatusOK || applied.body["verdict"] != string(VerdictClean) || itemID == "" || applied.header.Get("ETag") != `"v1"` {
		t.Fatalf("apply = %d %s, ETag %q", applied.code, applied.raw, applied.header.Get("ETag"))
	}
	replayed := h.doJSON(http.MethodPost, path+"?mode=apply", admin, body, applyHeaders)
	if replayed.code != http.StatusOK || replayed.header.Get("Idempotency-Replayed") != "true" ||
		replayed.body["command_id"] != applied.body["command_id"] || replayed.body["event_id"] != applied.body["event_id"] ||
		replayed.raw != applied.raw {
		t.Fatalf("replay = %d %s headers=%v", replayed.code, replayed.raw, replayed.header)
	}
	changedBody := workAPICreateBody(workspace, "owner:http", "different request under the same key")
	reused := h.doJSON(http.MethodPost, path+"?mode=apply", admin, changedBody, applyHeaders)
	if reused.code != http.StatusConflict || workAPIErrorCode(reused) != "idempotency_key_reused" {
		t.Fatalf("idempotency key reuse = %d %s", reused.code, reused.raw)
	}

	itemPath := path + "/" + itemID
	got := h.do(http.MethodGet, itemPath, admin, tenantHdr(tenant))
	if got.code != http.StatusOK || got.header.Get("ETag") != `"v1"` {
		t.Fatalf("get = %d %s, ETag %q", got.code, got.raw, got.header.Get("ETag"))
	}
	if strings.Contains(got.raw, `"lease":`) {
		t.Fatalf("WorkItem GET bypassed the dedicated lease:read projection: %s", got.raw)
	}
	listedAfterCreate := h.do(http.MethodGet, path, admin, tenantHdr(tenant))
	if listedAfterCreate.code != http.StatusOK || strings.Contains(listedAfterCreate.raw, `"lease":`) {
		t.Fatalf("WorkItem LIST bypassed the dedicated lease:read projection: %d %s",
			listedAfterCreate.code, listedAfterCreate.raw)
	}
	updateBody := map[string]any{"title": "K1 HTTP contract updated"}
	updatePlan := h.doJSON(http.MethodPatch, itemPath+"?mode=plan", admin, updateBody, tenantHdr(tenant))
	updatePlanHash, _ := updatePlan.body["plan_hash"].(string)
	if updatePlan.code != http.StatusOK || updatePlan.body["expected_etag"] != `"v1"` || len(updatePlanHash) != 64 {
		t.Fatalf("update plan = %d %s", updatePlan.code, updatePlan.raw)
	}
	missingETag := h.doJSON(http.MethodPatch, itemPath+"?mode=apply", admin, updateBody,
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	if missingETag.code != http.StatusPreconditionRequired || workAPIErrorCode(missingETag) != "version_required" {
		t.Fatalf("missing If-Match = %d %s", missingETag.code, missingETag.raw)
	}
	weakETag := h.doJSON(http.MethodPatch, itemPath+"?mode=apply", admin, updateBody,
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": `W/"v1"`}))
	if weakETag.code != http.StatusBadRequest || workAPIErrorCode(weakETag) != "invalid_command" {
		t.Fatalf("weak If-Match = %d %s", weakETag.code, weakETag.raw)
	}
	staleETag := h.doJSON(http.MethodPatch, itemPath+"?mode=apply", admin, updateBody,
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": `"v9"`}))
	if staleETag.code != http.StatusPreconditionFailed || workAPIErrorCode(staleETag) != "version_mismatch" {
		t.Fatalf("stale If-Match = %d %s", staleETag.code, staleETag.raw)
	}
	updateKey := model.NewID().String()
	updated := h.doJSON(http.MethodPatch, itemPath+"?mode=apply", admin, updateBody,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": updateKey, "If-Match": `"v1"`, "If-Plan-Hash": updatePlanHash,
		}))
	if updated.code != http.StatusOK || updated.header.Get("ETag") != `"v2"` || updated.body["version"] != float64(2) {
		t.Fatalf("update apply = %d %s, ETag %q", updated.code, updated.raw, updated.header.Get("ETag"))
	}
	reusedWithAnotherETag := h.doJSON(http.MethodPatch, itemPath+"?mode=apply", admin, updateBody,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": updateKey, "If-Match": `"v2"`, "If-Plan-Hash": updatePlanHash,
		}))
	if reusedWithAnotherETag.code != http.StatusConflict ||
		workAPIErrorCode(reusedWithAnotherETag) != "idempotency_key_reused" {
		t.Fatalf("same key with another ETag = %d %s", reusedWithAnotherETag.code, reusedWithAnotherETag.raw)
	}
	canceled := h.doJSON(http.MethodPost, itemPath+"/transitions?mode=apply", admin,
		map[string]any{"command": "item.cancel", "code": "test_done", "reason": "Exercise stale ETag precedence."},
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": `"v2"`}))
	if canceled.code != http.StatusOK || canceled.header.Get("ETag") != `"v3"` {
		t.Fatalf("cancel for stale-state witness = %d %s", canceled.code, canceled.raw)
	}
	staleState := h.doJSON(http.MethodPost, itemPath+"/transitions?mode=apply", admin,
		map[string]any{"command": "item.ready"},
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": `"v2"`}))
	if staleState.code != http.StatusPreconditionFailed || workAPIErrorCode(staleState) != "version_mismatch" {
		t.Fatalf("stale ETag against newly illegal state = %d %s", staleState.code, staleState.raw)
	}
}

func TestWorkAPIOutboxReplayIsAdminOnlyAndKeepsEventID(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-outbox-replay-api")
	viewer := h.viewerToken(admin, tenant, "work-replay-viewer@a.test")
	f := workFixture{
		m: h.m, st: h.st, tenant: tenant, workspace: workAPIWorkspace(t, h, tenant),
		principal: WorkPrincipal{
			ActorKind: model.ActorUser, ActorRef: model.NewID().String(),
			Actor: "user:" + model.NewID().String(), Admin: true,
		},
	}
	created := applyCreate(t, f, "admin replay API")
	deadLetterWorkOutboxForTest(t, f, created.EventID)
	path := "/v1/m/sessions/work-events/" + created.EventID.String() + "/replay"
	scopedAdmin := workAPIRoleTokenIn(
		t, h, admin, tenant, "admin", "work-replay-scoped-admin@a.test", f.workspace,
	)

	if got := h.do(http.MethodPost, path, viewer, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("viewer replay = %d %s, want 403", got.code, got.raw)
	}
	if got := h.do(http.MethodPost, path+"?mode=plan", scopedAdmin, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("workspace-scoped admin replay = %d %s, want tenant-admin 403", got.code, got.raw)
	}
	if state := workOutboxStateForTest(t, f, created.EventID); state != "dead_letter" {
		t.Fatalf("viewer replay changed state to %q", state)
	}
	if got := h.do(http.MethodPost, path, admin, tenantHdr(tenant)); got.code != http.StatusBadRequest || workAPIErrorCode(got) != "mode_required" {
		t.Fatalf("replay without mode = %d %s", got.code, got.raw)
	}
	if got := h.doJSON(http.MethodPost, path+"?mode=plan", admin,
		map[string]any{"unknown": true}, tenantHdr(tenant)); got.code != http.StatusBadRequest || workAPIErrorCode(got) != "invalid_command" {
		t.Fatalf("replay unknown body = %d %s", got.code, got.raw)
	}

	before := workOutboxSnapshotForTest(t, f, created.EventID)
	beforeReceipts, beforeAudit := workCount(t, f, workCommandKind), workAuditSeqForTest(t, f)
	validated := h.do(http.MethodPost, path+"?mode=validate", admin, tenantHdr(tenant))
	if validated.code != http.StatusOK || validated.body["verdict"] != string(VerdictClean) ||
		validated.body["plan_hash"] != "" {
		t.Fatalf("replay validate = %d %s", validated.code, validated.raw)
	}
	planned := h.do(http.MethodPost, path+"?mode=plan", admin, tenantHdr(tenant))
	planHash, _ := planned.body["plan_hash"].(string)
	wantETag := fmt.Sprintf("\"v%d\"", before.version)
	if planned.code != http.StatusOK || planned.body["verdict"] != string(VerdictClean) ||
		planned.body["command"] != "outbox.replay" || len(planHash) != 64 ||
		planned.header.Get("ETag") != wantETag || planned.body["expected_etag"] != wantETag ||
		planned.body["event_type"] != "" {
		t.Fatalf("replay plan = %d %s headers=%v", planned.code, planned.raw, planned.header)
	}
	if after := workOutboxSnapshotForTest(t, f, created.EventID); after != before ||
		workCount(t, f, workCommandKind) != beforeReceipts || workAuditSeqForTest(t, f) != beforeAudit {
		t.Fatalf("API validate/plan wrote state: before=%#v after=%#v", before, after)
	}

	key := model.NewID().String()
	if got := h.do(http.MethodPost, path+"?mode=apply", admin, workAPIHeaders(tenant, map[string]string{
		"Idempotency-Key": key, "If-Match": wantETag,
	})); got.code != http.StatusPreconditionFailed || workAPIErrorCode(got) != "plan_changed" {
		t.Fatalf("apply without If-Plan-Hash = %d %s", got.code, got.raw)
	}
	if got := h.do(http.MethodPost, path+"?mode=apply", admin, workAPIHeaders(tenant, map[string]string{
		"Idempotency-Key": key, "If-Plan-Hash": planHash,
	})); got.code != http.StatusPreconditionRequired || workAPIErrorCode(got) != "version_required" {
		t.Fatalf("apply without If-Match = %d %s", got.code, got.raw)
	}
	if got := h.do(http.MethodPost, path+"?mode=apply", admin, workAPIHeaders(tenant, map[string]string{
		"If-Match": wantETag, "If-Plan-Hash": planHash,
	})); got.code != http.StatusBadRequest || workAPIErrorCode(got) != "idempotency_key_required" {
		t.Fatalf("apply without idempotency = %d %s", got.code, got.raw)
	}
	if got := h.do(http.MethodPost, path+"?mode=apply", admin, workAPIHeaders(tenant, map[string]string{
		"Idempotency-Key": model.NewID().String(), "If-Match": `W/"v1"`, "If-Plan-Hash": planHash,
	})); got.code != http.StatusBadRequest || workAPIErrorCode(got) != "invalid_command" {
		t.Fatalf("apply with weak If-Match = %d %s", got.code, got.raw)
	}
	if got := h.do(http.MethodPost, path+"?mode=apply", admin, workAPIHeaders(tenant, map[string]string{
		"Idempotency-Key": model.NewID().String(), "If-Match": `"v999"`, "If-Plan-Hash": planHash,
	})); got.code != http.StatusPreconditionFailed || workAPIErrorCode(got) != "version_mismatch" {
		t.Fatalf("apply with stale If-Match = %d %s", got.code, got.raw)
	}

	applyHeaders := workAPIHeaders(tenant, map[string]string{
		"Idempotency-Key": key, "If-Match": wantETag, "If-Plan-Hash": planHash,
	})
	replayed := h.do(http.MethodPost, path+"?mode=apply", admin, applyHeaders)
	if replayed.code != http.StatusAccepted || replayed.body["verdict"] != string(VerdictClean) ||
		replayed.body["code"] != "requeued" || replayed.body["event_id"] != created.EventID.String() ||
		replayed.body["aggregate_kind"] != string(workItemKind) ||
		replayed.body["aggregate_id"] != created.ResultID.String() ||
		replayed.body["work_item_id"] != created.ResultID.String() ||
		replayed.body["state"] != "pending" || replayed.body["prior_state"] != "dead_letter" ||
		replayed.body["prior_version"] != float64(before.version) ||
		replayed.body["version"] != float64(before.version+1) {
		t.Fatalf("admin replay = %d %s", replayed.code, replayed.raw)
	}
	exact := h.do(http.MethodPost, path+"?mode=apply", admin, applyHeaders)
	if exact.code != http.StatusAccepted || exact.header.Get("Idempotency-Replayed") != "true" ||
		exact.raw != replayed.raw {
		t.Fatalf("exact replay = %d %s headers=%v; first=%s", exact.code, exact.raw, exact.header, replayed.raw)
	}
	if state := workOutboxStateForTest(t, f, created.EventID); state != "pending" {
		t.Fatalf("admin replay state = %q, want pending", state)
	}
	currentVersion := int64(replayed.body["version"].(float64))
	if got := h.do(http.MethodPost, path+"?mode=apply", admin, workAPIHeaders(tenant, map[string]string{
		"Idempotency-Key": model.NewID().String(),
		"If-Match":        fmt.Sprintf("\"v%d\"", currentVersion),
		"If-Plan-Hash":    planHash,
	})); got.code != http.StatusConflict || workAPIErrorCode(got) != "state_conflict" {
		t.Fatalf("second replay = %d %s, want state_conflict", got.code, got.raw)
	}
	if got := h.do(http.MethodPost, "/v1/m/sessions/work-events/not-a-uuid/replay?mode=plan", admin, tenantHdr(tenant)); got.code != http.StatusNotFound || workAPIErrorCode(got) != "not_found" {
		t.Fatalf("invalid event replay = %d %s", got.code, got.raw)
	}
}

func TestWorkOutboxReplayPreservesLegacyReceiptBody(t *testing.T) {
	t.Parallel()

	workItemID := model.NewID()
	legacy, err := canonicalJSON(struct {
		Verdict      AssessmentVerdict `json:"verdict"`
		Code         string            `json:"code"`
		CommandID    model.ID          `json:"command_id"`
		OutboxID     model.ID          `json:"outbox_id"`
		EventID      model.ID          `json:"event_id"`
		WorkItemID   model.ID          `json:"work_item_id"`
		State        string            `json:"state"`
		Version      int64             `json:"version"`
		Attempts     int64             `json:"attempts"`
		PriorState   string            `json:"prior_state"`
		PriorVersion int64             `json:"prior_version"`
		PlanHash     string            `json:"plan_hash"`
		AuditSeq     int64             `json:"audit_seq"`
	}{
		Verdict: VerdictClean, Code: "requeued", CommandID: model.NewID(),
		OutboxID: model.NewID(), EventID: model.NewID(), WorkItemID: workItemID,
		State: "pending", Version: 2, Attempts: 1, PriorState: "dead_letter",
		PriorVersion: 1, PlanHash: strings.Repeat("a", 64), AuditSeq: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeWorkOutboxReplay(legacy)
	if err != nil || result.AggregateKind != string(workItemKind) || result.AggregateID != workItemID {
		t.Fatalf("decode legacy outbox replay = %#v, %v", result, err)
	}
	result.Replayed = true
	recorder := httptest.NewRecorder()
	writeWorkOutboxReplay(recorder, http.StatusAccepted, result)
	if recorder.Code != http.StatusAccepted || recorder.Body.String() != string(legacy) {
		t.Fatalf("legacy exact replay body = %q, want durable %q", recorder.Body.String(), legacy)
	}
	if strings.Contains(recorder.Body.String(), "aggregate_kind") ||
		!strings.Contains(recorder.Body.String(), "work_item_id") {
		t.Fatalf("legacy replay response was rewritten across upgrade: %s", recorder.Body.String())
	}
}

func TestWorkAPIDecisionListEmptyItemsIsArray(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-empty-decisions")
	for _, query := range []string{"", "?effective=true"} {
		response := h.do(http.MethodGet, "/v1/m/sessions/decisions"+query, admin, tenantHdr(tenant))
		items, ok := response.body["items"].([]any)
		if response.code != http.StatusOK || !ok || len(items) != 0 {
			t.Fatalf("empty decisions %q = %d %s, want items=[]", query, response.code, response.raw)
		}
	}
}

func TestWorkAPILeaseSurfacePermissionsAndStrictFilters(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-lease-http")
	workspace := workAPIWorkspace(t, h, tenant)
	viewer := workAPIRoleToken(t, h, admin, tenant, auth.RoleViewer, "lease-viewer@a.test")
	editor := workAPIRoleToken(t, h, admin, tenant, auth.RoleEditor, "lease-editor@a.test")
	tenantAdmin := workAPIRoleToken(t, h, admin, tenant, auth.RoleAdmin, "lease-admin@a.test")
	leasePath := "/v1/m/sessions/leases"

	empty := h.do(http.MethodGet, leasePath, viewer, tenantHdr(tenant))
	items, ok := empty.body["items"].([]any)
	if empty.code != http.StatusOK || !ok || len(items) != 0 {
		t.Fatalf("empty lease list = %d %s, want items=[]", empty.code, empty.raw)
	}
	for _, query := range []string{
		"?sql_column=value",
		"?state=active&state=expired",
		"?cursor=&cursor=",
	} {
		response := h.do(http.MethodGet, leasePath+query, viewer, tenantHdr(tenant))
		if response.code != http.StatusBadRequest || workAPIErrorCode(response) != "invalid_command" {
			t.Errorf("lease list query %q = %d %s, want strict invalid_command", query, response.code, response.raw)
		}
	}

	created := h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", admin,
		workAPICreateBody(workspace, "owner:lease-http", "K2 lease HTTP contract"),
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	itemID, _ := created.body["result_id"].(string)
	if created.code != http.StatusOK || itemID == "" {
		t.Fatalf("create lease WorkItem = %d %s", created.code, created.raw)
	}

	nestedPath := "/v1/m/sessions/work-items/" + itemID + "/lease"
	undeclaredLeaseField := h.doJSON(
		http.MethodPost,
		nestedPath+"/revoke?mode=validate",
		tenantAdmin,
		map[string]any{"fence": 1, "reason": "test", "title": "ignored by the lease domain"},
		tenantHdr(tenant),
	)
	if undeclaredLeaseField.code != http.StatusBadRequest ||
		workAPIErrorCode(undeclaredLeaseField) != "invalid_command" {
		t.Fatalf("undeclared lease request field = %d %s", undeclaredLeaseField.code, undeclaredLeaseField.raw)
	}
	nested := h.do(http.MethodGet, nestedPath, viewer, tenantHdr(tenant))
	if nested.code != http.StatusOK || nested.body["work_item_id"] != itemID ||
		nested.body["state"] != workLeaseVacant || nested.header.Get("ETag") != `"v1"` {
		t.Fatalf("nested lease get = %d %s, ETag %q", nested.code, nested.raw, nested.header.Get("ETag"))
	}
	updated := h.doJSON(http.MethodPatch, "/v1/m/sessions/work-items/"+itemID+"?mode=apply", admin,
		map[string]any{"title": "K2 lease HTTP contract updated"},
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v1"`,
		}))
	if updated.code != http.StatusOK || updated.body["version"] != float64(2) {
		t.Fatalf("advance WorkItem without lease update = %d %s", updated.code, updated.raw)
	}
	nested = h.do(http.MethodGet, nestedPath, viewer, tenantHdr(tenant))
	if nested.code != http.StatusOK || nested.header.Get("ETag") != `"v2"` ||
		nested.body["version"] != float64(1) {
		t.Fatalf("lease GET must expose parent ETag: %d %s, ETag %q",
			nested.code, nested.raw, nested.header.Get("ETag"))
	}
	filtered := h.do(http.MethodGet, leasePath+"?work_item_id="+itemID+"&state="+workLeaseVacant,
		viewer, tenantHdr(tenant))
	filteredItems, _ := filtered.body["items"].([]any)
	if filtered.code != http.StatusOK || len(filteredItems) != 1 {
		t.Fatalf("filtered lease list = %d %s", filtered.code, filtered.raw)
	}

	for _, operation := range []string{"acquire", "renew", "release"} {
		response := h.doJSON(http.MethodPost, nestedPath+"/"+operation, editor, map[string]any{}, tenantHdr(tenant))
		if response.code != http.StatusBadRequest || workAPIErrorCode(response) != "mode_required" {
			t.Errorf("lease write route %s = %d %s, want editor to reach handler", operation, response.code, response.raw)
		}
	}
	for _, operation := range []string{"takeover", "revoke", "clock-rebase"} {
		denied := h.doJSON(http.MethodPost, nestedPath+"/"+operation, editor, map[string]any{}, tenantHdr(tenant))
		if denied.code != http.StatusForbidden {
			t.Errorf("lease admin route %s editor = %d %s", operation, denied.code, denied.raw)
		}
		allowed := h.doJSON(http.MethodPost, nestedPath+"/"+operation, tenantAdmin, map[string]any{}, tenantHdr(tenant))
		if allowed.code != http.StatusBadRequest || workAPIErrorCode(allowed) != "mode_required" {
			t.Errorf("lease admin route %s admin = %d %s, want handler mode guard", operation, allowed.code, allowed.raw)
		}
	}
}

func TestWorkAPIPermissionsListGetAndTenantIsolation(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "work-http-a")
	tenantB := h.createOrg(admin, "work-http-b")
	workspaceA := workAPIWorkspace(t, h, tenantA)
	workspaceB := workAPIWorkspace(t, h, tenantB)
	viewer := workAPIRoleToken(t, h, admin, tenantA, auth.RoleViewer, "work-viewer@a.test")
	editor := workAPIRoleToken(t, h, admin, tenantA, auth.RoleEditor, "work-editor@a.test")
	tenantAdmin := workAPIRoleToken(t, h, admin, tenantA, auth.RoleAdmin, "work-admin@a.test")
	path := "/v1/m/sessions/work-items"

	create := func(tenant model.TenantID, workspace model.ID, title, key string) resp {
		return h.doJSON(http.MethodPost, path+"?mode=apply", admin,
			workAPICreateBody(workspace, "owner:"+title, title),
			workAPIHeaders(tenant, map[string]string{"Idempotency-Key": key}))
	}
	sharedKey := model.NewID().String()
	itemA := create(tenantA, workspaceA, "same-title-and-key", sharedKey)
	itemA2 := create(tenantA, workspaceA, "tenant-a-second", model.NewID().String())
	itemB := create(tenantB, workspaceB, "same-title-and-key", sharedKey)
	idA, _ := itemA.body["result_id"].(string)
	idA2, _ := itemA2.body["result_id"].(string)
	idB, _ := itemB.body["result_id"].(string)
	if itemA.code != http.StatusOK || itemA2.code != http.StatusOK || itemB.code != http.StatusOK ||
		idA == "" || idA2 == "" || idB == "" {
		t.Fatalf("seed work: A1=%d %s A2=%d %s B=%d %s",
			itemA.code, itemA.raw, itemA2.code, itemA2.raw, itemB.code, itemB.raw)
	}

	if got := h.do(http.MethodGet, path, "", tenantHdr(tenantA)); got.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d %s", got.code, got.raw)
	}
	viewerList := h.do(http.MethodGet, path+"?status=draft&limit=1", viewer, tenantHdr(tenantA))
	items, _ := viewerList.body["items"].([]any)
	cursor, _ := viewerList.body["next_cursor"].(string)
	if viewerList.code != http.StatusOK || len(items) != 1 || viewerList.body["has_more"] != true || cursor == "" {
		t.Fatalf("viewer list = %d %s", viewerList.code, viewerList.raw)
	}
	firstListed, _ := items[0].(map[string]any)
	secondPage := h.do(http.MethodGet, path+"?status=draft&limit=1&cursor="+cursor, viewer, tenantHdr(tenantA))
	secondItems, _ := secondPage.body["items"].([]any)
	if secondPage.code != http.StatusOK || len(secondItems) != 1 || secondPage.body["has_more"] != false {
		t.Fatalf("viewer second page = %d %s", secondPage.code, secondPage.raw)
	}
	secondListed, _ := secondItems[0].(map[string]any)
	if firstListed["id"] == secondListed["id"] {
		t.Fatalf("pagination repeated id %v", firstListed["id"])
	}
	viewerGet := h.do(http.MethodGet, path+"/"+idA, viewer, tenantHdr(tenantA))
	if viewerGet.code != http.StatusOK || viewerGet.header.Get("ETag") != `"v1"` {
		t.Fatalf("viewer get = %d %s", viewerGet.code, viewerGet.raw)
	}
	viewerWrite := h.doJSON(http.MethodPost, path+"?mode=validate", viewer,
		workAPICreateBody(workspaceA, "owner:viewer", "viewer-write"), tenantHdr(tenantA))
	if viewerWrite.code != http.StatusForbidden {
		t.Fatalf("viewer write = %d %s", viewerWrite.code, viewerWrite.raw)
	}
	editorWrite := h.doJSON(http.MethodPost, path+"?mode=validate", editor,
		workAPICreateBody(workspaceA, "owner:editor", "editor-write"), tenantHdr(tenantA))
	if editorWrite.code != http.StatusOK || editorWrite.body["verdict"] != string(VerdictClean) {
		t.Fatalf("editor write = %d %s", editorWrite.code, editorWrite.raw)
	}
	assignment := map[string]any{"owner_kind": "user", "owner_ref": "owner:new"}
	if denied := h.doJSON(http.MethodPost, path+"/"+idA+"/assignments?mode=validate", editor, assignment, tenantHdr(tenantA)); denied.code != http.StatusForbidden {
		t.Errorf("editor assignment admin gate = %d %s", denied.code, denied.raw)
	}
	if allowed := h.doJSON(http.MethodPost, path+"/"+idA+"/assignments?mode=validate", tenantAdmin, assignment, tenantHdr(tenantA)); allowed.code != http.StatusOK || allowed.body["verdict"] != string(VerdictClean) {
		t.Fatalf("tenant admin assignment = %d %s", allowed.code, allowed.raw)
	}

	if crossMembership := h.do(http.MethodGet, path, viewer, tenantHdr(tenantB)); crossMembership.code != http.StatusForbidden {
		t.Fatalf("viewer cross-tenant list = %d %s", crossMembership.code, crossMembership.raw)
	}
	listA := h.do(http.MethodGet, path, admin, tenantHdr(tenantA))
	if got, _ := listA.body["items"].([]any); listA.code != http.StatusOK || len(got) != 2 {
		t.Fatalf("admin tenant A list = %d %s", listA.code, listA.raw)
	}
	if crossID := h.do(http.MethodGet, path+"/"+idB, admin, tenantHdr(tenantA)); crossID.code != http.StatusNotFound {
		t.Fatalf("foreign tenant id = %d %s", crossID.code, crossID.raw)
	}
	if unknown := h.do(http.MethodGet, path+"?sql_column=value", viewer, tenantHdr(tenantA)); unknown.code != http.StatusBadRequest || workAPIErrorCode(unknown) != "invalid_command" {
		t.Fatalf("unknown list filter = %d %s", unknown.code, unknown.raw)
	}
	if cursor := h.do(http.MethodGet, path+"?cursor=not-a-uuid", viewer, tenantHdr(tenantA)); cursor.code != http.StatusBadRequest || workAPIErrorCode(cursor) != "invalid_cursor" {
		t.Fatalf("invalid cursor = %d %s", cursor.code, cursor.raw)
	}
}

func TestWorkAPIArchivedFilterSupportsBothBooleanDirections(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-archived-filter")
	workspace := workAPIWorkspace(t, h, tenant)
	path := "/v1/m/sessions/work-items"
	create := func(title string) resp {
		return h.doJSON(http.MethodPost, path+"?mode=apply", admin,
			workAPICreateBody(workspace, "owner:"+title, title),
			workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	}
	archived, live := create("archived"), create("live")
	archivedID, _ := archived.body["result_id"].(string)
	liveID, _ := live.body["result_id"].(string)
	if archived.code != http.StatusOK || live.code != http.StatusOK || archivedID == "" || liveID == "" {
		t.Fatalf("seed archived filter: archived=%d %s live=%d %s", archived.code, archived.raw, live.code, live.raw)
	}

	canceled := h.doJSON(http.MethodPost, path+"/"+archivedID+"/transitions?mode=apply", admin,
		map[string]any{"command": "item.cancel", "code": "superseded", "reason": "archive filter witness"},
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v1"`,
		}))
	if canceled.code != http.StatusOK || canceled.body["version"] != float64(2) {
		t.Fatalf("cancel archived item = %d %s", canceled.code, canceled.raw)
	}
	stored := h.doJSON(http.MethodPost, path+"/"+archivedID+"/transitions?mode=apply", admin,
		map[string]any{"command": "item.archive"},
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v2"`,
		}))
	if stored.code != http.StatusOK || stored.body["version"] != float64(3) {
		t.Fatalf("archive item = %d %s", stored.code, stored.raw)
	}

	for filter, wantID := range map[string]string{"false": liveID, "true": archivedID} {
		filter, wantID := filter, wantID
		t.Run(filter, func(t *testing.T) {
			got := h.do(http.MethodGet, path+"?archived="+filter, admin, tenantHdr(tenant))
			items, _ := got.body["items"].([]any)
			if got.code != http.StatusOK || len(items) != 1 {
				t.Fatalf("archived=%s = %d %s", filter, got.code, got.raw)
			}
			row, _ := items[0].(map[string]any)
			if row["id"] != wantID {
				t.Fatalf("archived=%s id = %v, want %s", filter, row["id"], wantID)
			}
		})
	}
}

func TestRequestedDecisionHeadStateDirections(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		query      map[string][]string
		wantState  string
		wantFilter bool
		wantErr    bool
	}{
		{name: "history", query: nil},
		{name: "effective true", query: map[string][]string{"effective": {"true"}}, wantState: "effective", wantFilter: true},
		{name: "revoked false", query: map[string][]string{"revoked": {"false"}}, wantState: "effective", wantFilter: true},
		{name: "revoked true", query: map[string][]string{"revoked": {"true"}}, wantState: "revoked", wantFilter: true},
		{name: "effective false", query: map[string][]string{"effective": {"false"}}, wantState: "revoked", wantFilter: true},
		{name: "consistent pair", query: map[string][]string{"effective": {"true"}, "revoked": {"false"}}, wantState: "effective", wantFilter: true},
		{name: "contradictory pair", query: map[string][]string{"effective": {"true"}, "revoked": {"true"}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, filtered, err := requestedDecisionHeadState(tc.query)
			if (err != nil) != tc.wantErr || state != tc.wantState || filtered != tc.wantFilter {
				t.Fatalf("state=%q filtered=%v err=%v, want state=%q filtered=%v err=%v", state, filtered, err, tc.wantState, tc.wantFilter, tc.wantErr)
			}
		})
	}
}

func TestWorkAPIDecisionHeadFiltersAreAuthenticatedAndKeysetHonest(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-decision-heads")
	workspace := workAPIWorkspace(t, h, tenant)
	created := h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", admin,
		workAPICreateBody(workspace, "owner:decision-heads", "decision heads"),
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	itemID, _ := created.body["result_id"].(string)
	if created.code != http.StatusOK || itemID == "" {
		t.Fatalf("seed decision item = %d %s", created.code, created.raw)
	}

	version := int64(1)
	applyDecision := func(command, key, decisionID string) resp {
		t.Helper()
		body := map[string]any{
			"command": command, "work_item_id": itemID, "decision_key": key,
			"subject_kind": "work.scope", "subject_ref": itemID,
			"statement_md":  "Use the observable current decision head.",
			"rationale_md":  "The operator must distinguish history from current authority.",
			"authority_ref": "approval:decision-head-test",
		}
		path := "/v1/m/sessions/decisions"
		if command == "decision.revoke" {
			path += "/" + decisionID + "/revoke"
			delete(body, "subject_kind")
			delete(body, "subject_ref")
		}
		response := h.doJSON(http.MethodPost, path+"?mode=apply", admin, body,
			workAPIHeaders(tenant, map[string]string{
				"Idempotency-Key": model.NewID().String(),
				"If-Match":        `"v` + strconv.FormatInt(version, 10) + `"`,
			}))
		if response.code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", command, key, response.code, response.raw)
		}
		version++
		return response
	}

	alpha := applyDecision("decision.set", "alpha", "")
	alphaID, _ := alpha.body["result_id"].(string)
	alpha2 := applyDecision("decision.supersede", "alpha", "")
	alpha2ID, _ := alpha2.body["result_id"].(string)
	revoked := applyDecision("decision.revoke", "alpha", alpha2ID)
	revokedID, _ := revoked.body["result_id"].(string)
	beta := applyDecision("decision.set", "beta", "")
	betaID, _ := beta.body["result_id"].(string)
	gamma := applyDecision("decision.set", "gamma", "")
	gammaID, _ := gamma.body["result_id"].(string)
	if alphaID == "" || revokedID == "" || betaID == "" || gammaID == "" {
		t.Fatalf("decision ids missing: alpha=%q revoked=%q beta=%q gamma=%q", alphaID, revokedID, betaID, gammaID)
	}

	base := "/v1/m/sessions/decisions?work_item_id=" + itemID
	if denied := h.do(http.MethodGet, base+"&effective=true", "", tenantHdr(tenant)); denied.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated head list = %d %s", denied.code, denied.raw)
	}
	history := h.do(http.MethodGet, base, admin, tenantHdr(tenant))
	historyRows, _ := history.body["items"].([]any)
	if history.code != http.StatusOK || len(historyRows) != 5 {
		t.Fatalf("decision history = %d %s", history.code, history.raw)
	}
	for _, raw := range historyRows {
		row, _ := raw.(map[string]any)
		if _, projected := row["state"]; projected {
			t.Fatalf("history row was mislabeled as a head: %#v", row)
		}
	}

	first := h.do(http.MethodGet, base+"&effective=true&limit=1", admin, tenantHdr(tenant))
	firstRows, _ := first.body["items"].([]any)
	cursor, _ := first.body["next_cursor"].(string)
	if first.code != http.StatusOK || len(firstRows) != 1 || first.body["has_more"] != true || !validWorkCursor(cursor) {
		t.Fatalf("first effective page = %d %s", first.code, first.raw)
	}
	second := h.do(http.MethodGet, base+"&effective=true&limit=1&cursor="+cursor, admin, tenantHdr(tenant))
	secondRows, _ := second.body["items"].([]any)
	if second.code != http.StatusOK || len(secondRows) != 1 || second.body["has_more"] != false {
		t.Fatalf("second effective page = %d %s", second.code, second.raw)
	}
	effectiveIDs := map[string]bool{}
	for _, raw := range append(firstRows, secondRows...) {
		row, _ := raw.(map[string]any)
		id, _ := row["id"].(string)
		if row["state"] != "effective" || effectiveIDs[id] {
			t.Fatalf("effective projection row = %#v", row)
		}
		effectiveIDs[id] = true
	}
	if !effectiveIDs[betaID] || !effectiveIDs[gammaID] || effectiveIDs[alphaID] || effectiveIDs[alpha2ID] {
		t.Fatalf("effective ids = %#v, want beta/gamma heads only", effectiveIDs)
	}

	for _, filter := range []string{"revoked=true", "effective=false"} {
		page := h.do(http.MethodGet, base+"&"+filter, admin, tenantHdr(tenant))
		rows, _ := page.body["items"].([]any)
		if page.code != http.StatusOK || len(rows) != 1 {
			t.Fatalf("%s projection = %d %s", filter, page.code, page.raw)
		}
		row, _ := rows[0].(map[string]any)
		if row["id"] != revokedID || row["state"] != "revoked" {
			t.Fatalf("%s row = %#v, want revoked head %s", filter, row, revokedID)
		}
	}
	postFiltered := h.do(http.MethodGet, base+"&effective=true&subject_ref=missing", admin, tenantHdr(tenant))
	if rows, _ := postFiltered.body["items"].([]any); postFiltered.code != http.StatusOK || len(rows) != 0 || postFiltered.body["has_more"] != false {
		t.Fatalf("post-filtered head page = %d %s", postFiltered.code, postFiltered.raw)
	}
	contradictory := h.do(http.MethodGet, base+"&effective=true&revoked=true", admin, tenantHdr(tenant))
	if contradictory.code != http.StatusBadRequest || workAPIErrorCode(contradictory) != "invalid_command" {
		t.Fatalf("contradictory state filters = %d %s", contradictory.code, contradictory.raw)
	}
}

func TestWorkAPIStreamResumeAndTenantIsolation(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "work-stream-a")
	tenantB := h.createOrg(admin, "work-stream-b")
	workspaceA := workAPIWorkspace(t, h, tenantA)
	workspaceB := workAPIWorkspace(t, h, tenantB)
	viewer := workAPIRoleToken(t, h, admin, tenantA, auth.RoleViewer, "work-stream-viewer@a.test")

	create := func(tenant model.TenantID, workspace model.ID, title string) resp {
		return h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", admin,
			workAPICreateBody(workspace, "owner:"+title, title),
			workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	}
	firstA := create(tenantA, workspaceA, "stream-a-1")
	time.Sleep(5 * time.Millisecond)
	foreignB := create(tenantB, workspaceB, "stream-b-1")
	time.Sleep(5 * time.Millisecond)
	secondA := create(tenantA, workspaceA, "stream-a-2")
	firstEvent, _ := firstA.body["event_id"].(string)
	foreignEvent, _ := foreignB.body["event_id"].(string)
	secondEvent, _ := secondA.body["event_id"].(string)
	if firstA.code != http.StatusOK || foreignB.code != http.StatusOK || secondA.code != http.StatusOK ||
		firstEvent == "" || foreignEvent == "" || secondEvent == "" {
		t.Fatalf("seed events: A1=%s B=%s A2=%s", firstA.raw, foreignB.raw, secondA.raw)
	}

	if denied := h.do(http.MethodGet, "/v1/m/sessions/work-stream?cursor="+firstEvent, "", tenantHdr(tenantA)); denied.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stream = %d %s", denied.code, denied.raw)
	}
	conflict := h.do(http.MethodGet, "/v1/m/sessions/work-stream?cursor="+secondEvent, viewer,
		workAPIHeaders(tenantA, map[string]string{"Last-Event-ID": firstEvent}))
	if conflict.code != http.StatusBadRequest || workAPIErrorCode(conflict) != "invalid_cursor" {
		t.Fatalf("conflicting SSE cursors = %d %s", conflict.code, conflict.raw)
	}

	ts := httptest.NewServer(h.srv.Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/m/sessions/work-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+viewer)
	req.Header.Set("X-Olivares-Tenant", tenantA.String())
	req.Header.Set("Last-Event-ID", firstEvent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream response = %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(res.Body)
	var gotID string
	var payload map[string]any
	for gotID == "" || payload == nil {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read resumed event: %v", readErr)
		}
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "id: "); ok {
			gotID = value
		}
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			if err := json.Unmarshal([]byte(value), &payload); err != nil {
				t.Fatalf("decode resumed event: %v", err)
			}
		}
	}
	cancel()
	if gotID != secondEvent || gotID == foreignEvent {
		t.Fatalf("resumed event id = %q, want tenant A %q and not tenant B %q", gotID, secondEvent, foreignEvent)
	}
	if payload["workspace_id"] != workspaceA.String() || payload["workspace_id"] == workspaceB.String() {
		t.Fatalf("resumed event leaked workspace: %#v", payload)
	}
}

func TestWorkEventTimelineAndStreamRequireLeaseReadForLeasePayloads(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-event-lease-permission")
	workspace := workAPIWorkspace(t, h, tenant)
	created := h.doJSON(http.MethodPost, "/v1/m/sessions/work-items?mode=apply", admin,
		workAPICreateBody(workspace, "owner:event-permission", "Mixed event permissions"),
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	itemID, _ := created.body["result_id"].(string)
	if created.code != http.StatusOK || itemID == "" {
		t.Fatalf("seed WorkItem = %d %s", created.code, created.raw)
	}

	const leaseCanary = "LEASE_EVENT_PAYLOAD_MUST_BE_PRIVILEGED"
	const ordinaryCanary = "ORDINARY_WORK_EVENT_REMAINS_VISIBLE"
	leasePayload := `{"canary":"` + leaseCanary + `","holder_sid":"osn_SECRET",` +
		`"holder_run_ref":"RUN_SECRET","holder_agent_ref":"AGENT_SECRET",` +
		`"fence":17,"expires_at":"EXPIRY_SECRET","end_reason":"REASON_SECRET",` +
		`"end_reason_hash":"HASH_SECRET"}`
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		createEvent := func(sequence int64, eventType, payload string) error {
			_, createErr := repo.Create(context.Background(), model.Record{
				colWorkWorkspaceID:    workspace.String(),
				colEventID:            model.NewID().String(),
				colEventAggregateKind: string(workItemKind),
				colEventAggregateID:   itemID,
				colEventSeq:           sequence,
				colEventType:          eventType,
				colEventActorKind:     string(model.ActorSystem),
				colEventActorRef:      "test:event-permission",
				colEventOccurredAt:    model.SystemClock{}.Now().String(),
				colEventPayload:       payload,
				colEventPayloadHash:   hashBytes([]byte(payload)),
				colEventCommandID:     model.NewID().String(),
				colEventAuditSeq:      sequence,
				colEventAuditHash:     hashBytes([]byte(eventType)),
			})
			return createErr
		}
		if err := createEvent(2, "work.lease.acquired", leasePayload); err != nil {
			return err
		}
		return createEvent(3, "work.item.transitioned", `{"canary":"`+ordinaryCanary+`"}`)
	}); err != nil {
		t.Fatalf("seed mixed WorkEvents: %v", err)
	}

	// Model a principal whose event authority is exactly work:read. The HTTP
	// route's primary work:read check has already admitted the request; the
	// module's secondary decision must preserve sequence continuity while
	// replacing the lease authority document with the stable redacted payload.
	h.m.UseWorkAuthorizer(permissionSetWorkAuthorizer{permWorkRead: true})
	eventsPath := "/v1/m/sessions/work-items/" + itemID + "/events"
	filteredRows := workAPICollectChildren(t, h, admin, tenant, eventsPath, 1)
	if len(filteredRows) != 3 {
		t.Fatalf("work:read timeline rows = %d, want all three sequence positions", len(filteredRows))
	}
	var redactedLease, ordinary map[string]any
	for _, row := range filteredRows {
		switch row[colEventType] {
		case "work.lease.acquired":
			redactedLease = row
		case "work.item.transitioned":
			ordinary = row
		}
	}
	if redactedLease == nil || redactedLease[colEventSeq] != float64(2) ||
		redactedLease[colEventID] == "" || redactedLease[colEventOccurredAt] == "" ||
		redactedLease[colEventPayload] != redactedWorkLeaseEventPayload {
		t.Fatalf("redacted lease envelope = %#v", redactedLease)
	}
	for _, forbiddenColumn := range []string{
		model.ColID, colEventActorKind, colEventActorRef, colEventPayloadHash,
		colEventCommandID, colEventAuditSeq, colEventAuditHash,
	} {
		if _, present := redactedLease[forbiddenColumn]; present {
			t.Errorf("redacted lease timeline retained %s: %#v", forbiddenColumn, redactedLease)
		}
	}
	ordinaryPayload, _ := ordinary[colEventPayload].(string)
	if ordinary == nil || !strings.Contains(ordinaryPayload, ordinaryCanary) ||
		ordinary[colEventPayloadHash] == nil {
		t.Fatalf("ordinary WorkEvent was redacted: %#v", ordinary)
	}

	mc := api.ModuleContext{
		Tenant: tenant,
		Data:   api.NewScopedData(h.st, tenant),
	}
	streamReq := httptest.NewRequest(http.MethodGet, "/v1/m/sessions/work-stream", nil)
	var filteredStream bytes.Buffer
	emitted, scannedTo, err := h.m.streamWorkEvents(streamReq, mc, &filteredStream, "")
	if err != nil || !emitted || scannedTo == "" {
		t.Fatalf("filtered work stream = emitted %v cursor %q err %v", emitted, scannedTo, err)
	}
	if got := filteredStream.String(); strings.Contains(got, leaseCanary) ||
		!strings.Contains(got, "event: work.lease.acquired") ||
		!strings.Contains(got, redactedWorkLeaseEventPayload) ||
		!strings.Contains(got, ordinaryCanary) {
		t.Fatalf("work:read-only stream projection = %s", got)
	}

	// Positive direction: the same mixed surfaces retain both ordinary and lease
	// events once the caller also holds the explicit lease read permission.
	h.m.UseWorkAuthorizer(permissionSetWorkAuthorizer{
		permWorkRead:  true,
		permLeaseRead: true,
	})
	privileged := h.do(http.MethodGet, eventsPath, admin, tenantHdr(tenant))
	privilegedItems, _ := privileged.body["items"].([]any)
	if privileged.code != http.StatusOK || len(privilegedItems) != 3 ||
		!strings.Contains(privileged.raw, leaseCanary) ||
		!strings.Contains(privileged.raw, ordinaryCanary) {
		t.Fatalf("lease:read timeline = %d %s", privileged.code, privileged.raw)
	}
	var privilegedStream bytes.Buffer
	emitted, _, err = h.m.streamWorkEvents(streamReq, mc, &privilegedStream, "")
	if err != nil || !emitted || !strings.Contains(privilegedStream.String(), leaseCanary) ||
		!strings.Contains(privilegedStream.String(), ordinaryCanary) {
		t.Fatalf("lease:read stream = emitted %v err %v body %s",
			emitted, err, privilegedStream.String())
	}
}

type workStreamScopeForTest struct {
	store.Scope
	events store.GenericRepo
}

func (s workStreamScopeForTest) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind != workEventKind {
		return nil, fmt.Errorf("unexpected stream repository %s", kind)
	}
	return s.events, nil
}

type workStreamDataForTest struct {
	scope store.Scope
}

func (d workStreamDataForTest) View(_ context.Context, fn func(store.Scope) error) error {
	return fn(d.scope)
}

func (workStreamDataForTest) Mutate(context.Context, func(store.Scope) error) error {
	return errors.New("unexpected stream mutation")
}

func (workStreamDataForTest) Export(context.Context, func(store.ExportScope) error) error {
	return errors.New("unexpected stream export")
}

func TestLegacyWorkStreamExcludesMessageAggregate(t *testing.T) {
	t.Parallel()

	workspace := model.NewID()
	workEventID := model.NewID()
	messageEventID := model.NewID()
	event := func(id model.ID, kind model.Kind, payload string) model.Record {
		return model.Record{
			colEventID: id.String(), colWorkWorkspaceID: workspace.String(),
			colEventAggregateKind: string(kind), colEventAggregateID: model.NewID().String(),
			colEventSeq: int64(1), colEventType: "test.event",
			colEventOccurredAt: model.SystemClock{}.Now().String(), colEventPayload: payload,
		}
	}
	workRow := event(workEventID, workItemKind, `{"canary":"WORK_VISIBLE"}`)
	messageRow := event(messageEventID, messageKind, `{"canary":"MESSAGE_MUST_NOT_LEAK"}`)
	repo := workListRepoForTest{list: func(query model.Query) ([]model.Record, error) {
		for _, filter := range query.Filters {
			if filter.Column == colEventAggregateKind && filter.Op == model.OpEq &&
				filter.Value == string(workItemKind) {
				return []model.Record{workRow}, nil
			}
		}
		// This branch is the live mutation control: omitting the aggregate filter
		// makes the fake store return the Message event as a real backend would.
		return []model.Record{workRow, messageRow}, nil
	}}
	mc := api.ModuleContext{Data: workStreamDataForTest{scope: workStreamScopeForTest{events: repo}}}
	request := httptest.NewRequest(http.MethodGet, "/v1/m/sessions/work-stream", nil)
	var output bytes.Buffer
	emitted, cursor, err := (&Module{}).streamWorkEvents(request, mc, &output, "")
	if err != nil || !emitted || cursor != workEventID.String() {
		t.Fatalf("legacy work stream = emitted %v cursor %q err %v", emitted, cursor, err)
	}
	if body := output.String(); !strings.Contains(body, "WORK_VISIBLE") ||
		strings.Contains(body, "MESSAGE_MUST_NOT_LEAK") || strings.Contains(body, string(messageKind)) {
		t.Fatalf("legacy work stream crossed into Message aggregate: %s", body)
	}
}

func TestWorkAPIWorkspaceConfinementFiltersRowsAndMutations(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-workspace-confined")
	workspaceA := workAPICreateWorkspace(t, h, tenant, "work-a")
	workspaceB := workAPICreateWorkspace(t, h, tenant, "work-b")
	path := "/v1/m/sessions/work-items"
	create := func(workspace model.ID, title string) resp {
		return h.doJSON(http.MethodPost, path+"?mode=apply", admin,
			workAPICreateBody(workspace, "owner:"+title, title),
			workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	}
	// Seed B first so a tenant-wide outbox nudge would publish the foreign row.
	// The later confined mutation must retain its request-scoped data handle and
	// can therefore publish only a workspace-A event.
	itemB, itemA := create(workspaceB, "workspace-b"), create(workspaceA, "workspace-a")
	idA, _ := itemA.body["result_id"].(string)
	idB, _ := itemB.body["result_id"].(string)
	if itemA.code != http.StatusOK || itemB.code != http.StatusOK || idA == "" || idB == "" {
		t.Fatalf("seed workspace work: A=%d %s B=%d %s", itemA.code, itemA.raw, itemB.code, itemB.raw)
	}
	confined := workAPIRoleTokenIn(
		t, h, admin, tenant, auth.RoleAdmin, "work-confined@a.test", workspaceA,
	)

	listed := h.do(http.MethodGet, path, confined, tenantHdr(tenant))
	items, _ := listed.body["items"].([]any)
	if listed.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("confined list = %d %s", listed.code, listed.raw)
	}
	row, _ := items[0].(map[string]any)
	if row["id"] != idA || row["workspace_id"] != workspaceA.String() {
		t.Fatalf("confined list row = %#v, want only workspace A", row)
	}
	if got := h.do(http.MethodGet, path+"/"+idA, confined, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("confined get own item = %d %s", got.code, got.raw)
	}
	if got := h.do(http.MethodGet, path+"/"+idB, confined, tenantHdr(tenant)); got.code != http.StatusNotFound {
		t.Fatalf("confined get foreign item = %d %s", got.code, got.raw)
	}
	assignment := map[string]any{"owner_kind": "user", "owner_ref": "owner:confined"}
	sink := &recordingWorkSink{}
	h.m.UseWorkEventSink(sink)
	allowed := h.doJSON(http.MethodPost, path+"/"+idA+"/assignments?mode=apply", confined, assignment,
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String(), "If-Match": `"v1"`}))
	if allowed.code != http.StatusOK || allowed.body["verdict"] != string(VerdictClean) {
		t.Fatalf("confined assignment in workspace = %d %s", allowed.code, allowed.raw)
	}
	if len(sink.events) != 1 || sink.events[0].WorkspaceID != workspaceA {
		t.Fatalf("confined apply published outside workspace A: %#v", sink.events)
	}
	foreign := h.doJSON(http.MethodPost, path+"/"+idB+"/assignments?mode=validate", confined, assignment, tenantHdr(tenant))
	if foreign.code != http.StatusOK || foreign.body["verdict"] != string(VerdictBroken) ||
		workAPIErrorCode(foreign) != "not_found" {
		t.Fatalf("confined foreign assignment = %d %s", foreign.code, foreign.raw)
	}
	collectionCreate := h.doJSON(http.MethodPost, path+"?mode=validate", confined,
		workAPICreateBody(workspaceB, "owner:escape", "escape"), tenantHdr(tenant))
	if collectionCreate.code != http.StatusOK || collectionCreate.body["verdict"] != string(VerdictBroken) ||
		workAPIErrorCode(collectionCreate) != "not_found" {
		t.Fatalf("confined collection create = %d %s", collectionCreate.code, collectionCreate.raw)
	}
}

func TestWorkAPIChildCollectionsMutateAndPaginate(t *testing.T) {
	h := newWorkAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "work-child-collections")
	workspace := workAPIWorkspace(t, h, tenant)
	itemsPath := "/v1/m/sessions/work-items"
	create := func(title string) resp {
		return workAPIApplyAtVersion(
			h, http.MethodPost, itemsPath, admin, tenant, 0,
			workAPICreateBody(workspace, "owner:"+title, title),
		)
	}
	mainItem, predecessorA, predecessorB := create("children-main"), create("children-a"), create("children-b")
	mainID, _ := mainItem.body["result_id"].(string)
	predAID, _ := predecessorA.body["result_id"].(string)
	predBID, _ := predecessorB.body["result_id"].(string)
	if mainItem.code != http.StatusOK || predecessorA.code != http.StatusOK || predecessorB.code != http.StatusOK ||
		mainID == "" || predAID == "" || predBID == "" {
		t.Fatalf("seed children: main=%d %s A=%d %s B=%d %s",
			mainItem.code, mainItem.raw, predecessorA.code, predecessorA.raw, predecessorB.code, predecessorB.raw)
	}
	versionOf := func(response resp) int64 {
		value, _ := response.body["version"].(float64)
		return int64(value)
	}

	// A real child under another parent is the negative-direction witness: no
	// collection below mainID may return it even though tenant and workspace match.
	decoyDependency := workAPIApplyAtVersion(
		h, http.MethodPost, itemsPath+"/"+predAID+"/dependencies", admin, tenant, 1,
		map[string]any{"depends_on_id": predBID},
	)
	decoyDependencyID, _ := decoyDependency.body["result_id"].(string)
	if decoyDependency.code != http.StatusOK || decoyDependencyID == "" || versionOf(decoyDependency) != 2 {
		t.Fatalf("decoy dependency = %d %s", decoyDependency.code, decoyDependency.raw)
	}

	mainVersion := int64(1)
	dependencyA := workAPIApplyAtVersion(
		h, http.MethodPost, itemsPath+"/"+mainID+"/dependencies", admin, tenant, mainVersion,
		map[string]any{"depends_on_id": predAID},
	)
	dependencyAID, _ := dependencyA.body["result_id"].(string)
	if dependencyA.code != http.StatusOK || dependencyAID == "" || versionOf(dependencyA) != 2 {
		t.Fatalf("add dependency A = %d %s", dependencyA.code, dependencyA.raw)
	}
	mainVersion = versionOf(dependencyA)
	duplicate := workAPIApplyAtVersion(
		h, http.MethodPost, itemsPath+"/"+mainID+"/dependencies", admin, tenant, mainVersion,
		map[string]any{"depends_on_id": predAID},
	)
	if duplicate.code != http.StatusConflict || workAPIErrorCode(duplicate) != "dependency_exists" {
		t.Fatalf("duplicate dependency = %d %s", duplicate.code, duplicate.raw)
	}
	dependencyB := workAPIApplyAtVersion(
		h, http.MethodPost, itemsPath+"/"+mainID+"/dependencies", admin, tenant, mainVersion,
		map[string]any{"depends_on_id": predBID},
	)
	dependencyBID, _ := dependencyB.body["result_id"].(string)
	if dependencyB.code != http.StatusOK || dependencyBID == "" || versionOf(dependencyB) != 3 {
		t.Fatalf("add dependency B = %d %s", dependencyB.code, dependencyB.raw)
	}
	mainVersion = versionOf(dependencyB)

	wrongParent := workAPIApplyAtVersion(
		h, http.MethodDelete, itemsPath+"/"+predAID+"/dependencies/"+dependencyAID,
		admin, tenant, versionOf(decoyDependency), map[string]any{},
	)
	if wrongParent.code != http.StatusNotFound || workAPIErrorCode(wrongParent) != "not_found" {
		t.Fatalf("remove through wrong parent = %d %s", wrongParent.code, wrongParent.raw)
	}
	removed := workAPIApplyAtVersion(
		h, http.MethodDelete, itemsPath+"/"+mainID+"/dependencies/"+dependencyAID,
		admin, tenant, mainVersion, map[string]any{},
	)
	if removed.code != http.StatusOK || versionOf(removed) != 4 {
		t.Fatalf("remove dependency = %d %s", removed.code, removed.raw)
	}
	mainVersion = versionOf(removed)
	removedAgain := workAPIApplyAtVersion(
		h, http.MethodDelete, itemsPath+"/"+mainID+"/dependencies/"+dependencyAID,
		admin, tenant, mainVersion, map[string]any{},
	)
	if removedAgain.code != http.StatusConflict || workAPIErrorCode(removedAgain) != "target_closed" {
		t.Fatalf("repeat dependency removal = %d %s", removedAgain.code, removedAgain.raw)
	}

	dependenciesPath := itemsPath + "/" + mainID + "/dependencies"
	dependencies := workAPICollectChildren(t, h, admin, tenant, dependenciesPath, 1)
	if len(dependencies) != 2 {
		t.Fatalf("main dependencies = %d, want 2", len(dependencies))
	}
	dependencyState := map[string]bool{}
	for _, row := range dependencies {
		id, _ := row["id"].(string)
		active, _ := row[colDepActive].(bool)
		dependencyState[id] = active
		if row[colWorkItemID] != mainID || id == decoyDependencyID {
			t.Fatalf("dependency escaped its parent: %#v", row)
		}
	}
	if dependencyState[dependencyAID] || !dependencyState[dependencyBID] {
		t.Fatalf("dependency tombstone states = %#v", dependencyState)
	}
	decoyRows := workAPICollectChildren(
		t, h, admin, tenant, itemsPath+"/"+predAID+"/dependencies", 1,
	)
	if len(decoyRows) != 1 || decoyRows[0]["id"] != decoyDependencyID {
		t.Fatalf("decoy parent dependencies = %#v", decoyRows)
	}
	removedB := workAPIApplyAtVersion(
		h, http.MethodDelete, itemsPath+"/"+mainID+"/dependencies/"+dependencyBID,
		admin, tenant, mainVersion, map[string]any{},
	)
	if removedB.code != http.StatusOK || versionOf(removedB) != 5 {
		t.Fatalf("remove dependency B before execution = %d %s", removedB.code, removedB.raw)
	}
	mainVersion = versionOf(removedB)

	criterionA := workAPIApplyAtVersion(
		h, http.MethodPost, itemsPath+"/"+mainID+"/acceptance", admin, tenant, mainVersion,
		map[string]any{
			"criterion_key": "api-a", "ordinal": 1,
			"statement": "The first API criterion passes.", "required": false,
		},
	)
	criterionAID, _ := criterionA.body["result_id"].(string)
	if criterionA.code != http.StatusOK || criterionAID == "" || versionOf(criterionA) != 6 {
		t.Fatalf("add criterion A = %d %s", criterionA.code, criterionA.raw)
	}
	mainVersion = versionOf(criterionA)
	criterionB := workAPIApplyAtVersion(
		h, http.MethodPost, itemsPath+"/"+mainID+"/acceptance", admin, tenant, mainVersion,
		map[string]any{
			"criterion_key": "api-b", "ordinal": 2,
			"statement": "The second API criterion is observable.", "required": false,
		},
	)
	criterionBID, _ := criterionB.body["result_id"].(string)
	if criterionB.code != http.StatusOK || criterionBID == "" || versionOf(criterionB) != 7 {
		t.Fatalf("add criterion B = %d %s", criterionB.code, criterionB.raw)
	}
	mainVersion = versionOf(criterionB)
	updatedCriterion := workAPIApplyAtVersion(
		h, http.MethodPatch, itemsPath+"/"+mainID+"/acceptance/"+criterionAID,
		admin, tenant, mainVersion, map[string]any{
			"command": "acceptance.update", "ordinal": 3,
			"statement": "The edited API criterion remains observable.", "required": true,
		},
	)
	if updatedCriterion.code != http.StatusOK || versionOf(updatedCriterion) != 8 {
		t.Fatalf("update draft criterion = %d %s", updatedCriterion.code, updatedCriterion.raw)
	}
	mainVersion = versionOf(updatedCriterion)

	acceptancePath := itemsPath + "/" + mainID + "/acceptance"
	criteria := workAPICollectChildren(t, h, admin, tenant, acceptancePath, 2)
	if len(criteria) != 3 {
		t.Fatalf("main acceptance rows = %d, want initial + 2", len(criteria))
	}
	criterionIDs := map[string]bool{}
	var editedCriterion map[string]any
	for _, row := range criteria {
		id, _ := row["id"].(string)
		criterionIDs[id] = true
		if id == criterionAID {
			editedCriterion = row
		}
		if row[colWorkItemID] != mainID {
			t.Fatalf("acceptance escaped its parent: %#v", row)
		}
	}
	if !criterionIDs[criterionAID] || !criterionIDs[criterionBID] {
		t.Fatalf("added criteria missing: %#v", criterionIDs)
	}
	if editedCriterion[colAccKey] != "api-a" || editedCriterion[colAccStatement] != "The edited API criterion remains observable." ||
		editedCriterion[colAccOrdinal] != float64(3) || editedCriterion[colAccRequired] != true ||
		editedCriterion[colAccState] != "pending" {
		t.Fatalf("draft criterion definition was not updated without changing identity/state: %#v", editedCriterion)
	}

	evaluation := map[string]any{
		"state": "passed", "evidence_ref": "job:work-child-api",
		"evidence_hash": strings.Repeat("a", 64),
	}
	draftEvaluation := workAPIApplyAtVersion(
		h, http.MethodPatch, acceptancePath+"/"+criterionAID,
		admin, tenant, mainVersion, evaluation,
	)
	if draftEvaluation.code != http.StatusConflict || workAPIErrorCode(draftEvaluation) != "illegal_transition" {
		t.Fatalf("evaluate while draft = %d %s", draftEvaluation.code, draftEvaluation.raw)
	}
	if unchanged := h.do(http.MethodGet, itemsPath+"/"+mainID, admin, tenantHdr(tenant)); unchanged.code != http.StatusOK || unchanged.header.Get("ETag") != `"v8"` {
		t.Fatalf("failed evaluation changed item = %d %s ETag=%q",
			unchanged.code, unchanged.raw, unchanged.header.Get("ETag"))
	}

	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(context.Background(), model.ID(mainID))
		if err != nil {
			return err
		}
		now := model.SystemClock{}.Now().String()
		item[colWorkStatus], item[colWorkReadyAt] = "ready", now
		item, err = repo.Update(context.Background(), item)
		if err != nil {
			return err
		}
		item[colWorkStatus], item[colWorkStartedAt] = "active", now
		item, err = repo.Update(context.Background(), item)
		if err == nil {
			mainVersion = item.Int(model.ColVersion)
		}
		return err
	}); err != nil {
		t.Fatalf("seed active item: %v", err)
	}
	unfencedActive := workAPIApplyAtVersion(
		h, http.MethodPatch, acceptancePath+"/"+criterionAID,
		admin, tenant, mainVersion, evaluation,
	)
	if unfencedActive.code != http.StatusForbidden || workAPIErrorCode(unfencedActive) != "forbidden" {
		t.Fatalf("unfenced active evaluation = %d %s", unfencedActive.code, unfencedActive.raw)
	}
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(context.Background(), model.ID(mainID))
		if err != nil {
			return err
		}
		item[colWorkStatus], item[colWorkReviewAt] = "review", model.SystemClock{}.Now().String()
		item, err = repo.Update(context.Background(), item)
		if err == nil {
			mainVersion = item.Int(model.ColVersion)
		}
		return err
	}); err != nil {
		t.Fatalf("seed review item: %v", err)
	}
	passed := workAPIApplyAtVersion(
		h, http.MethodPatch, acceptancePath+"/"+criterionAID,
		admin, tenant, mainVersion, evaluation,
	)
	if passed.code != http.StatusOK || versionOf(passed) != mainVersion+1 {
		t.Fatalf("evaluate review criterion = %d %s", passed.code, passed.raw)
	}
	mainVersion = versionOf(passed)
	passedAgain := workAPIApplyAtVersion(
		h, http.MethodPatch, acceptancePath+"/"+criterionAID,
		admin, tenant, mainVersion, evaluation,
	)
	if passedAgain.code != http.StatusConflict || workAPIErrorCode(passedAgain) != "illegal_transition" {
		t.Fatalf("repeat criterion evaluation = %d %s", passedAgain.code, passedAgain.raw)
	}
	criteria = workAPICollectChildren(t, h, admin, tenant, acceptancePath, 2)
	var evaluated map[string]any
	for _, row := range criteria {
		if row["id"] == criterionAID {
			evaluated = row
		}
	}
	if evaluated == nil || evaluated[colAccState] != "passed" || evaluated[colAccEvidenceRef] != "job:work-child-api" ||
		evaluated[colAccStatement] != "The edited API criterion remains observable." ||
		evaluated[colAccRequired] != true || evaluated[colAccOrdinal] != float64(3) {
		t.Fatalf("evaluated criterion = %#v", evaluated)
	}

	eventsPath := itemsPath + "/" + mainID + "/events"
	events := workAPICollectChildren(t, h, admin, tenant, eventsPath, 2)
	if len(events) != 9 {
		t.Fatalf("timeline events = %d, want 9", len(events))
	}
	seenSeq := map[int64]bool{}
	for _, event := range events {
		seq, _ := event[colEventSeq].(float64)
		seenSeq[int64(seq)] = true
		if event[colEventAggregateID] != mainID || event[colEventAggregateKind] != string(workItemKind) {
			t.Fatalf("timeline escaped its aggregate: %#v", event)
		}
	}
	for seq := int64(1); seq <= 9; seq++ {
		if !seenSeq[seq] {
			t.Fatalf("timeline missing sequence %d: %#v", seq, seenSeq)
		}
	}

	if unauthenticated := h.do(http.MethodGet, dependenciesPath, "", tenantHdr(tenant)); unauthenticated.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated children = %d %s", unauthenticated.code, unauthenticated.raw)
	}
	if unknown := h.do(http.MethodGet, dependenciesPath+"?active=true", admin, tenantHdr(tenant)); unknown.code != http.StatusBadRequest || workAPIErrorCode(unknown) != "invalid_command" {
		t.Fatalf("unknown child filter = %d %s", unknown.code, unknown.raw)
	}
	if invalidCursor := h.do(http.MethodGet, dependenciesPath+"?cursor=not-a-uuid", admin, tenantHdr(tenant)); invalidCursor.code != http.StatusBadRequest || workAPIErrorCode(invalidCursor) != "invalid_cursor" {
		t.Fatalf("invalid child cursor = %d %s", invalidCursor.code, invalidCursor.raw)
	}
	missingParent := h.do(
		http.MethodGet, itemsPath+"/"+model.NewID().String()+"/events", admin, tenantHdr(tenant),
	)
	if missingParent.code != http.StatusNotFound || workAPIErrorCode(missingParent) != "not_found" {
		t.Fatalf("missing parent timeline = %d %s", missingParent.code, missingParent.raw)
	}
}
