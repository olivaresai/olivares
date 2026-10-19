// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
)

// runtime_limit.go records the one fact a person or a dispatcher needs when a
// tool's turn ends because its account ran out: that it was a limit, which kind,
// until when, and on which account. The fact comes from the tool's own result, never
// from reading its error text, so a vendor that rewords the message changes nothing.

// Limit kinds: the account's plan allowance ran out (usage), or the provider is
// throttling requests for a moment (rate).
const (
	limitKindUsage = "usage"
	limitKindRate  = "rate"
)

// limitEpochMax bounds a reset time a tool reports (epoch seconds, the year 2100), so a
// hostile or broken value never reaches time arithmetic or the stored fact.
const limitEpochMax = 4102444800

// limitResetSpan is how far from now a recorded reset time may lie, either way: a plan
// window lifts within days, so a time beyond a year is not a reset time.
const limitResetSpan = 366 * 24 * time.Hour

// eventLimitReached is the ledger event of a turn that ended on a limit.
const eventLimitReached = "limit_reached"

// runLimit is the structured fact a limit leaves on the run. ResetsAt is when the
// tool says the limit lifts (RFC 3339, UTC); it is absent when the tool names none.
// AccountRef is the profile the turn ran under.
type runLimit struct {
	Kind       string `json:"kind"`
	ResetsAt   string `json:"resets_at,omitempty"`
	AccountRef string `json:"account_ref,omitempty"`
}

// limitReport is what a driver or the stream-json bridge saw: a turn ended on a limit
// of this kind, and the reset time the tool reported (zero when it reported none).
type limitReport struct {
	Kind     string
	ResetsAt time.Time
}

// storedRunLimit reads the fact back; an unreadable value is no fact. A move clears the
// stored value, so a fact never outlives the account it was about.
func storedRunLimit(rec model.Record) *runLimit {
	raw := rec.String(colRunUsageLimit)
	if raw == "" {
		return nil
	}
	var l runLimit
	if json.Unmarshal([]byte(raw), &l) != nil || l.Kind == "" {
		return nil
	}
	return &l
}

// limitFrame is the little a Claude Code frame says about a limit. Each field is read
// on its own, so one the tool shapes differently one day costs this fact and nothing
// else: the frame still serves everything else that reads it.
type limitFrame struct {
	Type           string
	Rejected       bool      // a rate_limit_event whose window is used up
	ResetsAt       time.Time // that window's reset, zero when the tool names none
	IsError        bool
	APIError       string
	APIErrorStatus int64
}

func parseLimitFrame(line []byte) (limitFrame, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(line, &raw) != nil {
		return limitFrame{}, false
	}
	var f limitFrame
	_ = json.Unmarshal(raw["type"], &f.Type)
	switch f.Type {
	case "rate_limit_event":
		var info map[string]json.RawMessage
		if json.Unmarshal(raw["rate_limit_info"], &info) != nil {
			break
		}
		var status string
		_ = json.Unmarshal(info["status"], &status)
		if status != "rejected" {
			break
		}
		f.Rejected = true
		var resetsAt int64
		if json.Unmarshal(info["resetsAt"], &resetsAt) == nil && resetsAt > 0 && resetsAt < limitEpochMax {
			f.ResetsAt = time.Unix(resetsAt, 0).UTC()
		}
	case "result":
		_ = json.Unmarshal(raw["is_error"], &f.IsError)
		_ = json.Unmarshal(raw["api_error"], &f.APIError)
		_ = json.Unmarshal(raw["api_error_status"], &f.APIErrorStatus)
	}
	return f, true
}

// kind is the limit a failed result ended the turn on: the tool's own typed cause for a
// spent plan, else the provider's 429. Any other result says none.
func (f limitFrame) kind() string {
	switch {
	case f.Type != "result" || !f.IsError:
		return ""
	case f.APIError == "usage_limit_reached":
		return limitKindUsage
	case f.APIErrorStatus == http.StatusTooManyRequests:
		return limitKindRate
	}
	return ""
}

var (
	limitEventMarker  = []byte(`"rate_limit_event"`)
	limitResultMarker = []byte(`"is_error"`)
)

// observeStreamLimit reads one Claude Code line for the limit. The window event precedes
// the failed result and carries the reset time; the result decides whether the turn
// ended on a limit, and a result that did not fail clears it.
func (m *Module) observeStreamLimit(ctx context.Context, lr *liveRun, line []byte) {
	if !bytes.Contains(line, limitEventMarker) && !bytes.Contains(line, limitResultMarker) {
		return
	}
	f, ok := parseLimitFrame(line)
	if !ok {
		return
	}
	if f.Rejected {
		lr.mu.Lock()
		if f.ResetsAt.After(lr.windowResetsAt) {
			lr.windowResetsAt = f.ResetsAt
		}
		lr.mu.Unlock()
		return
	}
	if f.Type != "result" {
		return
	}
	lr.mu.Lock()
	windowResetsAt := lr.windowResetsAt
	lr.windowResetsAt = time.Time{}
	lr.mu.Unlock()
	switch kind := f.kind(); {
	case kind == limitKindUsage:
		m.recordLimit(ctx, lr, limitReport{Kind: kind, ResetsAt: windowResetsAt})
	case kind != "":
		m.recordLimit(ctx, lr, limitReport{Kind: kind})
	case !f.IsError:
		m.clearLimit(ctx, lr)
	}
}

