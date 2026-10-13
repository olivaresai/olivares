// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/catalog"
	"github.com/olivaresai/olivares/sdk"
)

// The real CLI, authenticated API, tenant catalog, installer and ledger run
// together. The only artifact network traffic goes to this owned Git fixture.
func TestToolInstallFromCatalogJourney(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	program := "#!/usr/bin/python3\nimport sys,json\nif '--version' in sys.argv: print('fixture-mcp 1.0.0'); sys.exit(0)\nfor line in sys.stdin:\n r=json.loads(line); result={'protocolVersion':'2024-11-05','capabilities':{'tools':{}},'serverInfo':{'name':'fixture-mcp','version':'1.0.0'}} if r['method']=='initialize' else {'tools':[{'name':'echo','inputSchema':{'type':'object'}}]}\n print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':result}),flush=True)\n"
	if err := os.WriteFile(filepath.Join(repo, "bin", "fixture-mcp"), []byte(program), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "bin"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "MCP fixture"}, {"tag", "v1.0.0"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	c := exec.Command("git", "archive", "--format=tar.gz", "v1.0.0")
	c.Dir = repo
	archive, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture.tar.gz" {
			t.Errorf("unexpected artifact network %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer fixture.Close()
	d := catalog.ToolDeclaration{SourceURL: "https://git.example/fixture.git", ArchiveURL: fixture.URL + "/fixture.tar.gz", Version: "1.0.0", SHA256: fmt.Sprintf("%x", sha256.Sum256(archive)), Program: "fixture-mcp", ConfigHome: "fixture-mcp", Kind: "mcp"}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	curated := catalog.New(catalog.WithSigningKey(priv))
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, curated.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.System(ctx, func(s store.SystemScope) error { _, e := s.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	curated.UseData(api.NewModuleData(st))
	root := filepath.Join(t.TempDir(), "tools")
	installer := toolinstall.NewEngineWithCapabilities(nil, toolinstall.EngineOptions{HTTPClient: fixture.Client()})
	tools, err := agenttoolsapi.New(ctx, installer, root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(curated, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rt.Stop(ctx)
	boot := &bootState{st: st, rt: rt, agentTools: tools, set: moduleSet{all: []api.Module{curated}}}
	boot.bindCatalogToolInstaller()
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{Store: st, Authenticator: authenticator, Authorizer: auth.NewAuthorizer(nil), PrincipalEvidenceProducer: authenticator, Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Modules: []api.Module{curated, tools}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.BootstrapSuperadmin(ctx, "fixture@example.invalid", "fixture-password-123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := authenticator.Login(ctx, "fixture@example.invalid", "fixture-password-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	tenant := model.TenantID("")
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+token)
		if tenant != "" {
			r.Header.Set("X-Olivares-Tenant", tenant.String())
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	tenant = createOrganizationFixture(t, st, "Fixture", "fixture").TenantID
	server := httptest.NewServer(srv.Handler())
	defer server.Close()
	cli := func(words ...string) (string, string, error) {
		return execSessionCLI(t, nil, append(words, "--server", server.URL, "--token", token, "--tenant", tenant.String())...)
	}
	_, _, err = cli("tool", "install", "--from", d.SourceURL, "--version", d.Version)
	if err == nil || exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "unadmitted_declaration") {
		t.Fatalf("unadmitted: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("unadmitted wrote destination: %v", err)
	}
	entry := call("POST", "/v1/m/catalog/entries", map[string]any{"kind": "mcp", "name": "Fixture MCP", "slug": "fixture-mcp", "version": d.Version, "spec": map[string]any{"tool_install": d}})
	if entry.Code != 201 {
		t.Fatalf("entry %d %s", entry.Code, entry.Body.String())
	}
	var e struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(entry.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cli("catalog", "admit", e.ID); err != nil {
		t.Fatal(err)
	}
	dormant := runtime.New(runtime.Options{})
	if err := dormant.AddDormantModule(curated, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	boot.rt = dormant
	_, _, err = cli("tool", "install", "--from", d.SourceURL, "--version", d.Version)
	if err == nil || !strings.Contains(err.Error(), "unadmitted_declaration") {
		t.Fatalf("disabled Catalog: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("disabled Catalog wrote destination: %v", err)
	}
	boot.rt = rt
	// Console-shaped preview becomes unusable if its source is deprecated.
	preview := call("POST", agentToolsPath+"/plans", map[string]string{"from": d.SourceURL, "version": d.Version})
	if preview.Code != 200 {
		t.Fatalf("preview %d %s", preview.Code, preview.Body.String())
	}
	var plan agenttoolsapi.Plan
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/v1/m/catalog/entries/"+e.ID+"/deprecate", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	refused := call("POST", agentToolsPath+"/installs", map[string]any{"plan_digest": plan.Digest, "request_id": model.NewID()})
	if refused.Code != 409 || !strings.Contains(refused.Body.String(), "unadmitted_declaration") {
		t.Fatalf("deprecated preview %d %s", refused.Code, refused.Body.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("deprecated plan wrote destination: %v", err)
	}
	// A new curated version row supplies the same exact declared payload.
	entry = call("POST", "/v1/m/catalog/entries", map[string]any{"kind": "mcp", "name": "Fixture MCP", "slug": "fixture-mcp-admitted", "version": d.Version, "spec": map[string]any{"tool_install": d}})
	if entry.Code != 201 {
		t.Fatal(entry.Body.String())
	}
	if err := json.Unmarshal(entry.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cli("catalog", "admit", e.ID); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := cli("tool", "install", "--from", d.SourceURL, "--version", d.Version, "-o", "json")
	if err != nil {
		t.Fatalf("CLI install %v %s", err, stderr)
	}
	var job struct {
		State   string                `json:"state"`
		Receipt toolinstall.ReceiptV2 `json:"receipt"`
	}
	if err := json.Unmarshal([]byte(out), &job); err != nil {
		t.Fatalf("receipt output %v %s", err, out)
	}
	if job.State != "succeeded" || job.Receipt.Tool == nil || job.Receipt.Tool.SHA256 != d.SHA256 || job.Receipt.PlanDigest != plan.Digest {
		t.Fatalf("job %+v", job)
	}
	mcp := exec.Command(job.Receipt.Destination.Executable)
	mcp.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	mcp.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	if out, err := mcp.CombinedOutput(); err != nil || !bytes.Contains(out, []byte("protocolVersion")) || !bytes.Contains(out, []byte("echo")) {
		t.Fatalf("MCP %v %s", err, out)
	}
	// An admitted bad digest fails through the real API job and CLI exit contract.
	bad := d
	bad.SourceURL, bad.Program, bad.ConfigHome, bad.Kind, bad.SHA256 = "https://git.example/tampered.git", "digest-fixture", "digest-fixture", "cli", strings.Repeat("a", 64)
	entry = call("POST", "/v1/m/catalog/entries", map[string]any{"kind": "agent", "name": "Bad digest fixture", "slug": "digest-fixture", "version": bad.Version, "spec": map[string]any{"tool_install": bad}})
	if entry.Code != 201 {
		t.Fatal(entry.Body.String())
	}
	if err := json.Unmarshal(entry.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cli("catalog", "admit", e.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = cli("tool", "install", "--from", bad.SourceURL, "--version", bad.Version)
	if err == nil || exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "digest_mismatch") {
		t.Fatalf("digest mismatch CLI: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, bad.Driver())); !os.IsNotExist(err) {
		t.Fatalf("digest mismatch wrote a tool directory: %v", err)
	}
	actions := map[string]int{}
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error { actions[ev.Action]++; return nil })
	}); err != nil {
		t.Fatal(err)
	}
	if actions["agenttools.install.requested"] != 2 || actions["agenttools.install.succeeded"] != 1 || actions["agenttools.plan"] != 3 || actions["agenttools.install.failed"] != 1 {
		t.Fatalf("ledger actions %v", actions)
	}
}
