// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// askModel is the model stand-in: it proposes whatever the test scripts. The unit under test is
// Ask and the approval it waits on, not the model.
type askModel struct{ reply string }

func (a *askModel) Execute(_ context.Context, req models.ExecuteRequest) (models.ExecuteResult, error) {
	res := models.ExecuteResult{Text: a.reply}
	if len(req.Chain) > 0 {
		res.Served = req.Chain[0]
	}
	return res, nil
}

type askNoStop struct{}

func (askNoStop) Check(context.Context, model.TenantID) (models.StopDecision, error) {
	return models.StopDecision{}, nil
}

type askGov struct {
	h      *harness
	model  *askModel
	admin  string
	tenant model.TenantID
	// runs counts the session starts Ask ran.
	runs int
}

// newAskGov is governance with the models module beside it and Ask wired as the engine wires
// it: each tool call through the engine's own routes as the derived Ask principal.
func newAskGov(t *testing.T) *askGov {
	t.Helper()
	g := &askGov{model: &askModel{}}
	m := models.New(models.WithExecutor(g.model), models.WithStopGate(askNoStop{}))
	g.h = newHarnessWith(t, harnessOpts{registerExtra: m.RegisterSchema, extraModules: []api.Module{m}})
	m.UseData(api.NewModuleData(g.h.st))
	m.UseAsk(models.AskPorts{
		Principal: g.h.authr.AskPrincipal,
		Serve: func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/m/sessions/runs" {
				g.runs++
			}
			g.h.srv.ServeSession(w, r, p)
		},
		PromptDenied: func(context.Context, model.TenantID, string) ([]string, error) { return nil, nil },
	})
	g.admin = g.h.adminLogin()
	g.tenant = g.h.createOrg(g.admin, "acme")
	if err := g.h.st.Mutate(context.Background(), g.tenant, func(sc store.Scope) error {
		p, err := sc.Providers().Create(context.Background(), model.Provider{Name: "ollama", Kind: "ollama", Status: model.StatusActive})
		if err != nil {
			return err
		}
		_, err = sc.Models().Create(context.Background(), model.Model{Name: "llama3", ProviderID: p.ID, Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := g.do("POST", "/v1/m/models/routing-policies", g.admin, map[string]any{"name": "local", "enabled": true, "strategy": "cost"})
	if r.code != http.StatusCreated {
		t.Fatalf("routing policy = %d %s", r.code, r.raw)
	}
	if r := g.do("PUT", "/v1/m/models/ask/binding", g.admin, map[string]any{"model": r.body["id"]}); r.code != http.StatusOK {
		t.Fatalf("bind = %d %s", r.code, r.raw)
	}
	return g
}

func (g *askGov) do(method, path, token string, body any) resp {
	return g.h.do(method, path, token, body, tenantHdr(g.tenant))
}

// propose has the model propose starting a session called name and returns the proposal.
func (g *askGov) propose(token, name string) map[string]any {
	g.h.t.Helper()
	g.model.reply = `{"tool":"run.start","args":{"name":"` + name + `"}}`
	r := g.do("POST", "/v1/m/models/ask/turns", token, map[string]any{"messages": []map[string]string{{"role": "user", "content": "start " + name}}})
	proposal, _ := r.body["proposal"].(map[string]any)
	if r.code != http.StatusOK || r.body["kind"] != "proposal" || proposal["approval_id"] == "" || proposal["status"] != "pending" {
		g.h.t.Fatalf("turn = %d %s", r.code, r.raw)
	}
	return proposal
}

func (g *askGov) run(token, id, name string) resp {
	return g.do("POST", "/v1/m/models/ask/proposals/"+id+"/run", token, map[string]any{"tool": "run.start", "args": map[string]string{"name": name}})
}

// D2, T5, T6, T12: a write Ask proposes only opens an approval bound to the exact action; it
// runs nothing until a person approved it. A person alone, with no approval policy for the
// action, may approve their own proposal from their own credential. It then runs once: a
// changed argument and a second run are refused.
func TestAskWriteRunsOnlyOnceAfterAPersonApproved(t *testing.T) {
	g := newAskGov(t)
	id := g.propose(g.admin, "fix-tests")["approval_id"].(string)

	if r := g.run(g.admin, id, "fix-tests"); r.code != http.StatusConflict || g.runs != 0 {
		t.Fatalf("run before approval = %d %s (runs %d)", r.code, r.raw, g.runs)
	}
	got := g.do("GET", govPath+"/approvals/"+id, g.admin, nil)
	if got.body["origin"] != "ask" || got.body["action"] != "ask.run.start" || got.body["may_decide"] != true {
		t.Fatalf("approval = %s", got.raw)
	}
	if r := g.do("POST", govPath+"/approvals/"+id+"/decisions", g.admin, map[string]any{"decision": "approve"}); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("own decision = %d %s", r.code, r.raw)
	}
	if r := g.run(g.admin, id, "other-name"); r.code != http.StatusConflict || g.runs != 0 {
		t.Fatalf("changed argument = %d %s (runs %d)", r.code, r.raw, g.runs)
	}
	if r := g.run(g.admin, id, "fix-tests"); r.code != http.StatusOK || g.runs != 1 {
		t.Fatalf("approved run = %d %s (runs %d)", r.code, r.raw, g.runs)
	}
	if r := g.run(g.admin, id, "fix-tests"); r.code != http.StatusConflict || g.runs != 1 {
		t.Fatalf("second run = %d %s (runs %d)", r.code, r.raw, g.runs)
	}
}

// T12: when an approval policy matches the action, its separation of duty holds: the person
// who asked cannot approve their own proposal.
func TestAskOwnDecisionYieldsToAMatchingPolicy(t *testing.T) {
	g := newAskGov(t)
	if r := g.do("POST", govPath+"/policies", g.admin, map[string]any{"name": "ask", "kind": "approval", "enabled": true,
		"spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": "ask.run.start"}}}); r.code != http.StatusCreated {
		t.Fatalf("policy = %d %s", r.code, r.raw)
	}
	id := g.propose(g.admin, "fix-tests")["approval_id"].(string)
	if r := g.do("POST", govPath+"/approvals/"+id+"/decisions", g.admin, map[string]any{"decision": "approve"}); r.code != http.StatusForbidden {
		t.Fatalf("own decision under a policy = %d %s", r.code, r.raw)
	}
}

