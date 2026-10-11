// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/models"
)

type textOperation interface {
	ExecuteText(context.Context, api.ModuleContext, model.ID, models.TextExecutionInput) (models.TextExecutionResult, error)
}

func TestTextOperationRequiresAuthenticationAndAdmission(t *testing.T) {
	chat := &stubChatExecutor{}
	m := models.New(models.WithChatExecutor(chat))
	op, ok := any(m).(textOperation)
	if !ok {
		t.Fatal("models has no supported in-process governed text operation")
	}
	result, err := op.ExecuteText(t.Context(), api.ModuleContext{}, model.NewID(), models.TextExecutionInput{Input: "local test turn"})
	if err == nil || result.StatusCode != http.StatusUnauthorized || result.Output != nil || chat.calls != 0 {
		t.Fatal("unauthenticated text operation must refuse before actuation")
	}
}

// operationContext uses real product authentication and its opaque credential
// reference. No model permission, resource or witness is manufactured here.
func operationContext(t *testing.T, c *chatHarness, token string) (context.Context, api.ModuleContext) {
	t.Helper()
	p, err := auth.NewAuthenticator(c.h.st, nil).Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal("authenticate operation caller:", err)
	}
	ref, ok := p.Ref()
	if !ok {
		t.Fatal("authenticated credential has no reference")
	}
	ctx, mc, err := c.h.srv.ModuleOperationContext(t.Context(), ref, c.tenant, models.Namespace)
	if err != nil {
		t.Fatal("resolve operation caller:", err)
	}
	return ctx, mc
}

func callTextOperation(t *testing.T, c *chatHarness, token string, in models.TextExecutionInput) (models.TextExecutionResult, error) {
	t.Helper()
	ctx, mc := operationContext(t, c, token)
	op, ok := any(c.module).(textOperation)
	if !ok {
		t.Fatal("governed text operation missing")
	}
	return op.ExecuteText(ctx, mc, model.ID(c.policy), in)
}

