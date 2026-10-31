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
	"regexp"
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

// The workflow runs one matrix leg per major; PG_MAJORS is how a leg selects
// its pass without putting a GitHub expression inside the Bash script.
func TestPGMajorsLegRunsOnlyItsDeclaredMajors(t *testing.T) {
	script := workflowScript(t)
	t.Run("one_major", func(t *testing.T) {
		result := executePasses(t, script, "PG_MAJORS=16")
		if result.exitCode != 0 {
			t.Fatalf("passes script exit %d", result.exitCode)
		}
		if len(result.invocations) != 1 {
			t.Fatalf("got %d invocations, want one", len(result.invocations))
		}
		if err := checkInvocation(0, result.invocations[0], 16); err != nil {
			t.Fatal(err)
		}
		if result.ledger != "16 0\n" {
			t.Fatalf("exit ledger = %q, want the leg's single pass", result.ledger)
		}
		assertFile(t, result.dir, "gotest-16.json", true)
		assertFile(t, result.dir, "gotest-15.json", false)
	})
	t.Run("two_majors", func(t *testing.T) {
		result := executePasses(t, script, "PG_MAJORS=16 17")
		if result.exitCode != 0 {
			t.Fatalf("passes script exit %d", result.exitCode)
		}
		if len(result.invocations) != 2 {
			t.Fatalf("got %d invocations, want two", len(result.invocations))
		}
		for i, major := range []int{16, 17} {
			if err := checkInvocation(i, result.invocations[i], major); err != nil {
				t.Fatal(err)
			}
		}
		if result.ledger != "16 0\n17 7\n" {
			t.Fatalf("exit ledger = %q, want both controlled statuses", result.ledger)
		}
		assertFile(t, result.dir, "gotest-16.json", true)
		assertFile(t, result.dir, "gotest-17.json", true)
		assertFile(t, result.dir, "gotest-15.json", false)
	})
	// An empty or unknown selection must refuse: running nothing (or the wrong
	// major) while exiting 0 is the empty green the evaluator exists to stop.
	t.Run("empty_selection", func(t *testing.T) {
		result := executePasses(t, script, "PG_MAJORS=")
		if result.exitCode != 1 || len(result.invocations) != 0 || !strings.Contains(result.stderr, "::error::PG_MAJORS") {
			t.Fatalf("empty selection: status=%d, calls=%d, stderr=%q", result.exitCode, len(result.invocations), result.stderr)
		}
	})
	t.Run("unknown_major", func(t *testing.T) {
		result := executePasses(t, script, "PG_MAJORS=14")
		if result.exitCode != 1 || len(result.invocations) != 0 || !strings.Contains(result.stderr, "::error::PG_MAJORS") {
			t.Fatalf("unknown major: status=%d, calls=%d, stderr=%q", result.exitCode, len(result.invocations), result.stderr)
		}
	})
	t.Run("whitespace_selection", func(t *testing.T) {
		result := executePasses(t, script, "PG_MAJORS= ")
		if result.exitCode != 1 || len(result.invocations) != 0 || !strings.Contains(result.stderr, "::error::PG_MAJORS") {
			t.Fatalf("whitespace selection: status=%d, calls=%d, stderr=%q", result.exitCode, len(result.invocations), result.stderr)
		}
	})
}

func assertFile(t *testing.T, dir, name string, want bool) {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, name))
	if got := err == nil; got != want {
		t.Fatalf("%s exists=%t, want %t", name, got, want)
	}
}

func TestPGMajorsReporterDoesNotHideCLIFailure(t *testing.T) {
	_, root := workflowYAML(t)
	jobs := mappingValue(root, "jobs")
	var script string
	for i := 1; i < len(jobs.Content); i += 2 {
		steps := mappingValue(jobs.Content[i], "steps")
		if steps == nil {
			continue
		}
		for _, step := range steps.Content {
			name := mappingValue(step, "name")
			if name != nil && strings.HasPrefix(name.Value, "failure telemetry") {
				script = mappingValue(step, "run").Value
			}
		}
	}
	if script == "" {
		t.Fatal("failure reporter missing")
	}
	if !strings.Contains(script, "--label github_actions") {
		t.Error("reporter must use github_actions, present in both repository label inventories")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' 'fixture gh rejected the request' >&2\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
	cmd.Env = []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "RUN_URL=https://example.invalid/run", "GH_REPO=example/fixture"}
	output, err := cmd.CombinedOutput()
	if err == nil || cmd.ProcessState.ExitCode() != 23 || !strings.Contains(string(output), "fixture gh rejected the request") {
		t.Fatalf("CLI failure or diagnostic was hidden: %v, output=%q", err, output)
	}
}

