// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

// HU2-23: with the product's Ollama stopped, an OpenCode session started and its turn
// failed with OpenCode's raw connection errors; nothing said Ollama was stopped. The
// launch now looks at the local endpoint once and, when it does not answer, is refused
// with one sentence that names the provider and how to start it.
func TestALaunchOnAStoppedLocalModelSaysSo(t *testing.T) {
	m, _, tenant, _, probe := providerHarness(t)
	ctx := context.Background()
	// A port nothing listens on: the connection is refused, as with a stopped Ollama.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + l.Addr().String()
	_ = l.Close()
	probe.result = ProviderProbeResult{Models: []string{"qwen2.5:0.5b"}, Detail: "1 models listed"}
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOllama, DisplayName: "Ollama on this server", BaseURL: endpoint})
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.mintFromProviderRecord(ctx, tenant, providerDriverOpenCode, rec.Ref); err != nil {
		t.Fatalf("a running local model = %v, want a launch", err)
	}
	probe.mu.Lock()
	probe.err = errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
	probe.mu.Unlock()
	_, _, err = m.mintFromProviderRecord(ctx, tenant, providerDriverOpenCode, rec.Ref)
	want := `the local model provider "Ollama on this server" does not answer at ` + endpoint + `: start it (olivares tool start ollama, or AI tools › Ollama › Start), then start the session again`
	if statusOf(err) != http.StatusConflict || err.Error() != want {
		t.Fatalf("a stopped local model = %v, want 409 %q", err, want)
	}
	if strings.Contains(err.Error(), "connection refused") {
		t.Fatal("the tool's raw connection error reached the sentence")
	}
}
