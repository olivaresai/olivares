// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

func TestUnknownConfigEnvKeys(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"OLIVARES_NONSENSE_XYZ=ignored",
		"OLIVARES_DATA_DIR=/var/lib/olivares",
		"OLIVARES_LOG_LEVEL=debug",
		"OLIVARES_OTEL_ENDPOINT=collector:4317",
		"OLIVARES_TEST_DSN=postgres://fixture",
		"OLIVARES_E2E_MARKER_OK=1",
	}
	got := unknownConfigEnvKeys(environ)
	if len(got) != 1 || got[0] != "OLIVARES_NONSENSE_XYZ" {
		t.Fatalf("unknownConfigEnvKeys() = %v, want [OLIVARES_NONSENSE_XYZ]", got)
	}

	if mode := configEnvKeyMode("OLIVARES_OTEL_FUTURE_EXPORTER"); mode != configKeyPrefix {
		t.Fatalf("dynamic prefix-family key mode = %v, want prefix", mode)
	}
	if mode := configEnvKeyMode("OLIVARES_TEST_DSN"); mode != configKeyTestOnly {
		t.Fatalf("test-only key mode = %v, want test-only", mode)
	}
	if mode := configEnvKeyMode("OLIVARES_LOG_LEVEL"); mode != configKeyExact {
		t.Fatalf("log capture-level key mode = %v, want exact", mode)
	}
}