func TestPGMajorsWritePermissionBelongsOnlyToReporter(t *testing.T) {
	raw, root := workflowYAML(t)
	if err := testJobPermissionError(root); err != nil {
		t.Fatal(err)
	}
	// A per-job override must not restore the privilege removed at workflow level.
	mutated := strings.Replace(string(raw), "  pg-majors:\n", "  pg-majors:\n    permissions:\n      issues: write\n", 1)
	var mutant yaml.Node
	if err := yaml.Unmarshal([]byte(mutated), &mutant); err != nil {
		t.Fatal(err)
	}
	if err := testJobPermissionError(mutant.Content[0]); err == nil {
		t.Error("test-job issue-write override was admitted")
	}
	report := mappingValue(mappingValue(root, "jobs"), "report-failure")
	if report == nil {
		t.Fatal("isolated failure reporter missing")
	}
	if issues := mappingValue(mappingValue(report, "permissions"), "issues"); issues == nil || issues.Value != "write" {
		t.Fatal("reporter lacks issue permission")
	}
	needs := needsJobs(report)
	if !contains(needs, "pg-majors") || !contains(needs, "evaluate") {
		t.Fatalf("reporter needs %v, want the matrix and the evaluation", needs)
	}
	condition := mappingValue(report, "if")
	if condition == nil || !strings.Contains(condition.Value, "always()") ||
		!strings.Contains(condition.Value, "(needs.pg-majors.result == 'failure' || needs.evaluate.result == 'failure')") {
		t.Fatal("failed matrix or evaluation can skip its reporter")
	}
}

