// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Local aliases so the flag walk below reads as what it is rather than as two
// import paths.
type (
	cobraCommand = cobra.Command
	pflagFlag    = pflag.Flag
)

// Provider CLI acceptance. These drive the real Cobra tree against a controlled
// listener. They pin the transport, the refusals, and the one property the whole
// file exists for: the credential never becomes a flag value.

const providerRecordJSON = `{"provider_ref":"prv_fixture","kind":"anthropic",` +
	`"display_name":"Anthropic (prod)","key_hint":"…ABCD","state":"active"}`

type providerProbeServer struct {
	*httptest.Server
	calls  atomic.Int64
	method atomic.Value
	path   atomic.Value
	body   atomic.Value
}

func newProviderProbeServer(t *testing.T, status int, payload string) *providerProbeServer {
	t.Helper()
	p := &providerProbeServer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		p.method.Store(r.Method)
		p.path.Store(r.URL.EscapedPath())
		raw, _ := io.ReadAll(r.Body)
		p.body.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *providerProbeServer) lastBody() string   { s, _ := p.body.Load().(string); return s }
func (p *providerProbeServer) lastPath() string   { s, _ := p.path.Load().(string); return s }
func (p *providerProbeServer) lastMethod() string { s, _ := p.method.Load().(string); return s }

func providerArgs(server string, extra ...string) []string {
	return append([]string{
		"provider", "add",
		"--kind", "anthropic", "--name", "Anthropic (prod)",
		"--server", server, "--token", "test-token", "--tenant", "tenant-a",
	}, extra...)
}

// The credential arrives on stdin and reaches the control plane in the body.
func TestProviderAddReadsTheKeyFromStdin(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	out, errb, err := execRootStdin(t, "sk-ant-api03-FROM-STDIN-0001\n", providerArgs(p.URL)...)
	if err != nil {
		t.Fatalf("add must succeed against 201: %v stderr=%s", err, errb)
	}
	if p.calls.Load() != 1 || p.lastMethod() != http.MethodPost || p.lastPath() != "/v1/m/sessions/providers" {
		t.Fatalf("calls=%d method=%q path=%q", p.calls.Load(), p.lastMethod(), p.lastPath())
	}
	var body map[string]any
	if uerr := json.Unmarshal([]byte(p.lastBody()), &body); uerr != nil {
		t.Fatal(uerr)
	}
	if body["api_key"] != "sk-ant-api03-FROM-STDIN-0001" {
		t.Fatalf("api_key=%v, want the value piped in (trimmed)", body["api_key"])
	}
	if body["kind"] != "anthropic" || body["display_name"] != "Anthropic (prod)" {
		t.Fatalf("body=%v", body)
	}
	// What the operator sees back carries the hint and never a credential.
	if strings.Contains(out, "sk-ant-api03-FROM-STDIN-0001") {
		t.Fatalf("the command printed the credential: %s", out)
	}
	if !strings.Contains(out, "…ABCD") {
		t.Fatalf("the hint must be shown: %s", out)
	}
	// And it names the next action rather than ending.
	if !strings.Contains(out, "olivares provider test prv_fixture") {
		t.Fatalf("the output must name the next step: %s", out)
	}
}

// --key-env names a variable; an empty or unset one is a usage refusal that says
// which variable it looked at, and the server is never called.
func TestProviderAddKeyEnv(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
		t.Setenv("D19_TEST_KEY", "sk-ant-api03-FROM-ENV-0002")
		if _, errb, err := execRootStdin(t, "", providerArgs(p.URL, "--key-env", "D19_TEST_KEY")...); err != nil {
			t.Fatalf("add with --key-env: %v %s", err, errb)
		}
		var body map[string]any
		if uerr := json.Unmarshal([]byte(p.lastBody()), &body); uerr != nil {
			t.Fatal(uerr)
		}
		if body["api_key"] != "sk-ant-api03-FROM-ENV-0002" {
			t.Fatalf("api_key=%v", body["api_key"])
		}
	})
	t.Run("unset", func(t *testing.T) {
		p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
		t.Setenv("D19_TEST_MISSING", "")
		_, errb, err := execRootStdin(t, "", providerArgs(p.URL, "--key-env", "D19_TEST_MISSING")...)
		if err == nil {
			t.Fatal("an empty --key-env variable must refuse")
		}
		if !strings.Contains(err.Error()+errb, "D19_TEST_MISSING") {
			t.Fatalf("the refusal must name the variable it looked at: %v %s", err, errb)
		}
		if p.calls.Load() != 0 {
			t.Fatal("a refused command called the server")
		}
	})
}

// An empty stdin is refused, and the server is not called: registering an empty
// credential would be worse than refusing.
func TestProviderAddRefusesAnEmptyCredential(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, providerRecordJSON)
	_, _, err := execRootStdin(t, "   \n", providerArgs(p.URL)...)
	if err == nil {
		t.Fatal("an empty credential must refuse")
	}
	if p.calls.Load() != 0 {
		t.Fatal("a refused command called the server")
	}
}