func TestTextOperationGateParity(t *testing.T) {
	tests := []struct {
		name   string
		opts   []models.Option
		change func(*chatHarness)
		input  models.TextExecutionInput
		status int
	}{
		{name: "success", status: 200},
		{name: "unsupported_operation", input: models.TextExecutionInput{Operation: "embeddings.create"}, status: 422},
		{name: "wrong_surface", input: models.TextExecutionInput{Surface: "other"}, status: 422},
		{name: "empty_input", input: models.TextExecutionInput{Input: " "}, status: 400},
		{name: "estate_stop", opts: []models.Option{models.WithStopGate(stoppedChatStopGate{})}, status: 423},
		{name: "unreadable_stop", opts: []models.Option{models.WithStopGate(stubStopGate{err: errors.New("sensitive vendor error endpoint")})}, status: 503},
		{name: "source_scope", opts: []models.Option{models.WithScopeGate(denyProfileModelScopeGate{modelRef: "claude-opus-4-8"})}, status: 403},
		{name: "withdrawn_profile", change: func(c *chatHarness) {
			delete(c.resolver.profiles, profileKey(c.tenant, c.profile.Ref, c.profile.Revision))
		}, status: 503},
		{name: "foreign_profile", change: func(c *chatHarness) {
			p := c.profile
			p.Tenant = model.NewTenantID()
			c.resolver.profiles[profileKey(c.tenant, p.Ref, p.Revision)] = p
		}, status: 503},
		{name: "endpoint_mismatch", change: func(c *chatHarness) {
			p := c.profile
			p.Endpoint = "https://other.invalid/v1/chat/completions"
			c.resolver.profiles[profileKey(c.tenant, p.Ref, p.Revision)] = p
			changeRoutingSpec(t, c, "gateway_endpoint", c.profile.Endpoint)
		}, status: 422},
		{name: "provider_mismatch", change: func(c *chatHarness) {
			p := c.profile
			p.ProviderRef = "other-provider"
			c.resolver.profiles[profileKey(c.tenant, p.Ref, p.Revision)] = p
		}, status: 422},
		{name: "unsupported_protocol", change: func(c *chatHarness) {
			p := c.profile
			p.Protocol = "other"
			c.resolver.profiles[profileKey(c.tenant, p.Ref, p.Revision)] = p
		}, status: 422},
		{name: "required_capability", change: func(c *chatHarness) { changeRoutingSpec(t, c, "required_capabilities", []string{"tool_use"}) }, status: 422},
		{name: "model_restriction", change: func(c *chatHarness) {
			owner := c.admin
			c.admin = c.h.roleToken(c.admin, c.tenant, "model-user@example.invalid", auth.RoleAdmin)
			r := createModelAccess(t, c.h, owner, c.tenant, map[string]any{"subject_kind": "role", "subject_ref": "admin", "target_kind": "model", "target_ref": "claude-sonnet-4-6"})
			if r.code != 201 {
				t.Fatal("create model access restriction")
			}
		}, status: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := "local stand-in response"
			chat := &stubChatExecutor{res: models.ChatExecutionResult{Output: &output, AttemptRef: "local-attempt", RequestRef: "local-request"}}
			c := newChatHarness(t, append(tt.opts, models.WithChatExecutor(chat))...)
			if tt.change != nil {
				tt.change(c)
			}
			in := tt.input
			if in.Input == "" {
				in.Input = "local test turn"
			}
			wire := c.execute(in)
			result, err := callTextOperation(t, c, c.admin, in)
			if result.StatusCode != tt.status || wire.code != tt.status || (err != nil) != (tt.status >= 400) {
				t.Fatalf("wire=%d operation=%d error=%v expected=%d", wire.code, result.StatusCode, err, tt.status)
			}
			encoded, e := json.Marshal(result)
			if e != nil {
				t.Fatal(e)
			}
			var opBody map[string]any
			if e = json.Unmarshal(encoded, &opBody); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(wire.body, opBody) {
				t.Fatal("HTTP and in-process execution answers diverged")
			}
			wantCalls := 0
			if tt.status == 200 {
				wantCalls = 2
			}
			if chat.calls != wantCalls {
				t.Fatalf("actuation calls=%d expected=%d", chat.calls, wantCalls)
			}
			if tt.status >= 400 && result.Output != nil {
				t.Fatal("refusal released output")
			}
			if tt.status == 200 && (result.Output == nil || *result.Output != output || chat.seen.Resource.ID != c.policy || chat.seen.Resource.Kind != "routing" || chat.seen.Authorization.PolicyVersion != 0) {
				t.Fatal("successful execution did not retain stored model-resource authority")
			}
		})
	}
}

