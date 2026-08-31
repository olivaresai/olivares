// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package evals is module XII — quality measurement: it SCORES candidate outputs
// against versioned golden suites and turns the result into the canonical,
// cross-module evidence other sessions consume.
//
// # What it is
//
//   - A versioned golden dataset: a SUITE (suite.go) holds its cases (an immutable
//     input→expected/criterion record per version — correcting a case is a new
//     suite_version, never an in-place edit), a default scorer, a pass threshold
//     and a regression threshold.
//   - A pluggable SCORER registry (scorers.go): deterministic built-ins (exact,
//     contains, not_contains, regex, json_valid, json_equal, numeric_range) plus
//     an llm_judge scorer that invokes the Judge port. A scorer is pure; the judge
//     is the only model invocation evals makes.
//   - A synchronous RUN executor (runs.go): given a suite, its cases and a map of
//     candidate outputs it scores each case, aggregates a score + pass_rate, and
//     persists append-only per-case evidence, a mutable run aggregate, and ONE core
//     EvalResult — the canonical artifact read without knowing evals' own
//     tables. A regression vs a baseline becomes a core Finding and a bus signal.
//
// # The minimal-data invariant (docs/SECURITY-HARDENING.md, non-negotiable)
//
// XII MEASURES; it does not run the subject (the only model it invokes is the
// Judge for llm_judge). A candidate output — from a sandbox, a CI, an inline
// request, or a real-session sample — is NEVER persisted: a case result stores
// only a one-way detail hash and a clamped+scrubbed label. The monitor scores
// behavioral SIGNALS of a session (state, finding count, severity, tokens/cost),
// never its raw output text, which the platform never persists.
//
// # Ports, fail-closed
//
// The seams XII declares are its own (ports.go): Judge (model invocation for
// llm_judge) defaults to an offline judge so an un-wired judge yields a SKIPPED
// score, never a silent pass; SessionSource (real-session sampling) defaults to a
// reader over CORE Session+Finding signals. The composition root injects the real
// adapters (for the judge, the module-II timeline for richer samples). XII
// never imports its sibling module XVII (sandbox): the sandbox calls the public
// ScoreOutputs method on this concrete type; the adapter lives in the boot root.
//
// The module emits EvalResults/Findings other sessions consume: (sandbox env) (compliance evidence) (regression delivery) (deploy verdict) (the UI). It re-implements none of them.
package evals
