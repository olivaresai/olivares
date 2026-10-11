// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ask.go answers an ask row of the decision table with the MCP 2026-07-28
// multi round-trip request (MRTR): instead of refusing a pending approval, the
// gateway returns an input_required tools/call result that asks the user to
// approve, and the client retries the same call with the sealed requestState.
//
// The requestState is attacker-controlled input (MRTR). It is sealed with
// AES-256-GCM under a per-process key, bound to the tenant, resource, subject,
// client, tool and plan hash of the call, valid for askStateTTL, and spent
// server-side on first use. It never authorizes anything by itself: the retry
// runs the whole gate again (kill switch, table, approval), and the approval
// gate spends the human approval for this one round trip (ConsumerID).
//
// Only a 2026-07-28 request that declares form elicitation gets this answer;
// every other client keeps the published refusal and its retry semantics.

const (
	// askStatePrefix marks a requestState this gateway sealed. The gateway strips
	// such a state (and its input response) from the forwarded call: it never
	// reaches the upstream, whose own MRTR states carry no such prefix.
	askStatePrefix = "olivares-ask-v1."
	askStateAAD    = "olivares.mcp.ask.v1"
	// askInputKey is the server-assigned inputRequests identifier of the approval.
	askInputKey = "olivares.approval"
	// askStateTTL bounds one round trip. A retry after it is refused; calling the
	// tool again asks afresh.
	askStateTTL = 10 * time.Minute
	// maxSpentAskStates bounds the single-use ledger's memory and
	// maxSpentAskStatesPerSubject one principal's share, so no subject can fill it
	// for the others. Entries leave when their state expires; a full share or
	// ledger refuses (deny-closed, 429).
	maxSpentAskStates           = 65536
	maxSpentAskStatesPerSubject = 256
)

// askBinding is the call a sealed state belongs to. A retry must match it
// exactly.
type askBinding struct {
	Tenant   string `json:"t"`
	Resource string `json:"r"`
	Subject  string `json:"s"`
	ClientID string `json:"c"`
	Tool     string `json:"n"`
	Plan     string `json:"p"`
}

type askState struct {
	askBinding
	ID      string `json:"id"`
	Expires int64  `json:"exp"`
}

// askStates seals and spends round-trip states. Its key lives only in this
// process: a restart, or another gateway node, refuses the state and the client
// calls again.
type askStates struct {
	aead   cipher.AEAD
	mu     sync.Mutex
	spent  map[string]spentAskState // by state ID
	shares map[string]int           // spent states per owner
}

type spentAskState struct {
	expires int64 // unix seconds
	owner   string
}

func newAskStates() (*askStates, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("mcp: rs: initialize approval round-trip key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mcp: rs: approval round-trip cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("mcp: rs: approval round-trip cipher: %w", err)
	}
	return &askStates{aead: aead, spent: map[string]spentAskState{}, shares: map[string]int{}}, nil
}

func (a *askStates) seal(b askBinding, now time.Time) (string, error) {
	if a == nil {
		return "", errAskStatesUnwired
	}
	id := make([]byte, 16)
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	plain, err := json.Marshal(askState{askBinding: b, ID: hex.EncodeToString(id), Expires: now.Add(askStateTTL).Unix()})
	if err != nil {
		return "", err
	}
	return askStatePrefix + base64.RawURLEncoding.EncodeToString(a.aead.Seal(nonce, nonce, plain, []byte(askStateAAD))), nil
}

var (
	errAskStateInvalid = errors.New("not a state this gateway issued")
	errAskStateCall    = errors.New("issued for another call")
	errAskStateExpired = errors.New("expired")
	errAskStateSpent   = errors.New("already used")
	errAskStateFull    = errors.New("too many approval round trips in flight")
	// errAskStatesUnwired: a server built without the sealer (the in-process
	// session tool server) offers no round trip and refuses a presented state.
	errAskStatesUnwired = errors.New("no approval round trip on this server")
)

