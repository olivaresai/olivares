<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Evaluation methodology — judge calibration, bias, intervals and regression gate

How module XII (`modules/evals`) measures quality and why its numbers are defensible before an
ML/research buyer. Guiding principle: **a number is either measured or it is not
reported** — an agreement is never fabricated, a degenerate statistic is never disguised as a zero, and
every degradation is declared, never silent.

## 0. What we claim (and what we don't)

| We claim | We don't claim |
|---|---|
| The LLM-judge is **measured against a human reference** before its verdicts block anything | That the judge "is good" without a calibration report to back it |
| Agreement **with a Wilson interval** + **Cohen's kappa** + sensitivity/specificity, all with their _n_ | A bare percentage with no denominator and no uncertainty |
| Bias mitigation **implemented and measured** (order/position, verbosity, CoT) | That bias "does not exist": the order-swap turns it into a visible number (`position_consistency`) |
| A **blocking** regression gate with a **governed and audited** escape | A gate that can be bypassed by lowering the bar per request (the floors are clamped upward) |
| Cross-validation with a public benchmark **not applicable today** (verified, §7) | Having validated against a domain benchmark that does not exist |

## 1. Measurement architecture (summary)

- **Pointwise** (`llm_judge`): the scorer invokes the `Judge` port per case; the connector
  (`connectors/claude-api/judge.go`) forces the reasoning BEFORE the verdict and controls
  verbosity (§3). The verdict is `{score∈[0,1], passed, reason}`; the evaluated text is **never**
  persisted (hash + trimmed label, docs/SECURITY-HARDENING.md).
- **Pairwise** (A/B with `"pairwise": true`): the `PairJudge` port compares two outputs with
  **order-swapping** (§3.1).
- **Calibration** (`POST /v1/m/evals/calibration/run`): measures the judge against human-labeled
  items (§2). Without a current report meeting the target, the gate **does not trust** the judge (§5).
- All judge calls happen **outside the write transaction** (two-phase: read →
  judge → persist) and emit their real runtime cost/forensics.

## 2. Judge↔human calibration

**Protocol**:

1. **Reference set**: N≈100–150 domain items (security findings, policy drift,
   allow/deny decisions), human-labeled with the guided tool
   `olivares evals label` (session ~2–3h; resumable — each label is persisted immediately).
   The item is an operator-authorized fixture, NOT production data. Published
   practice recommends 200–500 items with a double annotator and
   periodic recalibration ([futureagi], [testquality]); v1 starts with 100–150 and one annotator, and
   declares it as a limitation (§8).
2. **The set MUST contain human passes AND failures.** An all-pass set has undefined kappa (both
   evaluators constant) and a structural `meets_target=false`: it certifies nothing. This is not a
   bug — it is the rule that prevents "calibrating" with a trivial set.
3. **Measurement** (`modules/evals/calibration.go`): the real judge scores each item; the following is computed:
   - **percent agreement** over pass/fail with its **95% Wilson interval**;
   - **Cohen's kappa** (chance-corrected agreement — indispensable with imbalanced classes:
     a judge that always says "fail" over a 90%-fail set looks like 90% agreement with kappa≈0);
   - **sensitivity/specificity** vs the human reference, each with its denominator
     (n=0 ⇒ not-measured, never "0%");
   - **mean absolute error** of score and the **verbosity correlation**
     `corr(len(output), score_judge − score_human)` — length bias turned into a number;
   - the report is **append-only** (immutable evidence) + a core `EvalResult`
     (suite `judge-calibration`) consumed by the compliance module.
4. **Target**: agreement ≥ **0.85** AND defined kappa ≥ **0.60**. 85–90% is the practitioner
   consensus band anchored in MT-Bench (85% judge-human vs 81% human-human, Zheng et al.);
   0.60 is the "substantial" floor of Landis & Koch. A request MAY RAISE both bars, never
   lower them (they are clamped upward). Below target ⇒ `judge_calibration` finding (Medium; High if
   the gap is >0.10 or nothing was scored) + signal on the bus.
5. **Estimator bias correction**: with a current calibration, alongside the raw
   pass-rate the gate reports the **Rogan–Gladen** estimator `θ = (p + spec − 1)/(sens + spec − 1)` — the
   recommendation of Lee et al. (ICML 2026) not to publish judge pass-rates without correcting for their
   error profile. It is reported **alongside**, never **instead of**, the raw rate.
6. **Validity**: the gate uses the most recent report from the SAME model pin (`judge_model`). Changing the
   pin invalidates the calibration (there is no report for the new pin ⇒ the gate fails with
   `judge_uncalibrated` until recalibrated). Operational recommendation: recalibrate monthly and after
   every change to the judge prompt.

## 3. Bias mitigation

Implemented in the connector's prompt/schema and in the module; the canonical references are
Zheng et al. 2023 (MT-Bench) and the 2026 testquality/futureagi pipeline.

