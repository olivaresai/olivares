// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

type contentFirewallStubInspector struct{}

func (contentFirewallStubInspector) Inspect(context.Context, claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	return claudeapi.ContentInspectionDecision{Forward: true}
}

func TestInferenceProxyContentFirewallRecorderKeepsTheFirstAttachment(t *testing.T) {
	var unset *messagesInspectorBinding
	unset.record(&inferenceProxyDecider{inspector: contentFirewallStubInspector{}})
	if got := unset.ContentFirewallState(); got != inferenceproxy.ContentFirewallUnobserved {
		t.Fatalf("nil recorder state = %s, want unobserved", got)
	}
	cases := []struct {
		name  string
		first *inferenceProxyDecider
		want  inferenceproxy.ContentFirewallState
	}{
		{"no proxy", nil, inferenceproxy.ContentFirewallPEPNotComposed},
		{"proxy without inspector", &inferenceProxyDecider{}, inferenceproxy.ContentFirewallInspectorAbsent},
		{"proxy with inspector", &inferenceProxyDecider{inspector: contentFirewallStubInspector{}}, inferenceproxy.ContentFirewallInspectorAttached},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &messagesInspectorBinding{}
			if got := b.ContentFirewallState(); got != inferenceproxy.ContentFirewallUnobserved {
				t.Fatalf("state before record = %s, want unobserved", got)
			}
			b.record(tc.first)
			if got := b.ContentFirewallState(); got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
			b.record(nil)
			b.record(&inferenceProxyDecider{})
			b.record(&inferenceProxyDecider{inspector: contentFirewallStubInspector{}})
			if got := b.ContentFirewallState(); got != tc.want {
				t.Fatalf("state after later records = %s, want the first record %s", got, tc.want)
			}
		})
	}
}

func TestInferenceProxyContentFirewallRecorderConcurrentRecordAndRead(t *testing.T) {
	b := &messagesInspectorBinding{}
	dec := &inferenceProxyDecider{inspector: contentFirewallStubInspector{}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 256; j++ {
				switch got := b.ContentFirewallState(); got {
				case inferenceproxy.ContentFirewallUnobserved, inferenceproxy.ContentFirewallInspectorAttached:
				default:
					t.Errorf("concurrent read = %s", got)
					return
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			b.record(dec)
		}()
	}
	close(start)
	wg.Wait()
	if got := b.ContentFirewallState(); got != inferenceproxy.ContentFirewallInspectorAttached {
		t.Fatalf("final state = %s, want inspector_attached", got)
	}
}

func writeInferenceProxyRawConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inferenceproxy.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("OLIVARES_INFERENCE_PROXY_CONFIG", path)
}

