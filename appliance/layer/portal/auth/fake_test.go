// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"testing"
)

// The two humans every case below uses: one the host records as a member of the named
// local administrators group, one it does not. Neither name is a product identity.
const (
	administrator = "ada"
	outsider      = "kim"
)

var errStackRefused = errors.New("the stack refused")

// fakeStack is a PAM stack with no PAM in it. It answers from what a case recorded, counts
// what it was asked, and reports whether every transaction was closed, so a case can state
// not only who was admitted but what was asked of the host on the way.
//
// It is the only credential check in this package's tests. A fake transaction needs no PAM
// stack, no container and no privilege, which is why these cases run anywhere.
type fakeStack struct {
	measurement PAMMeasurement
	// groups is the host's group database: the local groups it records for a login.
	groups map[string][]string
	// admits records, per login, whether the human answers the stack's conversation with
	// the credential the host holds. The credential itself is not here and is not a value
	// this package can hold: only PAM ever sees one.
	admits map[string]bool
	// account records, per login, whether the stack's account group admits the login.
	account map[string]bool

	measured      int
	groupsRead    int
	started       int
	authenticated int
	accounted     int
	closed        int
}

// usableStack returns a stack that would admit the administrator and, if it were ever
// asked, the outsider too: so a refusal of the outsider is the group check refusing and
// not the fake declining to authenticate them.
func usableStack() *fakeStack {
	return &fakeStack{
		measurement: PAMMeasurement{Usable: true},
		groups: map[string][]string{
			administrator: {"ada", AdministratorsGroup},
			outsider:      {"kim", "users"},
		},
		admits:  map[string]bool{administrator: true, outsider: true},
		account: map[string]bool{administrator: true, outsider: true},
	}
}

// unusableStack returns a stack that cannot be used at all, as an absent or unreadable
// service file leaves one.
func unusableStack() *fakeStack {
	return &fakeStack{measurement: PAMMeasurement{Reason: "This console's PAM stack is absent."}}
}

func (s *fakeStack) Measure() PAMMeasurement {
	s.measured++
	return s.measurement
}

func (s *fakeStack) LocalGroups(login string) ([]string, error) {
	s.groupsRead++
	groups, ok := s.groups[login]
	if !ok {
		return nil, errStackRefused
	}
	return groups, nil
}

func (s *fakeStack) Start(login string) (Transaction, error) {
	s.started++
	return &fakeTransaction{stack: s, login: login}, nil
}

// fakeTransaction is one transaction on a fakeStack, in the shape the C interface gives a
// PAM transaction: authenticate, check the account, close exactly once.
type fakeTransaction struct {
	stack *fakeStack
	login string
}

func (t *fakeTransaction) Authenticate() error {
	t.stack.authenticated++
	if !t.stack.admits[t.login] {
		return errStackRefused
	}
	return nil
}

func (t *fakeTransaction) Account() error {
	t.stack.accounted++
	if !t.stack.account[t.login] {
		return errStackRefused
	}
	return nil
}

func (t *fakeTransaction) Close() error {
	t.stack.closed++
	return nil
}

// product reports the product as measured and answering, or measured and silent.
type product ProductMeasurement

func (p product) MeasureProduct() ProductMeasurement { return ProductMeasurement(p) }

// reachable returns a probe that measured the product and found it answering, or not.
func reachable(answers bool) product { return product{Measured: true, Answers: answers} }

// assertNoOffer fails when an offer carries anything at all: a refused sign-in is offered
// no mode, no login and no verb.
func assertNoOffer(t *testing.T, o Offer) {
	t.Helper()
	if o.Mode != "" || o.Login != "" || len(o.Verbs) != 0 {
		t.Fatalf("a refused sign-in was offered something: %+v", o)
	}
}