// ⛔ THE PROPERTY THIS FILE EXISTS FOR: there is no flag that takes the credential.
// A --key flag would put it in the shell history and the process table, and a test
// that only checked the happy path would not notice one being added.
func TestProviderCommandsHaveNoCredentialFlag(t *testing.T) {
	banned := map[string]bool{"key": true, "api-key": true, "apikey": true, "token-value": true, "secret": true}
	var walk func(c *cobraCommand)
	root := newProviderCmd()
	walk = func(c *cobraCommand) {
		c.Flags().VisitAll(func(f *pflagFlag) {
			if banned[f.Name] {
				t.Fatalf("%s declares --%s: the credential must never be a flag value", c.CommandPath(), f.Name)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// `provider ls` on an empty plane says what to do next instead of printing nothing.
func TestProviderListEmptyStateNamesTheNextAction(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusOK, `{"items":[]}`)
	out, errb, err := execRootStdin(t, "", "provider", "ls",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("ls: %v %s", err, errb)
	}
	if !strings.Contains(out, "No providers registered.") || !strings.Contains(out, "olivares provider add") {
		t.Fatalf("the empty state must name the next action: %q", out)
	}
}

// `provider bind` patches the PROFILE, which is where the binding lives.
func TestProviderBindPatchesTheProfile(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusOK, `{"profile_ref":"ppf_fixture"}`)
	if _, errb, err := execRootStdin(t, "", "provider", "bind", "prv_fixture",
		"--profile", "ppf_fixture",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("bind: %v %s", err, errb)
	}
	if p.lastMethod() != http.MethodPatch || p.lastPath() != "/v1/m/sessions/provider-profiles/ppf_fixture" {
		t.Fatalf("method=%q path=%q", p.lastMethod(), p.lastPath())
	}
	var body map[string]any
	if uerr := json.Unmarshal([]byte(p.lastBody()), &body); uerr != nil {
		t.Fatal(uerr)
	}
	if body["provider_record_ref"] != "prv_fixture" {
		t.Fatalf("body=%v", body)
	}
}

// `provider rm` without --yes refuses before it touches anything: it destroys a
// sealed credential and that cannot be undone.
func TestProviderRemoveNeedsConfirmation(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusOK, providerRecordJSON)
	_, _, err := execRootStdin(t, "", "provider", "rm", "prv_fixture",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
	if err == nil {
		t.Fatal("rm without --yes must refuse")
	}
	if p.calls.Load() != 0 {
		t.Fatal("a refused removal called the server")
	}
}

// `agent profile create --provider` without an authorized managed source refuses
// locally: a profile carrying a credential it may not use is a configuration that
// reads as done and launches as denied.
func TestAgentProfileCreateRefusesABoundProviderWithoutManagedInjection(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, `{"profile_ref":"ppf_fixture"}`)
	_, _, err := execRootStdin(t, "", "agent", "profile", "create",
		"--driver", "claude", "--config-home", "/tmp/x", "--user-home", "/tmp/y",
		"--provider", "prv_fixture",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
	if err == nil {
		t.Fatal("--provider without --auth-source managed_injection must refuse")
	}
	if p.calls.Load() != 0 {
		t.Fatal("a refused create called the server")
	}
}

// `agent profile ls` on an empty plane names the next action, and never a path.
func TestAgentProfileListEmptyState(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusOK, `{"items":[]}`)
	out, errb, err := execRootStdin(t, "", "agent", "profile", "ls",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("ls: %v %s", err, errb)
	}
	if !strings.Contains(out, "No provider profiles registered.") ||
		!strings.Contains(out, "olivares agent profile create") {
		t.Fatalf("the empty state must name the next action: %q", out)
	}
}

// `agent deploy` is a COMPOSITE and its test says so: it must register the profile the
// same way `agent profile create` does, and it must not invent a second way to do it.
func TestAgentDeployRegistersTheProfileAndBindsTheProvider(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated,
		`{"profile_ref":"ppf_fixture","driver":"claude","provider_record_ref":"prv_fixture","state":"active"}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	out, errb, err := execRootStdin(t, "", "agent", "deploy", "claude",
		"--provider", "prv_fixture",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("deploy: %v %s", err, errb)
	}
	if p.lastMethod() != http.MethodPost || p.lastPath() != "/v1/m/sessions/provider-profiles" {
		t.Fatalf("method=%q path=%q", p.lastMethod(), p.lastPath())
	}
	var body map[string]any
	if uerr := json.Unmarshal([]byte(p.lastBody()), &body); uerr != nil {
		t.Fatal(uerr)
	}
	if body["driver"] != "claude" {
		t.Fatalf("driver=%v", body["driver"])
	}
	// Naming a provider ALSO authorises managed injection. A profile that carried a
	// credential it was not allowed to use would read as configured and launch as
	// denied, so the two travel together.
	if body["auth_source"] != "managed_injection" || body["provider_record_ref"] != "prv_fixture" {
		t.Fatalf("body=%v", body)
	}
	// The homes are DEFAULTED from this account's home, and they are the driver's own.
	if body["config_home"] != filepath.Join(home, ".claude") || body["user_home"] != home {
		t.Fatalf("homes=%v/%v", body["config_home"], body["user_home"])
	}
	if !strings.Contains(out, "not installed") && !strings.Contains(out, "installed") {
		t.Fatalf("deploy must report the tool state: %q", out)
	}
	// And it names the action that follows, which here is launching.
	if !strings.Contains(out, "olivares agent session create --provider-profile ppf_fixture") &&
		!strings.Contains(out, "olivares agent tool install") {
		t.Fatalf("deploy must name the next action: %q", out)
	}
}

// Without --provider the profile is registered and NOT authorised for injection:
// the host's own variables decide, which is exactly the behaviour before v26.10.
//
// UPDATED 2026-09-18 after the first-hour walk. This test used to assert that
// `auth_source` was ABSENT, and that assertion had stopped serving its own
// comment. "The host's own variables decide" has a name in the wire contract —
// `provider_account_home` — and omitting the field did not keep the choice open:
// the server refuses to launch a profile that declares no auth source, the home
// is then occupied so `agent profile create` answers 409, and there is no
// `agent profile update` or `rm` to leave that state with. The measured result
// was a profile reported ready, exited 0, and unable to ever launch.
//
// So the guarantee is unchanged and the assertion now says it directly: the
// source is the host's own account home, and NOT managed injection, and no
// provider record is bound to a profile nobody named one for.
func TestAgentDeployWithoutProviderDoesNotAuthoriseInjection(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated,
		`{"profile_ref":"ppf_fixture","driver":"claude","state":"active"}`)
	t.Setenv("HOME", t.TempDir())
	if _, errb, err := execRootStdin(t, "", "agent", "deploy", "claude",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("deploy: %v %s", err, errb)
	}
	var body map[string]any
	if uerr := json.Unmarshal([]byte(p.lastBody()), &body); uerr != nil {
		t.Fatal(uerr)
	}
	if body["auth_source"] != "provider_account_home" {
		t.Fatalf("without --provider the host's own credentials decide, and the profile must say so: %v", body)
	}
	// The line that carries the original guarantee: injection is what must never
	// be authorised unasked, because it is the one that hands the session a
	// credential the operator did not name.
	if body["auth_source"] == "managed_injection" {
		t.Fatalf("deploy must not authorise injection nobody asked for: %v", body)
	}
	if _, ok := body["provider_record_ref"]; ok {
		t.Fatalf("deploy must not bind a provider nobody named: %v", body)
	}
}

// A driver with no default configuration home refuses rather than guessing one: a
// guessed home is how a session gets a provider identity nobody configured.
func TestAgentDeployRefusesAnUnknownDriverWithoutHomes(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusCreated, `{"profile_ref":"ppf_fixture"}`)
	t.Setenv("HOME", t.TempDir())
	_, _, err := execRootStdin(t, "", "agent", "deploy", "some-future-driver",
		"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
	if err == nil {
		t.Fatal("a driver with no default configuration home must refuse")
	}
	if p.calls.Load() != 0 {
		t.Fatal("a refused deploy called the server")
	}
}

// TestProviderRefVerbsRefuseABlankReferenceOnTheRealTree drives the four
// <provider-ref> verbs through the real cobra tree with an empty reference. Each
// must exit with the usage code and send nothing. An empty string IS one
// argument, so a verb wired to cobra.ExactArgs(1) accepts it, resolves its client
// and puts "/providers//<verb>" on the wire; the exactRef tests in
// cmd_args_test.go prove the validator, and this proves each command still uses
// it.
func TestProviderRefVerbsRefuseABlankReferenceOnTheRealTree(t *testing.T) {
	verbs := []struct {
		verb  string
		args  []string
		stdin string
	}{
		{"get", []string{"provider", "get", ""}, ""},
		{"test", []string{"provider", "test", ""}, ""},
		{"rotate", []string{"provider", "rotate", ""}, "sk-replacement\n"},
		{"rm", []string{"provider", "rm", "", "--yes"}, ""},
	}
	for _, v := range verbs {
		t.Run(v.verb, func(t *testing.T) {
			// A server that would ANSWER a blank reference: the refusal has to
			// come from the command, not from a 404.
			p := newProviderProbeServer(t, http.StatusOK, providerRecordJSON)
			args := append(append([]string{}, v.args...),
				"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
			_, _, err := execRootStdin(t, v.stdin, args...)
			if err == nil {
				t.Fatalf("provider %s \"\" exited 0; a blank reference must be refused", v.verb)
			}
			if code := exitcode.From(err); code != exitcode.Usage {
				t.Fatalf("provider %s \"\": exit code %d, want %d (usage): %v", v.verb, code, exitcode.Usage, err)
			}
			if n := p.calls.Load(); n != 0 {
				t.Fatalf("provider %s \"\" reached the server %d time(s) (%s %s); the refusal must happen before the wire",
					v.verb, n, p.lastMethod(), p.lastPath())
			}
		})
	}
}
