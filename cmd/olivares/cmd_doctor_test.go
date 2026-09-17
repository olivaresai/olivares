// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func doctorFixture(t *testing.T) (*doctorOptions, doctorDeps) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "olivares")
	config := filepath.Join(root, "olivares.env")
	unit := filepath.Join(root, "olivares.service")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("OLIVARES_TOKEN=never-print-this\nOLIVARES_EXTRA_ARGS=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unitBody := "[Service]\nExecStart=" + binary + " serve --data-dir=" + data +
		" --listen=127.0.0.1:8443\nEnvironmentFile=-" + config + "\n"
	if err := os.WriteFile(unit, []byte(unitBody), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema": "olivares.ai/local-install/v2", "mode": "user", "init": "systemd",
		"data_dir": data, "config": config,
		"files": []map[string]string{
			{"path": binary, "mode": "0755"},
			{"path": config, "mode": "0600"},
			{"path": unit, "mode": "0644"},
		},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "install-manifest.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	o := &doctorOptions{
		mode: "user", init: "systemd", dataDir: data, config: config, unit: unit,
		binary: binary, server: "https://127.0.0.1:8443", timeout: time.Second,
	}
	deps := defaultDoctorDeps()
	deps.euid = os.Geteuid
	deps.lookupUID = func(string) (int, error) { return os.Geteuid(), nil }
	deps.homeDir = func() (string, error) { return root, nil }
	deps.getenv = func(string) string { return "" }
	deps.goos = "linux"
	deps.run = func(context.Context, string, ...string) (int, error) { return 0, nil }
	deps.httpGet = func(_ context.Context, rawURL, _ string, _ time.Duration) (int, []byte, error) {
		if strings.HasSuffix(rawURL, "/status") {
			return 200, []byte(`{"status":"operational","components":[{"name":"store","status":"operational"}]}`), nil
		}
		return 200, []byte("ok"), nil
	}
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	return o, deps
}

func TestDoctorHealthyAndNeverEmitsConfigValues(t *testing.T) {
	o, deps := doctorFixture(t)
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK || report.Overall != "healthy" {
		t.Fatalf("doctor = code %d overall %q checks=%+v", code, report.Overall, report.Checks)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("never-print-this")) {
		t.Fatal("doctor JSON disclosed a configuration value")
	}
	found := map[string]string{}
	for _, check := range report.Checks {
		found[check.Name] = check.Status
	}
	for _, name := range []string{"binary", "configuration", "service-unit", "install-manifest", "init-state", "livez", "readyz", "store"} {
		if found[name] != "pass" {
			t.Errorf("check %s = %q, want pass", name, found[name])
		}
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("output", "json", "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := renderDoctor(cmd, report); err != nil {
		t.Fatal(err)
	}
	var decoded doctorReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("doctor JSON output is invalid: %v\n%s", err, out.String())
	}
	if decoded.Schema != doctorSchema || decoded.Overall != "healthy" {
		t.Fatalf("decoded report = %+v", decoded)
	}
}