func TestLoadLogCaptureLevel(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name string
		raw  string
		want slog.Level
	}{
		{name: "default", want: slog.LevelInfo},
		{name: "debug case insensitive", raw: "DeBuG", want: slog.LevelDebug},
		{name: "info", raw: "info", want: slog.LevelInfo},
		{name: "warn", raw: "warn", want: slog.LevelWarn},
		{name: "error", raw: "error", want: slog.LevelError},
		{name: "invalid defaults info", raw: "trace", want: slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level := loadLogCaptureLevel(func(string) string { return tt.raw }, log)
			if got := level.Level(); got != tt.want {
				t.Fatalf("capture level = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigRegistryListsAreSortedAndUnique(t *testing.T) {
	lists := map[string][]string{
		"exact":              exactConfigEnvKeys,
		"prefix":             prefixConfigEnvKeys,
		"test-only exact":    testOnlyConfigEnvKeys,
		"test-only prefixes": testOnlyConfigEnvPrefixes,
	}
	for name, values := range lists {
		if !sort.StringsAreSorted(values) {
			t.Errorf("%s registry list is not sorted", name)
		}
		for i := 1; i < len(values); i++ {
			if values[i] == values[i-1] {
				t.Errorf("%s registry list contains duplicate %q", name, values[i])
			}
		}
	}
}

func TestConfigEffectiveRedactsSecrets(t *testing.T) {
	clearOlivaresEnv(t)
	t.Setenv("OLIVARES_CLAUDE_INFERENCE_KEY", "supersecret")

	out, err := executeConfigCommand("effective")
	if err != nil {
		t.Fatalf("config effective: %v", err)
	}
	if strings.Contains(out, "supersecret") {
		t.Fatalf("config effective disclosed secret: %q", out)
	}
	want := "OLIVARES_CLAUDE_INFERENCE_KEY=<redacted>"
	if !strings.Contains(out, want) {
		t.Fatalf("config effective output %q does not contain %q", out, want)
	}
}

func TestConfigEffectiveRedactsCredentialDSN(t *testing.T) {
	if got := redactEffectiveConfigValue("OLIVARES_VECTOR_DSN", "postgres://app:secret@db/olivares"); got != redactedConfigValue {
		t.Fatalf("credential DSN = %q, want %q", got, redactedConfigValue)
	}
	const reference = "file:/run/secrets/vector.dsn"
	if got := redactEffectiveConfigValue("OLIVARES_VECTOR_DSN", reference); got != reference {
		t.Fatalf("externalized DSN reference = %q, want %q", got, reference)
	}
}

func TestEffectiveConfigEntriesPreserveRegistryRedactionAndSource(t *testing.T) {
	environ := []string{
		"OLIVARES_CLAUDE_INFERENCE_KEY=supersecret",
		"OLIVARES_VECTOR_DSN=postgres://app:secret@db/olivares",
		"OLIVARES_NONSENSE_XYZ=ignored",
	}
	values := map[string]string{
		"OLIVARES_CLAUDE_INFERENCE_KEY": "supersecret",
		"OLIVARES_VECTOR_DSN":           "postgres://app:secret@db/olivares",
		"OLIVARES_REPORTING_CONFIG":     "/data/reporting.json",
	}
	entries := effectiveConfigEntries(environ, func(key string) string { return values[key] })
	byKey := make(map[string]struct {
		value    string
		redacted bool
		source   string
	}, len(entries))
	for _, entry := range entries {
		byKey[entry.Key] = struct {
			value    string
			redacted bool
			source   string
		}{entry.Value, entry.Redacted, entry.Source}
	}
	for _, key := range []string{"OLIVARES_CLAUDE_INFERENCE_KEY", "OLIVARES_VECTOR_DSN"} {
		got := byKey[key]
		if got.value != redactedConfigValue || !got.redacted || got.source != "env" {
			t.Errorf("%s = %+v, want redacted env entry", key, got)
		}
	}
	if got := byKey["OLIVARES_REPORTING_CONFIG"]; got.value != "/data/reporting.json" ||
		got.redacted || got.source != "activation" {
		t.Errorf("activation entry = %+v", got)
	}
	if _, found := byKey["OLIVARES_NONSENSE_XYZ"]; found {
		t.Error("unknown key entered the effective API projection")
	}
}

func TestConfigEffectiveStrict(t *testing.T) {
	t.Run("flag rejects unknown key", func(t *testing.T) {
		clearOlivaresEnv(t)
		t.Setenv("OLIVARES_NONSENSE_XYZ", "1")
		if _, err := executeConfigCommand("effective", "--strict"); err == nil {
			t.Fatal("config effective --strict succeeded with an unknown key")
		}
	})

	t.Run("environment rejects unknown key", func(t *testing.T) {
		clearOlivaresEnv(t)
		t.Setenv(envConfigStrict, "1")
		t.Setenv("OLIVARES_NONSENSE_XYZ", "1")
		if _, err := executeConfigCommand("effective"); err == nil {
			t.Fatal("config effective succeeded with OLIVARES_CONFIG_STRICT=1 and an unknown key")
		}
	})

	t.Run("default remains advisory", func(t *testing.T) {
		clearOlivaresEnv(t)
		t.Setenv("OLIVARES_NONSENSE_XYZ", "1")
		if _, err := executeConfigCommand("effective"); err != nil {
			t.Fatalf("non-strict config effective rejected an unknown key: %v", err)
		}
	})

	t.Run("clean environment succeeds", func(t *testing.T) {
		clearOlivaresEnv(t)
		if _, err := executeConfigCommand("effective", "--strict"); err != nil {
			t.Fatalf("config effective --strict rejected a clean environment: %v", err)
		}
	})
}

func TestConfigValidateRejectsUnknownKey(t *testing.T) {
	clearOlivaresEnv(t)
	t.Setenv("OLIVARES_NONSENSE_XYZ", "1")
	if _, err := executeConfigCommand("validate"); err == nil {
		t.Fatal("config validate succeeded with an unknown key")
	}
}

// addonAIRSConstructorInputs are the five file-reference names the enterprise addon_airs
// constructors read (cmd-overlay/olivares/wire_enterprise_addon_airs.go, private). T9
// measured it on 2026-09-11: both built SKUs enforced OLIVARES_CONTENT_FIREWALL_CONFIG while
// `config validate` and `config effective --strict` refused a deployment that set it, and
// the same boot logged the key as ignored. OLIVARES_HOOK_FIREWALL_CONFIG, read by the same
// file and already registered, is the discriminator. Recognized is a statement about the
// name only: whether a build links the control, and what the file says, stays with the
// constructors, and these verbs never open the file.
var addonAIRSConstructorInputs = []string{
	"OLIVARES_COMPUTER_USE_CONFIG",
	"OLIVARES_CONTENT_FIREWALL_CONFIG",
	"OLIVARES_ELICITATION_MEDIATOR_CONFIG",
	"OLIVARES_RENDER_INSPECTOR_CONFIG",
	"OLIVARES_SERVERTOOL_EGRESS_CONFIG",
}

const addonAIRSHookFirewallKey = "OLIVARES_HOOK_FIREWALL_CONFIG"

// addonAIRSPolicyBody is what the referenced files contain. It must never reach the output:
// the configured value is the path, not the policy.
const addonAIRSPolicyBody = `{"sentinel":"airs-policy-body-not-for-display"}`

func TestConfigVerbsAcceptAddonAIRSConstructorInputs(t *testing.T) {
	keys := append([]string{addonAIRSHookFirewallKey}, addonAIRSConstructorInputs...)
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			clearOlivaresEnv(t)
			assertConfigVerbsAccept(t, map[string]string{key: setAddonAIRSPolicyFile(t, key)})
		})
	}
	t.Run("all six together", func(t *testing.T) {
		clearOlivaresEnv(t)
		want := make(map[string]string, len(keys))
		for _, key := range keys {
			want[key] = setAddonAIRSPolicyFile(t, key)
		}
		assertConfigVerbsAccept(t, want)
	})
	t.Run("unreadable reference is displayed, not opened", func(t *testing.T) {
		clearOlivaresEnv(t)
		missing := filepath.Join(t.TempDir(), "absent", "content-firewall.json")
		t.Setenv("OLIVARES_CONTENT_FIREWALL_CONFIG", missing)
		assertConfigVerbsAccept(t, map[string]string{"OLIVARES_CONTENT_FIREWALL_CONFIG": missing})
	})
}

