// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// TestGovernanceWritesRequireAAL3 measures all six operator write intents the
// inference-proxy console exposes. The AAL1 attempt must be refused before any
// mutation; the same valid request then succeeds under an AAL3 principal.
func TestGovernanceWritesRequireAAL3(t *testing.T) {
	m, st, tenant := newPolicyHarness(t)
	low := policyModuleContextAAL(st, tenant, auth.RoleAdmin, auth.AAL1)
	high := policyModuleContextAAL(st, tenant, auth.RoleAdmin, auth.AAL3)

	call := func(method, target, body string, mc api.ModuleContext, handler api.ModuleHandler) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		handler(rec, req, mc)
		return rec
	}
	callDelete := func(id string, mc api.ModuleContext) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, "/dlp/rules/"+id, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		m.handleDeleteDLPRule(rec, req, mc)
		return rec
	}
	assertStepUp := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s at AAL1 = %d %s, want 403", name, rec.Code, rec.Body.String())
		}
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "step_up_required" {
			t.Fatalf("%s at AAL1 body = %q, want step_up_required", name, rec.Body.String())
		}
	}

	configBody := `{"fail_open":true}`
	assertStepUp("save config", call(http.MethodPut, "/config", configBody, low, m.handlePutConfig))
	if rec := call(http.MethodPut, "/config", configBody, high, m.handlePutConfig); rec.Code != http.StatusOK {
		t.Fatalf("save config at AAL3 = %d %s", rec.Code, rec.Body.String())
	}

	createBody := `{"class":"pii","action":"deny","note":"initial"}`
	assertStepUp("create DLP rule", call(http.MethodPut, "/dlp/rules", createBody, low, m.handlePutDLPRule))
	created := call(http.MethodPut, "/dlp/rules", createBody, high, m.handlePutDLPRule)
	if created.Code != http.StatusCreated {
		t.Fatalf("create DLP rule at AAL3 = %d %s", created.Code, created.Body.String())
	}
	var rule dlpRuleDTO
	if err := json.Unmarshal(created.Body.Bytes(), &rule); err != nil || rule.ID == "" {
		t.Fatalf("decode created DLP rule: %v body=%s", err, created.Body.String())
	}

	editBody := `{"class":"pii","action":"allow","note":"reviewed"}`
	assertStepUp("edit DLP rule", call(http.MethodPut, "/dlp/rules", editBody, low, m.handlePutDLPRule))
	if rec := call(http.MethodPut, "/dlp/rules", editBody, high, m.handlePutDLPRule); rec.Code != http.StatusOK {
		t.Fatalf("edit DLP rule at AAL3 = %d %s", rec.Code, rec.Body.String())
	}

	assertStepUp("delete DLP rule", callDelete(rule.ID, low))
	if rec := callDelete(rule.ID, high); rec.Code != http.StatusNoContent {
		t.Fatalf("delete DLP rule at AAL3 = %d %s", rec.Code, rec.Body.String())
	}

	now := time.Now().UTC()
	for _, decision := range []struct {
		name, deviceCode, userCode string
		deny                       bool
	}{
		{"approve device", "device-approve", "ABCD-EFGH", false},
		{"deny device", "device-deny", "JKLM-NPQR", true},
	} {
		if _, err := m.CreateDeviceGrant(context.Background(), tenant, decision.deviceCode, decision.userCode, now, 10*time.Minute); err != nil {
			t.Fatalf("create grant for %s: %v", decision.name, err)
		}
		body, err := json.Marshal(approveDeviceGrantRequest{UserCode: decision.userCode, Deny: decision.deny})
		if err != nil {
			t.Fatal(err)
		}
		assertStepUp(decision.name, call(http.MethodPost, "/device/approve", string(body), low, m.handleApproveDeviceGrant))
		if rec := call(http.MethodPost, "/device/approve", string(body), high, m.handleApproveDeviceGrant); rec.Code != http.StatusOK {
			t.Fatalf("%s at AAL3 = %d %s", decision.name, rec.Code, rec.Body.String())
		}
	}

	pol, err := m.Policy(context.Background(), tenant)
	if err != nil || !pol.FailOpen {
		t.Fatalf("AAL3 config effect = %+v err=%v", pol, err)
	}
	listReq := httptest.NewRequest(http.MethodGet, "/dlp/rules", nil)
	listRec := httptest.NewRecorder()
	m.handleListDLPRules(listRec, listReq, high)
	var rules listResponse[dlpRuleDTO]
	if err := json.Unmarshal(listRec.Body.Bytes(), &rules); err != nil || len(rules.Items) != 0 {
		t.Fatalf("DLP rules after AAL3 delete = %+v err=%v", rules.Items, err)
	}
}
