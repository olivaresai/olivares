// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// runtime_usage.go turns what a governed turn COST into two durable facts: the
// counters on the session an operator is looking at, and a row on the tenant's
// spend ledger.
//
// ⛔ WHY IT EXISTS, MEASURED 2026-09-18. A real model turn answered through a governed session, the
// driver's own result frame carried `total_cost_usd` and the full token usage on
// the wire, and then: `agent session get -o json` returned 19 fields with no
// token or cost field among them, and `finops spend summary` over the same window
// answered `samples 0`. Cost samples reached FinOps only from the IN-PROCESS
// inference client; nothing folded an official-CLI session's result frame onto
// that bus. A FinOps product could not price the sessions it governs.
//
// ⛔ CUMULATIVE, NOT PER TURN, AND THAT IS READ FROM THE CLI ITSELF, NOT ASSUMED.
// Claude Code 2.1.276 documents the result frame's own fields in its build:
// *"Cumulative like modelUsage: read the latest result rather than summing across
// results; a resumed session starts fresh and a mid-session /clear zeroes it"*,
// naming `total_cost_usd`, `duration_api_ms` and `modelUsage` explicitly. So each
// result frame restates the session's total — and a session that is resumed, or
// cleared, starts its count again. Summing the frames would multiply the bill;
// taking the last one would lose everything a previous incarnation spent.
//
// What this file does instead is a monotone counter with resets: it credits the
// DELTA against what it has already credited for that model in this incarnation,
// and treats a value that went DOWN as a new counting epoch (the reset the CLI
// documents), crediting it whole. The row therefore accumulates across resumes,
// and the ledger receives one row per model per delta — never a row for zero.
//
// ⛔ AND UNKNOWN IS NOT ZERO. A driver that reports no usage leaves the columns
// NULL and the DTO fields ABSENT, so a reader sees "not reported" instead of a
// free turn. That is why the DTO carries pointers: `"cost_micro_usd": 0` would be
// a claim, and this plane does not make it.
//
// ⛔ AND IT IS PER COLUMN, NOT PER FRAME, which is the half that was missing.
// Tokens and money are reported independently: a CLI metering a turn it is not
// pricing sends counters and no `total_cost_usd`. The token columns advance and
// the COST column is left untouched — not advanced by zero, which would make it
// present. So one session record can honestly say "7855 in, 13 out, price not
// reported", and a later frame that does carry a price still records it.

// microUSDPerUSD is the fixed-point scale of every monetary value in this
// product: millionths of a US dollar (sdk/model.CostSample.CostMicroUSD).
const microUSDPerUSD = 1_000_000

// resultUsage is the metering an official CLI reports on its own result frame:
// numbers and a model name, never a prompt, a completion or a tool argument. The
// frame BODY stays undecoded, exactly as streamjson.go promises — a token count
// is not content.
type resultUsage struct {
	// Models is the per-model breakdown, keyed by the model reference the provider
	// itself used. It is the authoritative cumulative source when the driver
	// reports it (`modelUsage`).
	Models map[string]modelUsage
	// CostMicroUSD is the frame's own `total_cost_usd`, scaled. It is used when no
	// per-model breakdown is present, so a CLI that reports only the total is still
	// priced.
	CostMicroUSD int64
	// Fallback is the frame's flat `usage` object, used for the same reason.
	Fallback modelUsage
	// Reported is false for a frame that carries no metering at all, which is
	// recorded as UNKNOWN and never as zero.
	Reported bool
}

// modelUsage is one model's cumulative usage inside a result frame.
type modelUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CostMicroUSD        int64
}

// totalInputTokens is the TOTAL input volume the spend ledger wants: uncached
// plus cache-write plus cache-read (sdk/model.CostSample documents that meaning,
// and the cache split is a breakdown OF it rather than an addition to it).
func (u modelUsage) totalInputTokens() int64 {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

func (u modelUsage) empty() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 &&
		u.CacheCreationTokens == 0 && u.CostMicroUSD == 0
}