// One job cannot run four serial passes: the measured pass alone exceeds the
// job clock. The workflow must therefore be a four-leg matrix whose legs each
// publish their own evidence, plus one job that judges the assembled result.
func TestPGMajorsLegsAreWiredToTheMatrix(t *testing.T) {
	_, root := workflowYAML(t)
	jobs := mappingValue(root, "jobs")
	matrixJob := mappingValue(jobs, "pg-majors")
	if matrixJob == nil {
		t.Fatal("pg-majors job missing")
	}
	strategy := mappingValue(matrixJob, "strategy")
	if strategy == nil {
		t.Fatal("pg-majors is not a matrix: four serial passes do not fit one job clock")
	}
	if failFast := mappingValue(strategy, "fail-fast"); failFast == nil || failFast.Value != "false" {
		t.Fatal("matrix must keep every leg running (fail-fast: false)")
	}
	majors := mappingValue(mappingValue(strategy, "matrix"), "major")
	if majors == nil || majors.Kind != yaml.SequenceNode {
		t.Fatal("matrix.major is not a list of majors")
	}
	var got []string
	for _, item := range majors.Content {
		got = append(got, item.Value)
	}
	if !reflect.DeepEqual(got, []string{"15", "16", "17", "18"}) {
		t.Fatalf("matrix.major = %v, want all four supported majors", got)
	}
	// The guard tests walk the whole matrix inside one test, so every leg still
	// needs all four servers even though it runs only one pass.
	for _, major := range got {
		if mappingValue(mappingValue(matrixJob, "services"), "pg"+major) == nil {
			t.Fatalf("leg for pg%s lacks the pg%s service the guard tests require", major, major)
		}
	}
	passes := stepByID(matrixJob, "passes")
	if passes == nil {
		t.Fatal("passes step missing from the matrix job")
	}
	if selected := mappingValue(mappingValue(passes, "env"), "PG_MAJORS"); selected == nil || selected.Value != "${{ matrix.major }}" {
		t.Fatal("passes step does not select its leg's major through PG_MAJORS")
	}
	if limit := mappingValue(matrixJob, "timeout-minutes"); limit == nil || limit.Value != "350" {
		t.Fatal("pg-majors job ceiling leaves no room for provisioning and upload around a worst-case pass")
	}
	if limit := mappingValue(passes, "timeout-minutes"); limit == nil || limit.Value != "322" {
		t.Fatal("passes step ceiling is not the measured 322 minutes")
	}
	if !strings.Contains(mappingValue(passes, "run").Value, "-timeout 300m") {
		t.Fatal("passes step still caps go test below the measured worst case")
	}
	evaluate := mappingValue(jobs, "evaluate")
	if evaluate == nil {
		t.Fatal("evaluate job missing: the legs publish evidence that must be judged once")
	}
	if !contains(needsJobs(evaluate), "pg-majors") {
		t.Fatal("evaluate does not wait for the matrix")
	}
	assemble := stepByID(evaluate, "assemble")
	if assemble == nil {
		t.Fatal("evaluate has no step that assembles the legs' evidence")
	}
	assembly := mappingValue(assemble, "run")
	if assembly == nil {
		t.Fatal("assembly step has no run script")
	}
	// A failing pass exits zero by design, so the evaluator is the only thing
	// that turns a bad major red: deleting either command must fail a test.
	for _, command := range []string{"env -u GITHUB_STEP_SUMMARY bash scripts/test-pg-majors-evaluate.sh", "python3 scripts/pg-majors-evaluate.py"} {
		if !strings.Contains(assembly.Value, command) {
			t.Fatalf("assembly does not run %q", command)
		}
	}
	// The matrix, the assembly's loop and the evaluator's own constant are one
	// list of majors; drift between them drops a leg's evidence without a word.
	loop := regexp.MustCompile(`for m in ([0-9 ]+); do`).FindStringSubmatch(assembly.Value)
	if loop == nil || strings.Join(strings.Fields(loop[1]), " ") != strings.Join(got, " ") {
		t.Fatalf("assembly majors = %v, want the matrix's %v", loop, got)
	}
	evaluator, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "pg-majors-evaluate.py"))
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`(?m)^MAJORS = \(([^)]*)\)`).FindStringSubmatch(string(evaluator))
	if declared == nil {
		t.Fatal("evaluator declares no MAJORS constant")
	}
	var evaluated []string
	for _, field := range strings.Split(declared[1], ",") {
		evaluated = append(evaluated, strings.Trim(strings.TrimSpace(field), `"`))
	}
	if !reflect.DeepEqual(evaluated, got) {
		t.Fatalf("evaluator majors = %v, want the matrix's %v", evaluated, got)
	}
	if condition := mappingValue(evaluate, "if"); condition == nil ||
		!strings.Contains(condition.Value, "always()") ||
		!strings.Contains(condition.Value, "needs.pg-majors.result == 'success'") ||
		!strings.Contains(condition.Value, "needs.pg-majors.result == 'failure'") {
		t.Fatal("evaluate must judge the assembled evidence on both the success and failure paths")
	}
	if actions := mappingValue(mappingValue(evaluate, "permissions"), "actions"); actions == nil || actions.Value != "read" {
		t.Fatal("evaluate lists same-run artifacts and needs actions: read")
	}
	legName := ""
	for _, step := range mappingValue(matrixJob, "steps").Content {
		if name := mappingValue(mappingValue(step, "with"), "name"); name != nil &&
			strings.HasPrefix(name.Value, "pg-majors-logs-${{ matrix.major }}-") {
			legName = name.Value
		}
	}
	// The assembly globs the attempt so a re-run keeps the untouched legs'
	// evidence; the produced name must keep that prefix in step with it.
	if legName != "pg-majors-logs-${{ matrix.major }}-${{ github.run_id }}-${{ github.run_attempt }}" {
		t.Fatalf("leg artifact name = %q", legName)
	}
	if !strings.Contains(assembly.Value, `legs/pg-majors-logs-${m}-${GITHUB_RUN_ID}-`) {
		t.Fatal("assembly does not look for the directories the leg artifacts produce")
	}
	report := mappingValue(jobs, "report-failure")
	for _, want := range []string{"pg-majors", "evaluate"} {
		if !contains(needsJobs(report), want) {
			t.Fatalf("reporter does not depend on %s", want)
		}
	}
}

