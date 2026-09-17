// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// privateOperatorInputs are nine exact names that private enterprise code reads through
// osGetenv (cmd-overlay/olivares, private): six file paths, two booleans and one switch. The
// census of 2026-09-11 found each one read by a constructor or by an `olivares enterprise`
// assessor command, while `config validate` and `config effective --strict` refused a
// deployment that set it. literal is the value a test sets for a boolean or a switch; an empty
// literal marks a file path. The shape follows the reader, not the name:
// OLIVARES_CIRCUIT_BREAKER_CONFIG is parsed with strconv.ParseBool. Recognition says nothing
// about build linkage, activation or file contents, and these verbs never open the file.
var privateOperatorInputs = []struct {
	key, literal string
}{
	{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE", "true"},
	{"OLIVARES_CIRCUIT_BREAKER_CONFIG", "false"},
	{"OLIVARES_CREDENTIAL_MINTER_CONFIG", ""},
	{"OLIVARES_LOGIN_ENFORCEMENT", "off"},
	{"OLIVARES_ONBOARDING_CONFIG", ""},
	{"OLIVARES_PQC_POSTURE_CONFIG", ""},
	{"OLIVARES_RETENTION_GOVERNOR_CONFIG", ""},
	{"OLIVARES_RTBF_DEPTH_CONFIG", ""},
	{"OLIVARES_TOOL_PIN_CONFIG", ""},
}

// privateOperatorConfigBody is what the referenced files contain, credential-shaped field
// included. It must never reach the output: the configured value is the path.
const privateOperatorConfigBody = `{"client_secret":"private-operator-body-not-for-display"}`

// jetStreamResourceNames are the durable bus's default stream and dedup bucket names
// (enterprise/durablebus/config.go, private). Nothing reads them from the environment, so they
// stay unknown keys; the durable bus itself is configured by OLIVARES_DURABLE_BUS_CONFIG.
var jetStreamResourceNames = []string{"OLIVARES_DURABLE", "OLIVARES_DURABLE_DEDUP"}

func TestConfigVerbsAcceptPrivateOperatorInputs(t *testing.T) {
	for _, in := range privateOperatorInputs {
		t.Run(in.key, func(t *testing.T) {
			clearOlivaresEnv(t)
			value := setPrivateOperatorInput(t, in.key, in.literal)
			assertPrivateOperatorVerbsAccept(t, map[string]string{in.key: value})
		})
	}
	t.Run("all nine together", func(t *testing.T) {
		clearOlivaresEnv(t)
		assertPrivateOperatorVerbsAccept(t, setAllPrivateOperatorInputs(t))
	})
	t.Run("missing file references are displayed, not opened", func(t *testing.T) {
		clearOlivaresEnv(t)
		absent := filepath.Join(t.TempDir(), "absent")
		want := make(map[string]string, len(privateOperatorInputs))
		for _, in := range privateOperatorInputs {
			value := in.literal
			if value == "" {
				value = filepath.Join(absent, strings.ToLower(in.key)+".json")
			}
			t.Setenv(in.key, value)
			want[in.key] = value
		}
		assertPrivateOperatorVerbsAccept(t, want)
		if _, err := os.Stat(absent); !os.IsNotExist(err) {
			t.Fatalf("config verbs created %s: stat error %v", absent, err)
		}
	})
	// The verbs recognize names; they do not parse values. A value its reader would treat as
	// off, or would log as invalid, is still shown as given and never resolved as a path.
	t.Run("boolean and switch literals are shown as given", func(t *testing.T) {
		for _, tc := range []struct{ key, value string }{
			{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE", "1"},
			{"OLIVARES_AUDIT_LEGALHOLD_RECONCILE", "false"},
			{"OLIVARES_CIRCUIT_BREAKER_CONFIG", "true"},
			{"OLIVARES_CIRCUIT_BREAKER_CONFIG", "yes-please"},
			{"OLIVARES_LOGIN_ENFORCEMENT", "DISABLED"},
			{"OLIVARES_LOGIN_ENFORCEMENT", "on"},
		} {
			t.Run(tc.key+"="+tc.value, func(t *testing.T) {
				clearOlivaresEnv(t)
				t.Setenv(tc.key, tc.value)
				assertPrivateOperatorVerbsAccept(t, map[string]string{tc.key: tc.value})
			})
		}
	})
}

// The nine names must not widen into families, and the durable bus's resource names must not
// ride in with them: each of these is still an unknown key, named on its own by both gates,
// alone and beside the recognized names.
func TestConfigVerbsStillRejectPrivateOperatorNearMissesAndJetStreamNames(t *testing.T) {
	nearMisses := []string{
		"OLIVARES_AUDIT_LEGALHOLD_RECONCILER",
		"OLIVARES_CIRCUIT_BREAKER",
		"OLIVARES_CREDENTIAL_MINTER",
		"OLIVARES_LOGIN_ENFORCE",
		"OLIVARES_ONBOARDING_CONF",
		"OLIVARES_PQC_POSTURE",
		"OLIVARES_RETENTION_GOVERNORS_CONFIG",
		"OLIVARES_RTBF_DEPTH_CONF",
		"OLIVARES_TOOL_PIN",
	}
	for _, group := range []struct {
		name string
		keys []string
	}{
		{"near misses", nearMisses},
		{"JetStream resource names", jetStreamResourceNames},
	} {
		t.Run(group.name+" alone", func(t *testing.T) {
			clearOlivaresEnv(t)
			setUnknownOlivaresKeys(t, group.keys)
			assertOlivaresKeysRejected(t, group.keys, map[string]string{})
		})
		t.Run(group.name+" beside the recognized names", func(t *testing.T) {
			clearOlivaresEnv(t)
			want := setAllPrivateOperatorInputs(t)
			// The durable bus's registered file name is the control: it stays accepted.
			want["OLIVARES_DURABLE_BUS_CONFIG"] = setPrivateOperatorInput(t, "OLIVARES_DURABLE_BUS_CONFIG", "")
			setUnknownOlivaresKeys(t, group.keys)
			assertOlivaresKeysRejected(t, group.keys, want)
		})
	}
	t.Run("test-only names beside the recognized names stay accepted and hidden", func(t *testing.T) {
		clearOlivaresEnv(t)
		want := setAllPrivateOperatorInputs(t)
		t.Setenv("OLIVARES_TEST_CIRCUIT_BREAKER_CONFIG", "true")
		t.Setenv("OLIVARES_E2E_TOOL_PIN_CONFIG", "/fixture/tool-pin.json")
		assertPrivateOperatorVerbsAccept(t, want)
	})
}

// The API projection (/config/effective) is effectiveConfigEntries(os.Environ(), osGetenv), as
// boot wires it. The nine names enter it under the same overlay, source and redaction rules as
// every other key: a real environment value wins over the activation overlay, an empty one
// yields to it, booleans and the switch are shown as literals, sensitive siblings stay redacted,
// and unknown or test-only names stay out, including an overlay value under an unknown name.
func TestEffectiveConfigEntriesProjectPrivateOperatorInputs(t *testing.T) {
	const (
		envDir     = "/etc/olivares/private/"
		overlayDir = "/var/lib/olivares/activation/"
	)
	clearOlivaresEnv(t)
	setActivationOverlayForTest(map[string]string{
		"OLIVARES_AUDIT_LEGALHOLD_RECONCILE": "true",
		"OLIVARES_DURABLE_DEDUP":             "OLIVARES_DURABLE_DEDUP",
		"OLIVARES_ONBOARDING_CONFIG":         overlayDir + "onboarding.json",
		"OLIVARES_PQC_POSTURE_CONFIG":        overlayDir + "pqcposture.json",
		"OLIVARES_RETENTION_GOVERNOR_CONFIG": overlayDir + "retention-governor.json",
		"OLIVARES_RTBF_DEPTH_CONFIG":         overlayDir + "rtbf-depth.json",
		"OLIVARES_TOOL_PIN_CONFIG":           overlayDir + "tool-pinning.json",
	})
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	t.Setenv("OLIVARES_CIRCUIT_BREAKER_CONFIG", "false")
	t.Setenv("OLIVARES_CLAUDE_INFERENCE_KEY", "supersecret-inference")
	t.Setenv("OLIVARES_CREDENTIAL_MINTER_CONFIG", envDir+"credential-minter.json")
	t.Setenv("OLIVARES_DURABLE", "OLIVARES_DURABLE")
	t.Setenv("OLIVARES_LOGIN_ENFORCEMENT", "off")
	t.Setenv("OLIVARES_OIDC_CLIENT_SECRET", "supersecret-oidc")
	t.Setenv("OLIVARES_RTBF_DEPTH_CONF", envDir+"near-miss.json")
	t.Setenv("OLIVARES_RTBF_DEPTH_CONFIG", "") // present but empty: the overlay supplies it
	t.Setenv("OLIVARES_TEST_ONBOARDING_CONFIG", "/fixture/onboarding.json")
	t.Setenv("OLIVARES_TOOL_PIN_CONFIG", envDir+"tool-pin.json") // wins over the overlay

	got := effectiveConfigEntries(os.Environ(), osGetenv)
	want := []api.EffectiveConfigEntry{
		{Key: "OLIVARES_AUDIT_LEGALHOLD_RECONCILE", Value: "true", Source: "activation"},
		{Key: "OLIVARES_CIRCUIT_BREAKER_CONFIG", Value: "false", Source: "env"},
		{Key: "OLIVARES_CLAUDE_INFERENCE_KEY", Value: redactedConfigValue, Redacted: true, Source: "env"},
		{Key: "OLIVARES_CREDENTIAL_MINTER_CONFIG", Value: envDir + "credential-minter.json", Source: "env"},
		{Key: "OLIVARES_LOGIN_ENFORCEMENT", Value: "off", Source: "env"},
		{Key: "OLIVARES_OIDC_CLIENT_SECRET", Value: redactedConfigValue, Redacted: true, Source: "env"},
		{Key: "OLIVARES_ONBOARDING_CONFIG", Value: overlayDir + "onboarding.json", Source: "activation"},
		{Key: "OLIVARES_PQC_POSTURE_CONFIG", Value: overlayDir + "pqcposture.json", Source: "activation"},
		{Key: "OLIVARES_RETENTION_GOVERNOR_CONFIG", Value: overlayDir + "retention-governor.json", Source: "activation"},
		{Key: "OLIVARES_RTBF_DEPTH_CONFIG", Value: overlayDir + "rtbf-depth.json", Source: "activation"},
		{Key: "OLIVARES_TOOL_PIN_CONFIG", Value: envDir + "tool-pin.json", Source: "env"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective entries:\n got %+v\nwant %+v", got, want)
	}
}

// setPrivateOperatorInput sets key to literal or, for an empty literal, to an owned file
// carrying privateOperatorConfigBody. It returns the value set.
func setPrivateOperatorInput(t *testing.T, key, literal string) string {
	t.Helper()
	value := literal
	if value == "" {
		value = filepath.Join(t.TempDir(), strings.ToLower(key)+".json")
		if err := os.WriteFile(value, []byte(privateOperatorConfigBody), 0o600); err != nil {
			t.Fatalf("write %s: %v", value, err)
		}
	}
	t.Setenv(key, value)
	return value
}

func setAllPrivateOperatorInputs(t *testing.T) map[string]string {
	t.Helper()
	values := make(map[string]string, len(privateOperatorInputs))
	for _, in := range privateOperatorInputs {
		values[in.key] = setPrivateOperatorInput(t, in.key, in.literal)
	}
	return values
}

func setUnknownOlivaresKeys(t *testing.T, keys []string) {
	t.Helper()
	for _, key := range keys {
		t.Setenv(key, "unknown-"+strings.ToLower(key))
	}
}

// assertPrivateOperatorVerbsAccept runs the two documented gates and requires the effective
// dump to be exactly the wanted lines, with no referenced file body in it.
func assertPrivateOperatorVerbsAccept(t *testing.T, want map[string]string) {
	t.Helper()
	out := assertConfigVerbsAccept(t, want)
	if wantOut := effectiveConfigText(want); out != wantOut {
		t.Fatalf("config effective --strict = %q, want %q", out, wantOut)
	}
	if strings.Contains(out, "private-operator-body-not-for-display") {
		t.Fatalf("config effective displayed referenced file content:\n%s", out)
	}
}

// assertOlivaresKeysRejected requires both gates to fail with an error naming exactly the
// unknown keys, and advisory `config effective` to show exactly the recognized values.
func assertOlivaresKeysRejected(t *testing.T, unknown []string, want map[string]string) {
	t.Helper()
	names := append([]string(nil), unknown...)
	sort.Strings(names)
	wantErr := "unrecognized OLIVARES_* environment keys: [" + strings.Join(names, " ") + "]"
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
	if wantOut := effectiveConfigText(want); out != wantOut {
		t.Fatalf("advisory config effective = %q, want %q", out, wantOut)
	}
}

// effectiveConfigText is the text form `config effective` prints for values: sorted KEY=value lines.
func effectiveConfigText(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key + "=" + values[key] + "\n")
	}
	return b.String()
}
