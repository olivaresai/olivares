// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgmajorsenvtest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const unexpectedMarker = "pg-m1-synthetic-environment-must-not-be-printed"

func TestPGMajorsEnvironment(t *testing.T) {
	script := workflowScript(t)
	if err := checkPasses(executePasses(t, script)); err != nil {
		t.Fatal(err)
	}
}

func TestPGMajorsEnvironmentRejectsRegressions(t *testing.T) {
	script := workflowScript(t)
	for _, tc := range []struct {
		name, old, replacement, want string
		printsEnvironment            bool
	}{
		{
			name: "original_comment_boundary",
			old:  "OLIVARES_TEST_POSTGRES_REQUIRED=1 \\\n",
			replacement: "# This comment ends the continued env command.\n" +
				"OLIVARES_TEST_POSTGRES_REQUIRED=1 \\\n",
			want: "invocation 1 variable OLIVARES_TEST_POSTGRES_DSN", printsEnvironment: true,
		},
		{
			name: "missing_superuser_dsn",
			old:  "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=\"postgres://postgres:postgres@127.0.0.1:${port}/postgres?sslmode=disable\" \\\n",
			want: "invocation 1 variable OLIVARES_TEST_POSTGRES_SUPERUSER_DSN",
		},
		{
			name:        "last_exit_only",
			old:         "echo \"$m $pass_rc\" >> pg-majors-exits.txt",
			replacement: "echo \"$m $pass_rc\" > pg-majors-exits.txt",
			want:        "exit ledger",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := strings.Count(script, tc.old); n != 1 || tc.old == tc.replacement {
				t.Fatalf("mutation must change exactly one command fragment; matches=%d", n)
			}
			mutated := strings.Replace(script, tc.old, tc.replacement, 1)
			result := executePasses(t, mutated)
			if err := checkPasses(result); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mutation oracle = %v, want %q", err, tc.want)
			}
			if got := strings.Contains(result.stdout, unexpectedMarker); got != tc.printsEnvironment {
				t.Fatalf("synthetic environment marker printed=%t, want %t", got, tc.printsEnvironment)
			}
		})
	}
}

func TestPGMajorsEnvironmentRejectsEarlyExit(t *testing.T) {
	script := workflowScript(t)
	for _, change := range [][2]string{
		{"if env \\\n", "env \\\n"},
		{"2>&1; then\n    pass_rc=0\n  else\n    pass_rc=$?\n  fi", "2>&1\n  pass_rc=$?"},
	} {
		if count := strings.Count(script, change[0]); count != 1 {
			t.Fatalf("unguarded-command mutation matches %d fragments, want one", count)
		}
		script = strings.Replace(script, change[0], change[1], 1)
	}
	result := executePasses(t, script)
	if result.exitCode != 7 || len(result.invocations) != 2 || result.ledger != "15 0\n" {
		t.Fatalf("early-exit receipt: status=%d, calls=%d, ledger=%q", result.exitCode, len(result.invocations), result.ledger)
	}
	if err := checkPasses(result); err == nil || !strings.Contains(err.Error(), "script exit 7") {
		t.Fatalf("unguarded failing command was not rejected: %v", err)
	}
}

func TestPGMajorsScriptInput(t *testing.T) {
	for _, tc := range []struct{ name, document string }{
		{"malformed_yaml", "jobs: ["},
		{"missing_step", "jobs: {matrix: {steps: []}}"},
		{"duplicate_step", "jobs: {matrix: {steps: [{id: passes, run: echo}, {id: passes, run: echo}]}}"},
		{"duplicate_job_key", "jobs: {matrix: {steps: []}, matrix: {steps: [{id: passes, run: echo}]}}"},
		{"multiple_documents", "jobs: {}\n---\njobs: {}\n"},
		{"missing_run", "jobs: {matrix: {steps: [{id: passes}]}}"},
		{"sequence_run", "jobs: {matrix: {steps: [{id: passes, run: [echo]}]}}"},
		{"numeric_run", "jobs: {matrix: {steps: [{id: passes, run: 42}]}}"},
		{"empty_run", "jobs: {matrix: {steps: [{id: passes, run: ''}]}}"},
		{"alias_run", "command: &command echo\njobs: {matrix: {steps: [{id: passes, run: *command}]}}"},
		{"nul_run", `jobs: {matrix: {steps: [{id: passes, run: "echo\0"}]}}`},
		{"github_expression", "jobs: {matrix: {steps: [{id: passes, run: 'echo ${{ env.VALUE }}'}]}}"},
		{"unsupported_shell", "jobs: {matrix: {steps: [{id: passes, shell: python, run: echo}]}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := passesScript([]byte(tc.document)); err == nil {
				t.Fatal("unrepresentable workflow input was accepted")
			}
		})
	}
	t.Run("literal_script", func(t *testing.T) {
		script, err := passesScript([]byte("jobs:\n  matrix:\n    steps:\n      - id: passes\n        run: |\n          echo 'literal'\n"))
		if err != nil || script != "echo 'literal'\n" {
			t.Fatalf("literal script = %q, error = %v", script, err)
		}
	})
}

func workflowScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "workflows", "pg-majors.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := passesScript(raw)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

// Preserve the parsed scalar bytes; expressions needing GitHub evaluation are not Bash inputs.
func passesScript(raw []byte) (string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return "", fmt.Errorf("decode workflow: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", fmt.Errorf("workflow must contain exactly one YAML document")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return "", fmt.Errorf("workflow document is absent")
	}
	if err := validateMappings(&document); err != nil {
		return "", err
	}
	jobs := mappingValue(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return "", fmt.Errorf("workflow jobs must be a mapping")
	}
	var runs []*yaml.Node
	for i := 1; i < len(jobs.Content); i += 2 {
		steps := mappingValue(jobs.Content[i], "steps")
		if steps == nil {
			continue
		}
		if steps.Kind != yaml.SequenceNode {
			return "", fmt.Errorf("workflow steps must be a sequence")
		}
		for _, step := range steps.Content {
			id := mappingValue(step, "id")
			if id != nil && id.Kind == yaml.ScalarNode && id.Value == "passes" {
				if shell := mappingValue(step, "shell"); shell != nil &&
					(shell.Kind != yaml.ScalarNode || shell.Tag != "!!str" || shell.Value != "bash") {
					return "", fmt.Errorf("passes shell must use the default Linux shell or explicit bash")
				}
				runs = append(runs, mappingValue(step, "run"))
			}
		}
	}
	if len(runs) != 1 {
		return "", fmt.Errorf("want one step with id passes, got %d", len(runs))
	}
	run := runs[0]
	if run == nil || run.Kind != yaml.ScalarNode || run.Tag != "!!str" || strings.TrimSpace(run.Value) == "" {
		return "", fmt.Errorf("passes run must be a nonempty literal string")
	}
	if strings.ContainsRune(run.Value, 0) || strings.Contains(run.Value, "${{") {
		return "", fmt.Errorf("passes run cannot be represented as a local Bash script")
	}
	return run.Value, nil
}

func validateMappings(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || seen[key.Value] {
				return fmt.Errorf("workflow mapping has a duplicate or nonscalar key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateMappings(child); err != nil {
			return err
		}
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1]
			}
		}
	}
	return nil
}

type passResult struct {
	stdout, stderr, ledger string
	invocations            [][]string
	exitCode               int
}

// The PATH exposes only the script's local utilities and a recording substitute for go.
func executePasses(t *testing.T, script string) passResult {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, path := range []string{bin, filepath.Join(dir, "ci"), filepath.Join(dir, "calls")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env", "grep", "sed", "tee", "cat"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("ci/pg-majors-packages.txt", "# Synthetic packages; no real Go execution.\nfixture/first\nfixture/second\n", 0o600)
	write("passes.sh", script, 0o600)
	write("bin/go", "#!"+bash+"\n"+goSubstitute, 0o700)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-eo", "pipefail", "passes.sh")
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + bin, "HOME=" + dir, "LC_ALL=C",
		"PGPORT_15=41515", "PGPORT_16=41616", "PGPORT_17=41717", "PGPORT_18=41818",
		"PG_M1_CALLS=" + filepath.Join(dir, "calls"), "PG_M1_UNEXPECTED=" + unexpectedMarker,
	}
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("synthetic passes script exceeded its deadline: %v", ctx.Err())
	}
	if runErr != nil && cmd.ProcessState == nil {
		t.Fatalf("start synthetic passes script: %v", runErr)
	}
	result := passResult{stdout: stdout.String(), stderr: stderr.String()}
	result.exitCode = cmd.ProcessState.ExitCode()
	entries, err := os.ReadDir(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "call-") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "calls", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result.invocations = append(result.invocations, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"))
	}
	ledger, err := os.ReadFile(filepath.Join(dir, "pg-majors-exits.txt"))
	if err != nil {
		t.Fatal(err)
	}
	result.ledger = string(ledger)
	return result
}