// limitResetsAt is the reset time to record: the tool's own when it named one, else for
// a usage limit the reset of the organization's login windows, if that is still ahead.
func (m *Module) limitResetsAt(ctx context.Context, lr *liveRun, rep limitReport) time.Time {
	if !rep.ResetsAt.IsZero() || rep.Kind != limitKindUsage {
		return rep.ResetsAt
	}
	if at := m.loginWindowReset(ctx, lr); at.After(m.now()) {
		return at
	}
	return time.Time{}
}

// recordLimit writes the fact and its ledger event in one transaction. The fact is
// compared with the one stored on the run, so a result the tool restates (a replay, or
// the same limit met again after a resume or a restart) is recorded once. A restatement
// that lost the reset time keeps the stored one while it is still ahead.
func (m *Module) recordLimit(ctx context.Context, lr *liveRun, rep limitReport) {
	fact := runLimit{Kind: rep.Kind}
	if at := m.limitResetsAt(ctx, lr, rep); !at.IsZero() && at.After(m.now().Add(-limitResetSpan)) && at.Before(m.now().Add(limitResetSpan)) {
		fact.ResetsAt = at.UTC().Format(time.RFC3339)
	}
	if lr.profile != nil {
		fact.AccountRef = lr.profile.ProfileID
	}
	// A read that fails records the fact anyway: a repeated event costs less than a lost fact.
	rec, err := m.loadRun(ctx, lr.tenant, lr.runRef)
	if err != nil {
		m.warnf("sessions: could not read the run to record its limit", "run_ref", lr.runRef, "err", redactErr(err))
	}
	if stored := storedRunLimit(rec); stored != nil {
		if fact.ResetsAt == "" && stored.Kind == fact.Kind && stored.AccountRef == fact.AccountRef {
			if at, err := time.Parse(time.RFC3339, stored.ResetsAt); err == nil && at.After(m.now()) {
				fact.ResetsAt = stored.ResetsAt
			}
		}
		if *stored == fact {
			return
		}
	}
	lr.mu.Lock()
	wasClear := lr.limitClear
	lr.limitClear = false
	lr.mu.Unlock()
	// A marshal of three strings cannot fail.
	raw, _ := json.Marshal(fact)
	detail := fact.Kind + " limit"
	if fact.ResetsAt != "" {
		detail += ", resets " + fact.ResetsAt
	}
	if _, err := m.transition(ctx, lr.tenant, lr.runRef, transitionInput{
		event: eventLimitReached, actorKind: model.ActorSystem, detail: detail,
		keepReason: true, guard: guardRuntimeLaunch(lr.launchID),
		mutate: func(rec model.Record) { rec[colRunUsageLimit] = string(raw) },
	}); err != nil {
		lr.mu.Lock()
		lr.limitClear = wasClear
		lr.mu.Unlock()
		m.warnf("sessions: could not record the limit a turn ended on", "run_ref", lr.runRef, "err", redactErr(err))
	}
}

// clearLimit removes the fact once a turn succeeds. The stored column is read once per
// launch, and written only if a limit is there, so an ordinary turn costs nothing. The
// launch counts the column as clear only once that is known to be true; a write or read
// that failed is tried again by the next successful turn.
func (m *Module) clearLimit(ctx context.Context, lr *liveRun) {
	lr.mu.Lock()
	known := lr.limitClear
	lr.mu.Unlock()
	if known {
		return
	}
	rec, err := m.loadRun(ctx, lr.tenant, lr.runRef)
	if err != nil {
		m.warnf("sessions: could not read the run to clear its limit", "run_ref", lr.runRef, "err", redactErr(err))
		return
	}
	if rec.String(colRunUsageLimit) != "" {
		if err := m.mutateRun(ctx, lr, func(rec model.Record) { rec[colRunUsageLimit] = nil }); err != nil {
			m.warnf("sessions: could not clear the limit of a run whose turn succeeded", "run_ref", lr.runRef, "err", redactErr(err))
			return
		}
	}
	lr.mu.Lock()
	lr.limitClear = true
	lr.mu.Unlock()
}

// loginWindowReset is the fallback for a usage limit whose tool named no reset time:
// when the run uses the organization's own login of its tool, the reset of that
// login's used-up windows, as `olivares tool providers` reports them.
func (m *Module) loginWindowReset(ctx context.Context, lr *liveRun) time.Time {
	p := lr.profile
	if m.rt.LimitResets == nil || p == nil || !m.usesOwnToolLogin(lr.tenant, p.Driver, p.AuthSource, p.ConfigHome) {
		return time.Time{}
	}
	if at, ok := m.rt.LimitResets(ctx, lr.tenant, driverfacts.OlivaresLoginInstance(p.Driver)); ok {
		return at
	}
	return time.Time{}
}