// The evaluate job's assembly is new shell: run it end to end on synthetic leg
// artifacts, with the real evaluator and its mutation battery, so a missing,
// duplicated or failing piece of evidence cannot reach a green.
func TestPGMajorsEvaluationAssemblesTheLegs(t *testing.T) {
	_, root := workflowYAML(t)
	script := literalStepScript(t, root, "assemble")
	setup := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		write := func(path, content string) {
			t.Helper()
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("ci/pg-majors-packages.txt", "fixture/first\n")
		write("ci/pg-majors-expectations.json", `{"floors":{"fixture/first":1}}`)
		for _, name := range []string{"pg-majors-evaluate.py", "test-pg-majors-evaluate.sh"} {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", name))
			if err != nil {
				t.Fatal(err)
			}
			write("scripts/"+name, string(raw))
		}
		// Every leg verifies and provisions all four servers, so every leg's
		// receipts file carries four lines; the assembly keeps one per major.
		var receipts strings.Builder
		for _, verified := range []string{"15", "16", "17", "18"} {
			fmt.Fprintf(&receipts,
				"PG_MAJOR_DSN_VERIFIED|major=%s|port=4000%s|server_version_num=%s0000\n",
				verified, verified, verified)
		}
		for _, major := range []string{"15", "16", "17", "18"} {
			leg := "legs/pg-majors-logs-" + major + "-1-1/"
			// Each major's stream carries its own marker, its harness receipt and
			// its terminal package event: four interchangeable fixtures could not
			// tell a mis-paired copy from the right one, and a cut stream would
			// still clear every floor on its surviving prefix.
			write(leg+"gotest-"+major+".json",
				`{"Action":"pass","Package":"github.com/olivaresai/olivares/fixture/first","Test":"TestFixture`+major+`"}`+"\n"+
					`{"Action":"output","Package":"github.com/olivaresai/olivares/fixture/first","Output":"    pgtest.go:220: PG_MAJOR_ASSERTED|expected=`+major+`|connected=`+major+`|server_version_num=`+major+`0000\n"}`+"\n"+
					`{"Action":"pass","Package":"github.com/olivaresai/olivares/fixture/first"}`+"\n")
			write(leg+"pg-majors-exits.txt", major+" 0\n")
			write(leg+"pg-majors-receipts.txt", receipts.String())
		}
		write("passes.sh", script)
		return dir
	}
	run := func(t *testing.T, dir, attempt string) (int, string) {
		t.Helper()
		cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "passes.sh")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GITHUB_RUN_ID=1", "GITHUB_RUN_ATTEMPT="+attempt,
			"GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary.md"))
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			if cmd.ProcessState == nil {
				t.Fatalf("run assembly: %v", err)
			}
			return cmd.ProcessState.ExitCode(), output.String()
		}
		return 0, output.String()
	}
	leg := func(dir, major string) string {
		return filepath.Join(dir, "legs", "pg-majors-logs-"+major+"-1-1")
	}
	writeFile := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("clean_legs", func(t *testing.T) {
		dir := setup(t)
		status, output := run(t, dir, "1")
		if status != 0 {
			t.Fatalf("assembly exit %d: %s", status, output)
		}
		for _, major := range []string{"15", "16", "17", "18"} {
			stream, err := os.ReadFile(filepath.Join(dir, "gotest-"+major+".json"))
			if err != nil {
				t.Fatalf("merged stream for pg%s missing: %v", major, err)
			}
			if !strings.Contains(string(stream), `"Test":"TestFixture`+major+`"`) {
				t.Fatalf("merged stream for pg%s is another major's stream: %s", major, stream)
			}
			for _, other := range []string{"15", "16", "17", "18"} {
				if other != major && strings.Contains(string(stream), `"Test":"TestFixture`+other+`"`) {
					t.Fatalf("merged stream for pg%s carries pg%s's results", major, other)
				}
			}
		}
		if ledger, err := os.ReadFile(filepath.Join(dir, "pg-majors-exits.txt")); err != nil ||
			string(ledger) != "15 0\n16 0\n17 0\n18 0\n" {
			t.Fatalf("merged ledger = %q, error = %v", ledger, err)
		}
		// Four legs each published all four receipts; the merged stream must
		// carry exactly one per major, or the evaluator sees duplicates.
		raw, err := os.ReadFile(filepath.Join(dir, "pg-majors-receipts.txt"))
		if err != nil {
			t.Fatal(err)
		}
		var majors []string
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			majors = append(majors, strings.Split(strings.TrimPrefix(line, "PG_MAJOR_DSN_VERIFIED|major="), "|")[0])
		}
		if !reflect.DeepEqual(majors, []string{"15", "16", "17", "18"}) {
			t.Fatalf("merged receipts = %q, want exactly one per major", raw)
		}
	})
	t.Run("missing_artifact", func(t *testing.T) {
		dir := setup(t)
		if err := os.RemoveAll(leg(dir, "17")); err != nil {
			t.Fatal(err)
		}
		if status, output := run(t, dir, "1"); status != 1 ||
			!strings.Contains(output, "::error::matrix leg pg17 published no artifact") {
			t.Fatalf("missing leg: status=%d, output=%q", status, output)
		}
	})
	t.Run("missing_stream", func(t *testing.T) {
		dir := setup(t)
		if err := os.Remove(filepath.Join(leg(dir, "16"), "gotest-16.json")); err != nil {
			t.Fatal(err)
		}
		if status, output := run(t, dir, "1"); status != 1 ||
			!strings.Contains(output, "::error::matrix leg pg16 published no gotest-16.json") {
			t.Fatalf("missing stream: status=%d, output=%q", status, output)
		}
	})
	t.Run("missing_exit_status", func(t *testing.T) {
		dir := setup(t)
		writeFile(t, filepath.Join(leg(dir, "16"), "pg-majors-exits.txt"), "")
		if status, output := run(t, dir, "1"); status != 1 ||
			!strings.Contains(output, "::error::matrix leg pg16 did not publish exactly one exit status for pg16") {
			t.Fatalf("missing exit: status=%d, output=%q", status, output)
		}
	})
	// Two statuses for one major would let the evaluator's dict keep whichever
	// line came last: a failed pass could be laundered by a later zero.
	t.Run("duplicate_exit_status", func(t *testing.T) {
		dir := setup(t)
		writeFile(t, filepath.Join(leg(dir, "17"), "pg-majors-exits.txt"), "17 1\n17 0\n")
		if status, output := run(t, dir, "1"); status != 1 ||
			!strings.Contains(output, "::error::matrix leg pg17 did not publish exactly one exit status for pg17") {
			t.Fatalf("duplicate exit: status=%d, output=%q", status, output)
		}
	})
	t.Run("duplicate_receipt", func(t *testing.T) {
		dir := setup(t)
		path := filepath.Join(leg(dir, "15"), "pg-majors-receipts.txt")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(raw)+string(raw))
		if status, output := run(t, dir, "1"); status != 1 ||
			!strings.Contains(output, "::error::matrix leg pg15 did not verify pg15 exactly once") {
			t.Fatalf("duplicate receipt: status=%d, output=%q", status, output)
		}
	})
	t.Run("failing_leg_stays_red", func(t *testing.T) {
		dir := setup(t)
		writeFile(t, filepath.Join(leg(dir, "17"), "pg-majors-exits.txt"), "17 1\n")
		if status, output := run(t, dir, "1"); status != 1 || !strings.Contains(output, "pass pg17: go test exit 1") {
			t.Fatalf("failing leg laundered: status=%d, output=%q", status, output)
		}
	})
	// "Re-run failed jobs" starts evaluate at a later attempt while the legs
	// that succeeded keep the attempt that produced their evidence.
	t.Run("rerun_after_the_legs", func(t *testing.T) {
		dir := setup(t)
		status, output := run(t, dir, "2")
		if status != 0 {
			t.Fatalf("evaluate rerun refused the legs' attempt-1 evidence: exit %d: %s", status, output)
		}
		if _, err := os.Stat(filepath.Join(dir, "gotest-18.json")); err != nil {
			t.Fatalf("rerun assembled no stream: %v", err)
		}
	})
	t.Run("newest_attempt_wins", func(t *testing.T) {
		dir := setup(t)
		fresh := filepath.Join(dir, "legs", "pg-majors-logs-17-1-2")
		if err := os.MkdirAll(fresh, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"gotest-17.json", "pg-majors-receipts.txt"} {
			raw, err := os.ReadFile(filepath.Join(leg(dir, "17"), name))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(fresh, name), string(raw))
		}
		writeFile(t, filepath.Join(fresh, "pg-majors-exits.txt"), "17 1\n")
		if status, output := run(t, dir, "2"); status != 1 ||
			!strings.Contains(output, "pass pg17: go test exit 1") {
			t.Fatalf("stale attempt judged instead of the rerun: status=%d, output=%q", status, output)
		}
	})
}

