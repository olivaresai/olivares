// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// This file is the SCORER registry: a pluggable map[string]Scorer of pure
// deterministic built-ins plus the llm_judge scorer that invokes the Judge port. A
// scorer maps a candidate output (+ the case's expected/criterion) to a 0..1 score,
// a pass flag and an outcome (pass|fail|error|skipped). The raw output never leaves
// this layer — only the outcome + a short reason do.

// The scorer outcomes (contract §2.2). pass/fail are scored (counted in the
// denominator); error is an execution fault; skipped means it was not executed (no
// judge wired) and is EXCLUDED from the denominator, exactly like redteam.
const (
	outcomePass    = "pass"
	outcomeFail    = "fail"
	outcomeError   = "error"
	outcomeSkipped = "skipped"
)

// The built-in deterministic scorer ids + the llm_judge id.
const (
	scorerExact        = "exact"
	scorerContains     = "contains"
	scorerNotContains  = "not_contains"
	scorerRegex        = "regex"
	scorerJSONValid    = "json_valid"
	scorerJSONEqual    = "json_equal"
	scorerNumericRange = "numeric_range"
	scorerLLMJudge     = "llm_judge"
)

// ScoreInput is what a scorer is given for one case. Config carries scorer-specific
// knobs from the case metadata (kept open for pluggable scorers). Input is the
// case's original input: deterministic scorers ignore it, the llm_judge passes it
// to the judge as grading context (the connector rubric's optional INPUT; it is
// also part of the gate's verdict-cache key).
type ScoreInput struct {
	Tenant    model.TenantID
	Input     string
	Output    string
	Expected  string
	Criterion string
	ModelRef  string
	Config    map[string]any
}

// ScoreResult is one scorer's judgement of one output.
type ScoreResult struct {
	Score   float64
	Passed  bool
	Outcome string // pass|fail|error|skipped
	Reason  string
}

// Scorer maps an output to a result. Built-ins are pure and deterministic; llm_judge
// is the one scorer that invokes a model (the Judge port).
type Scorer interface {
	ID() string
	Score(ctx context.Context, in ScoreInput) ScoreResult
}

// registerBuiltins installs the deterministic scorers and the llm_judge scorer. They
// are registered before the With* options so WithScorer can override one by id.
func (m *Module) registerBuiltins() {
	for _, s := range []Scorer{
		fnScorer{id: scorerExact, fn: scoreExact},
		fnScorer{id: scorerContains, fn: scoreContains},
		fnScorer{id: scorerNotContains, fn: scoreNotContains},
		fnScorer{id: scorerRegex, fn: scoreRegex},
		fnScorer{id: scorerJSONValid, fn: scoreJSONValid},
		fnScorer{id: scorerJSONEqual, fn: scoreJSONEqual},
		fnScorer{id: scorerNumericRange, fn: scoreNumericRange},
	} {
		m.scorers[s.ID()] = s
	}
	m.scorers[scorerLLMJudge] = &judgeScorer{judge: func() Judge { return m.judge }}
}

// scorerByID returns the registered scorer, or a notFound scorer that yields an
// error outcome (never a silent pass) for an unknown id.
func (m *Module) scorerByID(id string) Scorer {
	if s, ok := m.scorers[id]; ok {
		return s
	}
	return notFoundScorer{id: id}
}

// fnScorer adapts a pure function to the Scorer interface.
type fnScorer struct {
	id string
	fn func(in ScoreInput) ScoreResult
}

func (s fnScorer) ID() string                                         { return s.id }
func (s fnScorer) Score(_ context.Context, in ScoreInput) ScoreResult { return s.fn(in) }

// notFoundScorer is returned for an unknown scorer id: every case errors with a clear
// reason, so a misconfigured suite degrades honestly rather than scoring a pass.
type notFoundScorer struct{ id string }

func (s notFoundScorer) ID() string { return s.id }
func (notFoundScorer) Score(_ context.Context, _ ScoreInput) ScoreResult {
	return ScoreResult{Outcome: outcomeError, Reason: "unknown scorer"}
}

// ---- pass/fail helpers -----------------------------------------------------------

func pass(reason string) ScoreResult {
	return ScoreResult{Score: 1.0, Passed: true, Outcome: outcomePass, Reason: reason}
}

func fail(reason string) ScoreResult {
	return ScoreResult{Score: 0.0, Passed: false, Outcome: outcomeFail, Reason: reason}
}

func scoreErr(reason string) ScoreResult {
	return ScoreResult{Outcome: outcomeError, Reason: reason}
}

// ---- deterministic built-ins -----------------------------------------------------