- **CoT-forcing (reasoning-before-verdict)**: the structured-outputs schema lists `analysis`
  as the FIRST property and the prompt requires writing it before deciding — generation is
  autoregressive, so the property order IS the forcing. The analysis is consumed in flight and
  discarded (data-minimization): it never reaches the ledger.
- **Verbosity control**: explicit instruction ("length is not quality… a short correct answer
  outranks a long padded one") in the pointwise and pairwise rubrics, AND measurement of the residual
  bias in every calibration (the correlation of §2.3 — if the prompt is not enough, the number exposes it).
- **Position/order (§3.1)** and **self-enhancement**: the judge is a model PINNED per suite
  (`judge_model`), normally distinct from the evaluated model; the pin travels in the cache key and
  in the calibration report. (A judge from a family different from the generator: documented
  operational recommendation, not enforced by code.)

### 3.1 Order-swapping (pairwise)

`POST /v1/m/evals/ab` with `"pairwise": true` judges each shared case **twice** with the
presentation order inverted and **only declares a winner if both orders agree** (the rule of
Zheng et al.). An inconsistent dual counts as a tie AND as `inconsistent`; the response carries
`position_consistency = {rate, n, ci}` — the measured position-bias, not hidden. Without `PairJudge`
wired, the block is `mode: "skipped"` with a declared reason, never a fabricated winner.

## 4. Confidence intervals

Reporting convention: **every rate travels with its denominator and its 95% interval**
(`{rate, n, ci}`); an undefined statistic is marked (`*_defined=false`, `n=0`) instead of
reporting a fabricated zero. Methods (`modules/evals/stats.go`, closed and deterministic):

- **pass-rate** (binomial proportion): **Wilson score** — stable with small n and p near 0/1,
  where Wald degenerates. Per run (`pass_rate_ci`, always derivable from the persisted counters) and
  case-weighted aggregate per scorecard (`pooled_pass_rate`).
- **mean score** (bounded mean): **Student's t interval** over the per-case scores (run) or
  over the run series (scorecard) — each CI pairs exactly with the statistic it
  accompanies. n<2 ⇒ no interval (fabricated precision = a lie).
- The CIs are **computed on-read** from the persisted evidence (the module tables are
  frozen; a derivative does not belong in the ledger) — a recomputation reproduces the number bit for bit.
- *Declared limitation*: we do not apply clustered standard errors (Anthropic, "Adding Error Bars
  to Evals") — the cases of a suite are treated as independent. If your cases come in
  clusters (variants of the same scenario), the real CI is wider than the reported one.

## 5. The CI regression gate (blocking)

`POST /v1/m/evals/gate` + CLI `olivares evals gate` (exit 0/1). Semantics
(implemented literally in `modules/evals/gate.go`):

| Measured condition | Verdict | Reason code |
|---|---|---|
| Regression vs baseline (drift > the suite's `regression_threshold`) | **fail** | `regression_vs_baseline` |
| `pass_rate` < the suite's `pass_threshold` | **fail** | `below_pass_threshold` |
| Run in error state (all cases errored) | **fail** | `run_error` |
| `llm_judge` suite with judge wired but WITHOUT calibration for the pin | **fail** | `judge_uncalibrated` |
| Current calibration below target | **fail** | `judge_calibration_below_target` |
| Budget in `block` (NOTHING is spent) | **fail** | `budget_blocked` |
| Budget in `throttle` (nothing is spent) | warn | `budget_throttled` |
| No judge credential (honest degradation — a recorded design decision) | warn | `no_judge_credential` |
| First execution (no baseline to compare) | note | `no_baseline` |

- **Controlled cost**: **seed-deterministic** subset (`sample_size` cases with the lowest
  `hash(seed|case_key)` — fixed across re-runs), **verdict cache** keyed by
  `(prompt-version | judge_model pin | criterion | input | expected | output)` — an identical
  re-run costs ZERO calls — and budget pre-flight over the spend of the CI itself.
  Changing the judge prompt requires a bump of `judgeCacheVersion` (`gate.go`), which invalidates the
  entire cache by construction (the version lives inside the hash).
- **Governed override**: `POST /v1/m/evals/gate/{id}/override` — admin-tier, mandatory written
  reason, audited with the original verdict. The recorded verdict does NOT change; what changes is the
  `effective_verdict` that CI consults with `--check-id`. A gate in pass is not "overrideable" and an
  override is not undone (re-run the gate).
- Each gate evaluation persists a normal run (it enters the suite's trend) + the gate row
  (verdict, reasons, seed, sample, override) — reproducible and auditable.

Example job (GitHub Actions):

```yaml
eval-gate:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - name: Generate candidate outputs
      run: ./scripts/generate-eval-outputs.sh > outputs.json   # your harness
    - name: Regression gate (blocking)
      run: |
        olivares evals gate \
          --server "$OLIVARES_SERVER_URL" --token "$OLIVARES_TOKEN" --tenant "$OLIVARES_TENANT" \
          --suite "$EVAL_SUITE_ID" --subject "$GITHUB_REPOSITORY@$GITHUB_SHA" \
          --outputs outputs.json --seed "$EVAL_SUITE_ID" --sample-size 50
      # exit 1 ⇒ the merge is blocked. After a governed override:
      #   olivares evals gate --check-id <gate-id>
```

## 6. Evidence and reproducibility

- Each run/calibration writes **ONE core `EvalResult`** — the canonical artifact that the compliance
  module counts as evidence without knowing the evals tables; regressions and calibrations
  below target are core **Findings** + a bus signal.
- Audited privileged actions: `evals.run.launch`, `evals.ab.score`, `evals.monitor`,
  `evals.baseline.pin`, `evals.calibration.label`, `evals.calibration.run`, `evals.gate.run`,
  `evals.gate.override`.
- Reproducibility: closed and deterministic statistics (no random sampling), hash-based
  sampling with an explicit persisted seed, cache keyed by content+pin+prompt version, append-only
  reports. Re-running with the same inputs yields the same numbers.

## 7. Public benchmarks: verified, none applicable (2026-06)

A recorded design decision required cross-validation with a public benchmark "only if an applicable one exists".
Verified (Jun 2026): **JudgeBench** (knowledge/reasoning/math/coding), **RewardBench 1/2**
(reward models; their "safety" is refusal behavior in chat), **LLMBar**
(adversarial instruction-following), **JETTS** (test-time scaling), **OffsetBias/EvalBiasBench**
(meta-eval of judge bias — useful as a robustness audit, not as domain calibration) and
**MT-Bench** measure domains foreign to agent governance. The closest one, `govllm`
(arXiv 2605.24737), brings n=49 pairs over regulatory criteria — insufficient for statistical
calibration and of a different object (compliance of LLM responses, not findings/drift/policy).
**Conclusion**: the internal human-labeled gold set IS the calibration artifact, which is
exactly what a research buyer expects to see when no domain benchmark exists.

## 8. Declared limitations and roadmap

- v1 calibration set: 100–150 items, ONE annotator (the maintainer). Planned hardening: 200–500
  items, double annotation with inter-annotator kappa as a ceiling, monthly recalibration.
- No clustered SE (§4); no self-consistency k-sampling of the judge (the CI comes from the sample
  size, not from re-sampling the judge); calibration staleness not enforced by code (operational
  guidance §2.6).
- **Adaptive red-teaming** (a recorded design decision): the v1 posture is **documented, not built** — the
  compensating controls are the plane's deny-closed enforcement + the isolated sandbox
  + module XVIII (redteam) with its static suites; an OSS adaptive engine (e.g.
  PyRIT/garak-style) is a designed post-v1 work item, not a half-build. The public
  comparison table must declare exactly this.

## References

- Lee et al., *How to Correctly Report LLM-as-a-Judge Evaluations* (ICML 2026) —
  <https://arxiv.org/abs/2511.21140> (Rogan–Gladen corrected estimator, adjusted Wald).
- Zheng et al., *Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena* (NeurIPS 2023) —
  <https://arxiv.org/abs/2306.05685> (85% vs 81%, order-swap, verbosity attack, CoT).
- Landis & Koch (1977), kappa bands — <https://pubmed.ncbi.nlm.nih.gov/843571/>.
- Anthropic, *Adding Error Bars to Evals* — <https://arxiv.org/abs/2411.00640>.
- TestQuality, *LLM regression testing pipeline* (2026) —
  <https://testquality.com/llm-regression-testing-pipeline/> (gating, order-swap, CoT-forcing).
- Galileo, *LLM-as-a-judge vs human evaluation* —
  <https://galileo.ai/blog/llm-as-a-judge-vs-human-evaluation> (band ≥80/85–90%).
- FutureAGI, *LLM-as-judge best practices 2026* —
  <https://futureagi.com/blog/llm-as-judge-best-practices-2026> (gold sets 200–500, kappa ≥0.6,
  recalibration cadence, sampling 5–20%).
- DeepEval, verdict caching in CI — <https://deepeval.com/docs/evaluation-flags-and-configs>.
- Position/bias: <https://arxiv.org/html/2406.07791v7> (position bias),
  <https://arxiv.org/pdf/2410.21819> (self-preference). Benchmarks §7: JudgeBench
  <https://arxiv.org/abs/2410.12784>, RewardBench 2 <https://arxiv.org/abs/2506.01937>, LLMBar
  <https://arxiv.org/abs/2310.07641>, JETTS <https://arxiv.org/abs/2504.15253>, OffsetBias
  <https://arxiv.org/abs/2407.06551>, govllm <https://arxiv.org/html/2605.24737>.
