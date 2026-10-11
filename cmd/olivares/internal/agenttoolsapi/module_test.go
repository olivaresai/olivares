// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	coreaudit "github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

type route struct {
	method, pattern string
	meta            api.RouteMetadata
	handler         api.ModuleHandler
}
type registrar struct{ routes []route }

type detectionFaultClaude struct {
	*toolinstall.Claude
	beforePaths func()
}

func (c *detectionFaultClaude) DefaultPaths(home string) []string {
	if c.beforePaths != nil {
		c.beforePaths()
	}
	return c.Claude.DefaultPaths(home)
}

func (*registrar) Handle(string, string, auth.Permission, api.ModuleHandler) {
	panic("unguarded route")
}
func (*registrar) HandleEntity(string, string, auth.Permission, api.EntityRef, api.ModuleHandler) {
	panic("unguarded route")
}
func (r *registrar) HandleSystem(method, pattern string, h api.ModuleHandler) {
	r.routes = append(r.routes, route{method: method, pattern: pattern, handler: h})
}
func TestEveryRouteRequiresSystemAdministrator(t *testing.T) {
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r := &registrar{}
	m.APIRoutes(r)
	// 5 sign-in routes + 5 tool routes, the providers read, and the 5 routes of the local Ollama (HU-R17).
	if len(r.routes) != 16 {
		t.Fatalf("routes = %d", len(r.routes))
	}
	for _, rt := range r.routes {
		t.Run(rt.pattern, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(rt.method, "http://localhost/", strings.NewReader(`{}`))
			rt.handler(w, req, api.ModuleContext{Principal: auth.Principal{Kind: auth.KindUser, AAL: auth.AAL3}})
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestInstallRejectsUnelevatedAdminBeforePlanLookup(t *testing.T) {
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r := &registrar{}
	m.APIRoutes(r)
	for _, rt := range r.routes {
		if rt.pattern == "/installs" || rt.pattern == "/detect" {
			w := httptest.NewRecorder()
			rt.handler(w, httptest.NewRequest(rt.method, "/?driver=claude&probe_path=/usr/bin/claude", strings.NewReader(`{}`)), api.ModuleContext{Principal: auth.Principal{Kind: auth.KindUser, Superadmin: true, AAL: auth.AAL1}})
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "step_up_required") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		}
	}
}

// The install route follows the deployment's administrative step-up policy, not a
// fixed assurance (its OpenAPI operation publishes none): a signed-in system
// administrator passes under none (the default), needs an authenticator code under
// totp and a passkey (AAL3) under passkey. Past the gate it reaches the body check.
func TestInstallFollowsTheStepUpPolicy(t *testing.T) {
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r := &registrar{}
	m.APIRoutes(r)
	var install api.ModuleHandler
	for _, rt := range r.routes {
		if rt.method == "POST" && rt.pattern == "/installs" {
			install = rt.handler
		}
	}
	admin := func(aal int, amr ...string) auth.Principal {
		return auth.Principal{Kind: auth.KindUser, Superadmin: true, AAL: aal, AMR: amr}
	}
	for _, tc := range []struct {
		policy  string
		who     auth.Principal
		refused bool
	}{
		{auth.StepUpNone, admin(auth.AAL1, "pwd"), false},
		{auth.StepUpTOTP, admin(auth.AAL1, "pwd"), true},
		{auth.StepUpTOTP, admin(auth.AAL2, "pwd", "totp"), false},
		{auth.StepUpPasskey, admin(auth.AAL2, "pwd", "totp"), true},
		{auth.StepUpPasskey, admin(auth.AAL3, "pwd", "webauthn"), false},
	} {
		ctx := auth.WithStepUpSource(context.Background(), func(context.Context) (string, error) { return tc.policy, nil })
		w := httptest.NewRecorder()
		install(w, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(ctx), api.ModuleContext{Principal: tc.who})
		refused := w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "step_up_required")
		admitted := w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "bad_request")
		if refused != tc.refused || refused == admitted {
			t.Fatalf("policy %s, AAL%d %v: %d %s", tc.policy, tc.who.AAL, tc.who.AMR, w.Code, w.Body.String())
		}
	}
}

