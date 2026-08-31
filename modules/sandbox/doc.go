// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sandbox is module XVII — testing-sandbox: ISOLATED, ephemeral execution
// of agent scenarios against mocked MCPs/resources, plus deterministic replay of a
// historical session and pre/post-deploy comparison of two variants. It is the
// sibling of module XII (evals): XVII EXECUTES in isolation and produces outputs;
// XII MEASURES quality. The two are decoupled — neither imports the other; the
// sandbox defines the scoring it needs as its OWN port and the composition root
// injects an evals-backed adapter.
//
// # What it is
//
//   - A catalog of SCENARIOS (sandbox_scenario): a sequence of step inputs plus
//     the mocked responses of the MCPs/resources the run is allowed to touch. No
//     secrets, no production handles — a scenario is a synthetic, operator-authored
//     fixture (clamped before persistence).
//   - A RUN (sandbox_run, mutable: running→terminal) recording WHICH runner ran it,
//     whether that runner is isolated, whether the ephemeral state was destroyed,
//     the per-step output counts and (if scored) the suite/score/passed verdict.
//   - Per-step OUTPUTS (sandbox_output, append-only evidence) and pre/post-deploy
//     COMPARISONS (sandbox_comparison, append-only decision evidence).
//
// # The isolation guarantee (docs/contracts/honest v1)
//
// The default runner (ports.go inprocMockRunner) is isolated BY CONSTRUCTION: its
// Run receives only a RunSpec{Steps, Mocks}; the type holds no handle to the store,
// the network or any secret. A step that asks for a resource absent from Mocks
// yields a DETERMINISTIC mock-miss marker ("[[mock-miss:<input>]]") — it NEVER
// reaches a real resource. It is ephemeral: state lives in the call and is discarded
// on return, so every run records destroyed=true. OS-level isolation (a hardened
// ephemeral container + seccomp/AppArmor, a Firecracker/E2B/gVisor microVM, ARCHITECTURE.md
// §7 · README.md) is a pluggable backend BEHIND the same Runner interface, wired by
// the composition root — never a faked microVM spawn. Each run records the REAL
// runner name and isolated flag, so a degraded/portable backend is visible and
// auditable, not hidden.
//
// # Honest defaults (fail-closed)
//
//   - No Scorer wired ⇒ a run with a suite_ref is recorded "executed, not scored"
//     (a status note), NEVER a silent pass.
//   - HistorySource cannot reconstruct an ordered timeline ⇒ replay is reported
//     DEGRADED with zero steps, never fabricated.
//   - SyntheticDataGenerator is a POST-v1 EXTENSION POINT (README.mdbis · §6): the
//     module ships NO generator; the default produces ZERO samples and an explicit
//     error. There is no route and no option that generates synthetic data.
//
// The module emits outputs/comparisons other sessions consume: (deploy gating)
// reads the pre/post verdict, XII (evals) scores the outputs via the composition
// root's adapter paints the runs/outputs. It re-implements none of them, and in
// v1 exports nothing a redteam.Sandbox adapter could use: the in-proc-mock
// runner is synthetic-only and cannot reach a real target over the network.
package sandbox
