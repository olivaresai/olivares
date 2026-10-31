// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The tools screens call the functions the commands call, so the signed audit
// ledger cannot tell them apart. The journeys in cmd_ui_tools_journey_linux_test.go
// show that on a fake engine's request log. This one shows it on the ledger:
// the engine is composed the way `olivares serve` composes it, the tool is a
// signed fixture release, and the events compared are the ones the ledger holds.

// uiSignInStandIn speaks the login protocol of Claude Code on a machine without
// a browser: a link, a code read from stdin, and the login stored where the tool
// keeps it. It is a labelled stand-in; no vendor account is used.
const uiSignInStandIn = `#!/bin/sh
# Labelled protocol stand-in for the screens' audit journey.
if [ "$1" = "--version" ]; then printf '%s (Claude Code)\n' '2.1.261'; exit 0; fi
case "$1 $2" in
"auth login")
  echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&state=stub"
  printf "Paste code here if prompted > "
  read code
  if [ "$code" = "good-code" ]; then mkdir -p "$CLAUDE_CONFIG_DIR"; echo '{}' > "$CLAUDE_CONFIG_DIR/.credentials.json"; echo "Login successful"; exit 0; fi
  echo "Invalid code"; exit 1;;
"auth status")
  if [ -f "$CLAUDE_CONFIG_DIR/.credentials.json" ]; then echo '{"loggedIn":true,"authMethod":"claude.ai","email":"ana@example.test"}'; exit 0; fi
  echo '{"loggedIn":false,"authMethod":"none"}'; exit 1;;
esac
exit 2
`

// realUIEngine is a composed engine with one organization and its owner.
type realUIEngine struct {
	URL, Token, Tenant string
	eng                *engine
	// output is what the commands run against it printed.
	output []string
}

// safeBuffer holds the engine's log lines; the engine writes from many goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// realRun is what one run against a real engine left behind.
type realRun struct {
	// events are what the run added to each ledger, by chain.
	events map[string][]string
	// everything is the text of all of it that could hold a secret: the canonical
	// text of the ledger, the engine's log and what the commands printed.
	everything []string
}

// vendorStandIn makes the one request the connection test sends to Anthropic, the
// model list, reach a local TLS server that answers it. The transport is still a
// real one with a trust store; only the address of that one host is changed, and
// every other request goes where it would have gone.
func vendorStandIn(t *testing.T) {
	t.Helper()
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") == "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-fixture-model"}],"has_more":false}`)
	}))
	t.Cleanup(vendor.Close)
	original, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("the default transport is %T, not a *http.Transport", http.DefaultTransport)
	}
	pool := x509.NewCertPool()
	pool.AddCert(vendor.Certificate())
	standIn := original.Clone()
	standIn.Proxy = nil
	// The test server's certificate names example.com, not the vendor.
	standIn.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{}
	standIn.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "api.anthropic.com:443" {
			addr = vendor.Listener.Addr().String()
		}
		return dialer.DialContext(ctx, network, addr)
	}
	http.DefaultTransport = standIn
	t.Cleanup(func() { http.DefaultTransport = original; standIn.CloseIdleConnections() })
}

// auditEvents is every event in the ledgers of the system and of the tenant, in
// order, as "<action> <target kind>", with the canonical text of each.
func auditEvents(t *testing.T, st store.Store, tenant string) (events map[string][]string, canonical []string) {
	t.Helper()
	events = map[string][]string{}
	for _, chain := range []struct {
		name   string
		tenant model.TenantID
	}{{"system", model.SystemTenantID}, {"tenant", model.TenantID(tenant)}} {
		err := st.View(context.Background(), chain.tenant, func(sc store.Scope) error {
			walker, ok := sc.Audit().(store.CanonicalWalker)
			if !ok {
				t.Fatal("the audit ledger cannot be walked with its canonical text")
			}
			return walker.WalkCanonical(context.Background(), 1, func(ev model.AuditEvent, meta string, _ []byte) error {
				events[chain.name] = append(events[chain.name], ev.Action+" "+string(ev.TargetKind))
				canonical = append(canonical, meta)
				return nil
			})
		})
		if err != nil {
			t.Fatalf("read the %s ledger: %v", chain.name, err)
		}
	}
	return events, canonical
}

// changeEvents is what a ledger holds of the changes made, in order. The ledger
// also records every request's authorization decision and every read, and the
// screens read more than a command does to draw a list; those say what was asked,
// not what was done, so they are left out. So is the models module's refresh of
// a new key's model list: the engine does it on its own after the request has
// returned, for the screens and the commands alike, and when it lands is timing.
func changeEvents(events map[string][]string) []string {
	var changes []string
	for _, chain := range []string{"system", "tenant"} {
		for _, ev := range events[chain] {
			action, _, _ := strings.Cut(ev, " ")
			switch {
			case action == "policy_artifact.record", action == "authorization_decision.record",
				action == "agenttools.inventory", action == "availability.create", strings.HasSuffix(action, ".read"):
				continue
			}
			changes = append(changes, chain+" "+ev)
		}
	}
	return changes
}

