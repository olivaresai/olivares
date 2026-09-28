// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "slices"

// PAMMeasurement is what this console's sign-in stack was measured to be. Its zero value
// is an unusable, unmeasured stack, so a stack nobody measured opens no door.
type PAMMeasurement struct {
	// Usable reports that the stack the console's service name selects was found and can
	// be used, and that the named administrators group exists on this host.
	Usable bool
	// Reason is a fixed sentence naming the failed check when the stack is not usable. A
	// page states it, so it carries no path and no credential material.
	Reason string
}

// Transaction is one transaction on the host's sign-in stack, in the shape the C interface
// gives it: it is opened for a service and a login, the human's credential is checked
// against the stack's auth group, the account is checked against its account group, and it
// is closed exactly once.
//
// The console never sees the credential. The stack carries it from the human through the
// transaction's own conversation, which is why this console can hold no password of its
// own — and why a fake transaction is a complete test double, so that no case here needs a
// stack, a container or any privilege.
type Transaction interface {
	// Authenticate runs the stack's auth group and answers only whether this is the human
	// they say they are.
	Authenticate() error
	// Account runs the stack's account group: whether this account may be used now — not
	// expired, not locked, and admitted by the stack's own rules.
	Account() error
	// Close ends the transaction. It is called exactly once, on every path, including
	// every refusal.
	Close() error
}

// Stack is this console's whole view of the host's human sign-in facts, behind one
// interface: whether the stack the console's service name selects can be used, a
// transaction on it, and the local groups the host records for a login. Nothing else about
// the host reaches this package.
//
// The implementation that speaks to a real stack belongs to the host adapters of this
// layer. None exists yet, and its verification against a real stack is pending: it is for
// a container job on a hosted Linux runner, and until that job records a result the
// stack's behavior on a host is unverified. Every case here runs against a fake.
type Stack interface {
	// Measure reports whether this console's stack can be used.
	//
	// An implementation requires the service file to be present and readable and the named
	// administrators group to exist, and reports neither of those as an empty stack: on a
	// Debian host a service with no file of its own falls back to a stack that includes
	// the host's common authentication scheme, so an absent file would admit every local
	// account rather than nobody. Absence is unusable here, never permissive.
	Measure() PAMMeasurement
	// Start opens a transaction for login on the console's service.
	Start(login string) (Transaction, error)
	// LocalGroups names the local groups the host records for login. It is a fact the host
	// keeps, not a decision this package makes.
	LocalGroups(login string) ([]string, error)
}

// PAM is this console's adapter onto the host's sign-in stack: the one credential check
// the console performs while the product is down, and the only one anywhere in it.
//
// It admits nobody by itself. Its admit step is unexported on purpose, so a sign-in enters
// through Selector.SignIn and passes the product measurement first.
type PAM struct {
	stack Stack
}

// NewPAM returns the adapter over the host's stack. The group it admits is not a parameter:
// it is AdministratorsGroup, so no caller can widen it. A nil stack is an unusable stack,
// which refuses every sign-in.
func NewPAM(stack Stack) *PAM { return &PAM{stack: stack} }

// measure reports whether the stack can be used. A nil adapter, a nil stack and a stack
// that reports itself unusable without saying why all answer unusable, with a sentence.
func (p *PAM) measure() PAMMeasurement {
	if p == nil || p.stack == nil {
		return PAMMeasurement{Reason: reasonNoStack}
	}
	measured := p.stack.Measure()
	if measured.Usable {
		return PAMMeasurement{Usable: true}
	}
	if measured.Reason == "" {
		measured.Reason = reasonStackUnusable
	}
	return PAMMeasurement{Reason: measured.Reason}
}

// admit authenticates one human on the host's stack and returns what the repair mode
// offers them.
//
// The group check comes first, before any credential is checked. A login the host does not
// place in the named administrators group can never be admitted here, so their credential
// is not presented to the stack at all: nothing about them is put to the host's
// authentication scheme, and nothing is offered back. Every refusal is the same phrase.
func (p *PAM) admit(login string) (Offer, error) {
	if measured := p.measure(); !measured.Usable {
		return Offer{}, ErrConsoleRemains
	}
	groups, err := p.stack.LocalGroups(login)
	if err != nil || !slices.Contains(groups, AdministratorsGroup) {
		return Offer{}, ErrNotAdmitted
	}
	transaction, err := p.stack.Start(login)
	if err != nil || transaction == nil {
		return Offer{}, ErrNotAdmitted
	}
	if err := check(transaction); err != nil {
		return Offer{}, err
	}
	return Offer{Mode: ProductDownRepair, Login: login, Verbs: RepairVerbs()}, nil
}

// check runs a transaction's auth and account groups and closes it exactly once, whatever
// either of them answered.
func check(transaction Transaction) error {
	defer func() { _ = transaction.Close() }()
	if err := transaction.Authenticate(); err != nil {
		return ErrNotAdmitted
	}
	if err := transaction.Account(); err != nil {
		return ErrNotAdmitted
	}
	return nil
}