func TestDoctorUnsafeDataModeIsMeasuredDefect(t *testing.T) {
	o, deps := doctorFixture(t)
	if err := os.Chmod(o.dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.Err || report.Overall != "unhealthy" {
		t.Fatalf("doctor mutant = code %d overall %q", code, report.Overall)
	}
	for _, check := range report.Checks {
		if check.Name == "data-directory" && check.Status == "fail" {
			return
		}
	}
	t.Fatal("unsafe data-directory mutant was not observed")
}

func TestDoctorRequiredRuntimeCheckUnmeasurableUsesRC2(t *testing.T) {
	o, deps := doctorFixture(t)
	deps.run = func(context.Context, string, ...string) (int, error) {
		return -1, errors.New("exec unavailable")
	}
	deps.httpGet = func(context.Context, string, string, time.Duration) (int, []byte, error) {
		return 0, nil, errors.New("probe unavailable")
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.Usage || report.Overall != "unmeasurable" {
		t.Fatalf("doctor = code %d overall %q, want 2/unmeasurable", code, report.Overall)
	}
	for _, check := range report.Checks {
		if check.Name == "init-state" && check.Status == "unknown" {
			return
		}
	}
	t.Fatal("unexecutable init check was not reported unknown")
}

func TestDoctorConfigUnknownKeyMutantIsRed(t *testing.T) {
	o, deps := doctorFixture(t)
	if err := os.WriteFile(o.config, []byte("OLIVARES_NOT_A_REAL_KEY=secret\n"), fs.FileMode(0o600)); err != nil {
		t.Fatal(err)
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.Err || report.Overall != "unhealthy" {
		t.Fatalf("unknown-key mutant = code %d overall %q", code, report.Overall)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("secret")) {
		t.Fatal("unknown-key refusal disclosed its value")
	}
}

func TestDoctorUnitDriftMutantIsRed(t *testing.T) {
	o, deps := doctorFixture(t)
	if err := os.WriteFile(o.unit, []byte("[Service]\nExecStart=/tmp/wrong serve\n"), fs.FileMode(0o644)); err != nil {
		t.Fatal(err)
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.Err || report.Overall != "unhealthy" {
		t.Fatalf("unit-drift mutant = code %d overall %q", code, report.Overall)
	}
	for _, check := range report.Checks {
		if check.Name == "service-unit" && check.Status == "fail" {
			return
		}
	}
	t.Fatal("service-unit drift mutant was not observed")
}

func TestDoctorRefusesPlaintextServer(t *testing.T) {
	o, deps := doctorFixture(t)
	o.server = "http://127.0.0.1:8443"
	_, _, err := runDoctor(context.Background(), o, deps)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("plaintext server error = %v (code %d), want usage", err, exitcode.From(err))
	}
}

func TestDoctorSignedChannelReportsVersionDifference(t *testing.T) {
	o, deps := doctorFixture(t)
	o.checkUpdates = true
	deps.runOutput = func(context.Context, string, ...string) (int, []byte, error) {
		return 0, []byte(`{"status":"upgrade-available","current":"26.8.0","available":"26.9.0"}`), nil
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK {
		t.Fatalf("doctor = code %d overall %q", code, report.Overall)
	}
	for _, check := range report.Checks {
		if check.Name == "update-channel" {
			if check.Status != "pass" || check.Detail != "current=26.8.0 available=26.9.0 status=upgrade-available" {
				t.Fatalf("channel check = %+v", check)
			}
			return
		}
	}
	t.Fatal("update-channel check was omitted")
}

func TestDoctorMalformedChannelResultIsRed(t *testing.T) {
	o, deps := doctorFixture(t)
	o.checkUpdates = true
	deps.runOutput = func(context.Context, string, ...string) (int, []byte, error) {
		return 0, []byte(`{"status":"up-to-date","current":"26.8.0"}`), nil
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.Err || report.Overall != "unhealthy" {
		t.Fatalf("malformed channel mutant = code %d overall %q", code, report.Overall)
	}
}

func TestDoctorBadInvocationIsUsage(t *testing.T) {
	o, deps := doctorFixture(t)
	o.mode = "tenant"
	_, _, err := runDoctor(context.Background(), o, deps)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("bad mode error = %v (code %d), want usage", err, exitcode.From(err))
	}
}

func doctorCheckByName(report doctorReport, name string) doctorCheck {
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	return doctorCheck{}
}

func TestDoctorManifestClaimingAnotherDataDirIsDrift(t *testing.T) {
	o, deps := doctorFixture(t)
	manifest := filepath.Join(o.dataDir, "install-manifest.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	mutant := strings.Replace(string(body), `"data_dir":"`+o.dataDir+`"`, `"data_dir":"/srv/elsewhere"`, 1)
	if mutant == string(body) {
		t.Fatal("fixture lacks the data_dir anchor")
	}
	if err := os.WriteFile(manifest, []byte(mutant), 0o600); err != nil {
		t.Fatal(err)
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil || code != exitcode.Err {
		t.Fatalf("manifest data_dir drift = code %d err=%v", code, err)
	}
	if check := doctorCheckByName(report, "install-manifest"); check.Status != "fail" {
		t.Fatalf("install-manifest = %+v, want fail", check)
	}
}

func TestDoctorAgentOpsLayoutIsNotApplicableWithoutRecordedFiles(t *testing.T) {
	o, deps := doctorFixture(t)
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil || code != exitcode.OK {
		t.Fatalf("doctor = code %d err=%v", code, err)
	}
	check := doctorCheckByName(report, "agentops-layout")
	if check.Status != "not_applicable" || check.Required {
		t.Fatalf("agentops-layout without AgentOps files = %+v", check)
	}
}

// doctorAgentOpsFixture records a managed drop-in, runtime env and external
// workspace in the fixture manifest and lays the files down coherently.
func doctorAgentOpsFixture(t *testing.T) (*doctorOptions, doctorDeps, string, string) {
	t.Helper()
	o, deps := doctorFixture(t)
	root := filepath.Dir(o.dataDir)
	dropin := filepath.Join(root, "olivares.service.d", "agentops.conf")
	runtimeEnv := filepath.Join(root, "agentops.env")
	workspace := filepath.Join(root, "workspaces")
	if err := os.MkdirAll(filepath.Dir(dropin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	dropinBody := "[Service]\nEnvironment=HOME=" + o.dataDir + "/claude-home\n" +
		"ExecStartPre=/usr/bin/install -d -m 0700 " + o.dataDir + "/run\nReadWritePaths=" + workspace + "\n"
	if err := os.WriteFile(dropin, []byte(dropinBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeEnv, []byte("OLIVARES_SESSION_RUNTIME_TOKEN_FILE=never-print-this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(o.dataDir, "install-manifest.json")
	value := map[string]any{
		"schema": "olivares.ai/local-install/v2", "mode": "user", "init": "systemd", "layout": "custom",
		"data_dir": o.dataDir, "config": o.config, "workspace_dir": workspace,
		"files": []map[string]any{
			{"path": o.binary, "role": "binary", "mode": "0755", "managed": true},
			{"path": o.config, "role": "config", "mode": "0600", "managed": true},
			{"path": o.unit, "role": "unit", "mode": "0644", "managed": true},
			{"path": dropin, "role": "dropin", "mode": "0644", "managed": true},
			{"path": runtimeEnv, "role": "runtime-env", "mode": "0600", "managed": true},
		},
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return o, deps, dropin, workspace
}

func TestDoctorAgentOpsLayoutPassesAndNeverReadsRuntimeEnvValues(t *testing.T) {
	o, deps, _, workspace := doctorAgentOpsFixture(t)
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil || code != exitcode.OK {
		t.Fatalf("doctor = code %d err=%v checks=%+v", code, err, report.Checks)
	}
	check := doctorCheckByName(report, "agentops-layout")
	if check.Status != "pass" || !check.Required || !strings.Contains(check.Detail, workspace) {
		t.Fatalf("agentops-layout = %+v", check)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("never-print-this")) {
		t.Fatal("doctor disclosed a runtime env value")
	}
}

func TestDoctorAgentOpsDropinDriftAndAbsentWorkspaceAreMeasuredDefects(t *testing.T) {
	o, deps, dropin, workspace := doctorAgentOpsFixture(t)
	if err := os.WriteFile(dropin, []byte("[Service]\nEnvironment=HOME=/var/lib/olivares/claude-home\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, code, _ := runDoctor(context.Background(), o, deps)
	if check := doctorCheckByName(report, "agentops-layout"); code != exitcode.Err || check.Status != "fail" || !strings.Contains(check.Detail, "does not reference") {
		t.Fatalf("drop-in drift = code %d check %+v", code, check)
	}

	o, deps, _, workspace = doctorAgentOpsFixture(t)
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	report, code, _ = runDoctor(context.Background(), o, deps)
	if check := doctorCheckByName(report, "agentops-layout"); code != exitcode.Err || check.Status != "fail" || !strings.Contains(check.Detail, "absent") {
		t.Fatalf("absent workspace = code %d check %+v", code, check)
	}
}
