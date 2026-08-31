// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// A LOCAL OPERATOR is somebody acting through a locally authenticated path —
// the CLI on the host, the boot seeder, a SIGHUP from the service manager —
// rather than through an account and a credential. It has no user row, so it has
// no user id, and the audit ledger's actor string is derived from the user id.
//
// The consequence was that every one of those paths recorded the SAME subject.
// Principal.Actor() emits "user:" + UserID, and these principals carried no
// UserID, so `olivares secrets rotate`, `olivares sources rm`, `olivares
// superadmin enable`, the boot seeder and a host SIGHUP all appended events whose
// actor was the literal string "user:" — a colon and nothing else. Five distinct
// authorities collapsed into one anonymous subject, and the DisplayName each of
// them set ("cli:secrets", "boot/seed", …) was never read by anything: Actor()
// does not look at it. Meanwhile the product's own security model page says every
// privileged read is recorded with "who looked at which agent's access".
//
// A LocalOperator makes that attribution first-class and MANDATORY. It carries
// WHO (a human or service identity the operator asserts), HOW (the command path)
// and WHY (a reason), plus the host provenance the engine can capture on its own.
type LocalOperator struct {
	// Subject is WHO is acting: the identity the operator asserts for itself
	// (an email, a service account, a change ticket owner). The engine cannot
	// authenticate it — filesystem access is the authentication — so it is
	// recorded as an assertion, which is strictly more than the nothing that was
	// recorded before.
	Subject string
	// Via is HOW: the local path being used, e.g. "cli:secrets". It is set by the
	// command, never by the operator, so a caller cannot claim a path it is not on.
	Via string
	// Reason is WHY. It is required for the same reason Subject is: a privileged
	// offline mutation with no stated cause is the one an auditor most needs
	// explained, and the moment to ask is while somebody still knows the answer.
	Reason string
}

// Errors returned when a local operator cannot be attributed. They are refusals,
// not warnings: the alternative is appending an event that says a privileged
// write happened and cannot say who did it.
var (
	// ErrActorRequired is returned when no subject was asserted.
	ErrActorRequired = errors.New("actor is required for a privileged local operation")
	// ErrReasonRequired is returned when no reason was given.
	ErrReasonRequired = errors.New("reason is required for a privileged local operation")
	// ErrUnattributable is returned by the audit path when a principal cannot
	// produce a distinguishable subject. It is deliberately raised at the LEDGER
	// and not at the command: a future privileged path that forgets to identify
	// itself fails when it tries to record, rather than recording an anonymous
	// event that looks exactly like every other one.
	ErrUnattributable = errors.New("principal cannot be attributed in the audit ledger")
)

// maxLocalField bounds each recorded field. The ledger is evidence, not a
// message bus, and an unbounded operator-supplied string is how a reason becomes
// a payload.
const maxLocalField = 200

// NewLocalOperator builds the principal for a locally authenticated privileged
// operation. It is fail-closed by construction: without a subject and a reason
// there is no principal, so there is no operation.
//
// There is no backward-compatible mode. Nothing runs in production yet, so the
// incompatible change is free today and only gets more expensive; and a
// compatibility path here would be an opt-out from attribution, which is the
// whole defect.
func NewLocalOperator(op LocalOperator) (Principal, error) {
	subject := strings.TrimSpace(op.Subject)
	reason := strings.TrimSpace(op.Reason)
	via := strings.TrimSpace(op.Via)
	if subject == "" {
		return Principal{}, ErrActorRequired
	}
	if reason == "" {
		return Principal{}, ErrReasonRequired
	}
	if via == "" {
		return Principal{}, fmt.Errorf("%w: no local path declared", ErrUnattributable)
	}
	if len(subject) > maxLocalField || len(reason) > maxLocalField || len(via) > maxLocalField {
		return Principal{}, fmt.Errorf("actor, reason and path must each be at most %d characters", maxLocalField)
	}
	// Neither field may carry a separator that would let one field impersonate the
	// structure of the actor string.
	if strings.ContainsAny(subject, ":\n\r") || strings.ContainsAny(via, "\n\r") {
		return Principal{}, fmt.Errorf("%w: actor and path must not contain ':' or newlines", ErrUnattributable)
	}
	host, _ := os.Hostname()
	return Principal{
		Kind:         KindUser,
		Superadmin:   true,
		DisplayName:  via,
		localVia:     via,
		localSubject: subject,
		localMeta: map[string]any{
			// Namespaced so they can never collide with an action's own meta.
			"actor_subject": subject,
			"actor_via":     via,
			"actor_reason":  reason,
			"actor_host":    host,
			"actor_uid":     os.Getuid(),
			"actor_pid":     os.Getpid(),
		},
	}, nil
}

