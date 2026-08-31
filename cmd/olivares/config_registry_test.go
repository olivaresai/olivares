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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
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
