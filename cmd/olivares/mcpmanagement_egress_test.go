// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	sessionegress "github.com/olivaresai/olivares/modules/sessions/egress"
)

func TestManagedMCPRejectsRedirectAndWrongDestination(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer target.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/secret-location")
		w.WriteHeader(307)
	}))
	defer first.Close()
	c := first.Client()
	c.Transport = managedMCPTransport{inner: c.Transport, endpoint: first.URL}
	req, _ := http.NewRequest("POST", first.URL, strings.NewReader(`{}`))
	_, err := c.Do(req)
	if err == nil || hits != 0 || strings.Contains(err.Error(), "secret-location") {
		t.Fatalf("redirect reached destination or leaked location: hits=%d", hits)
	}
	req, _ = http.NewRequest("POST", target.URL, strings.NewReader(`{}`))
	if _, err = c.Do(req); err == nil || hits != 0 {
		t.Fatal("unconfigured destination admitted")
	}
}

// A command server's egress hosts become its own launch network, at public
// addresses only, replacing the session's; without hosts it keeps the launch's
// network. A profile that cannot be enforced refuses the start instead of
// running on the host network.
func TestManagedStdioEgressHostsReplaceLaunchNetwork(t *testing.T) {
	unconfined := &mcpManagement{}
	spec := sessions.LaunchSpec{Program: os.Args[0], Dir: t.TempDir()}
	if err := unconfined.confineLocalServer(&spec, "registry.npmjs.org"); err == nil || !strings.Contains(err.Error(), "egress hosts need process confinement") {
		t.Fatalf("an unenforceable egress profile started: %v", err)
	}
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("the confined cases require Landlock")
	}
	session := &sessionegress.Policy{Providers: []string{"https://provider.test"}}
	m := &mcpManagement{eng: &engine{sessionsMod: sessions.New(sessions.WithConfinement([]string{t.TempDir()}, false))}}
	spec = sessions.LaunchSpec{Program: os.Args[0], Dir: t.TempDir(), NetworkPolicy: session}
	if err := m.confineLocalServer(&spec); err != nil || spec.NetworkPolicy != session {
		t.Fatalf("a server without egress hosts lost the launch network: %v", err)
	}
	spec = sessions.LaunchSpec{Program: os.Args[0], Dir: t.TempDir(), NetworkPolicy: session}
	if err := m.confineLocalServer(&spec, "registry.npmjs.org", "mirror.test:8443"); err != nil {
		t.Fatal(err)
	}
	got := spec.NetworkPolicy
	if got == session || !got.PublicOnly || strings.Join(got.Providers, ",") != "https://registry.npmjs.org,https://mirror.test:8443" || len(got.Controls) != 0 {
		t.Fatalf("egress hosts did not become the command's public-only network: %+v", got)
	}
}

// recordingRunner records the launch a session would start and starts nothing.
type recordingRunner struct{ specs []sessions.LaunchSpec }

func (r *recordingRunner) Launch(_ context.Context, spec sessions.LaunchSpec) (sessions.Process, error) {
	r.specs = append(r.specs, spec)
	return nil, errors.New("recorded")
}

// The session path gives the command the same egress profile as Test: a session
// started next to a provider-only network launches the server on its own hosts.
func TestManagedStdioEgressHostsReachTheSessionLaunch(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("this regression requires Landlock")
	}
	m, f, p := mcpManagementFixture(t)
	m.eng.sessionsMod = sessions.New(sessions.WithConfinement([]string{t.TempDir()}, false))
	runner := &recordingRunner{}
	session := &sessionegress.Policy{Providers: []string{"https://provider.test"}}
	companion := sessions.RuntimeCompanion{LaunchSpec: sessions.LaunchSpec{Dir: t.TempDir(), NetworkPolicy: session}, Runner: runner, Context: t.Context(), LaunchID: model.NewID()}
	for _, hosts := range [][]string{nil, {"registry.npmjs.org"}} {
		row := auth.MCPGatewayServer{ID: model.NewID().String(), MCPGatewayServerInput: auth.MCPGatewayServerInput{Name: "Filesystem", Transport: "stdio", Command: os.Args[0], EgressHosts: hosts}}
		if _, err := m.cachedSessionServer(t.Context(), f.tenant, p, companion, 1, row); err == nil {
			t.Fatal("the recording runner started a server")
		}
	}
	if len(runner.specs) != 2 || runner.specs[0].NetworkPolicy != session {
		t.Fatalf("a session server without egress hosts lost the session network: %+v", runner.specs)
	}
	if got := runner.specs[1].NetworkPolicy; got == nil || got == session || !got.PublicOnly || strings.Join(got.Providers, ",") != "https://registry.npmjs.org" {
		t.Fatalf("the session launch ignored the server's egress hosts: %+v", got)
	}
}

// The Test path with an egress profile never reaches an off-list host: the
// namespace proxy refuses it, or the start is refused where the engine host
// cannot build that boundary. The same fixture without a profile reaches it.
func TestManagedStdioEgressHostsBlockOffListHost(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("this regression requires Landlock")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	var hits atomic.Int32
	offList := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer offList.Close()
	script := filepath.Join(t.TempDir(), "server.py")
	source := `import json, sys, urllib.request
try:
    urllib.request.urlopen(` + strconv.Quote(offList.URL) + `, timeout=5).read()
    result = 'reached'
except Exception:
    result = 'blocked'
for line in sys.stdin:
    req = json.loads(line)
    if 'id' not in req: continue
    out = {'protocolVersion':'2025-11-25', 'capabilities':{'tools':{}}} if req['method'] == 'initialize' else {'tools':[{'name':'egress', 'description':result, 'inputSchema':{'type':'object'}}]}
    print(json.dumps({'jsonrpc':'2.0', 'id':req['id'], 'result':out}), flush=True)
`
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &mcpManagement{eng: &engine{sessionsMod: sessions.New(sessions.WithConfinement([]string{t.TempDir()}, false))}}
	probe := func(hosts ...string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		row := auth.MCPGatewayServer{MCPGatewayServerInput: auth.MCPGatewayServerInput{Command: python, Args: []string{script}, EgressHosts: hosts}}
		tools, err := m.probeLocalServer(ctx, model.TenantID(model.NewID().String()), row)
		if err != nil || len(tools) != 1 {
			return "", err
		}
		return tools[0].Description, nil
	}
	if got, err := probe(); err != nil || got != "reached" || hits.Load() != 1 {
		t.Fatalf("fixture without a profile: result=%q err=%v hits=%d", got, err, hits.Load())
	}
	// The listed host resolves to loopback, which a public-only profile does not
	// admit either; the off-list fixture is the one that must stay unreached.
	got, err := probe("localhost:1")
	refused := err != nil && strings.Contains(err.Error(), "network boundary could not be set up")
	if hits.Load() != 1 || (err == nil && got != "blocked") || (err != nil && !refused) {
		t.Fatalf("egress profile reached an off-list host or failed for another reason: result=%q err=%v hits=%d", got, err, hits.Load())
	}
	t.Logf("with egress hosts: result=%q err=%v", got, err)
}