// The recognized names must not widen into a family: a near miss of each is still an
// unknown key, named on its own, through both gates.
func TestConfigVerbsStillRejectNearMissAddonAIRSNames(t *testing.T) {
	nearMisses := []string{ // sorted, the order the error lists them in
		"OLIVARES_COMPUTERUSE_CONFIG",
		"OLIVARES_CONTENT_FIREWAL_CONFIG",
		"OLIVARES_ELICITATION_MEDIATOR_CONF",
		"OLIVARES_RENDER_INSPECTORS_CONFIG",
		"OLIVARES_SERVER_TOOL_EGRESS_CONFIG",
	}
	wantErr := "unrecognized OLIVARES_* environment keys: [" + strings.Join(nearMisses, " ") + "]"
	setNearMisses := func(t *testing.T) {
		for _, key := range nearMisses {
			t.Setenv(key, "/etc/olivares/airs/near-miss.json")
		}
	}
	assertRejected := func(t *testing.T) string {
		t.Helper()
		if _, err := executeConfigCommand("validate"); err == nil || err.Error() != wantErr {
			t.Fatalf("config validate error = %v, want %q", err, wantErr)
		}
		if _, err := executeConfigCommand("effective", "--strict"); err == nil || err.Error() != wantErr {
			t.Fatalf("config effective --strict error = %v, want %q", err, wantErr)
		}
		out, err := executeConfigCommand("effective")
		if err != nil {
			t.Fatalf("advisory config effective failed: %v", err)
		}
		for _, key := range nearMisses {
			// Whole-name match: OLIVARES_ELICITATION_MEDIATOR_CONF is a prefix of a real key.
			if strings.Contains("\n"+out, "\n"+key+"=") {
				t.Fatalf("config effective displayed unknown key %s:\n%s", key, out)
			}
		}
		return out
	}

	t.Run("alone", func(t *testing.T) {
		clearOlivaresEnv(t)
		setNearMisses(t)
		assertRejected(t)
	})
	t.Run("beside the recognized names", func(t *testing.T) {
		clearOlivaresEnv(t)
		want := map[string]string{}
		for _, key := range append([]string{addonAIRSHookFirewallKey}, addonAIRSConstructorInputs...) {
			want[key] = setAddonAIRSPolicyFile(t, key)
		}
		setNearMisses(t)
		out := assertRejected(t)
		assertEffectiveLines(t, out, want)
	})
	t.Run("test-only names stay outside", func(t *testing.T) {
		clearOlivaresEnv(t)
		want := map[string]string{}
		for _, key := range addonAIRSConstructorInputs {
			want[key] = setAddonAIRSPolicyFile(t, key)
		}
		t.Setenv("OLIVARES_TEST_CONTENT_FIREWALL_CONFIG", "/fixture/content-firewall.json")
		t.Setenv("OLIVARES_E2E_RENDER_INSPECTOR_CONFIG", "/fixture/render-inspector.json")
		out := assertConfigVerbsAccept(t, want)
		if strings.Contains(out, "OLIVARES_TEST_") || strings.Contains(out, "OLIVARES_E2E_") {
			t.Fatalf("config effective displayed a test-only key:\n%s", out)
		}
	})
}

