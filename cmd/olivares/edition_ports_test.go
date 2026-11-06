// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

type sentinelHandler struct{}

func (*sentinelHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// Every edition fills the ports its callers use without a nil check, and an absent
// port builds the zero value those callers already read as "not in this edition".
func TestEditionPortsContract(t *testing.T) {
	if thisEdition.name == "" || thisEdition.seatPolicy == nil || thisEdition.upstreamCredentialProvider == nil ||
		thisEdition.durableBus == nil || thisEdition.moduleRegistrars == nil || thisEdition.circuitBreakerDeclarations == nil {
		t.Fatalf("edition %q leaves a port its callers require empty", thisEdition.name)
	}
	var absent editionPorts
	if absent.groupMapper.get() != nil || absent.rootCommands.get() != nil {
		t.Fatal("an absent port must build the zero value")
	}
	if absent.contentInspector.get(func(string) string { return "" }, nil) != nil {
		t.Fatal("an absent environment port must build the zero value")
	}
	next := &sentinelHandler{}
	if got, ok := absent.routes(next, nil, nil).(*sentinelHandler); !ok || got != next {
		t.Fatal("an edition without HTTP routes must serve next unchanged")
	}
	base := fstest.MapFS{"index.html": &fstest.MapFile{}}
	if got, ok := absent.console(base).(fstest.MapFS); !ok || len(got) != 1 || got["index.html"] != base["index.html"] {
		t.Fatal("an edition without its own console must serve the base bundle")
	}
}

// contentInspector and hookContentInspector share a type, so a call site that read the
// other one would compile in every build. Each inspection surface must read its own port:
// the hook PEP the hook firewall, the Messages proxy and the governed Chat path the
// content firewall.
func TestInspectionSurfacesReadTheirOwnEditionPort(t *testing.T) {
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", "")
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Logger: discardLog(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	var content, hook int
	prevContent, prevHook := thisEdition.contentInspector, thisEdition.hookContentInspector
	thisEdition.contentInspector = func(func(string) string, *slog.Logger) contentInspector { content++; return nil }
	thisEdition.hookContentInspector = func(func(string) string, *slog.Logger) contentInspector { hook++; return nil }
	t.Cleanup(func() { thisEdition.contentInspector, thisEdition.hookContentInspector = prevContent, prevHook })

	if _, err := buildClaudeHookPEPServer(eng, discardLog()); err != nil {
		t.Fatal(err)
	}
	if content != 0 || hook != 1 {
		t.Fatalf("hook PEP read content=%d hook=%d, want only the hook firewall", content, hook)
	}
	writeInferenceProxyRawConfig(t, `{"surface":"direct"}`)
	if _, err := buildClaudeMessagesProxyServer(eng, discardLog(), ""); err != nil {
		t.Fatal(err)
	}
	if content != 1 || hook != 1 {
		t.Fatalf("Messages proxy read content=%d hook=%d, want the content firewall once", content, hook-1)
	}
	if newModelsChatExecutor(modelGatewayChatDevelopmentPrecheck, &modelGatewayProfileRegistry{}, osGetenv, nil) == nil {
		t.Fatal("the development Chat executor was not built")
	}
	if content != 2 || hook != 1 {
		t.Fatalf("governed Chat path read content=%d hook=%d, want the content firewall", content-1, hook-1)
	}
}

// One resolver instance must reach both policy consumers through the edition port.
func TestEditionPolicyResolverReachesApprovalAndDLP(t *testing.T) {
	resolver := &editionPolicyResolverFixture{err: errors.New("policy selection unavailable")}
	previous := thisEdition.policyResolver
	t.Cleanup(func() { thisEdition.policyResolver = previous })
	constructions := 0
	thisEdition.policyResolver = func(scope *sourcescope.Resolver) store.PolicyResolver {
		if scope == nil {
			t.Fatal("policy resolver needs the live source-scope resolver")
		}
		constructions++
		return resolver
	}
	eng, token, tenant := wireBoot(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, t.TempDir())
	if constructions != 1 {
		t.Fatalf("resolver constructions = %d, want 1", constructions)
	}
	if _, err := eng.engineApprovals.ReviewPolicy(context.Background(), tenant, "session.launch", "session"); !errors.Is(err, resolver.err) {
		t.Fatalf("approval resolver error = %v, want %v", err, resolver.err)
	}
	code, kb, raw := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/knowledge/kbs", token, tenant.String(), map[string]any{"name": "policy-wiring"})
	if code != http.StatusCreated {
		t.Fatalf("create knowledge base = %d: %s", code, raw)
	}
	code, _, raw = doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/knowledge/kbs/"+kb["id"].(string)+"/ingest", token, tenant.String(), map[string]any{
		"documents": []map[string]any{{"source_doc_id": "policy-wiring", "body": "Policy wiring document."}},
	})
	if code != http.StatusInternalServerError || resolver.approvalCalls != 1 || resolver.dlpCalls != 1 {
		t.Fatalf("ingest = %d, approval calls = %d, DLP calls = %d: %s", code, resolver.approvalCalls, resolver.dlpCalls, raw)
	}
}

type editionPolicyResolverFixture struct {
	err                     error
	approvalCalls, dlpCalls int
}

func (r *editionPolicyResolverFixture) ApprovalPolicies(context.Context, store.Scope, string) ([]model.Policy, bool, error) {
	r.approvalCalls++
	return nil, false, r.err
}
func (r *editionPolicyResolverFixture) DLPRules(context.Context, store.Scope, string) ([]model.Record, bool, error) {
	r.dlpCalls++
	return nil, false, r.err
}