func testJobPermissionError(root *yaml.Node) error {
	writes := func(permissions *yaml.Node) bool {
		if permissions == nil {
			return false
		}
		if permissions.Kind == yaml.ScalarNode {
			return permissions.Value == "write-all"
		}
		for i := 1; i < len(permissions.Content); i += 2 {
			if permissions.Content[i].Value == "write" {
				return true
			}
		}
		return false
	}
	if writes(mappingValue(root, "permissions")) {
		return fmt.Errorf("workflow grants write access to test jobs")
	}
	jobs := mappingValue(root, "jobs")
	for i := 0; i < len(jobs.Content); i += 2 {
		if jobs.Content[i].Value != "report-failure" && writes(mappingValue(jobs.Content[i+1], "permissions")) {
			return fmt.Errorf("test job %s grants write access", jobs.Content[i].Value)
		}
	}
	return nil
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
			name: "missing_other_cluster_dsn",
			old:  "OLIVARES_TEST_POSTGRES_OTHER_DSN=\"postgres://postgres:postgres@127.0.0.1:${other_port}/postgres?sslmode=disable\" \\\n",
			want: "invocation 1 variable OLIVARES_TEST_POSTGRES_OTHER_DSN",
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
	raw, _ := workflowYAML(t)
	script, err := passesScript(raw)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func workflowYAML(t *testing.T) ([]byte, *yaml.Node) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "workflows", "pg-majors.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc.Content[0]
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
	if node != nil && node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1]
			}
		}
	}
	return nil
}

