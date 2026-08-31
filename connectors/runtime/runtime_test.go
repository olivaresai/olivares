// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func TestSource_Descriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Version != version {
		t.Fatalf("descriptor identity mismatch: %+v", d)
	}
	if d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor type/api mismatch: %+v", d)
	}
	// The k8s token field must be declared Secret.
	var sawSecret bool
	for _, f := range d.ConfigFields {
		if f.Key == cfgK8sToken {
			if !f.Secret {
				t.Fatalf("k8s_token must be declared Secret")
			}
			sawSecret = true
		}
	}
	if !sawSecret {
		t.Fatalf("descriptor is missing the k8s_token field")
	}
}

// TestSource_Gather_FullInventory drives all three discoverers in one pass: a
// fake procfs, a docker unix socket, and a k8s httptest server, and asserts the
// golden union of edges.
func TestSource_Gather_FullInventory(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	// procfs with one matching AI process.
	procRoot := filepath.Join(t.TempDir(), "proc")
	writeFakeProc(t, procRoot, []fakeProc{
		{pid: 9, comm: "ollama", argv: []string{"/usr/bin/ollama", "serve"}, uid: 1000},
		{pid: 50, comm: "bash", argv: []string{"/bin/bash"}, uid: 1000}, // no match
	})

	// docker socket.
	sock, _ := startDockerServer(t, dockerFixtures(), nil)

	// k8s server.
	k8sURL, _ := startK8sServer(t, k8sFixtures(), nil)

	src := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgEnableLinux:           "true",
		cfgProcRoot:              procRoot,
		cfgHost:                  "node1",
		cfgEnableDocker:          "true",
		cfgDockerSocket:          sock,
		cfgEnableK8s:             "true",
		cfgK8sAPIServer:          k8sURL,
		cfgK8sToken:              "dummy-token",
		cfgK8sInsecureSkipVerify: "true",
	}}
	if err := src.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &fakeSink{}
	if err := src.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if err := src.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	k8sHost := strings.TrimPrefix(k8sURL, "https://")
	got := sink.sortedEdgeKeys()
	want := []string{
		// linux
		"host|node1 -> process|node1/ollama#9 tool=ollama mode=unknown src=runtime conf=attributed",
		// docker (host from /info)
		"docker.host|docker-host-1 -> container|ai-agent tool=ollama/ollama:latest mode=unknown src=runtime conf=attributed",
		"container|ai-agent -> container.image|ollama/ollama:latest tool= mode=unknown src=runtime conf=attributed",
		"docker.host|docker-host-1 -> container|web tool=nginx:1.25 mode=unknown src=runtime conf=attributed",
		"container|web -> container.image|nginx:1.25 tool= mode=unknown src=runtime conf=attributed",
		// k8s
		"k8s.cluster|" + k8sHost + " -> k8s.node|node-a tool= mode=unknown src=runtime conf=attributed",
		"k8s.cluster|" + k8sHost + " -> k8s.node|node-b tool= mode=unknown src=runtime conf=attributed",
		"k8s.node|node-a -> k8s.pod|ai/agent-1 tool=ollama/ollama:latest mode=unknown src=runtime conf=attributed",
		"k8s.pod|ai/agent-1 -> container.image|ollama/ollama:latest tool= mode=unknown src=runtime conf=attributed",
		"k8s.pod|ai/agent-1 -> container.image|busybox:1.36 tool= mode=unknown src=runtime conf=attributed",
		"k8s.node|unscheduled -> k8s.pod|ai/pending-1 tool=vllm/vllm:latest mode=unknown src=runtime conf=attributed",
		"k8s.pod|ai/pending-1 -> container.image|vllm/vllm:latest tool= mode=unknown src=runtime conf=attributed",
		"k8s.node|node-b -> k8s.pod|default/web-1 tool=nginx:1.25 mode=unknown src=runtime conf=attributed",
		"k8s.pod|default/web-1 -> container.image|nginx:1.25 tool= mode=unknown src=runtime conf=attributed",
		"k8s.namespace|ai -> k8s.deployment|ai/agent tool= mode=unknown src=runtime conf=attributed",
		"k8s.namespace|default -> k8s.deployment|default/web tool= mode=unknown src=runtime conf=attributed",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full inventory edge set mismatch:\n got=%v\nwant=%v", got, want)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("expected 0 findings on a healthy pass, got %d: %+v", len(fs), fs)
	}
}

func TestSource_Gather_ContextCanceled(t *testing.T) {
	procRoot := filepath.Join(t.TempDir(), "proc")
	writeFakeProc(t, procRoot, []fakeProc{
		{pid: 1, comm: "ollama", argv: []string{"/bin/ollama"}, uid: 0},
	})
	src := New()
	if err := src.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgEnableLinux:  "true",
		cfgProcRoot:     procRoot,
		cfgEnableDocker: "false",
		cfgEnableK8s:    "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := src.Gather(ctx, &fakeSink{})
	if err == nil {
		t.Fatalf("expected Gather to return ctx.Err(), got nil")
	}
	if !isCanceled(err) {
		t.Fatalf("expected a context cancellation error, got %v", err)
	}
}