// onRealEngine composes an engine, runs drive against it and returns the events
// drive added to the ledgers, with the text that must hold no secret.
func onRealEngine(t *testing.T, drive func(*realUIEngine)) realRun {
	t.Helper()
	f := newAgentToolFixture(t)
	f.srv.Publish(t, f.key, "2.1.261", map[string][]byte{"linux-x64": []byte(uiSignInStandIn)})
	f.srv.SetPointer("latest", "2.1.261")
	prepareCompliancePacksTestEntitlement(t)
	resetBootProcessScope(t)
	logs := &safeBuffer{}
	eng, err := boot(context.Background(), bootConfig{DataDir: os.Getenv("OLIVARES_DATA_DIR"), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatalf("compose the engine: %v", err)
	}
	defer eng.Close()
	srv := httptest.NewServer(eng.api.Handler())
	defer srv.Close()
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, setup, raw := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/setup", "", "", map[string]any{"token": setupToken, "email": "owner@olivares.invalid", "password": "fixture-password-2026!"})
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/setup = %d %s", code, raw)
	}
	org, _ := setup["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	token, _, err := eng.authr.Login(context.Background(), "owner@olivares.invalid", "fixture-password-2026!", "127.0.0.1")
	if err != nil || tenant == "" {
		t.Fatalf("log in to the organization %q: %v", tenant, err)
	}
	before, _ := auditEvents(t, eng.store, tenant)
	driven := &realUIEngine{URL: srv.URL, Token: token, Tenant: tenant, eng: eng}
	drive(driven)
	after, canonical := auditEvents(t, eng.store, tenant)
	run := realRun{events: map[string][]string{}, everything: append(append(canonical, driven.output...), logs.String())}
	for chain, events := range after {
		run.events[chain] = events[len(before[chain]):]
	}
	return run
}

// holdsSecret is whether text holds any run of secretWindow characters of secret.
func holdsSecret(text, secret string) bool {
	for i := 0; i+secretWindow <= len(secret); i++ {
		if strings.Contains(text, secret[i:i+secretWindow]) {
			return true
		}
	}
	return false
}

func TestToolsScreensLeaveTheAuditEventsOfTheCommandsOnARealEngine(t *testing.T) {
	// The tool runs with nothing but the programs it needs, and in a home of its
	// own, so no vendor login of this machine is read.
	fixtureBin := t.TempDir()
	for _, name := range []string{"gpg", "gpgconf", "sha256sum", "sh", "mkdir"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("this journey needs %s: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(fixtureBin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", fixtureBin)
	for _, facts := range driverfacts.All() {
		if facts.ConfigHomeEnv != "" {
			t.Setenv(facts.ConfigHomeEnv, "")
		}
	}
	vendorStandIn(t)

	var journey *uiJourney
	screens := onRealEngine(t, func(e *realUIEngine) {
		j, wait := startUIJourneyAt(t, e.URL, e.Token, e.Tenant)
		journey = j
		j.see("Get started")
		j.press(keyEnterBytes) // Get started
		j.see("Next: olivares tool install claude")
		j.press(keyEnterBytes) // install it
		j.seeWithin("Next: olivares tool login claude", installWait)
		j.press(keyEnterBytes) // sign it in
		j.see("Paste the code the page shows")
		j.typeText(uiTestCode)
		j.press(keyEnterBytes)
		j.sampleEngineArgv() // while the engine hands the code to the tool
		j.see("Next: olivares session start <folder>")
		j.search("Add an API key")
		j.press(keyEnterBytes)
		j.see("The key is never shown")
		j.typeKey(uiTestKey)
		j.sampleArgv() // while the engine seals and tests it
		j.sampleEngineArgv()
		j.see("Connection test: works.")
		j.press(keyEnterBytes) // back to the list
		j.press(keyCtrlDBytes)
		if code := j.finish(wait); code != 0 {
			t.Fatalf("Ctrl-D exited %d, want 0", code)
		}
	})

	commands := onRealEngine(t, func(e *realUIEngine) {
		creds := []string{"--server", e.URL, "--token", e.Token, "--tenant", e.Tenant}
		for _, step := range []struct {
			stdin string
			args  []string
		}{
			{"", []string{"tool", "install", "claude"}},
			{uiTestCode + "\n", []string{"tool", "login", "claude"}},
			{uiTestKey + "\n", []string{"provider", "add", "--kind", "anthropic", "--name", "Anthropic"}},
		} {
			out, errb, err := execSessionCLI(t, strings.NewReader(step.stdin), append(step.args, creds...)...)
			if err != nil {
				t.Fatalf("olivares %s: %v\n%s", strings.Join(step.args, " "), err, errb)
			}
			e.output = append(e.output, out, errb)
		}
	})

	screenChanges, commandChanges := changeEvents(screens.events), changeEvents(commands.events)
	changes := strings.Join(screenChanges, "\n")
	for _, want := range []string{"agenttools.install.succeeded", "agenttools.signin.start", "sessions.provider_record.create"} {
		if !strings.Contains(changes, want) {
			t.Errorf("the screens left no %s event on the ledger:\n%s", want, changes)
		}
	}
	if !reflect.DeepEqual(screenChanges, commandChanges) {
		t.Errorf("the screens left different changes on the ledger than the commands:\nscreens:\n%s\ncommands:\n%s", changes, strings.Join(commandChanges, "\n"))
	}
	// Neither the ledger, the engine's log nor what the commands printed holds the
	// key or the code.
	for _, secret := range []string{uiTestKey, uiTestCode} {
		for _, text := range append(screens.everything, commands.everything...) {
			if holdsSecret(text, secret) {
				t.Errorf("a secret reached the ledger, the engine log or a command's output: %.200s", text)
			}
		}
	}
	journey.assertNeverShown(uiTestKey)
	journey.assertNeverShown(uiTestCode)
	journey.assertNotOnDisk(uiTestKey)
	journey.assertNotOnDisk(uiTestCode)
}
