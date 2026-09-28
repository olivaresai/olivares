// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// Status is the status read's answer: the confirmed policy and every window.
type Status struct {
	ConfirmedDigest string           `json:"confirmed_digest"`
	Confirmed       *policy.Document `json:"confirmed"`
	Windows         []Window         `json:"windows"`
}

// Answer answers one admitted document through the owner. A read answers the status; an act
// answers its window, performed when the owner performed it, refused with the owner's closed
// code when it refused before any effect, and failed when a load or read-back failed.
func Answer(ctx context.Context, o *Owner, r *Request) helperschema.Response {
	switch r.Op {
	case OpStatus:
		confirmed, ok, err := o.Confirmed()
		if err != nil {
			return failed("the confirmed policy cannot be read")
		}
		windows, err := o.Windows()
		if err != nil {
			return failed("the windows cannot be read")
		}
		status := Status{Windows: windows}
		if status.Windows == nil {
			status.Windows = []Window{}
		}
		if ok {
			status.ConfirmedDigest, status.Confirmed = policy.Digest(confirmed), &confirmed
		}
		return withBundle(helperschema.Response{Result: helperschema.ResultAnswered}, status)
	case OpApply:
		return windowAnswer(o.Apply(ctx, ApplyRequest{OperationID: r.OperationID, Candidate: *r.Candidate, App: r.App,
			RevertAfter: time.Duration(r.RevertAfterS) * time.Second}))
	case OpConfirm:
		return windowAnswer(o.Confirm(ctx, r.OperationID))
	case OpRevert:
		return windowAnswer(o.Revert(ctx, r.OperationID))
	}
	return helperschema.Response{Result: helperschema.ResultRefused, Code: helperschema.CodeInputRefused, Detail: "not a subcommand of this helper"}
}

func windowAnswer(w Window, err error) helperschema.Response {
	response := helperschema.Response{Result: helperschema.ResultPerformed}
	if err != nil {
		response = helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed, Detail: "the firewall owner failed"}
		var refusal *Refusal
		if errors.As(err, &refusal) {
			response.Code, response.Detail = refusal.Code, refusal.Detail
			switch refusal.Code {
			case CodeLoadFailed, CodeMeasurementFailed, CodeStateUnreadable:
			default:
				response.Result = helperschema.ResultRefused
			}
		}
	}
	if w.OperationID == "" {
		return response
	}
	return withBundle(response, w)
}

func withBundle(response helperschema.Response, v any) helperschema.Response {
	data, err := json.Marshal(v)
	if err != nil {
		return failed("the answer could not be encoded")
	}
	response.Bundle = data
	return response
}

func failed(detail string) helperschema.Response {
	return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed, Detail: detail}
}