func changeRoutingSpec(t *testing.T, c *chatHarness, key string, value any) {
	t.Helper()
	if err := c.h.st.Mutate(t.Context(), c.tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(t.Context(), model.ID(c.policy))
		if err != nil {
			return err
		}
		p.Spec[key] = value
		_, err = sc.Policies().Update(t.Context(), p)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTextOperationAuthorityRefusals(t *testing.T) {
	chat := &stubChatExecutor{}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	ctx, mc := operationContext(t, c, c.admin)
	t.Run("missing_admission", func(t *testing.T) {
		p, err := auth.NewAuthenticator(c.h.st, nil).Authenticate(ctx, c.admin)
		if err != nil {
			t.Fatal(err)
		}
		r, e := c.module.ExecuteText(ctx, api.ModuleContext{Principal: p, Tenant: c.tenant, Data: api.NewScopedData(c.h.st, c.tenant), Admission: c.h.srv.Admits}, model.ID(c.policy), models.TextExecutionInput{Input: "local test turn"})
		if e == nil || r.StatusCode != 403 {
			t.Fatal("hand-built admission must not establish engine-issued scope")
		}
	})
	t.Run("unmounted_receiver", func(t *testing.T) {
		other := models.New(models.WithChatExecutor(chat))
		r, e := other.ExecuteText(ctx, mc, model.ID(c.policy), models.TextExecutionInput{Input: "local turn"})
		if e == nil || r.StatusCode != 404 {
			t.Fatal("unmounted receiver bypassed the configured model gates")
		}
	})
	t.Run("cross_tenant_context", func(t *testing.T) {
		foreign := mc
		foreign.Tenant = model.NewTenantID()
		r, e := c.module.ExecuteText(ctx, foreign, model.ID(c.policy), models.TextExecutionInput{Input: "local test turn"})
		if e == nil || r.StatusCode != 403 {
			t.Fatal("cross-tenant context admitted")
		}
	})
	t.Run("foreign_policy", func(t *testing.T) {
		tenant := c.h.createOrg(c.admin, "other-tenant")
		id := createRoutingPolicy(t, c.h, c.admin, tenant)
		r, e := c.module.ExecuteText(ctx, mc, model.ID(id), models.TextExecutionInput{Input: "local test turn"})
		if e == nil || r.StatusCode != 404 {
			t.Fatal("foreign policy admitted")
		}
		w := c.h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", c.admin, models.TextExecutionInput{Input: "local test turn"}, tenantHdr(c.tenant))
		if w.code != 404 {
			t.Fatal("foreign HTTP policy admitted")
		}
	})
	t.Run("viewer_selection_is_not_execution", func(t *testing.T) {
		viewer := c.h.roleToken(c.admin, c.tenant, "reader@example.invalid", auth.RoleViewer)
		r, e := callTextOperation(t, c, viewer, models.TextExecutionInput{Input: "local test turn", SessionRef: "admin-session"})
		if e == nil || r.StatusCode != 403 {
			t.Fatal("selection/attribution conferred execution authority")
		}
		w := c.h.do("POST", "/v1/m/models/routing-policies/"+c.policy+"/execute", viewer, models.TextExecutionInput{Input: "local test turn"}, tenantHdr(c.tenant))
		if w.code != 403 {
			t.Fatal("HTTP viewer admitted")
		}
	})
	if chat.calls != 0 {
		t.Fatal("authority refusals actuated Chat")
	}
}

func TestTextOperationErrorsWithholdOutputAndKeepObservation(t *testing.T) {
	output := "sensitive output must be withheld"
	count := int64(0)
	chat := &stubChatExecutor{res: models.ChatExecutionResult{Output: &output, AttemptRef: "local-attempt"}, err: errors.New("sensitive upstream body credential endpoint")}
	chat.res.Usage = &mp.ChatTextUsage{PromptTokens: &count}
	chat.res.DispatchState = models.ChatDispatchAttempted
	c := newChatHarness(t, models.WithChatExecutor(chat))
	r, e := callTextOperation(t, c, c.admin, models.TextExecutionInput{Input: "sensitive local turn"})
	if e == nil || r.StatusCode != 500 || r.Output != nil || r.InputTokens == nil || *r.InputTokens != 0 || r.OutputTokens != nil || r.Execution.DispatchState != models.ChatDispatchAttempted {
		t.Fatal("error lost observation or exposed output")
	}
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "sensitive") || strings.Contains(e.Error(), "sensitive") {
		t.Fatal("operation error exposed unsafe content")
	}
}

func replaceOperationServer(t *testing.T, c *chatHarness, edit func(*api.Options)) {
	t.Helper()
	options := c.h.options
	edit(&options)
	s, err := api.New(options)
	if err != nil {
		t.Fatal(err)
	}
	c.h.srv = s
}

type operationRecorder struct {
	gateErr        error
	missingSession bool
	calls          []api.RecordedCall
	recordedCalls  []api.RecordedCall
	results        []api.RecordedResult
}

func (r *operationRecorder) Gate(_ context.Context, c api.RecordedCall) (api.RecordingDecision, error) {
	r.calls = append(r.calls, c)
	if r.missingSession {
		return api.RecordingDecision{Record: true}, nil
	}
	return api.RecordingDecision{Record: true, Session: model.NewID()}, r.gateErr
}

func TestTextOperationRecordingRequiresReservedSession(t *testing.T) {
	chat := &stubChatExecutor{}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	rec := &operationRecorder{missingSession: true}
	replaceOperationServer(t, c, func(o *api.Options) { o.Recorder = rec })
	in := models.TextExecutionInput{Input: "local turn"}
	wire := c.execute(in)
	r, e := callTextOperation(t, c, c.admin, in)
	if wire.code != 503 || r.StatusCode != 503 || e == nil || chat.calls != 0 || len(rec.results) != 0 {
		t.Fatal("recording without an exact reserved session admitted execution")
	}
}

// This route is an actual in-process module consumer: its admitted resource is
// distinct from the stored models policy, and both actions must be recorded.
type textConsumer struct {
	models          *models.Module
	policy          model.ID
	seen            *api.ModuleContext
	tamperPrincipal string
}

func (*textConsumer) APINamespace() string { return "text-consumer" }
func (*textConsumer) Permissions() []auth.Permission {
	return []auth.Permission{"text-consumer:turn:admin"}
}
func (c *textConsumer) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/turn", "text-consumer:turn:admin", func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		switch c.tamperPrincipal {
		case "module_context":
			mc.Principal.AAL = auth.AAL3
			mc.Principal.AMR[0] = "unverified"
			_, rebound, err := mc.ForModule(r.Context(), c.models)
			if err != nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			rebound.Principal.AMR[0] = "unverified-again"
		case "request_context":
			p, ok := api.RequestPrincipal(r.Context())
			if !ok || len(p.AMR) != 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			p.AMR[0] = "totp"
		}
		if c.seen != nil {
			*c.seen = mc
		}
		result, _ := c.models.ExecuteText(r.Context(), mc, c.policy, models.TextExecutionInput{Input: "local consumer turn"})
		w.WriteHeader(result.StatusCode)
		_ = json.NewEncoder(w).Encode(result)
	})
}
func TestTextOperationKeepsAuthenticatedPrincipalImmutable(t *testing.T) {
	for _, source := range []string{"module_context", "request_context"} {
		t.Run(source, func(t *testing.T) {
			chat := &stubChatExecutor{}
			c := newChatHarness(t, models.WithChatExecutor(chat))
			rec := &operationRecorder{}
			p, err := auth.NewAuthenticator(c.h.st, nil).Authenticate(t.Context(), c.admin)
			if err != nil || len(p.AMR) != 1 || p.AMR[0] != "pwd" {
				t.Fatal("fixture must enter with an authenticated password session")
			}
			replaceOperationServer(t, c, func(o *api.Options) {
				o.Modules = append(o.Modules, &textConsumer{models: c.module, policy: model.ID(c.policy), tamperPrincipal: source})
				o.Recorder = rec
			})
			wire := c.h.do("POST", "/v1/m/text-consumer/turn", c.admin, nil, tenantHdr(c.tenant))
			if wire.code != 200 || chat.calls != 1 {
				t.Fatal("authenticated consumer must execute once")
			}
			if chat.seen.Principal.AAL != p.AAL || !reflect.DeepEqual(chat.seen.Principal.AMR, p.AMR) {
				t.Error("public context changed the retained authenticated principal")
			}
			if len(rec.calls) != 2 || len(rec.recordedCalls) != 2 {
				t.Fatal("consumer and models must retain their own recording frames")
			}
			for phase, calls := range map[string][]api.RecordedCall{"gate": rec.calls, "record": rec.recordedCalls} {
				for _, call := range calls {
					if call.Principal.AAL != p.AAL || !reflect.DeepEqual(call.Principal.AMR, p.AMR) {
						t.Errorf("public context changed %s authentication methods for %s", phase, call.Namespace)
					}
				}
			}
		})
	}
}