// The API projection (/config/effective) shares the registry: the five names enter it
// with the same source rule and redaction as every other key, and a near miss does not.
func TestEffectiveConfigEntriesProjectAddonAIRSConstructorInputs(t *testing.T) {
	const dir = "/etc/olivares/airs/"
	environ := []string{
		"OLIVARES_CLAUDE_INFERENCE_KEY=supersecret",
		"OLIVARES_COMPUTER_USE_CONFIG=", // present but empty: the activation overlay supplies it
		"OLIVARES_CONTENT_FIREWALL_CONFIG=" + dir + "content-firewall.json",
		"OLIVARES_CONTENT_FIREWAL_CONFIG=" + dir + "near-miss.json",
	}
	values := map[string]string{
		"OLIVARES_CLAUDE_INFERENCE_KEY":        "supersecret",
		"OLIVARES_COMPUTER_USE_CONFIG":         dir + "computer-use.json",
		"OLIVARES_CONTENT_FIREWALL_CONFIG":     dir + "content-firewall.json",
		"OLIVARES_CONTENT_FIREWAL_CONFIG":      dir + "near-miss.json",
		"OLIVARES_ELICITATION_MEDIATOR_CONFIG": dir + "elicitation-mediator.json",
		"OLIVARES_HOOK_FIREWALL_CONFIG":        dir + "hook-firewall.json",
		"OLIVARES_RENDER_INSPECTOR_CONFIG":     dir + "render-inspector.json",
		"OLIVARES_SERVERTOOL_EGRESS_CONFIG":    dir + "servertool-egress.json",
	}
	got := effectiveConfigEntries(environ, func(key string) string { return values[key] })
	want := []api.EffectiveConfigEntry{
		{Key: "OLIVARES_CLAUDE_INFERENCE_KEY", Value: redactedConfigValue, Redacted: true, Source: "env"},
		{Key: "OLIVARES_COMPUTER_USE_CONFIG", Value: dir + "computer-use.json", Source: "activation"},
		{Key: "OLIVARES_CONTENT_FIREWALL_CONFIG", Value: dir + "content-firewall.json", Source: "env"},
		{Key: "OLIVARES_ELICITATION_MEDIATOR_CONFIG", Value: dir + "elicitation-mediator.json", Source: "activation"},
		{Key: "OLIVARES_HOOK_FIREWALL_CONFIG", Value: dir + "hook-firewall.json", Source: "activation"},
		{Key: "OLIVARES_RENDER_INSPECTOR_CONFIG", Value: dir + "render-inspector.json", Source: "activation"},
		{Key: "OLIVARES_SERVERTOOL_EGRESS_CONFIG", Value: dir + "servertool-egress.json", Source: "activation"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective entries:\n got %+v\nwant %+v", got, want)
	}
}

// setAddonAIRSPolicyFile writes a policy file carrying the sentinel body and points key at it.
func setAddonAIRSPolicyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), strings.ToLower(key)+".json")
	if err := os.WriteFile(path, []byte(addonAIRSPolicyBody), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Setenv(key, path)
	return path
}

// assertConfigVerbsAccept runs the two documented gates and returns the effective dump.
func assertConfigVerbsAccept(t *testing.T, want map[string]string) string {
	t.Helper()
	if out, err := executeConfigCommand("validate"); err != nil || out != configValidateOKLine+"\n" {
		t.Fatalf("config validate = %q, %v; want %q", out, err, configValidateOKLine)
	}
	out, err := executeConfigCommand("effective", "--strict")
	if err != nil {
		t.Fatalf("config effective --strict: %v\n%s", err, out)
	}
	assertEffectiveLines(t, out, want)
	return out
}

