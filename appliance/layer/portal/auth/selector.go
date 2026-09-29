// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "errors"

// ProductMeasurement is what a ProductProbe observed of the product on this host.
//
// Its zero value is unmeasured, and unmeasured is not down. The difference is the whole
// point of the type: mistaking a product nobody measured for a product that is down is
// exactly what would offer the host's sign-in stack while the product is answering.
type ProductMeasurement struct {
	// Measured reports that a probe ran and reached a verdict.
	Measured bool
	// Answers reports that the product answered that probe.
	Answers bool
}

// ProductProbe measures whether the product answers. The console reaches the product
// through the product client plane, which is the only plane that speaks to it; this
// package holds no transport and imports nothing of the product.
type ProductProbe interface {
	MeasureProduct() ProductMeasurement
}

// NoProductProbe is the default probe. It measures nothing, so the mode is Unavailable and
// no door opens over the network until a real probe is wired.
type NoProductProbe struct{}

// MeasureProduct reports an unmeasured product.
func (NoProductProbe) MeasureProduct() ProductMeasurement { return ProductMeasurement{} }

// Offer is what a mode offers one named human right now.
//
// It is not a session and not an identity: it carries no credential, no token and no
// expiry, and nothing of it is stored, so every act asks again. Its whole content is the
// mode that made it, the human the host answered for, and the verbs offered.
type Offer struct {
	// Mode is the mode that made the offer, which the audit record of every act carries.
	Mode Mode
	// Login is the human the host's stack answered for, as the host names them.
	Login string
	// Verbs are the acts offered, in a fixed order.
	Verbs []Verb
}

// What a network sign-in is refused with. Each refusal names the owner that refused, and
// none of them says which check refused a human, because that would be an oracle.
var (
	// ErrProductIsTheRoute refuses a sign-in through the host's stack while the product
	// answers, and names the route that decides instead.
	ErrProductIsTheRoute = errors.New("the product answers: sign in through the product's own route")
	// ErrConsoleRemains refuses every sign-in over the network while the product does not
	// answer and this console's sign-in stack cannot be used, and names the door that
	// remains.
	ErrConsoleRemains = errors.New("nobody signs in over the network: the remaining door is the repair console on tty1")
	// ErrNotAdmitted refuses a human this host does not admit. It is deliberately one
	// phrase for every reason — not a member of the named group, no such login, the wrong
	// credential, an account the stack refuses — so that a refusal answers "not admitted"
	// and never which of those it was.
	ErrNotAdmitted = errors.New("this host does not admit this sign-in")
)

// Selector decides the sign-in mode, and is the only way in.
//
// The network door is SignIn, and the PAM adapter's own admit step is unexported, so there
// is no path to the host's stack that does not pass the product measurement first. That is
// what makes "PAM is never offered while the product answers" a property of the code and
// not a rule someone has to remember.
type Selector struct {
	product ProductProbe
	pam     *PAM
}

// NewSelector returns a selector over the product probe and the PAM adapter. A nil probe
// is the refusing default and a nil adapter is an unusable stack, so a selector nobody
// wired refuses over the network rather than opening a door.
func NewSelector(product ProductProbe, pam *PAM) *Selector {
	if product == nil {
		product = NoProductProbe{}
	}
	return &Selector{product: product, pam: pam}
}

// Select measures the product and, only if the product does not answer, this console's
// sign-in stack, and returns the mode in force with the sentence every page states.
//
// The order is the rule: the product is measured first and a product that answers ends the
// decision there, so the host's stack is never offered beside a product that is up. An
// unmeasured product opens nothing at all.
func (s *Selector) Select() State {
	if s == nil {
		return State{Mode: Unavailable, Reason: reasonNoSelector}
	}
	product := s.product.MeasureProduct()
	switch {
	case !product.Measured:
		return State{Mode: Unavailable, Reason: reasonProductUnmeasured}
	case product.Answers:
		return State{Mode: ProductUp}
	}
	if stack := s.pam.measure(); !stack.Usable {
		return State{Mode: Unavailable, Reason: stack.Reason}
	}
	return State{Mode: ProductDownRepair}
}

// SignIn admits a human over the network, and is the whole of the network door.
//
// It measures the mode first. While the product answers it refuses and names the product's
// route; while the sign-in stack is unusable it refuses and names the console on tty1. Only
// in the repair mode does it open a transaction on the host's stack, and then it offers the
// repair verbs and nothing else.
//
// In that mode a step-up is a re-authentication, since no policy engine is running to ask:
// the console asks again, which is another call here and another transaction on the host,
// never a decision read back from something kept.
func (s *Selector) SignIn(login string) (Offer, error) {
	switch s.Select().Mode {
	case ProductUp:
		return Offer{}, ErrProductIsTheRoute
	case ProductDownRepair:
		return s.pam.admit(login)
	}
	return Offer{}, ErrConsoleRemains
}