func TestTextOperationNestedModuleConsumer(t *testing.T) {
	output := "local consumer response"
	chat := &stubChatExecutor{res: models.ChatExecutionResult{Output: &output}}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	rec := &operationRecorder{}
	var saved api.ModuleContext
	replaceOperationServer(t, c, func(o *api.Options) {
		o.Modules = append(o.Modules, &textConsumer{models: c.module, policy: model.ID(c.policy), seen: &saved})
		o.Recorder = rec
	})
	wire := c.h.do("POST", "/v1/m/text-consumer/turn", c.admin, nil, tenantHdr(c.tenant))
	if wire.code != 200 || chat.calls != 1 || len(rec.calls) != 2 || len(rec.results) != 2 || chat.seen.Resource.ID != c.policy || chat.seen.Resource.Kind != "routing" || chat.seen.Authorization.PolicyVersion != 0 {
		t.Fatal("module consumer failed stored model authority or nested recording")
	}
	if rec.calls[0].Namespace != "text-consumer" || rec.calls[1].Namespace != models.Namespace {
		t.Fatal("recording reused the consumer admission for model execution")
	}
	r, e := c.module.ExecuteText(t.Context(), saved, model.ID(c.policy), models.TextExecutionInput{Input: "local later turn"})
	if e == nil || r.StatusCode != 403 || chat.calls != 1 || len(rec.calls) != 2 {
		t.Fatal("completed HTTP scope retained execution or recording authority")
	}
}