// resultFrameUsage decodes the metering of one stream-json `result` line.
//
// It accepts BOTH shapes of every field name the official CLIs use for the same
// number (`inputTokens` beside `input_tokens`), because the two spellings appear
// in the same protocol — the flat `usage` object is snake_case and the per-model
// `modelUsage` entries are camelCase — and a decoder that knew only one would
// silently price a turn at zero.
func resultFrameUsage(line []byte) (resultUsage, bool) {
	var raw struct {
		Type       string `json:"type"`
		TotalCost  *float64 `json:"total_cost_usd"`
		Usage      *usageJSON `json:"usage"`
		ModelUsage map[string]usageJSON `json:"modelUsage"`
	}
	if err := json.Unmarshal(line, &raw); err != nil || raw.Type != "result" {
		return resultUsage{}, false
	}
	out := resultUsage{Models: map[string]modelUsage{}}
	for name, entry := range raw.ModelUsage {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if u := entry.usage(); !u.empty() {
			out.Models[name] = u
			out.Reported = true
		}
	}
	if raw.TotalCost != nil && *raw.TotalCost > 0 {
		out.CostMicroUSD = int64(math.Round(*raw.TotalCost * microUSDPerUSD))
		out.Reported = true
	}
	if raw.Usage != nil {
		if u := raw.Usage.usage(); !u.empty() {
			out.Fallback = u
			out.Reported = true
		}
	}
	return out, out.Reported
}

// usageJSON is the union of the spellings the official CLIs use for one usage
// object. Every field is a pointer so "absent" and "zero" stay distinguishable at
// the decoding layer, which is the whole difference between unknown and free.
type usageJSON struct {
	InputTokens      *int64 `json:"input_tokens"`
	InputTokensCamel *int64 `json:"inputTokens"`

	OutputTokens      *int64 `json:"output_tokens"`
	OutputTokensCamel *int64 `json:"outputTokens"`

	CacheRead      *int64 `json:"cache_read_input_tokens"`
	CacheReadCamel *int64 `json:"cacheReadInputTokens"`

	CacheCreation      *int64 `json:"cache_creation_input_tokens"`
	CacheCreationCamel *int64 `json:"cacheCreationInputTokens"`

	CostUSD      *float64 `json:"costUSD"`
	CostUSDSnake *float64 `json:"cost_usd"`
}

func (u usageJSON) usage() modelUsage {
	pick := func(a, b *int64) int64 {
		switch {
		case a != nil && *a > 0:
			return *a
		case b != nil && *b > 0:
			return *b
		default:
			return 0
		}
	}
	out := modelUsage{
		InputTokens:         pick(u.InputTokens, u.InputTokensCamel),
		OutputTokens:        pick(u.OutputTokens, u.OutputTokensCamel),
		CacheReadTokens:     pick(u.CacheRead, u.CacheReadCamel),
		CacheCreationTokens: pick(u.CacheCreation, u.CacheCreationCamel),
	}
	for _, cost := range []*float64{u.CostUSD, u.CostUSDSnake} {
		if cost != nil && *cost > 0 {
			out.CostMicroUSD = int64(math.Round(*cost * microUSDPerUSD))
			break
		}
	}
	return out
}

// usageAccount credits cumulative reports as deltas, per model, for one live
// incarnation of a run. It is the monotone-counter-with-resets rule this file's
// header explains; nothing else in the module needs to know the rule.
type usageAccount struct {
	credited map[string]modelUsage
}

// creditedDelta is one model's newly-credited usage, ready for the ledger.
type creditedDelta struct {
	ModelRef string
	Usage    modelUsage
}

