// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

func TestHookPEPUsesSavedClientContext(t *testing.T) {
	for _, key := range []string{"OLIVARES_SERVER_URL", "OLIVARES_TOKEN", "OLIVARES_TENANT", "OLIVARES_HOOK_PEP_URL", "OLIVARES_HOOK_PEP_TOKEN"} {
		t.Setenv(key, "")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != hookPEPPDPPath+"versions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer saved-policy-token" {
			t.Error("policy command did not use the saved credential")
		}
		if r.Header.Get("X-Olivares-Tenant") != "tenant-selected" {
			t.Error("policy command did not use the selected tenant")
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "engine.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "client.yaml")
	t.Setenv(cliConfigOverrideEnv, config)
	if err := writeCLIConfig(config, cliConfig{CurrentContext: "selected", Contexts: []cliContext{{
		Name: "selected", Server: server.URL, Token: "saved-policy-token", Tenant: "tenant-selected", CACert: ca,
	}}}); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"hookpep", "versions", "-o", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("policy versions after login: %v", err)
	}
	assertSameJSON(t, `{"items":[]}`, output.String())
}

func TestHookPEPValidatePrintsServerVerdictAndMapsExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/governance/pdp/validate" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer policy-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body struct {
			Engine string `json:"engine"`
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Engine != "cedar" {
			t.Errorf("engine = %q", body.Engine)
		}
		w.Header().Set("Content-Type", "application/json")
		if body.Source == "invalid" {
			_, _ = w.Write([]byte(`{"ok":false,"diagnostics":[{"message":"compile failed","severity":"error"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"diagnostics":[]}`))
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	err, output := runHookPEPCLI(t, "validate", "--source", "valid", "--format", "json")
	if err != nil {
		t.Fatalf("valid policy returned error: %v (output %s)", err, output)
	}
	if !strings.Contains(output, `"ok": true`) {
		t.Fatalf("valid server verdict was not printed: %s", output)
	}

	err, output = runHookPEPCLI(t, "validate", "--source", "invalid", "--format", "json")
	if !errors.Is(err, errHookPEPValidationFailed) {
		t.Fatalf("invalid policy error = %v, want validation-failed sentinel", err)
	}
	if !strings.Contains(output, `"ok": false`) || !strings.Contains(output, "compile failed") {
		t.Fatalf("invalid server verdict was not printed: %s", output)
	}
}

func TestHookPEPVersionsUsesGETAndRendersTextAndJSON(t *testing.T) {
	const response = `{"items":[{"revision":3,"surface":"cedar","validated":true,"active":true},{"revision":2,"surface":"opa","validated":false}],"total":2}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/m/governance/pdp/versions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer policy-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("GET Content-Type = %q, want empty", got)
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	err, output := runHookPEPCLI(t, "versions")
	if err != nil {
		t.Fatalf("versions text returned error: %v (output %q)", err, output)
	}
	wantText := "revision: engine=cedar revision=3 validated=true active=true\n" +
		"revision: engine=opa revision=2 validated=false active=false\n"
	if output != wantText {
		t.Fatalf("versions text output = %q, want %q", output, wantText)
	}

	err, output = runHookPEPCLI(t, "versions", "--format", "json")
	if err != nil {
		t.Fatalf("versions json returned error: %v (output %q)", err, output)
	}
	assertSameJSON(t, response, output)
}

func TestHookPEPTestsUsesGETQueryAndRendersTextAndJSON(t *testing.T) {
	const (
		compiledResponse  = `{"engine":"cedar","revision":9,"available":true,"passed":1,"failed":0,"total":1,"results":[{"name":"publish_compile_validate","passed":true}]}`
		unavailableResult = `{"engine":"opa","available":false,"passed":0,"failed":0,"total":0,"reason":"no stored artifact"}`
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/m/governance/pdp/tests" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer policy-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch engine := r.URL.Query().Get("engine"); engine {
		case "cedar":
			if got := r.URL.Query().Get("revision"); got != "9" {
				t.Errorf("cedar revision = %q, want 9", got)
			}
			_, _ = w.Write([]byte(compiledResponse))
		case "opa":
			if _, present := r.URL.Query()["revision"]; present {
				t.Errorf("opa revision query should be omitted: %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(unavailableResult))
		default:
			t.Errorf("engine = %q", engine)
			http.Error(w, "bad engine", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")

	err, output := runHookPEPCLI(t, "tests", "--engine", "cedar", "--revision", "9")
	if err != nil {
		t.Fatalf("tests text returned error: %v (output %q)", err, output)
	}
	if want := "tests: engine=cedar revision=9 available=true compiled=true\n"; output != want {
		t.Fatalf("tests text output = %q, want %q", output, want)
	}

	err, output = runHookPEPCLI(t, "tests", "--engine", "opa", "--format", "json")
	if err != nil {
		t.Fatalf("tests json returned error: %v (output %q)", err, output)
	}
	assertSameJSON(t, unavailableResult, output)
}

// TestHookPEPRequestShapeIsShownWhereOperatorsLook pins the one request body
// (governance.PDPExampleRequestJSON, itself posted to the live handler by the
// governance tests) into every place an operator learns the shape: the dry-run and
// explain help, the CLI recipes and each copy of the deny-closed cookbook. Before
// this the help said only "example-request JSON" and the cookbook showed a body the
// engine rejects, so a new user had to read Go source to get a first decision.
func TestHookPEPRequestShapeIsShownWhereOperatorsLook(t *testing.T) {
	root := newHookPEPCmd()
	for _, name := range []string{"dry-run", "explain"} {
		sub, _, err := root.Find([]string{name})
		if err != nil || sub.Name() != name {
			t.Fatalf("hookpep %s not found: %v", name, err)
		}
		if !strings.Contains(sub.Example, governance.PDPExampleRequestJSON) {
			t.Errorf("hookpep %s help example does not show the request body %s:\n%s", name, governance.PDPExampleRequestJSON, sub.Example)
		}
		for _, flag := range []string{"request", "request-file"} {
			f := sub.Flags().Lookup(flag)
			if f == nil {
				t.Fatalf("hookpep %s has no --%s flag", name, flag)
			}
			if !strings.Contains(f.Usage, "principal") || !strings.Contains(f.Usage, "permission") || !strings.Contains(f.Usage, "resource") {
				t.Errorf("hookpep %s --%s usage %q does not name principal, permission and resource", name, flag, f.Usage)
			}
		}
	}

	readDoc := func(doc string) string {
		raw, err := os.ReadFile(filepath.Clean(doc))
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		return string(raw)
	}
	if !strings.Contains(readDoc(recipesDoc), governance.PDPExampleRequestJSON) {
		t.Errorf("%s does not show the dry-run request body %s", recipesDoc, governance.PDPExampleRequestJSON)
	}

	// Every cookbook copy: the same curl bodies, built with the engine/source/request
	// envelope the routes decode, and the Rego reading the OPA input's real field
	// (opaInput has `permission`, not `action`).
	cookbooks, err := filepath.Glob("../../docs-site/src/content/docs/*/how-to/cookbook/deny-closed-policies.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(cookbooks) < 7 {
		t.Fatalf("found %d cookbook locale copies, want at least 7; the glob is not seeing the docs tree", len(cookbooks))
	}
	cookbooks = append(cookbooks, "../../docs-site/src/content/docs/how-to/cookbook/deny-closed-policies.md")
	for _, doc := range cookbooks {
		text := readDoc(doc)
		for _, want := range []string{
			"--argjson request '" + governance.PDPExampleRequestJSON + "'",
			`{engine:"cedar",$source,$request}`,
			`{engine:"cedar",$source}`,
			`endswith(input.permission, ":read")`,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not contain %s", doc, want)
			}
		}
		if strings.Contains(text, "input.action") {
			t.Errorf("%s reads input.action, which the OPA input does not have", doc)
		}
	}
}

func runHookPEPCLI(t *testing.T, args ...string) (error, string) {
	t.Helper()
	cmd := newHookPEPCmd()
	var output, errOut bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	return cmd.Execute(), output.String()
}