func TestTextOperationAdmitsStoredPolicyResource(t *testing.T) {
	chat := &stubChatExecutor{}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	// Use the product's actual Cedar evaluator. The collection and consumer
	// route remain permitted; only this stored routing policy is forbidden.
	eval, e := governance.NewCedarEvaluator(fmt.Sprintf(`forbid(principal, action, resource == Resource::"%s") when { resource.kind == "routing" };`, c.policy), nil)
	if e != nil {
		t.Fatal(e)
	}
	replaceOperationServer(t, c, func(o *api.Options) {
		o.Authorizer = auth.NewAuthorizer(eval)
		o.Modules = append(o.Modules, &textConsumer{models: c.module, policy: model.ID(c.policy)})
	})
	in := models.TextExecutionInput{Input: "local turn"}
	wire := c.execute(in)
	r, e := callTextOperation(t, c, c.admin, in)
	consumer := c.h.do("POST", "/v1/m/text-consumer/turn", c.admin, nil, tenantHdr(c.tenant))
	if wire.code != 403 || r.StatusCode != 403 || e == nil || consumer.code != 403 || chat.calls != 0 {
		t.Fatal("collection/consumer admission substituted for stored policy authority")
	}
}

func TestTextOperationPreservesGlobalTokenHTTPContract(t *testing.T) {
	chat := &stubChatExecutor{}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	a := auth.NewAuthenticator(c.h.st, nil)
	p, e := a.Authenticate(t.Context(), c.admin)
	if e != nil {
		t.Fatal(e)
	}
	token, _, e := a.IssueToken(t.Context(), p, auth.TokenSpec{Name: "local global-token regression", Superadmin: true})
	if e != nil {
		t.Fatal(e)
	}
	wire := c.h.do("POST", "/v1/m/models/routing-policies/"+c.policy+"/execute", token, models.TextExecutionInput{Input: "local turn"}, tenantHdr(c.tenant))
	if wire.code != 200 || chat.calls != 1 {
		t.Fatal("existing global API-token HTTP contract regressed")
	}
}

func TestTextOperationRechecksRevokedCredential(t *testing.T) {
	chat := &stubChatExecutor{}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	ctx, mc := operationContext(t, c, c.admin)
	a := auth.NewAuthenticator(c.h.st, nil)
	p, e := a.Authenticate(t.Context(), c.admin)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.RevokeSession(t.Context(), p, p.CredID); e != nil {
		t.Fatal(e)
	}
	r, e := c.module.ExecuteText(ctx, mc, model.ID(c.policy), models.TextExecutionInput{Input: "local turn"})
	wire := c.execute(models.TextExecutionInput{Input: "local turn"})
	if r.StatusCode != 401 || wire.code != 401 || e == nil || chat.calls != 0 {
		t.Fatal("cached operation context retained revoked credential authority")
	}
}