// scoreExact passes when the output equals the expected reference (trimmed).
func scoreExact(in ScoreInput) ScoreResult {
	if strings.TrimSpace(in.Output) == strings.TrimSpace(in.Expected) {
		return pass("output matched expected exactly")
	}
	return fail("output did not match expected")
}

// scoreContains passes when the output contains the expected substring.
func scoreContains(in ScoreInput) ScoreResult {
	if in.Expected == "" {
		return scoreErr("contains requires a non-empty expected substring")
	}
	if strings.Contains(in.Output, in.Expected) {
		return pass("output contained the expected substring")
	}
	return fail("output did not contain the expected substring")
}

// scoreNotContains passes when the output does NOT contain the forbidden substring.
func scoreNotContains(in ScoreInput) ScoreResult {
	if in.Expected == "" {
		return scoreErr("not_contains requires a non-empty forbidden substring")
	}
	if strings.Contains(in.Output, in.Expected) {
		return fail("output contained the forbidden substring")
	}
	return pass("output did not contain the forbidden substring")
}

// scoreRegex passes when the output matches the expected regular expression. A bad
// pattern is an error outcome (never a silent pass).
func scoreRegex(in ScoreInput) ScoreResult {
	re, err := regexp.Compile(in.Expected)
	if err != nil {
		return scoreErr("invalid regex pattern")
	}
	if re.MatchString(in.Output) {
		return pass("output matched the pattern")
	}
	return fail("output did not match the pattern")
}

// scoreJSONValid passes when the output parses as JSON.
func scoreJSONValid(in ScoreInput) ScoreResult {
	if json.Valid([]byte(in.Output)) {
		return pass("output is valid JSON")
	}
	return fail("output is not valid JSON")
}

// scoreJSONEqual passes when output and expected parse to deeply-equal normalized
// values (semantic JSON equality — key order / whitespace insensitive). Either side
// failing to parse is an error outcome.
func scoreJSONEqual(in ScoreInput) ScoreResult {
	var a, b any
	if err := json.Unmarshal([]byte(in.Output), &a); err != nil {
		return scoreErr("output is not valid JSON")
	}
	if err := json.Unmarshal([]byte(in.Expected), &b); err != nil {
		return scoreErr("expected is not valid JSON")
	}
	if reflect.DeepEqual(a, b) {
		return pass("output is semantically equal to expected JSON")
	}
	return fail("output differs from expected JSON")
}

// scoreNumericRange passes when the output parses as a number within the inclusive
// "lo..hi" range carried in expected. A malformed range or non-numeric output is an
// error outcome.
func scoreNumericRange(in ScoreInput) ScoreResult {
	lo, hi, ok := parseRange(in.Expected)
	if !ok {
		return scoreErr("expected must be a numeric range \"lo..hi\"")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(in.Output), 64)
	if err != nil {
		return scoreErr("output is not a number")
	}
	if v >= lo && v <= hi {
		return pass("output is within the numeric range")
	}
	return fail("output is outside the numeric range")
}

// parseRange parses a "lo..hi" inclusive range.
func parseRange(s string) (lo, hi float64, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(s), "..", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	l, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	h, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil || l > h {
		return 0, 0, false
	}
	return l, h, true
}

// ---- llm_judge -------------------------------------------------------------------

// judgeScorer invokes a Judge resolved at Score time via the getter (so a late
// WithJudge still takes effect, and the gate can substitute a CACHED judge over the
// same scorer): an offline judge → SKIPPED (degraded, never a silent pass); a judge
// error → ERROR; otherwise it maps the verdict.
type judgeScorer struct{ judge func() Judge }

func (s *judgeScorer) ID() string { return scorerLLMJudge }

func (s *judgeScorer) Score(ctx context.Context, in ScoreInput) ScoreResult {
	verdict, err := s.judge().Judge(ctx, in.Tenant, JudgeRequest{
		ModelRef:  in.ModelRef,
		Input:     in.Input,
		Output:    in.Output,
		Expected:  in.Expected,
		Criterion: in.Criterion,
	})
	switch {
	case errors.Is(err, errNoJudge):
		return ScoreResult{Outcome: outcomeSkipped, Reason: "no judge wired — llm_judge skipped"}
	case err != nil:
		return scoreErr(clamp(err.Error(), maxLabelLen))
	}
	outcome := outcomeFail
	if verdict.Passed {
		outcome = outcomePass
	}
	score := verdict.Score
	if score < 0 {
		score = 0
	} else if score > 1 {
		score = 1
	}
	return ScoreResult{Score: score, Passed: verdict.Passed, Outcome: outcome, Reason: clamp(verdict.Reason, maxLabelLen)}
}