func stepByID(job *yaml.Node, id string) *yaml.Node {
	steps := mappingValue(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return nil
	}
	for _, step := range steps.Content {
		if got := mappingValue(step, "id"); got != nil && got.Value == id {
			return step
		}
	}
	return nil
}

// literalStepScript returns a step's run script, refusing one that still needs
// GitHub to expand an expression: the sandbox cannot, and neither can a reader.
func literalStepScript(t *testing.T, root *yaml.Node, id string) string {
	t.Helper()
	step := stepByID(mappingValue(mappingValue(root, "jobs"), "evaluate"), id)
	if step == nil {
		t.Fatalf("no evaluate step with id %q", id)
	}
	run := mappingValue(step, "run")
	if run == nil || run.Kind != yaml.ScalarNode {
		t.Fatalf("step %q has no literal run script", id)
	}
	if strings.Contains(run.Value, "${{") {
		t.Fatalf("step %q run script is not a local Bash script", id)
	}
	return run.Value
}

// needs accepts one job name or a list of them; both forms are valid YAML here.
func needsJobs(job *yaml.Node) []string {
	needs := mappingValue(job, "needs")
	if needs == nil {
		return nil
	}
	if needs.Kind == yaml.ScalarNode {
		return []string{needs.Value}
	}
	var jobs []string
	for _, item := range needs.Content {
		jobs = append(jobs, item.Value)
	}
	return jobs
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type passResult struct {
	stdout, stderr, ledger, dir string
	invocations                 [][]string
	exitCode                    int
}

// The PATH exposes only the script's local utilities and a recording substitute for go.
func executePasses(t *testing.T, script string, extraEnv ...string) passResult {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, path := range []string{bin, filepath.Join(dir, "ci"), filepath.Join(dir, "calls"), filepath.Join(dir, "scripts")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env", "grep", "sed", "tee", "cat", "python3"} {
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
	write("ci/pg-majors-expectations.json", `{"floors":{"fixture/first":1,"fixture/second":1}}`, 0o600)
	evaluator, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "pg-majors-evaluate.py"))
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/pg-majors-evaluate.py", string(evaluator), 0o600)
	write("passes.sh", script, 0o600)
	write("bin/go", "#!"+bash+"\n"+goSubstitute, 0o700)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-eo", "pipefail", "passes.sh")
	cmd.Dir = dir
	cmd.Env = append([]string{
		"PATH=" + bin, "HOME=" + dir, "LC_ALL=C",
		"PGPORT_15=41515", "PGPORT_16=41616", "PGPORT_17=41717", "PGPORT_18=41818",
		"PG_M1_CALLS=" + filepath.Join(dir, "calls"), "PG_M1_UNEXPECTED=" + unexpectedMarker,
	}, extraEnv...)
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
	result := passResult{stdout: stdout.String(), stderr: stderr.String(), dir: dir}
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
	// A script that refuses its input before opening the ledger leaves no file;
	// the refusal is asserted through exit status and diagnostics.
	if ledger, err := os.ReadFile(filepath.Join(dir, "pg-majors-exits.txt")); err == nil {
		result.ledger = string(ledger)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return result
}

func checkPasses(result passResult) error {
	if result.exitCode != 0 {
		return fmt.Errorf("passes script exit %d", result.exitCode)
	}
	if len(result.invocations) != 4 {
		return fmt.Errorf("got %d invocations, want four", len(result.invocations))
	}
	for i, fields := range result.invocations {
		if err := checkInvocation(i, fields, 15+i); err != nil {
			return err
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

func checkInvocation(i int, fields []string, major int) error {
	majorMap := "15=postgres://postgres:postgres@127.0.0.1:41515/postgres?sslmode=disable," +
		"16=postgres://postgres:postgres@127.0.0.1:41616/postgres?sslmode=disable," +
		"17=postgres://postgres:postgres@127.0.0.1:41717/postgres?sslmode=disable," +
		"18=postgres://postgres:postgres@127.0.0.1:41818/postgres?sslmode=disable"
	names := []string{"OLIVARES_TEST_POSTGRES_DSN", "OLIVARES_TEST_POSTGRES_ADMIN_DSN", "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN",
		"OLIVARES_TEST_VECTOR_DSN", "OLIVARES_TEST_POSTGRES_REQUIRED", "OLIVARES_TEST_PG_EXPECT_MAJOR", "OLIVARES_TEST_POSTGRES_MAJOR_DSNS",
		"OLIVARES_TEST_POSTGRES_OTHER_DSN", "OLIVARES_TEST_POSTGRES_OTHER_EXPECT_MAJOR"}
	// 300m matches the measured worst case of this suite on this host and the
	// ceiling main-suite.yml already budgets; 45m is what cut every pass.
	args := []string{"test", "-json", "-skip", "^$", "-count=1", "-timeout", "300m",
		"github.com/olivaresai/olivares/fixture/first", "github.com/olivaresai/olivares/fixture/second"}
	if len(fields) < len(names) {
		return fmt.Errorf("invocation %d has an incomplete receipt", i+1)
	}
	port := fmt.Sprintf("4%d%d", major, major)
	app := "postgres://olivares_app:apppw@127.0.0.1:" + port + "/olivares?sslmode=disable"
	want := []string{app, "postgres://olivares_admin:adminpw@127.0.0.1:" + port + "/olivares?sslmode=disable",
		"postgres://postgres:postgres@127.0.0.1:" + port + "/postgres?sslmode=disable", app, "1", fmt.Sprint(major), majorMap}
	other := 15 + (major-15+1)%4
	want = append(want, fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:4%d%d/postgres?sslmode=disable", other, other), fmt.Sprint(other))
	for j, name := range names {
		if fields[j] != want[j] {
			return fmt.Errorf("invocation %d variable %s differs from the synthetic input", i+1, name)
		}
	}
	if !reflect.DeepEqual(fields[len(names):], args) {
		return fmt.Errorf("invocation %d arguments differ from the exact test command", i+1)
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
  printf '%s\0' "${OLIVARES_TEST_POSTGRES_OTHER_DSN-<absent>}"
  printf '%s\0' "${OLIVARES_TEST_POSTGRES_OTHER_EXPECT_MAJOR-<absent>}"
  printf '%s\0' "$@"
} > "$PG_M1_CALLS/call-$count"
case "$count" in
  1|4) exit 0 ;;
  2) exit 7 ;;
  3) exit 19 ;;
  *) exit 29 ;;
esac
`