func assertEffectiveLines(t *testing.T, out string, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if !strings.Contains("\n"+out, "\n"+key+"="+value+"\n") {
			t.Errorf("config effective lacks %s=%s:\n%s", key, value, out)
		}
	}
	if strings.Contains(out, "airs-policy-body-not-for-display") {
		t.Fatalf("config effective displayed referenced file content:\n%s", out)
	}
}

func executeConfigCommand(args ...string) (string, error) {
	cmd := newConfigCmd()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	_, err := cmd.ExecuteC()
	return out.String(), err
}

func clearOlivaresEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "OLIVARES_") {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		restoreKey, restoreValue := key, value
		t.Cleanup(func() {
			if err := os.Setenv(restoreKey, restoreValue); err != nil {
				t.Errorf("restore %s: %v", restoreKey, err)
			}
		})
	}
}

// TestConfigRegistryCoversBootAndPumpEnvKeys (D-10) is the drift guard:
// every OLIVARES_* env key this package READS must be registered, so
// `olivares config validate --strict` never rejects a runtime knob.
//
// ⚠ THIS GUARD USED TO SCAN TWO FILES: boot.go and *pump*.go. That shape is why it
// was green while four honored keys were unregistered. The engine loads
// operator config from plenty of files that are neither: eventingegress.go carries
// the egress ceiling, codexhookpepserver.go the Codex PEP config, auditspool.go the
// metadata-blinding rule, cliconfig.go the CLI config override. `config effective
// --strict` — the documented CI gate — refused a deployment that set the first one,
// measured 2026-08-10. A guard whose reach is narrower than its claim reports clean
// about the part it never looked at, which is the third answer disguised as the first.
//
// It now scans the WHOLE package, and two things make that precise rather than noisy:
//
//   - It reads STRING LITERALS through go/parser, not a regex over bytes. A key named
//     in a comment (auditkey.go's "OLIVARES_LEDGER_*" precedent, wire_noenterprise.go's
//     note that the closed side reads OLIVARES_CIRCUIT_BREAKER_CONFIG) is prose about a
//     key, not a read of one, and no longer produces a finding.
//   - A literal that is a STEM of a registered prefix is covered: claude_inference.go
//     passes "OLIVARES_EMBEDDINGS" to a helper that appends _BASE_URL/_KEY/_MODEL, and
//     the registry holds the family as the prefix "OLIVARES_EMBEDDINGS_".
func TestConfigRegistryCoversEveryEnvKeyThisPackageReads(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	envRe := regexp.MustCompile(`^OLIVARES_[A-Z0-9_]+$`)
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// The two exclusions, each with its reason rather than a bare skip:
		//   config_registry.go — the registry declares every key as a literal, so it
		//     would report itself; its own drift is covered by the sorted/unique test.
		//   supportbundle.go  — supportPublicConfigKeys is a CAPTURE allowlist naming
		//     which environment variables are safe to include in a support bundle
		//     (it lists bare HOST/LISTEN/PROFILE too). Those are not keys this engine
		//     reads, and registering them would widen the config contract by accident.
		if name == "config_registry.go" || name == "supportbundle.go" {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !envRe.MatchString(key) {
				return true
			}
			if configEnvKeyMode(key) != configKeyUnknown || isRegisteredPrefixStem(key) {
				return true
			}
			t.Errorf("%s reads %s but it is not in the config registry (config_registry.go) — "+
				"`config validate --strict` would reject a deployment that sets it", name, key)
			return true
		})
	}
	// The floor is the whole package now, not two files: if this drops, the widening
	// was undone and the guard is back to reporting clean about what it cannot see.
	if scanned < 100 {
		t.Fatalf("only %d package sources were scanned; the drift guard is vacuous", scanned)
	}
	t.Logf("scanned %d sources for unregistered config env keys", scanned)
}

// isRegisteredPrefixStem reports whether key is the stem a registered prefix family
// is built from — "OLIVARES_EMBEDDINGS" for the prefix "OLIVARES_EMBEDDINGS_". Such a
// literal names a family, not a variable, so it is covered by definition.
func isRegisteredPrefixStem(key string) bool {
	for _, prefix := range prefixConfigEnvKeys {
		if strings.HasPrefix(prefix, key) {
			return true
		}
	}
	return false
}