func TestTextOperationConsentPrecedesPolicyRead(t *testing.T) {
	chat := &stubChatExecutor{}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	rec := &operationRecorder{gateErr: api.ErrRecordingConsentRequired}
	replaceOperationServer(t, c, func(o *api.Options) { o.Recorder = rec })
	ctx, mc := operationContext(t, c, c.admin)
	id := model.NewID()
	in := models.TextExecutionInput{Input: "local turn"}
	r, e := c.module.ExecuteText(ctx, mc, id, in)
	wire := c.h.do("POST", "/v1/m/models/routing-policies/"+id.String()+"/execute", c.admin, in, tenantHdr(c.tenant))
	if e == nil || r.StatusCode != 403 || wire.code != 403 || len(rec.calls) != 2 || chat.calls != 0 {
		t.Fatal("policy existence bypassed the recording consent door")
	}
}
func (r *operationRecorder) Record(_ context.Context, call api.RecordedCall, _ api.RecordingDecision, result api.RecordedResult) error {
	r.recordedCalls = append(r.recordedCalls, call)
	r.results = append(r.results, result)
	return nil
}

func TestTextOperationRecordingParity(t *testing.T) {
	for _, failure := range []error{nil, api.ErrRecordingConsentRequired, errors.New("sensitive recording failure")} {
		t.Run(fmt.Sprint(failure == nil), func(t *testing.T) {
			chat := &stubChatExecutor{}
			c := newChatHarness(t, models.WithChatExecutor(chat))
			rec := &operationRecorder{gateErr: failure}
			replaceOperationServer(t, c, func(o *api.Options) { o.Recorder = rec })
			input := models.TextExecutionInput{Input: "local turn never recorded as raw text"}
			wire := c.execute(input)
			result, err := callTextOperation(t, c, c.admin, input)
			if wire.code != result.StatusCode || (err != nil) != (failure != nil) {
				t.Fatalf("recording parity wire=%d operation=%d error=%v", wire.code, result.StatusCode, err)
			}
			if len(rec.calls) != 2 {
				t.Fatal("direct HTTP recording must be reserved exactly once; in-process must also gate")
			}
			for _, call := range rec.calls {
				if call.Namespace != models.Namespace || call.Permission != "models:routing:admin" || call.Params["id"] != c.policy {
					t.Fatal("recording described a different operation")
				}
			}
			if failure != nil {
				if chat.calls != 0 || len(rec.results) != 0 {
					t.Fatal("recording failure reached execution or completion")
				}
			} else {
				if chat.calls != 2 || len(rec.results) != 2 {
					t.Fatal("operation did not complete its existing recording seam")
				}
				for _, r := range rec.results {
					if r.Status != 200 || len(r.BodySHA256) != 32 || r.BodyBytes <= 0 {
						t.Fatal("recording lost input commitment or outcome")
					}
				}
			}
		})
	}
}

