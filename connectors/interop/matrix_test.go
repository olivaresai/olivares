// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"bufio"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestMatrixWellFormed validates the declared matrix's structure and its policy.
func TestMatrixWellFormed(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("load matrix: %v", err)
	}
	if strings.TrimSpace(m.Note) == "" {
		t.Error("matrix note (the 'counts are not a compatibility claim' statement) must not be empty")
	}
	if m.Policy.DeprecationSLADays <= 0 {
		t.Errorf("policy.deprecation_sla_days must be positive, got %d", m.Policy.DeprecationSLADays)
	}
	if strings.TrimSpace(m.Policy.Breakage) == "" {
		t.Error("policy.breakage must describe the downgrade/deprecation posture")
	}
	for _, level := range []string{VerificationFixtureOnly, VerificationConformanceDefined, VerificationContinuouslyVerified} {
		if strings.TrimSpace(m.Policy.Badges[level]) == "" {
			t.Errorf("policy.badges must define %q", level)
		}
	}
	if len(m.Entries) == 0 {
		t.Fatal("matrix has no entries")
	}
}

// TestMatrixEntriesHonest is the load-bearing gate: every entry names a real connector,
// carries a known verification level, and — the honesty invariant — a job-backed badge
// (conformance-defined / continuously-verified) is bound to an ACTUAL runnable conformance
// job: a real Test function, in an integration-ONLY test file, whose name the entry's
// advertised `-run` target matches. A count is never evidence; a badge above fixture-only
// demands a job that genuinely exists and genuinely runs only under the integration tag.
func TestMatrixEntriesHonest(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("load matrix: %v", err)
	}
	known := map[string]bool{
		VerificationFixtureOnly:          true,
		VerificationConformanceDefined:   true,
		VerificationContinuouslyVerified: true,
	}
	now := time.Now()
	seen := map[string]bool{}
	for _, e := range m.Entries {
		if e.Connector == "" {
			t.Error("entry with empty connector name")
			continue
		}
		if seen[e.Connector] {
			t.Errorf("duplicate matrix entry for connector %q", e.Connector)
		}
		seen[e.Connector] = true

		for field, val := range map[string]string{"surface": e.Surface, "vendor": e.Vendor, "api_version": e.APIVersion, "tier": e.Tier} {
			if strings.TrimSpace(val) == "" {
				t.Errorf("%s: %s must not be empty", e.Connector, field)
			}
		}
		if !known[e.Verification] {
			t.Errorf("%s: unknown verification level %q", e.Connector, e.Verification)
			continue
		}
		dir := filepath.Join("..", e.Connector)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Errorf("%s: connector directory %s does not exist", e.Connector, dir)
			continue
		}

		// The set of Test functions defined in this connector's INTEGRATION-ONLY test
		// files (files whose build constraint is satisfied by the integration tag and
		// NOT without it — a real per-release conformance job, not a default unit test).
		jobTests := integrationOnlyTestFuncs(t, e.Connector)

		if RequiresConformanceJob(e.Verification) {
			if e.Conformance == nil || strings.TrimSpace(e.Conformance.Job) == "" || len(e.Conformance.Env) == 0 {
				t.Errorf("%s: verification %q requires a conformance block with a job and its endpoint env var(s)", e.Connector, e.Verification)
			}
			// Honesty invariant: the badge must be bound to an ACTUAL conformance job — a
			// real integration-only test following the TestConformance… convention, that
			// the advertised `-run` selects. This defeats a tagged-but-empty file, a
			// renamed/deleted test, AND a catch-all `-run .` that only latches onto some
			// unrelated integration test.
			conformanceTests := withPrefix(jobTests, "TestConformance")
			if len(conformanceTests) == 0 {
				t.Errorf("%s: verification %q claims a live qualification job but connectors/%s has NO TestConformance… function in an integration-only file — a badge above fixture-only requires a real, integration-tagged conformance test", e.Connector, e.Verification, e.Connector)
			} else if e.Conformance != nil {
				if run := runTarget(e.Conformance.Job); run == "" {
					t.Errorf("%s: conformance.job must carry a `-run <TestName>` target so the badge is bound to a specific job (got %q)", e.Connector, e.Conformance.Job)
				} else if !matchesAnyTest(run, conformanceTests) {
					t.Errorf("%s: conformance.job -run %q selects none of the connector's TestConformance… tests %v — bind the badge to the actual conformance job", e.Connector, run, conformanceTests)
				}
			}
		}
		if e.Verification == VerificationContinuouslyVerified && e.LastVerified == nil {
			t.Errorf("%s: continuously-verified requires a last_verified date (an actual run)", e.Connector)
		}
		if e.LastVerified != nil {
			d, perr := time.Parse("2006-01-02", *e.LastVerified)
			switch {
			case perr != nil:
				t.Errorf("%s: last_verified %q is not a YYYY-MM-DD date: %v", e.Connector, *e.LastVerified, perr)
			case d.After(now):
				t.Errorf("%s: last_verified %q is in the FUTURE — a run cannot have happened yet", e.Connector, *e.LastVerified)
			case e.Verification == VerificationContinuouslyVerified && now.Sub(d) > time.Duration(m.Policy.DeprecationSLADays)*24*time.Hour:
				// A continuously-verified badge whose last real run is older than the SLA
				// has rotted: downgrade it or re-run the job. This makes the gate
				// deliberately time-dependent for that badge (the honest semantics).
				t.Errorf("%s: continuously-verified but last_verified %q is older than the %d-day SLA — re-run the job or downgrade the badge", e.Connector, *e.LastVerified, m.Policy.DeprecationSLADays)
			}
		}
	}
}

