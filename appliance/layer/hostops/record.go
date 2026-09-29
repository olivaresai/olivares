// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import "strings"

// Command is one submission. The operation id is the client's. The engine does
// not mint one and does not read a path from the command.
type Command struct {
	OperationID string
	TaskID      string
	Module      string
	Verb        string
	Target      string
	PlanDigest  string
	Mode        string
	Surface     string
	Actor       string
}

// Record is one stored operation. It is the document a surface prints.
type Record struct {
	consumerRequestID string // private correlation in the durable projection, never surface JSON
	OperationID       string `json:"operation_id"`
	TaskID            string `json:"task_id"`
	Module            string `json:"module"`
	Verb              string `json:"verb"`
	Target            string `json:"target"`
	PlanDigest        string `json:"plan_digest"`
	Mode              string `json:"mode"`
	Surface           string `json:"surface"`
	Actor             string `json:"actor"`
	State             string `json:"state"`
	// Outcome is unknown while the operation is running. It is empty once the
	// operation has a finished state.
	Outcome string `json:"outcome,omitempty"`
	// Reason preserves the terminal journal distinction without claiming success.
	Reason string `json:"reason,omitempty"`
	// Postconditions are the measured facts that justify a finished state.
	Postconditions []string `json:"postconditions,omitempty"`
	EffectPending  bool     `json:"effect_pending,omitempty"`
	LogRef         string   `json:"log_ref"`
}

// InputRefused is a closed refusal. It names the field and never a value the
// caller sent.
type InputRefused struct {
	Field string
}

func (e *InputRefused) Error() string {
	if e == nil || e.Field == "" {
		return "422 input_refused"
	}
	return "422 input_refused " + e.Field
}

func refuse(field string) error { return &InputRefused{Field: field} }

const (
	// StatePlanned is a confirmed plan that has not started.
	StatePlanned = "planned"
	// StateRunning is an accepted operation whose outcome is still unknown.
	StateRunning = "running"
	// StateSucceeded is a performed operation whose postconditions were measured.
	StateSucceeded = "succeeded"
	// StateFailed is an operation that did not change the host.
	StateFailed = "failed"
	// StateRolledBack is a measured revert.
	StateRolledBack = "rolled_back"
	// StatePartial is a change that was not fully measured, or a measured change
	// that did not succeed.
	StatePartial = "partial"
	// OutcomeUnknown is the outcome carried while an operation is running.
	OutcomeUnknown = "unknown"

	// StatusRunning is the transport status of a running operation.
	StatusRunning = 202
	// StatusRecord is the transport status of a finished operation.
	StatusRecord = 200
	// StatusLocked is the transport status of a target lock refusal.
	StatusLocked = 409
	// StatusRefused is the transport status of a refused input.
	StatusRefused = 422
)

func validate(cmd Command) error {
	if !isHex(cmd.OperationID, 32) {
		return refuse("operation_id")
	}
	if !matchToken(cmd.TaskID, true) {
		return refuse("task_id")
	}
	if !matchName(cmd.Module) {
		return refuse("module")
	}
	if !matchName(cmd.Verb) {
		return refuse("verb")
	}
	if !validTarget(cmd.Target) {
		return refuse("target")
	}
	if !isHex(cmd.PlanDigest, 64) {
		return refuse("plan_digest")
	}
	if !oneOf(cmd.Mode, "product-up", "product-down-repair", "tty1", "timer", "guard", "projection") {
		return refuse("mode")
	}
	if !oneOf(cmd.Surface, "web", "cli", "tui", "tty1", "system") {
		return refuse("surface")
	}
	if !validActor(cmd.Actor) {
		return refuse("actor")
	}
	return nil
}

func oneOf(s string, allowed ...string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !hexDigit(s[i]) {
			return false
		}
	}
	return true
}

func hexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

func matchName(s string) bool {
	if s == "" || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// matchToken accepts a dotted task id: letters, digits, dots and hyphens, with
// no empty segment and no slash.
func matchToken(s string, dotted bool) bool {
	if s == "" || len(s) > 64 || s[0] == '.' || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		case dotted && c == '.':
			if i == 0 || s[i-1] == '.' {
				return false
			}
		default:
			return false
		}
	}
	return s[len(s)-1] != '.'
}

func validActor(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == ':' || c == '@' || c == '-':
		default:
			return false
		}
	}
	return true
}

func validTarget(s string) bool {
	switch s {
	case "network", "packages", "system", "app:ingress":
		return true
	}
	if rest, ok := strings.CutPrefix(s, "unit:"); ok {
		return unitName(rest)
	}
	if rest, ok := strings.CutPrefix(s, "drive:"); ok {
		return isHex(rest, 64)
	}
	if rest, ok := strings.CutPrefix(s, "vg:"); ok {
		return isUUID(rest)
	}
	if rest, ok := strings.CutPrefix(s, "app:"); ok {
		return matchName(rest) && rest != "ingress"
	}
	return false
}

func unitName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == ':' || c == '.' || c == '_' || c == '@' || c == '-':
		default:
			return false
		}
	}
	return true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !hexDigit(c) {
				return false
			}
		}
	}
	return true
}

func logRef(id string) string { return "journal:OLIVARES_OPERATION_ID=" + id }

// TargetLocked refuses a new operation because target already has one that is
// not finished. OperationID and State name that open operation.
type TargetLocked struct {
	OperationID string
	State       string
}

func (e *TargetLocked) Error() string { return "409 target_locked" }

func terminal(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateRolledBack, StatePartial:
		return true
	default:
		return false
	}
}

func statusFor(rec Record) int {
	if rec.State == StateRunning {
		return StatusRunning
	}
	return StatusRecord
}

func validRecord(r Record) bool {
	c := Command{r.OperationID, r.TaskID, r.Module, r.Verb, r.Target, r.PlanDigest, r.Mode, r.Surface, r.Actor}
	if validate(c) != nil || !oneOf(r.State, StatePlanned, StateRunning, StateSucceeded, StateFailed, StateRolledBack, StatePartial) || r.LogRef != logRef(r.OperationID) {
		return false
	}
	if r.Reason != "" && (r.Reason != "abandoned" || !oneOf(r.State, StateFailed, StatePartial)) {
		return false
	}
	if r.State == StateRunning {
		return r.Outcome == OutcomeUnknown
	}
	return r.Outcome == "" && (!r.EffectPending || r.State == StatePartial)
}

// PlanChanged refuses reuse of an operation id for a different confirmed plan.
type PlanChanged struct{}

func (*PlanChanged) Error() string { return "409 plan_changed" }

func samePlan(cmd Command, rec Record) bool {
	return cmd.TaskID == rec.TaskID && cmd.Module == rec.Module && cmd.Verb == rec.Verb &&
		cmd.Target == rec.Target && cmd.PlanDigest == rec.PlanDigest && cmd.Mode == rec.Mode && cmd.Actor == rec.Actor
}
