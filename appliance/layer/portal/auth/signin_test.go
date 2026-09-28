// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSignIn_PamRefusedWhileProductIsReachable(t *testing.T) {
	stack := usableStack()
	selector := NewSelector(reachable(true), NewPAM(stack))

	state := selector.Select()
	if state.Mode != ProductUp || !state.OffersEverything() {
		t.Fatalf("a product that answers decides by its own identity: %+v", state)
	}
	if statement := state.Statement(); !strings.Contains(statement, string(ProductUp)) {
		t.Errorf("the page states %q, which does not name the mode", statement)
	}
	if verbs := state.NetworkVerbs(); len(verbs) != 0 {
		t.Errorf("the product decides act by act, so the mode lists no verbs: %v", verbs)
	}

	offer, err := selector.SignIn(administrator)
	if !errors.Is(err, ErrProductIsTheRoute) {
		t.Fatalf("a PAM sign-in while the product answers = %v, want the product's route", err)
	}
	assertNoOffer(t, offer)
	if stack.started != 0 || stack.authenticated != 0 || stack.groupsRead != 0 {
		t.Fatalf("PAM was asked while the product answers: %+v", stack)
	}

	// Control: the same stack admits the same human as soon as the product stops
	// answering, so the refusal above is the reachability check and not a fake that
	// refuses everybody.
	down := NewSelector(reachable(false), NewPAM(stack))
	if _, err := down.SignIn(administrator); err != nil {
		t.Fatalf("control: the repair mode must admit the administrator: %v", err)
	}
	if stack.authenticated != 1 {
		t.Fatalf("control: the repair mode checked %d credentials, want 1", stack.authenticated)
	}

	// An unmeasured product is not a product that is down. Nothing is offered over the
	// network, and above all PAM is not.
	for name, s := range map[string]*Selector{
		"no probe wired":     NewSelector(nil, NewPAM(stack)),
		"the refusing probe": NewSelector(NoProductProbe{}, NewPAM(stack)),
		"no selector at all": nil,
	} {
		t.Run(name, func(t *testing.T) {
			if got := s.Select().Mode; got != Unavailable {
				t.Errorf("an unmeasured product selected %q", got)
			}
			offer, err := s.SignIn(administrator)
			if !errors.Is(err, ErrConsoleRemains) {
				t.Errorf("an unmeasured product answered %v, want the console on tty1", err)
			}
			assertNoOffer(t, offer)
		})
	}
	if stack.authenticated != 1 {
		t.Errorf("an unmeasured product presented a credential to PAM: %+v", stack)
	}
}