// T1: whatever the person holds, the principal Ask acts under never holds the decision
// permission, so the decision route refuses it.
func TestAskPrincipalCannotDecide(t *testing.T) {
	g := newAskGov(t)
	id := g.propose(g.admin, "fix-tests")["approval_id"].(string)
	person, err := g.h.authr.Authenticate(context.Background(), g.admin)
	if err != nil {
		t.Fatal(err)
	}
	ask, err := g.h.authr.AskPrincipal(context.Background(), person, g.tenant,
		[]auth.Permission{"governance:approval:admin", "governance:approval:write", "models:routing:admin"})
	if err != nil {
		t.Fatal(err)
	}
	if ceiling, _ := ask.PurposePermissionsIn(g.tenant); len(ceiling) != 1 || ceiling[0] != "governance:approval:write" {
		t.Fatalf("Ask's ceiling = %v", ceiling)
	}
	req := httptest.NewRequest(http.MethodPost, govPath+"/approvals/"+id+"/decisions", strings.NewReader(`{"decision":"approve"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.h.srv.ServeSession(rec, req, ask)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Ask deciding = %d %s", rec.Code, rec.Body.String())
	}
	if got := g.do("GET", govPath+"/approvals/"+id, g.admin, nil); got.body["status"] != "pending" {
		t.Fatalf("approval = %s", got.raw)
	}
}

// D2: an approval Ask opened is decided by a person in a signed-in session. The person's own
// API token, whatever its role, proposes but cannot approve, and the read says so.
func TestAskApprovalNeedsASignedInPerson(t *testing.T) {
	g := newAskGov(t)
	tok := g.h.do("POST", "/v1/tokens", g.admin, map[string]any{"name": "ci", "tenant": g.tenant.String(), "role": "admin"}, nil)
	if tok.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", tok.code, tok.raw)
	}
	token := tok.body["token"].(string)
	id := g.propose(token, "fix-tests")["approval_id"].(string)
	if got := g.do("GET", govPath+"/approvals/"+id, token, nil); got.body["may_decide"] != false {
		t.Fatalf("read for the token = %s", got.raw)
	}
	if r := g.do("POST", govPath+"/approvals/"+id+"/decisions", token, map[string]any{"decision": "approve"}); r.code != http.StatusForbidden {
		t.Fatalf("token decision = %d %s", r.code, r.raw)
	}
	if r := g.run(token, id, "fix-tests"); r.code != http.StatusConflict || g.runs != 0 {
		t.Fatalf("run = %d %s (runs %d)", r.code, r.raw, g.runs)
	}
}

// D3: turning Ask off stops a proposal a person already approved: it is not spent and nothing runs.
func TestAskOffStopsAnApprovedProposal(t *testing.T) {
	g := newAskGov(t)
	id := g.propose(g.admin, "fix-tests")["approval_id"].(string)
	if r := g.do("POST", govPath+"/approvals/"+id+"/decisions", g.admin, map[string]any{"decision": "approve"}); r.code != http.StatusOK {
		t.Fatalf("decision = %d %s", r.code, r.raw)
	}
	if r := g.do("PUT", "/v1/m/models/ask/binding", g.admin, map[string]any{"model": ""}); r.code != http.StatusOK {
		t.Fatalf("turning Ask off = %d %s", r.code, r.raw)
	}
	r := g.run(g.admin, id, "fix-tests")
	if errBody, _ := r.body["error"].(map[string]any); r.code != http.StatusConflict || errBody["code"] != "ask_off" || g.runs != 0 {
		t.Fatalf("run with Ask off = %d %s (runs %d)", r.code, r.raw, g.runs)
	}
	if got := g.do("GET", govPath+"/approvals/"+id, g.admin, nil); got.body["status"] != "approved" {
		t.Fatalf("approval after the refused run = %s", got.raw)
	}
}
