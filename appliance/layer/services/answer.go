// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

// CodeOutcomeUnknown marks an effect the service manager accepted whose job did not report
// within the bound.
const CodeOutcomeUnknown = "outcome_unknown"

// readTimeout bounds the calls of one read.
const readTimeout = 30 * time.Second

// Answer performs req with x and answers it in the helper seam's document. The data of each
// answer rides in the document's bundle member, at most MaxAnswerBytes: the inventory for list,
// the unit's status for status, the entries for logs, and for an effect its result and outcome.
// A refusal carries no data. An effect is "performed" when the service manager accepted it,
// whatever its job then reported; the data says which outcome the operation records, and a job
// that did not report is answered with outcome_unknown.
func Answer(ctx context.Context, x Executor, req *Request) helperschema.Response {
	switch req.Op {
	case OpList:
		ctx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()
		inventory, err := x.List(ctx)
		if err != nil {
			return refusedOrFailed(err)
		}
		return withData(helperschema.Response{Result: helperschema.ResultAnswered, Detail: "the loaded units and their classes"}, inventory)
	case OpStatus:
		ctx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()
		status, err := x.Status(ctx, req.Unit)
		if err != nil {
			return refusedOrFailed(err)
		}
		return withData(helperschema.Response{Result: helperschema.ResultAnswered, Detail: "the unit's class, what it admits and its state"}, status)
	case OpLogs:
		ctx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()
		logs, err := x.Logs(ctx, req.Unit, req.LogLines())
		if err != nil {
			return refusedOrFailed(err)
		}
		return withData(helperschema.Response{Result: helperschema.ResultPerformed, Detail: "the unit's journal entries were read; nothing on the host changed"}, logs)
	}
	res := x.Apply(ctx, req.Op, req.Unit)
	if res.Refusal != nil {
		return refused(res.Refusal)
	}
	effect := struct {
		Result
		Outcome string `json:"outcome"`
	}{res, outcome(res.Observation())}
	switch {
	case res.Failure != "":
		return withData(helperschema.Response{Result: helperschema.ResultFailed, Code: CodeEffectFailed,
			Detail: "the service manager did not accept the request; no job was queued"}, effect)
	case effect.Outcome == hostops.StateRunning:
		return withData(helperschema.Response{Result: helperschema.ResultPerformed, Code: CodeOutcomeUnknown,
			Detail: "the service manager accepted the request; its job did not report in time, so the operation stays running"}, effect)
	}
	return withData(helperschema.Response{Result: helperschema.ResultPerformed,
		Detail: "the service manager accepted the request; the outcome is measured from the unit's state"}, effect)
}

// outcome is the operation state the host operation model records for obs.
func outcome(obs hostops.Observation) string {
	switch obs.P1 {
	case "not-performed":
		return hostops.StateFailed
	case "partial":
		return hostops.StatePartial
	case "failed":
		if obs.MeasuredChange {
			return hostops.StatePartial
		}
		return hostops.StateFailed
	case "performed":
		if len(obs.Postconditions) > 0 {
			return hostops.StateSucceeded
		}
		return hostops.StatePartial
	}
	return hostops.StateRunning
}

// withData puts v in the answer's bundle. Data beyond MaxAnswerBytes is never sent.
func withData(resp helperschema.Response, v any) helperschema.Response {
	data, err := json.Marshal(v)
	if err != nil || len(data) > MaxAnswerBytes {
		resp.Code, resp.Detail = CodeOutcomeUnknown, "the answer's data exceeds its bound and is not sent"
		return resp
	}
	resp.Bundle = data
	return resp
}

// refusedOrFailed answers a read's error: its refusal, or a failure that changed nothing.
func refusedOrFailed(err error) helperschema.Response {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refused(refusal)
	}
	return helperschema.Response{Result: helperschema.ResultFailed, Code: CodeEffectFailed,
		Detail: "the service manager or the journal reader did not answer; nothing changed"}
}

// refused answers a refusal with its code, and names the class and its owner as fixed text.
func refused(r *Refusal) helperschema.Response {
	detail := r.Detail
	if r.Class != "" {
		detail += " Class: " + string(r.Class) + "; changed by " + r.Owner + "."
	}
	return helperschema.Response{Result: helperschema.ResultRefused, Code: r.Code, Detail: detail}
}
