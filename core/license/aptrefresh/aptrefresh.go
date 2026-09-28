// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package aptrefresh is the product side of the connect-v1 operation apt-refresh: the rules of
// `olivares license connect apt-refresh --cycle`, kept here so they are tested without the command.
//
// It holds the cycle and invocation rules, the checks of the service's answer, the outcome of each
// refresh path and the handoff document the appliance helper reads, with its custody-checked writer.
// It opens no connection and reads no connected-client state: cmd/olivares sends the operation and
// keeps its pending step.
//
// The contract is Interface Q3 r1-r3: the operation (r2 §3.1.1.1), the download credential (§3.1.1.4),
// the refresh paths and answer checks (§3.1.4, r3 §3.1.4.1), the cycle, invocation and handoff custody
// (r3 §3.1.5.1, §3.1.5.4, §3.1.5.5) and the handoff document (r2 §3.10 I5, r3 §3.10).
package aptrefresh

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

// HandoffDir is the one directory apt-refresh writes into. It is compiled into the product: no flag,
// environment variable or configuration names another, so no caller chooses a path.
const HandoffDir = "/run/olivares-apt-handoff"

// CodeCycleInvalid is the product's refusal of a --cycle that is not 32 lowercase hex characters.
const CodeCycleInvalid = "cycle_invalid"

// ErrCycleInvalid is the refusal of a cycle; its text leads with CodeCycleInvalid.
var ErrCycleInvalid = errors.New(CodeCycleInvalid + ": --cycle must be exactly 32 lowercase hexadecimal characters")

// id128Pattern is a cycle or a systemd invocation id: 128 bits as 32 lowercase hex characters. Such a
// value holds no character systemd escapes, so %i and %I agree on it, and it names a file safely.
var id128Pattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// CheckCycle refuses anything but exactly 32 characters of [0-9a-f]. The command checks it before any
// network call, state read, operation change or file write.
func CheckCycle(cycle string) error {
	if !id128Pattern.MatchString(cycle) {
		return ErrCycleInvalid
	}
	return nil
}

// Invocation is the handoff's invocation: $INVOCATION_ID when it is exactly 32 lowercase hex
// characters, otherwise nil (null). The helper refuses a null or unequal invocation, so a run by hand
// never produces a handoff it accepts.
func Invocation(getenv func(string) string) *string {
	id := getenv("INVOCATION_ID")
	if !id128Pattern.MatchString(id) {
		return nil
	}
	return &id
}

// Outcome is the handoff's outcome of one apt-refresh run.
type Outcome string

// The outcomes, one per refresh path (Interface Q3 r2 §3.1.4.1).
const (
	OutcomeIssued                Outcome = "issued"                  // R2-R5
	OutcomeNotBound              Outcome = "not_bound"               // R1: no active binding; nothing sent
	OutcomePendingOtherOperation Outcome = "pending_other_operation" // R6: another operation is pending; nothing sent
	OutcomeRefused               Outcome = "refused"                 // R7, R8, R11: a definitive refusal; its code names it
	OutcomeUnknown               Outcome = "unknown"                 // R9: the answer was lost or failed its checks
	OutcomeUnavailable           Outcome = "unavailable"             // R10: 5xx, timeout, or the service was not reached
	OutcomeIdentityMismatch      Outcome = "identity_mismatch"       // R12: the local key is not the bound key; nothing sent
	OutcomeBusy                  Outcome = "busy"                    // R12: another command holds the data directory
)

var outcomes = map[Outcome]bool{
	OutcomeIssued: true, OutcomeNotBound: true, OutcomePendingOtherOperation: true, OutcomeRefused: true,
	OutcomeUnknown: true, OutcomeUnavailable: true, OutcomeIdentityMismatch: true, OutcomeBusy: true,
}

// Refusal is a connect-v1 refusal as the client read it: the HTTP status, the refusal code and the
// reason token the body names beside it ("" for none, see connectv1.RefusalReason).
type Refusal interface {
	error
	RefusalStatus() int
	RefusalCode() connectv1.ErrorCode
	RefusalReason() string
}

// r7Reasons are the reasons with which authority_denied is an authenticated definitive refusal, R7
// (Interface Q3 r3 §3.1.5.4). The helper de-admits on R7, so no other reason reaches that code.
var r7Reasons = map[string]bool{"revoked": true, "refunded": true, "absent": true}

// OutcomeOf is the outcome and code of an attempt that issued nothing. The code is "" when there is none.
//
// A refusal with a 4xx other than 401 is refused, and the command ends the pending operation exactly for
// refused; a 401 is unknown, because a proof refusal is not definitive; a 5xx is unavailable. The one
// exception fails safe: authority_denied is refused only with reason revoked, refunded or absent (R7);
// with any other reason or none it is unknown with no code, so the operation is kept and the helper
// removes nothing. Without a refusal,
// an error that shows the service was not reached (DNS, dial) or that time ran out is unavailable, and
// anything else — a lost or unreadable answer, one that fails its checks — is unknown. A code outside the
// connect-v1 vocabulary is written as unknown, never echoed.
func OutcomeOf(err error) (Outcome, string) {
	var ref Refusal
	if errors.As(err, &ref) {
		code := string(ref.RefusalCode().Display())
		switch status := ref.RefusalStatus(); {
		case status == http.StatusUnauthorized:
			return OutcomeUnknown, code
		case status >= 400 && status < 500:
			if code == string(connectv1.ErrAuthorityDenied) && !r7Reasons[ref.RefusalReason()] {
				return OutcomeUnknown, ""
			}
			return OutcomeRefused, code
		case status >= 500:
			return OutcomeUnavailable, code
		}
		return OutcomeUnknown, code
	}
	if notReached(err) {
		return OutcomeUnavailable, ""
	}
	return OutcomeUnknown, ""
}

// notReached reports a failure to reach the service (name resolution or connection), or time running out.
func notReached(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