func TestRestartNeverReplaysAnUnfinishedInstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tools")
	eng := toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{})
	m, err := New(context.Background(), eng, root, false)
	if err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	tenant := model.NewTenantID()
	if err = m.save(&savedJob{Tenant: tenant, Job: Job{ID: id, State: "running", Progress: "fetched bytes", Digest: "approved"}}); err != nil {
		t.Fatal(err)
	}
	m.Close()
	m, err = New(context.Background(), eng, root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.jobs[id].State != "interrupted" || m.jobs[id].Progress != "fetched bytes" {
		t.Fatalf("recovered = %+v", m.jobs[id])
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart touched tools root: %v", err)
	}
	// A dense but bounded installer receipt must remain a completed job after
	// restart. Its compact envelope can exceed the old 128 KiB journal cap.
	receipt := &toolinstall.ReceiptV2{Schema: toolinstall.ReceiptSchemaV2, Driver: "ollama", Version: "0.35.0"}
	for i := 0; i < 400; i++ {
		receipt.Payload.Members = append(receipt.Payload.Members, toolinstall.ObservedMember{
			Path: "lib/ollama/lib" + strings.Repeat("a", 220) + fmt.Sprintf("%03d.so", i),
			Kind: toolinstall.MemberKindRegular, SHA256: strings.Repeat("a", 64), Size: 1, Mode: 0644,
		})
	}
	encodedReceipt, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil || len(encodedReceipt) > 256<<10 {
		t.Fatalf("receipt fixture must fit the installer's existing bound: %d %v", len(encodedReceipt), err)
	}
	largeID := model.NewID()
	completed := &savedJob{Tenant: tenant, Job: Job{ID: largeID, State: "succeeded", Receipt: receipt, Progress: strings.Repeat("p", 16<<10)}}
	encodedJob, err := json.Marshal(completed)
	if err != nil || len(encodedJob) <= 128<<10 {
		t.Fatalf("job fixture must cross the old journal bound: %d %v", len(encodedJob), err)
	}
	if err := m.save(completed); err != nil {
		t.Fatalf("installer-sized receipt could not be retained: %v", err)
	}
	m.Close()
	restarted, err := New(context.Background(), eng, root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restored := restarted.jobs[largeID]
	if restored == nil || restored.State != "succeeded" || restored.Progress != completed.Progress {
		t.Fatalf("completed job was lost after restart: %+v", restored)
	}
	restoredJSON, err := json.Marshal(restored.Receipt)
	var recoveredReceipt toolinstall.ReceiptV2
	if err != nil || json.Unmarshal(restoredJSON, &recoveredReceipt) != nil || len(recoveredReceipt.Payload.Members) != 400 {
		t.Fatalf("receipt inventory was lost after restart: %v", err)
	}
	oversized := &savedJob{Tenant: tenant, Job: Job{ID: model.NewID(), State: "succeeded", Receipt: strings.Repeat("x", 512<<10)}}
	if err := restarted.save(oversized); err == nil {
		t.Fatal("oversized journal was accepted")
	}
}

func TestAPIAuthenticationPlanInstallAuditAndReplay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	key := toolinstalltest.GenerateKey(t, "Agent Tools Fixture <tools@olivares.invalid>")
	vendor := toolinstalltest.NewServer(t)
	ver, err := toolinstall.NewGPGVerifier(ctx, exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	const release = "2.1.261"
	platform := toolinstall.HostPlatform()
	vp := "linux-x64"
	if platform.Arch == "arm64" {
		vp = "linux-arm64"
	}
	if platform.Libc == "musl" {
		vp += "-musl"
	}
	managedMarker := filepath.Join(t.TempDir(), "managed-runs")
	vendor.Publish(t, key, release, map[string][]byte{vp: toolinstalltest.Executable(release, managedMarker)})
	vendor.SetPointer("latest", release)
	claude := toolinstall.NewClaudeWithTrust(toolinstall.ClaudeOptions{BaseURL: vendor.URL, Verifier: ver}, key.Public, key.Fingerprint)
	driver := &detectionFaultClaude{Claude: claude}
	installer := toolinstall.NewEngine(toolinstall.NewCatalog(driver), toolinstall.EngineOptions{})
	m, err := New(ctx, installer, filepath.Join(toolinstalltest.ExecCapableDir(t), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.System(ctx, func(s store.SystemScope) error { _, e := s.EnsureSystemTenant(ctx); return e })
	if err != nil {
		t.Fatal(err)
	}
	// This test pins the passkey step-up policy (the default asks for nothing
	// beyond the sign-in; core/api covers it).
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.AuthPolicy().Create(ctx, model.AuthPolicy{AdminStepUp: auth.StepUpPasskey})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := coreaudit.NewSigner(priv)
	a := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{Store: st, Authenticator: a, Authorizer: auth.NewAuthorizer(nil), PrincipalEvidenceProducer: a, Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Modules: []api.Module{m}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := a.BootstrapSuperadmin(ctx, "root@olivares.invalid", "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Login(ctx, "root@olivares.invalid", "fixture-password-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential string, body any, tenant model.TenantID) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.RemoteAddr = "127.0.0.1:1234"
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		if tenant != "" {
			r.Header.Set("X-Olivares-Tenant", tenant.String())
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/v1/m/agenttools/inventory", "", nil, ""); w.Code != 401 {
		t.Fatalf("anonymous %d %s", w.Code, w.Body.String())
	}
	org := call("POST", "/v1/system/orgs", token, map[string]string{"name": "Tools", "slug": "tools"}, "")
	if org.Code != 201 {
		t.Fatalf("org %d %s", org.Code, org.Body.String())
	}
	var o struct {
		Tenant model.TenantID `json:"tenant_id"`
	}
	json.Unmarshal(org.Body.Bytes(), &o)
	p := call("POST", "/v1/m/agenttools/plans", token, map[string]string{"driver": "claude", "version": "latest"}, o.Tenant)
	if p.Code != 200 {
		t.Fatalf("plan %d %s", p.Code, p.Body.String())
	}
	var preview Plan
	json.Unmarshal(p.Body.Bytes(), &preview)
	id := model.NewID()
	body := map[string]any{"plan_digest": preview.Digest, "request_id": id}
	if w := call("POST", "/v1/m/agenttools/installs", token, body, o.Tenant); w.Code != 403 {
		t.Fatalf("AAL1 %d %s", w.Code, w.Body.String())
	}
	principal, err := a.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.ElevateSession(ctx, principal, "webauthn", auth.AAL3)
	if err != nil {
		t.Fatal(err)
	}
	probeDir := toolinstalltest.ExecCapableDir(t)
	path := filepath.Join(probeDir, "claude")
	selectedMarker := filepath.Join(t.TempDir(), "selected-runs")
	if err := os.WriteFile(path, toolinstalltest.Executable(release, selectedMarker), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", probeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	detection := call("GET", "/v1/m/agenttools/detect?driver=claude&probe_path="+url.QueryEscape(path), token, nil, o.Tenant)
	if detection.Code != 200 || !strings.Contains(detection.Body.String(), `"version":"`+release+`"`) {
		t.Fatalf("explicit detected version: %d %s", detection.Code, detection.Body.String())
	}
	foreign := call("GET", "/v1/m/agenttools/detect?driver=claude&probe_path="+url.QueryEscape(filepath.Join(probeDir, "other")), token, nil, o.Tenant)
	if foreign.Code != 400 {
		t.Fatalf("non-candidate probe: %d %s", foreign.Code, foreign.Body.String())
	}
	installed := call("POST", "/v1/m/agenttools/installs", token, body, o.Tenant)
	if installed.Code != 202 {
		t.Fatalf("install %d %s", installed.Code, installed.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	var result Job
	for time.Now().Before(deadline) {
		w := call("GET", "/v1/m/agenttools/jobs/"+id.String(), token, nil, o.Tenant)
		if w.Code != 200 {
			t.Fatalf("job %d %s", w.Code, w.Body.String())
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		if result.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.State != "succeeded" || result.Receipt == nil || result.Progress == "" || result.AuditError != "" {
		t.Fatalf("result %+v", result)
	}
	managedBefore, err := os.ReadFile(managedMarker)
	if err != nil {
		t.Fatal(err)
	}
	selectedBefore, err := os.ReadFile(selectedMarker)
	if err != nil {
		t.Fatal(err)
	}
	detection = call("GET", "/v1/m/agenttools/detect?driver=claude&probe_path="+url.QueryEscape(path), token, nil, o.Tenant)
	if detection.Code != 200 || !strings.Contains(detection.Body.String(), `"version":"`+release+`"`) {
		t.Fatalf("selected probe with a managed release: %d %s", detection.Code, detection.Body.String())
	}
	managedAfter, err := os.ReadFile(managedMarker)
	if err != nil {
		t.Fatal(err)
	}
	selectedAfter, err := os.ReadFile(selectedMarker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(managedBefore, managedAfter) || string(selectedAfter) != string(selectedBefore)+"ran\n" {
		t.Fatalf("explicit selection executed unrelated tools or ran more than once: managed %q -> %q; selected %q -> %q", managedBefore, managedAfter, selectedBefore, selectedAfter)
	}
	if w := call("POST", "/v1/m/agenttools/installs", token, body, o.Tenant); w.Code != 202 {
		t.Fatalf("replay %d %s", w.Code, w.Body.String())
	}
	body["plan_digest"] = "changed"
	if w := call("POST", "/v1/m/agenttools/installs", token, body, o.Tenant); w.Code != 409 {
		t.Fatalf("changed retry %d %s", w.Code, w.Body.String())
	}
	other := model.NewTenantID()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/jobs/"+id.String(), nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id.String())
	r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
	m.job(w, r, api.ModuleContext{Tenant: other})
	if w.Code != 404 {
		t.Fatalf("cross tenant %d", w.Code)
	}
	// Real ledger events must carry the operator and the exact approved selection.
	err = st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		found := false
		e := sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action == "agenttools.install.succeeded" {
				found = true
				if !strings.Contains(ev.Actor, user.ID.String()) {
					t.Errorf("actor %s", ev.Actor)
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		if !found {
			t.Error("missing completion audit event")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fail the managed-root scan only after candidate discovery has succeeded.
	// No process or sleep races the two scans: the owned driver injects the fault.
	pathReads := 0
	driver.beforePaths = func() {
		pathReads++
		if pathReads == 2 {
			if err := os.Rename(m.root, m.root+".fault-fixture"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(m.root, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	fault := call("GET", "/v1/m/agenttools/detect?driver=claude&probe_path="+url.QueryEscape(path), token, nil, o.Tenant)
	if fault.Code != http.StatusUnprocessableEntity || !strings.Contains(fault.Body.String(), `"code":"destination_unsafe"`) {
		t.Fatalf("global selected-probe failure was not reported: %d %s", fault.Code, fault.Body.String())
	}
}