func TestInferenceProxyContentFirewallBuilderRecordsEveryNilReturn(t *testing.T) {
	cases := []struct {
		name    string
		config  func(t *testing.T)
		wantErr bool
		want    inferenceproxy.ContentFirewallState
	}{
		{"not provisioned", func(t *testing.T) { t.Setenv("OLIVARES_INFERENCE_PROXY_CONFIG", "") }, false, inferenceproxy.ContentFirewallPEPNotComposed},
		{"unsupported surface", func(t *testing.T) { writeInferenceProxyRawConfig(t, `{"surface":"vertex"}`) }, false, inferenceproxy.ContentFirewallPEPNotComposed},
		{"governance dependencies not wired", func(t *testing.T) { writeInferenceProxyConfig(t, "") }, false, inferenceproxy.ContentFirewallPEPNotComposed},
		{"unreadable config", func(t *testing.T) {
			t.Setenv("OLIVARES_INFERENCE_PROXY_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
		}, true, inferenceproxy.ContentFirewallUnobserved},
		{"invalid tenant", func(t *testing.T) { writeInferenceProxyConfig(t, "not-a-tenant-id") }, true, inferenceproxy.ContentFirewallUnobserved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLIVARES_CONTENT_FIREWALL_CONFIG", "")
			tc.config(t)
			b := &messagesInspectorBinding{}
			srv, err := buildClaudeMessagesProxyServer(&engine{contentFirewall: b}, discardLog())
			if srv != nil {
				t.Fatalf("server = %+v, want none", srv)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %t", err, tc.wantErr)
			}
			if got := b.ContentFirewallState(); got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

// contentFirewallStatusEstate is a booted engine with one tenant read through a bound
// viewer token and a second tenant whose viewer holds a session.
type contentFirewallStatusEstate struct {
	eng          *engine
	tenant       model.TenantID
	viewer       string
	otherTenant  model.TenantID
	otherSession string
}

func bootContentFirewallStatusEstate(t *testing.T) contentFirewallStatusEstate {
	t.Helper()
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, err := eng.authr.BootstrapSuperadmin(ctx, "root@firewall-status.test", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap superadmin: %v", err)
	}
	rootSession, _, err := eng.authr.Login(ctx, "root@firewall-status.test", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatalf("login superadmin: %v", err)
	}
	super, err := eng.authr.Authenticate(ctx, rootSession)
	if err != nil {
		t.Fatalf("authenticate superadmin: %v", err)
	}
	var tenant, other model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		a, err := sys.CreateOrg(ctx, model.Org{Name: "status-a", Slug: "status-a", Status: model.StatusActive})
		if err != nil {
			return err
		}
		b, err := sys.CreateOrg(ctx, model.Org{Name: "status-b", Slug: "status-b", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant, other = a.TenantID, b.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenants: %v", err)
	}
	viewer, _, err := eng.authr.IssueToken(ctx, super, auth.TokenSpec{Name: "firewall-status-viewer", BoundTenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatalf("issue viewer token: %v", err)
	}
	user, err := eng.authr.CreateUser(ctx, super, auth.NewUser{Email: "other@firewall-status.test", DisplayName: "Other", Password: "other-pass-12345"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	if _, err := eng.authr.GrantMembership(ctx, super, user.ID, other, auth.RoleViewer, model.ID("")); err != nil {
		t.Fatalf("grant other membership: %v", err)
	}
	otherSession, _, err := eng.authr.Login(ctx, "other@firewall-status.test", "other-pass-12345", "127.0.0.1")
	if err != nil {
		t.Fatalf("login other user: %v", err)
	}
	return contentFirewallStatusEstate{eng: eng, tenant: tenant, viewer: viewer, otherTenant: other, otherSession: otherSession}
}

func (e contentFirewallStatusEstate) get(t *testing.T, token string, tenant model.TenantID) (int, map[string]any, string) {
	t.Helper()
	return doDemoViewJSON(t, e.eng.api.Handler(), http.MethodGet, inferenceProxyFirewallStatusPath, token, tenant.String(), nil)
}

// state reads the route as the tenant viewer and checks the exact body keys.
func (e contentFirewallStatusEstate) state(t *testing.T) inferenceproxy.ContentFirewallState {
	t.Helper()
	code, body, raw := e.get(t, e.viewer, e.tenant)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, raw)
	}
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"note", "pep", "state"}) || body["pep"] != "messages_proxy" {
		t.Fatalf("body = %s, want exactly pep messages_proxy, state and note", raw)
	}
	state, _ := body["state"].(string)
	return inferenceproxy.ContentFirewallState(state)
}

func TestInferenceProxyContentFirewallStatusWithoutAProxyThroughTheEngineWrapper(t *testing.T) {
	t.Setenv("OLIVARES_INFERENCE_PROXY_CONFIG", "")
	t.Setenv("OLIVARES_CONTENT_FIREWALL_CONFIG", "")
	estate := bootContentFirewallStatusEstate(t)

	if code, body, raw := estate.get(t, "", estate.tenant); code != http.StatusUnauthorized || body["state"] != nil {
		t.Fatalf("anonymous read = %d %s, want 401 without a state", code, raw)
	}
	// Tenant resolution admits a session's tenant header; the route's permission decision
	// refuses a principal that holds no role in that tenant.
	if code, body, raw := estate.get(t, estate.otherSession, estate.tenant); code != http.StatusForbidden || body["state"] != nil {
		t.Fatalf("read without inferenceproxy:config:read in the tenant = %d %s, want 403 without a state", code, raw)
	}
	if code, _, raw := estate.get(t, estate.otherSession, estate.otherTenant); code != http.StatusOK {
		t.Fatalf("control: the same session in its own tenant = %d %s, want 200", code, raw)
	}

	if got := estate.state(t); got != inferenceproxy.ContentFirewallUnobserved {
		t.Fatalf("state before the builder = %s, want unobserved", got)
	}
	srv, err := buildClaudeMessagesProxyServer(estate.eng, discardLog())
	if err != nil || srv != nil {
		t.Fatalf("builder = (%v, %v), want no proxy and no error", srv, err)
	}
	if got := estate.state(t); got != inferenceproxy.ContentFirewallPEPNotComposed {
		t.Fatalf("state = %s, want pep_not_composed", got)
	}
}

func TestInferenceProxyContentFirewallStatusReportsTheProxyTheBuilderReturned(t *testing.T) {
	writeInferenceProxyConfig(t, "")
	t.Setenv("OLIVARES_CONTENT_FIREWALL_CONFIG", "")
	estate := bootContentFirewallStatusEstate(t)

	if got := estate.state(t); got != inferenceproxy.ContentFirewallUnobserved {
		t.Fatalf("state before the builder = %s, want unobserved", got)
	}
	srv, err := buildClaudeMessagesProxyServer(estate.eng, discardLog())
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if srv == nil {
		t.Fatal("the booted engine did not build the Messages proxy, so no attachment can be observed")
	}
	if got := estate.state(t); got != inferenceproxy.ContentFirewallInspectorAbsent {
		t.Fatalf("state = %s, want inspector_absent", got)
	}
}

func TestInferenceProxyContentFirewallContractMatchesTheModuleStates(t *testing.T) {
	doc, err := moduleOpenAPIDocument()
	if err != nil {
		t.Fatalf("module OpenAPI document: %v", err)
	}
	op, ok := doc["paths"].(map[string]any)[inferenceProxyFirewallStatusPath].(map[string]any)["get"].(map[string]any)
	if !ok {
		t.Fatalf("GET %s is not published", inferenceProxyFirewallStatusPath)
	}
	if op["x-required-permission"] != "inferenceproxy:config:read" {
		t.Fatalf("required permission = %v", op["x-required-permission"])
	}
	schema := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	want := []any{
		string(inferenceproxy.ContentFirewallUnobserved),
		string(inferenceproxy.ContentFirewallPEPNotComposed),
		string(inferenceproxy.ContentFirewallInspectorAbsent),
		string(inferenceproxy.ContentFirewallInspectorAttached),
	}
	if got := properties["state"].(map[string]any)["enum"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("published state enum = %v, want the module states %v", got, want)
	}
}

// TestInferenceProxyContentFirewallBuilderRecordsTheDeciderItServes pins the recording
// sites: the returned server records the decider passed to claudeapi.NewMessagesProxy,
// each no-proxy return records nil, and construction errors record nothing.
func TestInferenceProxyContentFirewallBuilderRecordsTheDeciderItServes(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "inferenceproxy.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var builder *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "buildClaudeMessagesProxyServer" {
			builder = fn
			break
		}
	}
	if builder == nil {
		t.Fatal("buildClaudeMessagesProxyServer not found")
	}
	served, records := "", 0
	ast.Inspect(builder.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if contentFirewallIsSelector(call.Fun, "claudeapi", "NewMessagesProxy") && len(call.Args) >= 2 {
			served = types.ExprString(call.Args[1])
		}
		if _, ok := contentFirewallRecordArg(call); ok {
			records++
		}
		return true
	})
	if served == "" {
		t.Fatal("no claudeapi.NewMessagesProxy call in the builder")
	}
	noProxy, built := 0, 0
	ast.Inspect(builder.Body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 2 {
				continue
			}
			recorded, hasRecord := "", false
			if i > 0 {
				if expr, ok := block.List[i-1].(*ast.ExprStmt); ok {
					if call, ok := expr.X.(*ast.CallExpr); ok {
						recorded, hasRecord = contentFirewallRecordArg(call)
					}
				}
			}
			at := fset.Position(ret.Pos())
			switch {
			case types.ExprString(ret.Results[1]) != "nil":
				if hasRecord {
					t.Errorf("%s: an error return records %q; construction errors leave the state unobserved", at, recorded)
				}
			case types.ExprString(ret.Results[0]) == "nil":
				noProxy++
				if !hasRecord || recorded != "nil" {
					t.Errorf("%s: return nil, nil is not immediately preceded by eng.contentFirewall.record(nil)", at)
				}
			default:
				built++
				if !hasRecord || recorded != served {
					t.Errorf("%s: the built server records %q, want %q, the decider passed to claudeapi.NewMessagesProxy", at, recorded, served)
				}
			}
		}
		return true
	})
	if noProxy != 3 || built != 1 || records != 4 {
		t.Fatalf("builder has %d no-proxy returns, %d built returns and %d records; want 3, 1 and 4", noProxy, built, records)
	}
}

func contentFirewallIsSelector(expr ast.Expr, x, sel string) bool {
	s, ok := expr.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == x
}

// contentFirewallRecordArg returns the argument of an eng.contentFirewall.record call.
func contentFirewallRecordArg(call *ast.CallExpr) (string, bool) {
	s, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != "record" || len(call.Args) != 1 || !contentFirewallIsSelector(s.X, "eng", "contentFirewall") {
		return "", false
	}
	return types.ExprString(call.Args[0]), true
}