// redeem opens a presented state, checks it belongs to this call and is still
// valid, and spends it. It returns the state's ID, the consumer identity the
// approval gate spends the human approval under.
func (a *askStates) redeem(token string, b askBinding, now time.Time) (string, error) {
	if a == nil {
		return "", errAskStatesUnwired
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, askStatePrefix))
	if err != nil || len(sealed) < a.aead.NonceSize() {
		return "", errAskStateInvalid
	}
	ns := a.aead.NonceSize()
	plain, err := a.aead.Open(nil, sealed[:ns], sealed[ns:], []byte(askStateAAD))
	if err != nil {
		return "", errAskStateInvalid
	}
	var st askState
	if err := json.Unmarshal(plain, &st); err != nil || st.ID == "" {
		return "", errAskStateInvalid
	}
	if st.askBinding != b {
		return "", errAskStateCall
	}
	if now.Unix() >= st.Expires {
		return "", errAskStateExpired
	}
	owner := b.Tenant + "\x00" + b.Subject
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, used := a.spent[st.ID]; used {
		return "", errAskStateSpent
	}
	full := func() bool {
		return len(a.spent) >= maxSpentAskStates || a.shares[owner] >= maxSpentAskStatesPerSubject
	}
	if full() {
		// Expired entries cannot be presented again; free them only when a limit
		// is reached, so the common path stays constant time.
		for id, e := range a.spent {
			if now.Unix() >= e.expires {
				delete(a.spent, id)
				if a.shares[e.owner]--; a.shares[e.owner] <= 0 {
					delete(a.shares, e.owner)
				}
			}
		}
		if full() {
			return "", errAskStateFull
		}
	}
	a.spent[st.ID] = spentAskState{expires: st.Expires, owner: owner}
	a.shares[owner]++
	return st.ID, nil
}

// askInputRequired is the tools/call result that asks for the approval, built
// with the official go-sdk types so the wire shape is the SDK's own: an
// InputRequiredResult whose one input request is a form elicitation with no
// fields — the human approves in Olivares and confirms in the client.
func askInputRequired(state, tool, approvalRef string) (json.RawMessage, error) {
	var res mcpsdk.CallToolResult
	// The SDK sets resultType only in its server pipeline or when decoding; the
	// decoder is the exported way to mark this result input_required.
	if err := json.Unmarshal([]byte(`{"resultType":"input_required"}`), &res); err != nil {
		return nil, err
	}
	// The message is for the person at the client. It names the request so they
	// can find it, and no command: an agent that reads it must not be handed a
	// way to approve its own call.
	approve := "Approve it in the Olivares console"
	if approvalRef != "" {
		approve = fmt.Sprintf("Approve request %s in the Olivares console", approvalRef)
	}
	res.InputRequests = mcpsdk.InputRequestMap{askInputKey: &mcpsdk.ElicitParams{
		Mode:            "form",
		Message:         fmt.Sprintf("Olivares needs a human approval before it runs tool %q. %s, then accept here.", tool, approve),
		RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}}
	res.RequestState = state
	// Content stays nil: the go-sdk server sends "content":null in an
	// input_required result too, and the official client reads it.
	return json.Marshal(&res)
}

// askAccepted reports whether the client user accepted the approval request,
// and a fixed label of the answer for the audit: the action is client text, so
// it never reaches the record as sent. Accepting is not an approval: the gate
// still requires the human decision.
func askAccepted(response json.RawMessage) (bool, string) {
	if len(response) == 0 {
		return false, "missing"
	}
	var res mcpsdk.ElicitResult
	if err := json.Unmarshal(response, &res); err != nil {
		return false, "unreadable"
	}
	switch res.Action {
	case "accept":
		return true, "accept"
	case "decline", "cancel":
		return false, res.Action
	}
	return false, "unrecognized"
}

// askApproval is the approval that let an ask row through.
type askApproval struct{ ref, plan, grantMode string }

