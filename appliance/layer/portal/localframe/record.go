// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package localframe owns the bounded, credential-aware local record transport.
// It does not buffer a subsequent record or grant an operation.
package localframe

import (
	"encoding/json"
	"errors"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

// Expect identifies the account that must send every byte. PID zero permits any
// positive sender PID for client-side portal account authentication only.
type Expect struct {
	UID uint32
	PID int32
}

// Record is the closed transport union. Body has the operation's own closed schema.
type Record struct {
	Type    string          `json:"t"`
	Version int             `json:"v,omitempty"`
	Op      string          `json:"op,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
	Status  int             `json:"status,omitempty"`
	Code    string          `json:"code,omitempty"`
	Reason  string          `json:"reason,omitempty"`
}

// Fault contains a closed protocol reason, never received input or credentials.
type Fault struct{ Reason string }

func (e *Fault) Error() string  { return e.Reason }
func fault(reason string) error { return &Fault{Reason: reason} }

func parseRecord(data []byte) (Record, error) {
	var fields map[string]json.RawMessage
	if hostops.DecodeClosed(data, &fields) != nil {
		return Record{}, fault("record_malformed")
	}
	var r Record
	if hostops.DecodeClosed(data, &r) != nil {
		return Record{}, fault("record_malformed")
	}
	var required, optional []string
	switch r.Type {
	case "ready":
		required = []string{"t", "v"}
		if r.Version != 1 {
			return Record{}, fault("record_malformed")
		}
	case "refused":
		required = []string{"t", "code"}
		if !contains(r.Code, "local_peer_refused", "local_capacity", "pidfd_unproven", "passcred_unset") {
			return Record{}, fault("record_malformed")
		}
	case "request":
		required = []string{"t", "op", "body"}
	case "response":
		required = []string{"t", "op", "status", "body"}
	case "error":
		required = []string{"t", "op", "status", "code"}
		optional = []string{"reason"}
		if !contains(r.Code, "input_refused", "mode_refused", "consumer_unavailable", "act_not_adopted", "p1_stage_missing", "pidfd_unproven", "operation_unknown", "target_locked", "plan_changed") {
			return Record{}, fault("record_malformed")
		}
	case "ended":
		required = []string{"t", "reason"}
	default:
		return Record{}, fault("record_malformed")
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return Record{}, fault("record_malformed")
		}
	}
	for name := range fields {
		if !contains(name, append(required, optional...)...) {
			return Record{}, fault("record_malformed")
		}
	}
	if r.Type == "request" || r.Type == "response" || r.Type == "error" {
		if !contains(r.Op, "operation.get", "task.list", "task.describe", "task.plan", "task.apply", "handoff.register", "module.read") {
			return Record{}, fault("record_malformed")
		}
	}
	if r.Type == "response" && r.Status != 200 && r.Status != 202 {
		return Record{}, fault("record_malformed")
	}
	if r.Type == "error" && !containsInt(r.Status, 400, 403, 404, 409, 422, 503) {
		return Record{}, fault("record_malformed")
	}
	if r.Reason != "" && !contains(r.Reason, "owner_exited", "connection_closed", "portal_stopping", "protocol_violation", "peer_not_owner", "pipelined_request", "bytes_before_ready") {
		return Record{}, fault("record_malformed")
	}
	if r.Type == "ended" && r.Reason == "" {
		return Record{}, fault("record_malformed")
	}
	if len(r.Body) > 0 && (r.Body[0] != '{' || !json.Valid(r.Body)) {
		return Record{}, fault("record_malformed")
	}
	return r, nil
}
func contains(s string, values ...string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}
func containsInt(n int, values ...int) bool {
	for _, v := range values {
		if v == n {
			return true
		}
	}
	return false
}

// Reason returns a protocol reason without exposing an OS error.
func Reason(err error) string {
	var f *Fault
	if errors.As(err, &f) {
		return f.Reason
	}
	return "connection_closed"
}