func checkPasses(result passResult) error {
	if result.exitCode != 0 {
		return fmt.Errorf("passes script exit %d", result.exitCode)
	}
	if len(result.invocations) != 4 {
		return fmt.Errorf("got %d invocations, want four", len(result.invocations))
	}
	majorMap := "15=postgres://postgres:postgres@127.0.0.1:41515/postgres?sslmode=disable," +
		"16=postgres://postgres:postgres@127.0.0.1:41616/postgres?sslmode=disable," +
		"17=postgres://postgres:postgres@127.0.0.1:41717/postgres?sslmode=disable," +
		"18=postgres://postgres:postgres@127.0.0.1:41818/postgres?sslmode=disable"
	names := []string{"OLIVARES_TEST_POSTGRES_DSN", "OLIVARES_TEST_POSTGRES_ADMIN_DSN", "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN",
		"OLIVARES_TEST_VECTOR_DSN", "OLIVARES_TEST_POSTGRES_REQUIRED", "OLIVARES_TEST_PG_EXPECT_MAJOR", "OLIVARES_TEST_POSTGRES_MAJOR_DSNS"}
	args := []string{"test", "-json", "-count=1", "-timeout", "45m",
		"github.com/olivaresai/olivares/fixture/first", "github.com/olivaresai/olivares/fixture/second"}
	for i, fields := range result.invocations {
		if len(fields) < len(names) {
			return fmt.Errorf("invocation %d has an incomplete receipt", i+1)
		}
		major := 15 + i
		port := fmt.Sprintf("4%d%d", major, major)
		app := "postgres://olivares_app:apppw@127.0.0.1:" + port + "/olivares?sslmode=disable"
		want := []string{app, "postgres://olivares_admin:adminpw@127.0.0.1:" + port + "/olivares?sslmode=disable",
			"postgres://postgres:postgres@127.0.0.1:" + port + "/postgres?sslmode=disable", app, "1", fmt.Sprint(major), majorMap}
		for j, name := range names {
			if fields[j] != want[j] {
				return fmt.Errorf("invocation %d variable %s differs from the synthetic input", i+1, name)
			}
		}
		if !reflect.DeepEqual(fields[len(names):], args) {
			return fmt.Errorf("invocation %d arguments differ from the exact test command", i+1)
		}
	}
	if result.ledger != "15 0\n16 7\n17 19\n18 0\n" {
		return fmt.Errorf("exit ledger did not retain every controlled status")
	}
	if strings.Contains(result.stdout, unexpectedMarker) || result.stderr != "" {
		return fmt.Errorf("unexpected environment output or script diagnostic")
	}
	return nil
}

const goSubstitute = `set -eu
count=0
if [[ -f "$PG_M1_CALLS/count" ]]; then
  count=$(<"$PG_M1_CALLS/count")
fi
count=$((count + 1))
printf '%s\n' "$count" > "$PG_M1_CALLS/count"
{
  printf '%s\0' "${OLIVARES_TEST_POSTGRES_DSN-<absent>}"
  printf '%s\0' "${OLIVARES_TEST_POSTGRES_ADMIN_DSN-<absent>}"
  printf '%s\0' "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN-<absent>}"
  printf '%s\0' "${OLIVARES_TEST_VECTOR_DSN-<absent>}"
  printf '%s\0' "${OLIVARES_TEST_POSTGRES_REQUIRED-<absent>}"
  printf '%s\0' "${OLIVARES_TEST_PG_EXPECT_MAJOR-<absent>}"
  printf '%s\0' "${OLIVARES_TEST_POSTGRES_MAJOR_DSNS-<absent>}"
  printf '%s\0' "$@"
} > "$PG_M1_CALLS/call-$count"
case "$count" in
  1|4) exit 0 ;;
  2) exit 7 ;;
  3) exit 19 ;;
  *) exit 29 ;;
esac
`