// credit folds one result frame into the account and returns the deltas to
// record. The primary model is the one with the largest newly-credited output,
// which is what the session record names.
func (a *usageAccount) credit(r resultUsage) []creditedDelta {
	if a.credited == nil {
		a.credited = map[string]modelUsage{}
	}
	reported := r.Models
	if len(reported) == 0 {
		// A CLI that reports no per-model breakdown still reports a total. It is
		// credited under the empty model key, which the record reads as "the model is
		// not reported" rather than inventing one.
		flat := r.Fallback
		if r.CostMicroUSD > 0 {
			flat.CostMicroUSD = r.CostMicroUSD
		}
		if flat.empty() {
			return nil
		}
		reported = map[string]modelUsage{"": flat}
	} else if r.CostMicroUSD > 0 && modelUsageCostTotal(reported) == 0 {
		// The breakdown carried tokens but no money, and the frame carried the money.
		// Attribute it to the model with the most output rather than dropping it: a
		// priced turn with no price on the ledger is the defect this file removes.
		primary := primaryModel(reported)
		entry := reported[primary]
		entry.CostMicroUSD = r.CostMicroUSD
		reported[primary] = entry
	}

	names := make([]string, 0, len(reported))
	for name := range reported {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic ledger order for one frame
	out := make([]creditedDelta, 0, len(names))
	for _, name := range names {
		now, before := reported[name], a.credited[name]
		delta := modelUsage{
			InputTokens:         creditDelta(before.InputTokens, now.InputTokens),
			OutputTokens:        creditDelta(before.OutputTokens, now.OutputTokens),
			CacheReadTokens:     creditDelta(before.CacheReadTokens, now.CacheReadTokens),
			CacheCreationTokens: creditDelta(before.CacheCreationTokens, now.CacheCreationTokens),
			CostMicroUSD:        creditDelta(before.CostMicroUSD, now.CostMicroUSD),
		}
		a.credited[name] = now
		if delta.empty() {
			continue
		}
		out = append(out, creditedDelta{ModelRef: name, Usage: delta})
	}
	return out
}

// creditDelta is the monotone-with-reset rule for ONE counter: the increase
// since the last report, or the whole value when the counter went backwards
// (the reset a resume or a mid-session clear produces).
func creditDelta(before, now int64) int64 {
	if now <= 0 {
		return 0
	}
	if now < before {
		return now
	}
	return now - before
}

func modelUsageCostTotal(in map[string]modelUsage) int64 {
	var total int64
	for _, u := range in {
		total += u.CostMicroUSD
	}
	return total
}

// primaryModel is the model with the most output tokens, ties broken by name so
// the choice is deterministic.
func primaryModel(in map[string]modelUsage) string {
	best, bestOut := "", int64(-1)
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if out := in[name].OutputTokens; out > bestOut {
			best, bestOut = name, out
		}
	}
	return best
}

// recordTurnUsage is the bridge's entry point: it credits the frame, advances the
// run's counters in one row write, and posts the spend-ledger rows.
//
// It is best-effort on purpose, like every other bridge-side write: a ledger that
// cannot be reached must not kill a running session. A row write this plane can
// SEE fail leaves the account uncredited, so the next cumulative frame re-offers
// the same delta and it is recovered.
//
// ⛔ AND THE LIMIT OF THAT IS WORTH STATING, because the seam it rests on is
// deliberately quiet. mutateRunBest reports no error (it retries a conflict once
// and returns), so this function learns only that its callback RAN — not that the
// UPDATE committed. A conflict that loses twice therefore leaves the run's own
// counters one delta short while the spend row still lands. That direction is
// chosen rather than tolerated: the LEDGER is the billing record and the counters
// on the row are a convenience beside it, so when only one of the two can be
// guaranteed it is the ledger. The alternative — refusing to post a row because a
// convenience field might be stale — is how a real cost comes to exist nowhere,
// which is the defect this file was written for.
func (m *Module) recordTurnUsage(ctx context.Context, lr *liveRun, line []byte, at time.Time) bool {
	r, ok := resultFrameUsage(line)
	if !ok {
		return false
	}
	lr.mu.Lock()
	deltas := lr.usage.credit(r)
	lr.mu.Unlock()
	if len(deltas) == 0 {
		return false
	}
	var input, output, cost int64
	primary, primaryOut := "", int64(-1)
	for _, d := range deltas {
		input += d.Usage.totalInputTokens()
		output += d.Usage.OutputTokens
		cost += d.Usage.CostMicroUSD
		if d.ModelRef != "" && d.Usage.OutputTokens > primaryOut {
			primary, primaryOut = d.ModelRef, d.Usage.OutputTokens
		}
	}
	if !m.advanceRunUsage(ctx, lr, input, output, cost, primary, at) {
		lr.mu.Lock()
		// Roll the credit back so the next cumulative frame re-offers it.
		for _, d := range deltas {
			before := lr.usage.credited[d.ModelRef]
			lr.usage.credited[d.ModelRef] = modelUsage{
				InputTokens:         before.InputTokens - d.Usage.InputTokens,
				OutputTokens:        before.OutputTokens - d.Usage.OutputTokens,
				CacheReadTokens:     before.CacheReadTokens - d.Usage.CacheReadTokens,
				CacheCreationTokens: before.CacheCreationTokens - d.Usage.CacheCreationTokens,
				CostMicroUSD:        before.CostMicroUSD - d.Usage.CostMicroUSD,
			}
		}
		lr.mu.Unlock()
		return false
	}
	m.postSpendRows(ctx, lr, deltas, at)
	return true
}