// integrationOnlyTestFuncs returns the names of the Test functions declared in
// connectors/<name>/*_test.go files whose build constraint makes them INTEGRATION-ONLY:
// satisfied when the `integration` tag is set and unsatisfied when it is not. This
// rejects a default unit test (no constraint), a negated constraint (`!integration`) and
// a compound constraint the per-release `-tags integration` run can never satisfy
// (`extra && integration`) — none of those is a runnable conformance job.
func integrationOnlyTestFuncs(t *testing.T, connector string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", connector, "*_test.go"))
	if err != nil {
		t.Fatalf("glob %s test files: %v", connector, err)
	}
	var names []string
	for _, f := range matches {
		if !fileIsIntegrationOnly(t, f) {
			continue
		}
		names = append(names, testFuncNames(t, f)...)
	}
	return names
}

// fileIsIntegrationOnly parses the file's build constraint and reports whether it builds
// under the integration tag but not without it.
func fileIsIntegrationOnly(t *testing.T, path string) bool {
	t.Helper()
	expr, ok := buildConstraint(t, path)
	if !ok {
		return false // no constraint → a default-build unit test, never a conformance job
	}
	withIntegration := expr.Eval(func(tag string) bool { return tag == "integration" })
	// "builds without the integration tag": model every OTHER tag as PRESENT (the ambient
	// GOOS/GOARCH/unix/go1.x tags a default `go test` build carries), so a disjunction like
	// `integration || linux` is correctly seen as NOT integration-only — it also builds in
	// the default gate on that platform. All-false would under-approximate and accept it.
	buildsWithoutIntegration := expr.Eval(func(tag string) bool { return tag != "integration" })
	return withIntegration && !buildsWithoutIntegration
}

// buildConstraint returns the parsed //go:build (or legacy // +build) constraint from a
// file's leading comment block, if any.
func buildConstraint(t *testing.T, path string) (constraint.Expr, bool) {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = fh.Close() }()
	sc := bufio.NewScanner(fh)
	var plusBuild constraint.Expr // legacy // +build lines are AND'd together
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			break // constraints must precede the package clause
		}
		if constraint.IsGoBuild(line) {
			// A //go:build line is the single authoritative constraint (it wins over any
			// legacy // +build lines in the same file).
			if expr, err := constraint.Parse(line); err == nil {
				return expr, true
			}
		}
		if constraint.IsPlusBuild(line) {
			if expr, err := constraint.Parse(line); err == nil {
				if plusBuild == nil {
					plusBuild = expr
				} else {
					plusBuild = &constraint.AndExpr{X: plusBuild, Y: expr}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	if plusBuild != nil {
		return plusBuild, true
	}
	return nil, false
}

// testFuncNames parses a Go file and returns its top-level `func TestXxx(*testing.T)`
// names — the functions `go test` would actually run.
func testFuncNames(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil {
			continue
		}
		name := fn.Name.Name
		if !strings.HasPrefix(name, "Test") || !isTestName(name) {
			continue
		}
		if !isTestSignature(fn) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// isTestName enforces Go's test-naming rule: "Test" followed by end-of-name or a
// non-lowercase rune (so a helper like "Testing" or "TestingHelper" is not a test).
func isTestName(name string) bool {
	rest := strings.TrimPrefix(name, "Test")
	if rest == "" {
		return true
	}
	r := rune(rest[0])
	return !(r >= 'a' && r <= 'z')
}

// isTestSignature reports whether the function has EXACTLY Go's runnable test signature
// `func Test…(t *testing.T)` — one parameter of type *testing.T (at most one name) and no
// results. This matches cmd/go's own rule, so a `func TestX(t *testing.T, extra int)` or a
// `func TestX() error` (which `go test` rejects as a wrong-signature test) is not counted.
func isTestSignature(fn *ast.FuncDecl) bool {
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		return false
	}
	p := fn.Type.Params
	if p == nil || len(p.List) != 1 || len(p.List[0].Names) > 1 {
		return false
	}
	star, ok := p.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
}

var runFlag = regexp.MustCompile(`-run[\s=]+(\S+)`)

// runTarget extracts the `-run <pattern>` argument from a conformance job command.
func runTarget(job string) string {
	m := runFlag.FindStringSubmatch(job)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// withPrefix returns the names having the given prefix (the TestConformance… convention
// that binds a badge to a real qualification job, not any incidental integration test).
func withPrefix(names []string, prefix string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// matchesAnyTest reports whether the `-run` pattern (a Go test regexp) matches at least
// one of the discovered test names — i.e. the advertised job would actually select a test.
func matchesAnyTest(run string, tests []string) bool {
	re, err := regexp.Compile(run)
	if err != nil {
		return false
	}
	for _, name := range tests {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}