// authorizeAsk runs the approval of an ask row: a destructive tool
// (server-owned classification, NOT the tool's UNTRUSTED annotation) or a Cedar
// condition requires an ApprovalGate authorization bound to the (tool, subject,
// args-shape) plan. Deny-closed on any gate error or non-approval.: the plan
// binds the CANONICAL argument digest — the same argument identity the
// EffectDigest binds, so approval and evidence can never disagree about which
// arguments were authorized. It returns the approval, or the refusal or round
// trip that answers the call instead; rc marks a 2026-07-28 request.
func (rs *ResourceServer) authorizeAsk(ctx context.Context, c gatedCall, rc bool, canon canonicalToolCallParams, policy ToolPolicy) (askApproval, *refusal) {
	approvalNeed, notApproved := "destructive tool requires human approval", "destructive tool not approved"
	if !policy.Destructive {
		approvalNeed, notApproved = "these tool arguments require human approval", "conditional tool call not approved"
	}
	plan := toolCallPlanHash(c.tool, c.tok.Subject, hashArgs(canon.Args))
	roundTrip := askBinding{Tenant: rs.tenant, Resource: rs.resource, Subject: c.tok.Subject, ClientID: c.tok.ClientID, Tool: c.tool, Plan: plan}
	// A retry of this gateway's approval round trip (ask.go): the state must be
	// one this process sealed for exactly this call, unexpired and unused, and
	// the client user must have accepted. It authorizes nothing by itself; it
	// names the round trip whose human approval the gate spends.
	consumer := ""
	if canon.AskState != "" {
		id, rerr := rs.asks.redeem(canon.AskState, roundTrip, rs.clock())
		if rerr != nil {
			reason := "approval round trip refused: " + rerr.Error()
			if errors.Is(rerr, errAskStateFull) {
				return askApproval{}, rs.deny(c, reason, "MCP07", http.StatusTooManyRequests, rpcAccessDenied,
					"too many approval round trips are in flight; call the tool again later")
			}
			return askApproval{}, rs.deny(c, reason, "MCP07", http.StatusForbidden, rpcAccessDenied,
				"the approval round trip is invalid, expired or already used; call the tool again")
		}
		if ok, action := askAccepted(canon.AskResponse); !ok {
			return askApproval{}, rs.deny(c, "approval round trip not accepted by the client user ("+action+")", "MCP02",
				http.StatusForbidden, rpcAccessDenied, "human approval was not confirmed in the client")
		}
		consumer = id
	}
	dec, gerr := rs.gate.Authorize(ctx, ToolApprovalRequest{
		Tenant: rs.tenant, Subject: c.tok.Subject, Tool: c.tool,
		Scope: policy.RequiredScope, PlanHash: plan, RequestedBy: c.tok.Subject, Rule: c.row.RuleID,
		Arguments:  append(json.RawMessage(nil), canon.Args...),
		ConsumerID: consumer,
	})
	if gerr != nil {
		if errors.Is(gerr, ErrArgumentsNotReviewable) {
			return askApproval{}, rs.deny(c, "argument not reviewable", "MCP07", http.StatusForbidden, rpcAccessDenied, "argument not reviewable")
		}
		return askApproval{}, rs.deny(c, "gate error (fail-closed)", "MCP07", http.StatusForbidden, rpcAccessDenied, "approval gate error")
	}
	// Review round 2 (blocker 4, same class as S5-05): the equality is
	// STRICT. `plan` is always non-empty (a canonical argument digest), so an
	// approval carrying an EMPTY PlanHash — bound to no plan — is not an approval
	// for THIS plan. The prior `PlanHash != "" &&` guard let an unbound approval
	// authorize a destructive tools/call.
	if !dec.Allowed() || dec.PlanHash != plan {
		reason := notApproved + " (" + string(dec.Status) + ")"
		// A 2026-07-28 client that can show a form gets the approval round trip
		// instead of the refusal; every other client keeps the published 403.
		if dec.Status == StatusPending && rc && canon.DeclaresFormElicitation {
			result, err := rs.askResult(roundTrip, c.tool, dec.ApprovalRef)
			if err == nil {
				return askApproval{}, &refusal{record: rs.verdictRecord(c, false, reason+"; approval round trip offered", dec.ApprovalRef, "MCP02"), result: result}
			}
			reason += "; approval round trip unavailable: " + err.Error()
		}
		return askApproval{}, &refusal{record: rs.verdictRecord(c, false, reason, dec.ApprovalRef, "MCP02"),
			status: http.StatusForbidden, code: rpcAccessDenied, message: approvalNeed + " (" + string(dec.Status) + ")"}
	}
	granted := askApproval{ref: dec.ApprovalRef, plan: plan}
	if consumer != "" {
		// The round trip's grant is the spend itself; a gate that approved
		// without spending would let the same approval complete it again.
		if !dec.Spent {
			return askApproval{}, &refusal{record: rs.verdictRecord(c, false, "approval gate did not spend the approval for the round trip (fail-closed)", dec.ApprovalRef, "MCP07"),
				status: http.StatusForbidden, code: rpcAccessDenied, message: "approval gate error"}
		}
		granted.grantMode = "round_trip"
	}
	return granted, nil
}

// askResult seals a round-trip state for a pending approval and builds the
// input_required result that carries it. On an error the caller keeps the
// published refusal and records why.
func (rs *ResourceServer) askResult(b askBinding, tool, approvalRef string) (json.RawMessage, error) {
	state, err := rs.asks.seal(b, rs.clock())
	if err != nil {
		return nil, fmt.Errorf("seal state: %w", err)
	}
	result, err := askInputRequired(state, tool, approvalRef)
	if err != nil {
		return nil, fmt.Errorf("build result: %w", err)
	}
	return result, nil
}