func TestSignIn_ProductDownOffersRepairVerbsOnly(t *testing.T) {
	stack := usableStack()
	selector := NewSelector(reachable(false), NewPAM(stack))

	state := selector.Select()
	if state.Mode != ProductDownRepair || state.OffersEverything() {
		t.Fatalf("a product that does not answer offers the repair verbs only: %+v", state)
	}
	if !slices.Equal(state.NetworkVerbs(), RepairVerbs()) {
		t.Fatalf("the repair mode offers %v, want %v", state.NetworkVerbs(), RepairVerbs())
	}
	if statement := state.Statement(); !strings.Contains(statement, string(ProductDownRepair)) ||
		!strings.Contains(statement, AdministratorsGroup) {
		t.Errorf("the page states %q, which names neither the mode nor the group", statement)
	}

	offer, err := selector.SignIn(administrator)
	if err != nil {
		t.Fatalf("a member of %s must be admitted while the product is down: %v", AdministratorsGroup, err)
	}
	if offer.Mode != ProductDownRepair || offer.Login != administrator {
		t.Errorf("the offer does not carry the mode and the human: %+v", offer)
	}
	if !slices.Equal(offer.Verbs, RepairVerbs()) {
		t.Errorf("the offer carries %v, want exactly the repair verbs %v", offer.Verbs, RepairVerbs())
	}
	for _, v := range []Verb{VerbVPN, VerbRepairPortalPAM} {
		if slices.Contains(offer.Verbs, v) {
			t.Errorf("the repair mode offers %q, which is not a repair verb", v)
		}
	}
	if stack.closed != stack.started || stack.started != 1 {
		t.Errorf("one transaction, opened and closed: %+v", stack)
	}

	// Widening the returned list must not widen the next offer.
	offer.Verbs[0] = VerbRepairPortalPAM
	if next, err := selector.SignIn(administrator); err != nil || !slices.Equal(next.Verbs, RepairVerbs()) {
		t.Errorf("an offer that was written to changed the next one: %v %+v", err, next)
	}

	// In this mode a step-up is a re-authentication, because no policy engine is running:
	// asking again opens another transaction rather than reading a decision kept here.
	if stack.authenticated != 2 || stack.closed != 2 {
		t.Errorf("a second ask must be a second transaction: %+v", stack)
	}

	// A human the host does not place in the named group is offered nothing, and their
	// credential is never presented to the stack at all.
	before := stack.authenticated
	offer, err = selector.SignIn(outsider)
	if !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("a human outside %s answered %v, want a refusal", AdministratorsGroup, err)
	}
	assertNoOffer(t, offer)
	if stack.authenticated != before {
		t.Errorf("the outsider's credential was presented to the stack: %+v", stack)
	}
	if !stack.admits[outsider] {
		t.Fatal("control: the fake must admit the outsider, so that only the group check refuses them")
	}

	// A login the host's group database does not know is refused with the same phrase, so
	// a refusal is not an oracle about which check refused.
	if _, err := selector.SignIn("nobody"); !errors.Is(err, ErrNotAdmitted) {
		t.Errorf("an unknown login answered %v, want the same refusal", err)
	}

	// Nothing is stored, so a stack that stops admitting refuses the next ask.
	stack.admits[administrator] = false
	if _, err := selector.SignIn(administrator); !errors.Is(err, ErrNotAdmitted) {
		t.Errorf("a sign-in was answered from something kept here: %v", err)
	}
	stack.admits[administrator] = true
	stack.account[administrator] = false
	if _, err := selector.SignIn(administrator); !errors.Is(err, ErrNotAdmitted) {
		t.Errorf("an account the stack refuses was admitted: %v", err)
	}
	if stack.closed != stack.started {
		t.Errorf("a refused transaction was left open: %+v", stack)
	}

	// With the product down and the stack unusable, nobody signs in over the network and
	// the refusal names the door that remains.
	broken := unusableStack()
	last := NewSelector(reachable(false), NewPAM(broken))
	state = last.Select()
	if state.Mode != Unavailable || len(state.NetworkVerbs()) != 0 {
		t.Fatalf("an unusable stack must offer nothing over the network: %+v", state)
	}
	if statement := state.Statement(); !strings.Contains(statement, "tty1") {
		t.Errorf("the page states %q, which does not name the remaining door", statement)
	}
	offer, err = last.SignIn(administrator)
	if !errors.Is(err, ErrConsoleRemains) {
		t.Fatalf("an unusable stack answered %v, want the console on tty1", err)
	}
	assertNoOffer(t, offer)
	if broken.started != 0 || broken.authenticated != 0 {
		t.Errorf("an unusable stack was asked for a transaction: %+v", broken)
	}
	if _, err := NewSelector(reachable(false), nil).SignIn(administrator); !errors.Is(err, ErrConsoleRemains) {
		t.Error("no PAM adapter at all must refuse over the network too")
	}

	// The stack the layer ships is the stack this package names, and it admits the group
	// this package names: a drift between the two would admit a group nobody decided on.
	t.Run("the shipped stack names this service and this group", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("..", "units", "pam.d", PAMService))
		if err != nil {
			t.Fatal(err)
		}
		if shipped := string(data); !strings.Contains(shipped, "ingroup "+AdministratorsGroup) {
			t.Errorf("the shipped stack does not admit %q:\n%s", AdministratorsGroup, shipped)
		}
	})
}