// advanceRunUsage adds this turn's delta to the run's stored counters. The
// counters are SUMS of credited deltas, so they survive a resume that restarts
// the provider's own cumulative count.
func (m *Module) advanceRunUsage(
	ctx context.Context, lr *liveRun, input, output, cost int64, modelRef string, at time.Time,
) bool {
	written := false
	m.mutateRunBest(ctx, lr, func(rec model.Record) {
		rec[colRunInputTokens] = rec.Int(colRunInputTokens) + input
		rec[colRunOutputTokens] = rec.Int(colRunOutputTokens) + output
		// ⛔ ONLY WHEN MONEY WAS REPORTED. A token delta must not TOUCH this column:
		// writing `0 + 0` turns NULL into a present zero, and a present zero says the
		// turn was free — the claim this file's header says the plane does not make.
		// An official CLI that meters tokens without pricing them is an ordinary
		// shape, not a corner, and it was recording `cost_micro_usd = 0` (measured
		// 2026-09-18). Adding nothing to an already-present total is a no-op, so the
		// guard costs a priced session nothing.
		if cost > 0 {
			rec[colRunCostMicroUSD] = rec.Int(colRunCostMicroUSD) + cost
		}
		if modelRef != "" {
			rec[colRunUsageModelRef] = modelRef
		}
		rec[colLastActivityAt] = model.NewTimestamp(at).String()
		written = true
	})
	return written
}

// postSpendRows hands each credited delta to the tenant's spend ledger through
// the composition-root port. Unwired, it says so ONCE per run rather than per
// frame: a session whose cost reaches no ledger is a real gap, and a line per
// turn would bury it.
func (m *Module) postSpendRows(ctx context.Context, lr *liveRun, deltas []creditedDelta, at time.Time) {
	sink := m.rt.costSink
	if sink == nil {
		lr.mu.Lock()
		reported := lr.costSinkUnwiredReported
		lr.costSinkUnwiredReported = true
		lr.mu.Unlock()
		if !reported {
			m.warnf("sessions: this session's cost reaches no spend ledger (no cost sink is wired); the session record still carries its own counters",
				"run_ref", lr.runRef)
		}
		return
	}
	for _, d := range deltas {
		sample := SessionCostSample{
			RunRef:       lr.runRef,
			SessionRef:   lr.claim.SID,
			ModelRef:     d.ModelRef,
			ProviderRef:  lr.providerRefForCost(),
			WorkspaceRef: lr.workspaceRefForCost(),
			AgentRef:     lr.agentRef,

			InputTokens:            d.Usage.totalInputTokens(),
			OutputTokens:           d.Usage.OutputTokens,
			CacheReadTokens:        d.Usage.CacheReadTokens,
			CacheCreationTokens:    d.Usage.CacheCreationTokens,
			CostMicroUSD:           d.Usage.CostMicroUSD,
			CostFromProviderClient: d.Usage.CostMicroUSD > 0,
			OccurredAt:             at,
		}
		if err := sink.PublishSessionCost(ctx, lr.tenant, sample); err != nil {
			m.warnf("sessions: a governed turn's cost could not be posted to the spend ledger",
				"run_ref", lr.runRef, "err", redactErr(err))
		}
	}
}

// providerRefForCost names WHICH provider was billed, using the driver of the
// profile this run launched under. It is a label for attribution, never a
// credential and never a home path.
func (lr *liveRun) providerRefForCost() string {
	if lr.profile != nil && lr.profile.Driver != "" {
		return lr.profile.Driver
	}
	return providerDriverClaude
}

// workspaceRefForCost is the billing workspace dimension: the run's own
// authorization lineage is not a finance dimension, so the operator-facing
// workspace reference is used, and an unworkspaced run reports none rather than
// borrowing one.
func (lr *liveRun) workspaceRefForCost() string { return lr.workspaceRef }
