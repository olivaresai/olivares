// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

// Observation maps the effect to the host operation's receipt outcome. A refusal performed
// nothing. A call the service manager refused failed, partially when the state changed
// anyway. A job with no JobRemoved within the bound is unknown, so its operation stays running.
// A done job, or a unit-file change, succeeds only when the measured state is the plan's:
// done with another state is partial. A job with another result failed, partially when the
// state changed. Without an after-measurement a performed effect has no postconditions, which
// the operation model records as partial.
func (r Result) Observation() hostops.Observation {
	switch {
	case r.Refusal != nil:
		return hostops.Observation{P1: "not-performed"}
	case r.Failure != "":
		return hostops.Observation{P1: "failed", MeasuredChange: r.changed()}
	}
	_, job := jobMethods[r.Op]
	if job && r.JobResult == "" {
		return hostops.Observation{P1: "unknown"}
	}
	if !r.After.Measured {
		return hostops.Observation{P1: "performed"}
	}
	facts := r.postconditions()
	switch {
	case job && r.JobResult != "done":
		return hostops.Observation{P1: "failed", MeasuredChange: r.changed(), Postconditions: facts}
	case r.reached():
		return hostops.Observation{P1: "performed", Postconditions: facts}
	case job || r.changed():
		return hostops.Observation{P1: "partial", Postconditions: facts}
	}
	return hostops.Observation{P1: "not-performed", Postconditions: facts}
}

// Observation maps the logs act to the host operation's receipt outcome: performed, with no
// host effect.
func (l Logs) Observation() hostops.Observation {
	return hostops.Observation{P1: "performed", Postconditions: []string{"no host effect", "entries_read=" + strconv.Itoa(len(l.Entries))}}
}

// planned returns the measured field and value an effect's plan expects.
func planned(op string) (field, value string) {
	switch op {
	case OpStart, OpRestart, OpReload:
		return "ActiveState", "active"
	case OpStop:
		return "ActiveState", "inactive"
	case OpEnable:
		return "UnitFileState", "enabled"
	case OpDisable:
		return "UnitFileState", "disabled"
	}
	return "", ""
}

// reached reports whether the state measured after the effect is the plan's.
func (r Result) reached() bool {
	field, value := planned(r.Op)
	switch field {
	case "ActiveState":
		return r.After.Measured && r.After.ActiveState == value
	case "UnitFileState":
		return r.After.Measured && r.After.UnitFileState == value
	}
	return false
}

// changed reports whether the state measured after the effect differs from the one before.
func (r Result) changed() bool {
	return r.Before.Measured && r.After.Measured && (r.Before.ActiveState != r.After.ActiveState ||
		r.Before.SubState != r.After.SubState || r.Before.UnitFileState != r.After.UnitFileState)
}

// postconditions are the measured facts after the effect and the job's result, each a closed
// word from the service manager or "unmeasured".
func (r Result) postconditions() []string {
	var facts []string
	if r.JobResult != "" {
		facts = append(facts, "job_result="+word(r.JobResult))
	}
	facts = append(facts, "ActiveState="+word(r.After.ActiveState), "SubState="+word(r.After.SubState))
	if r.After.UnitFileState != "" {
		facts = append(facts, "UnitFileState="+word(r.After.UnitFileState))
	}
	return facts
}

// word returns s when it is a state word of lowercase letters, digits and hyphens, and
// "unmeasured" otherwise, so a postcondition never carries another byte.
func word(s string) string {
	if s == "" || len(s) > 64 {
		return "unmeasured"
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return "unmeasured"
		}
	}
	return s
}

// PlanDigest is the digest of the helper document an act sends, without its operation id:
// SHA-256 of {"op", "unit", "lines"}, lines only for logs. The plan shown and the plan applied
// are the same document.
func PlanDigest(op, unit string, lines int) string {
	doc := struct {
		Op    string `json:"op"`
		Unit  string `json:"unit,omitempty"`
		Lines int    `json:"lines,omitempty"`
	}{op, unit, lines}
	data, _ := json.Marshal(doc)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// NewCommand is the host operation command of one act on unit: module service, the act as its
// verb, the unit as its lock target and the plan digest of its document.
func NewCommand(operationID, taskID, op, unit string, lines int, mode, surface, actor string) hostops.Command {
	return hostops.Command{
		OperationID: operationID,
		TaskID:      taskID,
		Module:      Module,
		Verb:        op,
		Target:      "unit:" + unit,
		PlanDigest:  PlanDigest(op, unit, lines),
		Mode:        mode,
		Surface:     surface,
		Actor:       actor,
	}
}