// localActorString is the audit subject of a local operator: the path it came in
// on and the identity it asserted. Both halves matter — "who" without "how"
// cannot tell an offline secret rotation from a SIGHUP reload.
func (p Principal) localActorString() string {
	return "local:" + p.localVia + ":" + p.localSubject
}

// IsLocalOperator reports whether this principal came in through a locally
// authenticated privileged path.
func (p Principal) IsLocalOperator() bool { return p.localVia != "" && p.localSubject != "" }

// AuditMeta returns the provenance a local operator carries — subject, path,
// reason, host, uid, pid — as a defensive copy, or nil for any other principal.
// The audit path merges it into every event this principal causes, so a new
// privileged command inherits the attribution instead of having to remember it.
func (p Principal) AuditMeta() map[string]any {
	if len(p.localMeta) == 0 {
		return nil
	}
	out := make(map[string]any, len(p.localMeta))
	for k, v := range p.localMeta {
		out[k] = v
	}
	return out
}

// AttributableActor returns the audit subject for this principal, or an error if
// it does not have one.
//
// This is the fail-closed half, and it is why the guard lives here rather than in
// a test that greps for a literal. A test asserting "no cmd_*.go contains
// DisplayName: <string>" is narrower than the thing it guards: it passes the day
// somebody builds the same anonymous principal from a variable, a helper or
// another package. The property that actually matters is behavioral — a
// principal that reaches the ledger must name somebody — so it is checked where
// the ledger is written.
func (p Principal) AttributableActor() (string, error) {
	if p.IsLocalOperator() {
		return p.localActorString(), nil
	}
	if p.Kind == KindToken {
		if p.CredID.IsZero() {
			return "", fmt.Errorf("%w: token principal with no credential id", ErrUnattributable)
		}
		return "token:" + p.CredID.String(), nil
	}
	if p.UserID.IsZero() {
		return "", fmt.Errorf("%w: user principal with no user id and no local operator", ErrUnattributable)
	}
	return "user:" + p.UserID.String(), nil
}

// NewSystemOperator builds the principal for a privileged path that NO HUMAN
// triggered: the boot seeder importing a config roster, a SIGHUP from the service
// manager. There is nobody to name, so nothing is asserted — the subject is
// derived by the engine from the host and process it is running as.
//
// It is a separate constructor, and it reports model.ActorSystem rather than
// model.ActorUser, because the distinction is exactly what was lost. Recording a
// boot-time seed as a human operator would be a more precise lie than the
// anonymous "user:" it replaces: an auditor filtering for human privileged
// activity would find an event nobody performed.
func NewSystemOperator(via, reason string) (Principal, error) {
	via = strings.TrimSpace(via)
	reason = strings.TrimSpace(reason)
	if via == "" || reason == "" {
		return Principal{}, fmt.Errorf("%w: a system path must declare its path and its trigger", ErrUnattributable)
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	p, err := NewLocalOperator(LocalOperator{Subject: host, Via: via, Reason: reason})
	if err != nil {
		return Principal{}, err
	}
	p.localSystem = true
	return p, nil
}

// IsSystemOperator reports whether this principal is an engine-triggered local
// path rather than a human one.
func (p Principal) IsSystemOperator() bool { return p.localSystem }
