// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ToolFlowCall names the managed server and tool one tools/call targets.
// Identifiers only, never an argument or a result. The flow itself belongs to
// one exact session launch, so it needs no session or tenant here.
type ToolFlowCall struct {
	Server string
	Tool   string
}

// ToolFlowRefusal is a ToolDataFlow's deny-closed answer to a restore. Class
// names the kind of value (never the value) and Reason is the sentence the
// caller reads, so it must carry no argument content. Any other error from a
// ToolDataFlow also refuses the call, with a fixed message.
type ToolFlowRefusal struct {
	Class  string
	Reason string
}

func (r *ToolFlowRefusal) Error() string { return r.Reason }

// ToolDataFlow is the seam for reversible tokens in tool data (Business: AI
// Runtime Security). It sits on the session tool server only, which refuses MCP
// Tasks, so every result it relays passes through TokenizeResult. A nil flow
// leaves arguments and results untouched, exactly as before the add-on.
//
// The call's arguments and the approval, evidence and audit of the call are all
// the tokenized form. RestoreArguments produces the form forwarded upstream and
// TokenizeResult the form the caller reads; the restored bytes exist only in the
// memory of that one call. Counts map a class to how many values were handled;
// they are what the audit trail records.
type ToolDataFlow interface {
	// RestoreArguments runs after the tool is admitted and before any approval or
	// evidence claim, so a refusal asks nobody and anchors nothing. It returns the
	// arguments to forward and the counts it restored; arguments is nil when the
	// call has none.
	RestoreArguments(ctx context.Context, call ToolFlowCall, arguments json.RawMessage) (json.RawMessage, map[string]int, error)
	// TokenizeResult runs on every result the upstream completed, before the
	// settlement digest, the mediation and the relay. An error withholds the result.
	TokenizeResult(ctx context.Context, call ToolFlowCall, result json.RawMessage) (json.RawMessage, map[string]int, error)
}

// restoreArguments applies the flow to a canonicalized call. It returns the
// params to forward (canon.Forward itself when nothing was restored), the counts
// to audit, and a refusal when the call may not run. A call without arguments
// still asks the flow (with nil), so a flow that cannot run refuses it before
// its effect rather than withholding its result after.
func (rs *ResourceServer) restoreArguments(ctx context.Context, c gatedCall, canon canonicalToolCallParams) (json.RawMessage, map[string]int, *refusal) {
	if rs.dataFlow == nil {
		return canon.Forward, nil, nil
	}
	unavailable := func(reason string) *refusal {
		return rs.deny(c, reason, "MCP02", 403, rpcAccessDenied, "tool call refused: reversible tokens unavailable")
	}
	args, counts, err := rs.dataFlow.RestoreArguments(ctx, ToolFlowCall{Server: rs.flowServer, Tool: c.tool}, canon.Args)
	if err != nil {
		var deny *ToolFlowRefusal
		if errors.As(err, &deny) {
			return nil, nil, rs.deny(c, "token restore refused: class "+deny.Class+": "+deny.Reason, "MCP02",
				403, rpcAccessDenied, "tool call refused: "+deny.Reason)
		}
		return nil, nil, unavailable("token restore unavailable (fail-closed)")
	}
	if string(args) == string(canon.Args) {
		return canon.Forward, counts, nil
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(canon.Forward, &params); err != nil {
		return nil, nil, unavailable("restored arguments could not be placed (fail-closed)")
	}
	params["arguments"] = args
	forward, err := json.Marshal(params)
	if err != nil {
		return nil, nil, unavailable("restored arguments could not be placed (fail-closed)")
	}
	return forward, counts, nil
}

// tokenizeResult returns the result the caller may read. A flow that cannot
// tokenize withholds the result: the upstream effect already ran, so the answer
// is a tool error that carries nothing.
func (rs *ResourceServer) tokenizeResult(ctx context.Context, tool string, result json.RawMessage) (json.RawMessage, map[string]int, error) {
	out, counts, err := rs.dataFlow.TokenizeResult(ctx, ToolFlowCall{Server: rs.flowServer, Tool: tool}, result)
	if err != nil {
		return json.RawMessage(`{"content":[{"type":"text","text":"tool result withheld: reversible tokens unavailable"}],"isError":true}`), nil, err
	}
	return out, counts, nil
}

// flowCounts renders counts as "class=n" in class order, the only form of a
// handled value an audit reason may carry.
func flowCounts(counts map[string]int) string {
	classes := make([]string, 0, len(counts))
	for class, n := range counts {
		if n > 0 {
			classes = append(classes, class)
		}
	}
	sort.Strings(classes)
	parts := make([]string, len(classes))
	for i, class := range classes {
		parts[i] = fmt.Sprintf("%s=%d", class, counts[class])
	}
	return strings.Join(parts, " ")
}
