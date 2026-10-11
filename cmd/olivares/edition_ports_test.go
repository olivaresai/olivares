// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"testing/fstest"
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
