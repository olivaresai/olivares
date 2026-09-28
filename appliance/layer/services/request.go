// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"slices"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// The helper's closed set of subcommands.
const (
	OpList    = "list"
	OpStatus  = "status"
	OpLogs    = "logs"
	OpStart   = "start"
	OpStop    = "stop"
	OpRestart = "restart"
	OpReload  = "reload"
	OpEnable  = "enable"
	OpDisable = "disable"
)

// Ops returns the closed set of subcommands in a fixed order.
func Ops() []string {
	return []string{OpList, OpStatus, OpLogs, OpStart, OpStop, OpRestart, OpReload, OpEnable, OpDisable}
}

// IsEffect reports whether op changes the host.
func IsEffect(op string) bool {
	return slices.Contains([]string{OpStart, OpStop, OpRestart, OpReload, OpEnable, OpDisable}, op)
}

// IsAct reports whether op is an act: an effect, or the logs read.
func IsAct(op string) bool { return op == OpLogs || IsEffect(op) }

// Bounds of the logs read.
const (
	// MaxLogLines is the most journal entries one logs act reads.
	MaxLogLines = 500
	// DefaultLogLines is what a logs act without lines reads.
	DefaultLogLines = 100
)

// Request is the helper's closed document: {"op", "unit", "lines", "operation_id"}. It has no
// field for a path, unit content, a mode or an invoker.
type Request struct {
	Op          string `json:"op"`
	Unit        string `json:"unit,omitempty"`
	Lines       *int   `json:"lines,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
}

// Subcommand implements helperschema.Request.
func (r *Request) Subcommand() string { return r.Op }

// Operation implements helperschema.Request.
func (r *Request) Operation() string { return r.OperationID }

// Validate implements helperschema.Request. op is one of the closed set; list names no unit and
// every other op names one unit by its unit name; an act carries the operation id the client
// minted, and a read carries none; lines, from 1 to MaxLogLines, belongs to logs alone.
func (r *Request) Validate() error {
	switch {
	case r.Op == "":
		return refuse("$.op", "required: one of "+strings.Join(Ops(), ", "))
	case !slices.Contains(Ops(), r.Op):
		return refuse("$.op", "expected one of "+strings.Join(Ops(), ", "))
	case r.Op == OpList && r.Unit != "":
		return refuse("$.unit", "list takes no unit")
	case r.Op != OpList && !ValidUnitName(r.Unit):
		return refuse("$.unit", "expected a unit name with a unit type suffix; a path, a pattern or unit content is never accepted")
	case IsAct(r.Op) && !isOperationID(r.OperationID):
		return refuse("$.operation_id", "required: 32 lowercase hexadecimal digits, minted when the operator confirms")
	case !IsAct(r.Op) && r.OperationID != "":
		return refuse("$.operation_id", "list and status change nothing, so they carry no operation id")
	case r.Lines != nil && r.Op != OpLogs:
		return refuse("$.lines", "only logs takes lines")
	case r.Lines != nil && (*r.Lines < 1 || *r.Lines > MaxLogLines):
		return refuse("$.lines", "expected from 1 to 500")
	}
	return nil
}

func refuse(field, reason string) error {
	return &helperschema.InputError{Field: field, Reason: reason}
}

// isOperationID reports whether s has an operation id's shape: 32 lowercase hexadecimal digits.
func isOperationID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'f') {
			return false
		}
	}
	return true
}

// LogLines is the number of entries a logs document asks for.
func (r *Request) LogLines() int {
	if r.Lines == nil {
		return DefaultLogLines
	}
	return *r.Lines
}

// Rules is the helper's admission table. list and status are its read-only set, for the
// Appliance Console and the repair console on tty1. logs is an act, not a read: the console's
// account cannot read the system journal, so the helper reads it only for an act, which the
// seam admits like an effect (a proven connection identity) for the same two invokers. The
// effects are the Appliance Console's alone.
func Rules() []helperschema.Rule {
	readers := []helperschema.Invoker{helperschema.Portal, helperschema.RepairConsole}
	rules := []helperschema.Rule{
		{Subcommand: OpList, Invokers: readers},
		{Subcommand: OpStatus, Invokers: readers},
		{Subcommand: OpLogs, Mutating: true, Invokers: readers},
	}
	for _, op := range []string{OpStart, OpStop, OpRestart, OpReload, OpEnable, OpDisable} {
		rules = append(rules, helperschema.Rule{Subcommand: op, Mutating: true, Invokers: []helperschema.Invoker{helperschema.Portal}})
	}
	return rules
}