func TestTextOperationModuleOffAndServiceWithdrawal(t *testing.T) {
	t.Run("module_off", func(t *testing.T) {
		chat := &stubChatExecutor{}
		c := newChatHarness(t, models.WithChatExecutor(chat))
		replaceOperationServer(t, c, func(o *api.Options) { o.Modules = nil; o.NotEnabledModules = []string{models.Namespace} })
		p, e := auth.NewAuthenticator(c.h.st, nil).Authenticate(t.Context(), c.admin)
		if e != nil {
			t.Fatal(e)
		}
		ref, _ := p.Ref()
		_, _, e = c.h.srv.ModuleOperationContext(t.Context(), ref, c.tenant, models.Namespace)
		if !errors.Is(e, api.ErrModuleOperationDisabled) {
			t.Fatal("module-off admitted operation")
		}
		wire := c.execute(models.TextExecutionInput{Input: "local turn"})
		if wire.code != 404 || chat.calls != 0 {
			t.Fatal("module-off reached HTTP execution")
		}
	})
	t.Run("service_withdrawn", func(t *testing.T) {
		chat := &stubChatExecutor{}
		c := newChatHarness(t, models.WithChatExecutor(chat))
		replaceOperationServer(t, c, func(o *api.Options) { o.Store = suspension.Guard(o.Store, nil) })
		if e := c.h.st.System(t.Context(), func(s store.SystemScope) error {
			_, e := s.SetOrgStatus(t.Context(), c.tenant, model.StatusSuspended)
			return e
		}); e != nil {
			t.Fatal(e)
		}
		r, e := callTextOperation(t, c, c.admin, models.TextExecutionInput{Input: "local turn"})
		wire := c.execute(models.TextExecutionInput{Input: "local turn"})
		if e == nil || r.StatusCode != 423 || wire.code != 423 || chat.calls != 0 {
			t.Fatalf("withdrawal wire=%d operation=%d error=%v calls=%d", wire.code, r.StatusCode, e, chat.calls)
		}
	})
}

func TestTextOperationBudgetPrecheckParity(t *testing.T) {
	for _, budgetErr := range []error{nil, errors.New("sensitive budget error")} {
		t.Run(fmt.Sprint(budgetErr == nil), func(t *testing.T) {
			gate := &countingBudgetGate{decision: models.BudgetDecision{Allowed: true}, err: budgetErr}
			chat := &stubChatExecutor{budget: func(check models.ChatBudgetCheck) {
				_, denied := check(t.Context())
				if denied {
					t.Fatal("existing fail-open precheck denied allowed attempt")
				}
			}}
			c := newChatHarness(t, models.WithChatExecutor(chat), models.WithBudgetGate(gate))
			in := models.TextExecutionInput{Input: "local turn", SessionRef: "attribution-only"}
			wire := c.execute(in)
			r, e := callTextOperation(t, c, c.admin, in)
			if wire.code != 200 || r.StatusCode != 200 || e != nil || gate.calls != 2 || r.Execution.BudgetAssurance != models.ChatBudgetAssuranceDevelopmentPrecheck {
				t.Fatal("shared operation changed the existing budget assurance or sequencing")
			}
			for _, d := range gate.dims {
				if d.ProviderRef != c.profile.ProviderRef || d.ModelRef != c.profile.ModelRef || d.SessionRef != in.SessionRef {
					t.Fatal("precheck was asked about another decision")
				}
			}
		})
	}
}

func TestTextOperationRestoresWorkspaceConfinement(t *testing.T) {
	chat := &stubChatExecutor{}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	user := c.h.roleToken(c.admin, c.tenant, "confined@example.invalid", auth.RoleAdmin)
	a := auth.NewAuthenticator(c.h.st, nil)
	owner, e := a.Authenticate(t.Context(), c.admin)
	if e != nil {
		t.Fatal(e)
	}
	p, e := a.Authenticate(t.Context(), user)
	if e != nil {
		t.Fatal(e)
	}
	var workspace model.ID
	if e = c.h.st.Mutate(t.Context(), c.tenant, func(sc store.Scope) error {
		w, e := sc.Workspaces().Create(t.Context(), model.Workspace{Name: "Other workspace", Slug: "other", Status: model.StatusActive})
		workspace = w.ID
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.GrantMembership(t.Context(), owner, p.UserID, c.tenant, auth.RoleAdmin, workspace); e != nil {
		t.Fatal(e)
	}
	result, e := callTextOperation(t, c, user, models.TextExecutionInput{Input: "local turn"})
	wire := c.h.do("POST", "/v1/m/models/routing-policies/"+c.policy+"/execute", user, models.TextExecutionInput{Input: "local turn"}, tenantHdr(c.tenant))
	if e == nil || result.StatusCode != 403 || wire.code != 403 || chat.calls != 0 {
		t.Fatalf("wrong workspace wire=%d operation=%d error=%v", wire.code, result.StatusCode, e)
	}
}
