// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" // #nosec G505 -- npm's shasum field, beside the sha512 integrity
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// dev#1517, real use: the real npm, through the production runner, Landlock and
// the egress proxy, installs a package from a private registry declared on its
// profile, and a session on another profile cannot reach that registry. Two
// record-bound (network-confined) Claude profiles are launched through the real
// launch path; only the child program is replaced by a shell running npm.
// OLIVARES_TEST_REAL_NPM=1 runs it (it needs npm, Landlock and user namespaces).
func TestRealProfileRegistryReachesOnlyItsProfile(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_REAL_NPM") != "1" {
		t.Skip("OLIVARES_TEST_REAL_NPM is not set")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Fatal("npm is not installed")
	}
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this reproducer requires Landlock: " + s.Reason)
	}
	registry, requests := fakeNPMRegistry(t, registryCanary)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer provider.Close()

	dataDir := t.TempDir()
	runner := &fakeRunner{}
	vault := newFakeVault()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}),
		WithProviderSecretVault(vault), WithClaudeHookPEP(dataDir, "/bin/true"), WithConfinement([]string{dataDir}, true))
	m.UseExecutionEnvironmentRef(testEnvRef)
	vault.values[tenant.String()+"|env/npm-acme"] = registryCanary
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	configA, userA, configB, userB := twoHomes(t)
	withRegistry := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configA, UserHome: userA,
		DisplayName: "with registry", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref,
		SessionRegistries: []SessionRegistry{{Kind: "npm", URL: registry.URL, Scope: "@acme", Secret: "env/npm-acme"}}, MayUseSecretEnv: true})
	other := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configB, UserHome: userB,
		DisplayName: "other", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})

	install := "npm install --no-audit --no-fund --update-notifier=false --prefix \"$PWD\" @acme/widget"
	out, code := runNPMSession(t, m, tenant, runner, withRegistry, provider.URL,
		install+" && node -e 'console.log(require(process.cwd()+\"/node_modules/@acme/widget\"))' && env")
	if code != 0 || !strings.Contains(out, "acme widget 1.0.0") {
		t.Fatalf("the profile's session did not install from its registry (exit %d):\n%s", code, out)
	}
	if strings.Contains(out, registryCanary) {
		t.Fatalf("the registry token is in the session's environment or output:\n%s", out)
	}
	if !strings.Contains(out, envNPMGlobalConfig+"=") {
		t.Fatalf("the environment listing does not show npm's configuration file:\n%s", out)
	}
	served := requests.Load()
	t.Logf("profile %s installed @acme/widget through the egress proxy; registry requests=%d", withRegistry.Ref, served)

	out, code = runNPMSession(t, m, tenant, runner, other, provider.URL,
		"npm install --no-audit --no-fund --update-notifier=false --fetch-retries=0 --fetch-timeout=5000 --registry "+registry.URL+" --prefix \"$PWD\" @acme/widget")
	if code == 0 || requests.Load() != served {
		t.Fatalf("a session on another profile reached the registry (exit %d, requests %d -> %d):\n%s", code, served, requests.Load(), out)
	}
	t.Logf("profile %s: npm exited %d and the registry saw no request", other.Ref, code)
}

// runNPMSession launches prof through the real launch path, then runs script in
// the resulting spec (its environment, Landlock policy and network policy) with
// the bound provider moved to a local stand-in.
func runNPMSession(t *testing.T, m *Module, tenant model.TenantID, runner *fakeRunner, prof ProviderProfile, provider, script string) (string, int) {
	t.Helper()
	if _, err := launchBound(m, tenant, prof); err != nil {
		t.Fatal(err)
	}
	spec := runner.lastSpec()
	if spec.NetworkPolicy == nil || spec.Confinement == nil {
		t.Fatal("a record-bound Claude launch is not confined")
	}
	spec.Program, spec.Args = "/bin/sh", []string{"-c", script}
	// npm and node may sit outside the system paths a confined child reads (a
	// test host's own install): grant that install, as a tool install is.
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(npm); err == nil {
		spec.AllowRead(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(real))))))
	}
	if node, err := exec.LookPath("node"); err == nil {
		spec.AllowRead(filepath.Dir(node))
	}
	spec.NetworkPolicy.Providers[0] = provider
	spec.ConfinementRequired = true
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	proc, err := NewProcRunner().Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for frame := range proc.Output() {
		out.Write(frame.Data)
		out.WriteByte('\n')
	}
	code, _ := proc.Wait()
	return out.String(), code
}

// fakeNPMRegistry serves @acme/widget 1.0.0 to a request bearing token and
// counts every request it receives.
func fakeNPMRegistry(t *testing.T, token string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	tgz := npmTarball(t, map[string]string{
		"package/package.json": `{"name":"@acme/widget","version":"1.0.0","main":"index.js"}`,
		"package/index.js":     "module.exports = 'acme widget 1.0.0';\n",
	})
	sum512 := sha512.Sum512(tgz)
	sum1 := sha1.Sum(tgz) // #nosec G401 -- npm's shasum field
	var served atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every request counts, authorized or not: what keeps another profile's
		// session out is its network, not the missing token.
		served.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ".tgz"):
			_, _ = w.Write(tgz)
		case r.URL.Path == "/@acme/widget":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "@acme/widget", "dist-tags": map[string]string{"latest": "1.0.0"},
				"versions": map[string]any{"1.0.0": map[string]any{
					"name": "@acme/widget", "version": "1.0.0", "main": "index.js",
					"dist": map[string]string{
						"tarball":   srv.URL + "/@acme/widget/-/widget-1.0.0.tgz",
						"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum512[:]),
						"shasum":    hex.EncodeToString(sum1[:]),
					},
				}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &served
}

func npmTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"package/package.json", "package/index.js"} {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
