// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package agenttoolsapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/core/api"
	coreaudit "github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Deny the Landlock probe in a subprocess, on every thread and future child
// (as sessionconfinement_unavailable_linux_test.go does), so the module's own
// install flow runs on a node that cannot confine a child. An unsigned install
// job ends failed, with the node-wide refusal sentence as the job's error: that
// sentence is what `olivares tool install` prints and the console install
// details show, and the unverified bytes never run (#3312).
func TestUnsignedInstallJobFailsWithTheRefusalSentence(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_NO_LANDLOCK") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestUnsignedInstallJobFailsWithTheRefusalSentence$", "-test.v")
		cmd.Env = append(os.Environ(), "OLIVARES_TEST_NO_LANDLOCK=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("no-Landlock subprocess: %v\n%s", err, out)
		}
		t.Log(string(out))
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_LANDLOCK_CREATE_RULESET, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		t.Fatal(errno)
	}
	if state := confine.Probe(); state.Mode != confine.ModeNone {
		t.Fatalf("probe = %+v", state)
	}

	const version = "1.0.13"
	dir := toolinstalltest.ExecCapableDir(t)
	marker := filepath.Join(dir, "grok-ran.marker")
	body := toolinstalltest.VersionReporter("Grok", version, marker)
	grokMux := http.NewServeMux()
	grokMux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, version+"\n") })
	for _, arch := range []string{"x86_64", "aarch64"} {
		grokMux.HandleFunc("/grok-"+version+"-linux-"+arch, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	}
	origin := httptest.NewTLSServer(grokMux)
	t.Cleanup(origin.Close)
	grok := toolinstall.NewGrok(toolinstall.GrokOptions{BaseURL: origin.URL, Client: origin.Client(), ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond})
	cat, err := toolinstall.NewCapabilityCatalog(toolinstall.NewCatalog(), grok)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := New(ctx, toolinstall.NewEngineWithCapabilities(cat, toolinstall.EngineOptions{InstallerVersion: "test"}), filepath.Join(dir, "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(s store.SystemScope) error { _, e := s.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := coreaudit.NewSigner(priv)
	a := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{Store: st, Authenticator: a, Authorizer: auth.NewAuthorizer(nil), PrincipalEvidenceProducer: a, Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Modules: []api.Module{m}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BootstrapSuperadmin(ctx, "root@olivares.invalid", "fixture-password-123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Login(ctx, "root@olivares.invalid", "fixture-password-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	tenant := seedOrg(t, st, "Tools", "tools")
	call := func(method, path, credential string, payload any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(payload)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("X-Olivares-Tenant", tenant.String())
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	p := call("POST", "/v1/m/agenttools/plans", token, map[string]string{"driver": "grok", "version": "latest"})
	if p.Code != 200 {
		t.Fatalf("plan %d %s", p.Code, p.Body.String())
	}
	var preview Plan
	if err := json.Unmarshal(p.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Verification != toolinstall.VerificationNoneOriginOnly {
		t.Fatalf("plan verification %q, want %s", preview.Verification, toolinstall.VerificationNoneOriginOnly)
	}
	id := model.NewID()
	started := call("POST", "/v1/m/agenttools/installs", token, map[string]any{"plan_digest": preview.Digest, "request_id": id})
	if started.Code != 202 {
		t.Fatalf("install %d %s", started.Code, started.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	var job Job
	for time.Now().Before(deadline) {
		w := call("GET", "/v1/m/agenttools/jobs/"+id.String(), token, nil)
		if w.Code != 200 {
			t.Fatalf("job %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.State != "running" && job.State != "queued" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.State != "failed" {
		t.Fatalf("job state %q, want failed: %+v", job.State, job)
	}
	for _, want := range []string{"grok", "publisher signature", "Landlock", "olivares doctor"} {
		if !strings.Contains(job.Error, want) {
			t.Errorf("job error %q lacks %q", job.Error, want)
		}
	}
	if _, merr := os.Stat(marker); !os.IsNotExist(merr) {
		t.Errorf("the unverified grok bytes ran: marker stat = %v", merr)
	}
}
