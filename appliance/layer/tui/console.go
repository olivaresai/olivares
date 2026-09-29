// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package tui serves the appliance's repair console on tty1: the last door, and the first
// text-mode surface of this layer.
//
// It is the last door by design, not by accident. It is not a network service, so the
// failure that locks an operator out of the console on 9443 — the product down and that
// console's sign-in stack unusable — cannot lock them out of this one. It authenticates its
// operator by its own sign-in, through the host's local-console sign-in service, never by
// access to this machine: access to the console alone authorizes no act. It keeps no credential
// store of its own, because a second credential store on an appliance is the thing this layer
// refuses to have.
//
// It carries the same repair verbs the network repair mode offers, and one more that only
// it can carry: repairing the sign-in stack of the console on 9443, which over the network
// is the thing that is broken.
//
// Its acts need that sign-in qualified: the operator signs in through the distribution's
// local-console sign-in service (Authority), never the portal's own stack, and the sign-in ends on
// leaving, on a restart and after five minutes without input. Without it every verb refuses and a
// selection answers with the verb, what it would ask for and why it is not performed. With it,
// power, certificate and the firewall restore ask their helpers over the helpers' sockets, and each
// helper admits this console from the connection itself; repair-portal-pam asks the repair package to restore the
// portal's stack from the package's pristine copy. The package starts no process and writes no
// file itself.
package tui

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
)

// ConsoleName is the descriptive label of the console on tty1. It is a description, not a
// brand, and it is distinct from the console on 9443 and from the product's own console.
const ConsoleName = "Appliance Repair Console"

// Verbs are the acts this console offers, in a fixed order: the repair verbs the network
// repair mode offers, and then the one that repairs that mode's own sign-in stack. The
// result is a fresh list, so writing to it cannot widen the next one.
func Verbs() []auth.Verb {
	return append(auth.RepairVerbs(), auth.VerbRepairPortalPAM)
}

// asks says, for each verb this console offers, what selecting it would ask for. It
// describes the act; it does not perform it, and nothing in this package can.
var asks = map[auth.Verb]string{
	auth.VerbApplyUpdate:     "apply an approved update, and show what a pending one contains",
	auth.VerbRollBack:        "roll this appliance back to the previous snapshot",
	auth.VerbNetwork:         "revert to the last network profile that worked, and show or repair the firewall policy",
	auth.VerbCertificate:     "show the certificate fingerprint of the console on 9443, and replace the certificate",
	auth.VerbSupportBundle:   "produce a support bundle",
	auth.VerbPower:           "reboot or shut down this appliance",
	auth.VerbRepairPortalPAM: "repair the sign-in stack of the console on 9443, which no other surface can",
}

// notImplemented is the second half of every answer this console gives. It is stated for
// each verb rather than once at the top, because an operator reading one answer must not
// have to remember a line they read earlier to know that nothing happened.
const notImplemented = "The act is not implemented in this version: this console offers the verb, " +
	"and the helper that performs it arrives with its own change."

// Helper is the privileged-helper seam as this console reaches it: helperclient.Client on an
// installed appliance, for the power helper and the certificate helper.
type Helper interface {
	Call(ctx context.Context, name string, request helperschema.Request) (helperschema.Response, error)
}

// powerTimeout bounds one power request; the helper answers as soon as the service manager
// accepted it.
const powerTimeout = 30 * time.Second

// SignIn is the operator's ordinary sign-in on this console: the qualified local-console
// sign-in, through the host's own local-console sign-in service, with its authentication and
// account checks, root or current membership of olivares-admins, and its end on exit, restart
// or five minutes idle. Authority.SignIn makes one (*Session).
type SignIn interface {
	// Qualified reports whether the sign-in authorizes an ordinary act now. It is asked again
	// before every act.
	Qualified() bool
}

// Console is the repair console on tty1. It starts no process and writes no file itself: power and
// certificate leave through their helpers, and the portal's stack is restored by the repair package.
type Console struct {
	network       auth.State
	helper        Helper
	networkClient NetworkClient
	// random is where the console mints each request's operation id.
	random io.Reader
	// signIn is the operator's ordinary sign-in; nil until one is qualified.
	signIn SignIn
	// authority signs the operator in through the host's local-console sign-in service.
	authority Authority
	// record and recovery are E12's host record and its account-recovery entry.
	record   HostRecord
	recovery AccountRecovery
	// certificateFingerprint reads only the public certificate; portalPAM restores the portal's stack.
	certificateFingerprint CertificateFingerprint
	portalPAM              PortalPAMRepair
	// admission holds the retained gates of every power act; boot reads this boot's identity.
	admission PowerAdmission
	boot      func() (string, error)
}

// withSignIn returns the console holding s as its operator's ordinary sign-in. Run makes one when
// the operator signs in; a case may hand one in.
func (c Console) withSignIn(s SignIn) Console {
	c.signIn = s
	return c
}

// qualified reports whether the operator's ordinary sign-in authorizes an act now.
func (c Console) qualified() bool { return c.signIn != nil && c.signIn.Qualified() }

// signInRequired is the answer to every act without a qualified sign-in, whatever else holds: no
// helper's own state is consulted, so none is asked.
const signInRequired = "The act is refused: an ordinary act on this console needs its qualified sign-in (s), " +
	"and access to this machine alone authorizes none. Nothing was asked of any helper."

// signInRefused is the one phrase every refused sign-in states, whichever check refused it, so a
// refusal never says which one that was.
const signInRefused = "The sign-in is refused."

// NewConsole returns the console beside a network sign-in state, which it states so that an
// operator reads in one place which doors are open. It reaches no helper until WithHelper.
func NewConsole(network auth.State) Console { return Console{network: network, random: rand.Reader} }

// WithHelper returns the console reaching the helpers through h.
func (c Console) WithHelper(h Helper) Console {
	c.helper = h
	return c
}

// Header is what the console states before anything else: what it is, that it is the last
// door, how it authenticates, which mode the console on 9443 is in, and what it performs.
func (c Console) Header() []string {
	return []string{
		ConsoleName,
		"This console is the last door, and it runs on tty1. It is not a network service, so the " +
			"failure that closes the console on 9443 cannot close this one.",
		"It authenticates its operator by its own sign-in, never by access to this machine, and an " +
			"ordinary act needs that sign-in qualified.",
		c.network.Statement(),
		c.performs(),
	}
}

// performs is the header's last line: what the console performs with the sign-in it holds. It
// names no act as unimplemented, because each selection says that of its own act.
func (c Console) performs() string {
	if !c.qualified() {
		return "It offers the verbs below and performs none of them until its operator signs in (s) through " +
			"this host's local-console sign-in service: an ordinary act needs that qualified sign-in."
	}
	return "It offers the verbs below. With its qualified sign-in it performs power, certificate, " +
		"repair-portal-pam, the network restore and the firewall restore, and none of the others in this version."
}

// Entry is the console's menu line for v: the verb and what selecting it would ask for.
func (c Console) Entry(v auth.Verb) string {
	ask, offered := asks[v]
	if !offered {
		return string(v) + ": this console does not offer that."
	}
	return string(v) + ": " + ask + "."
}

// Select is what the console answers when v is chosen. For power with a qualified sign-in, it
// states the verb and that the power helper performs it, and Run asks next which of the two
// acts; without one, it states the verb and the refusal and asks nothing. For every other verb
// it offers, it states the verb, what the act would ask for, and that the act is not
// implemented. For anything else it states that there is no such verb, and nothing more.
func (c Console) Select(v auth.Verb) string {
	if _, offered := asks[v]; !offered {
		return "There is no such verb on this console: " + string(v) + "."
	}
	if !c.qualified() {
		return c.Entry(v) + " " + signInRequired
	}
	switch v {
	case auth.VerbNetwork:
		return c.Entry(v) + " The network guard measures restoration of open change windows. The firewall's local entry point " +
			"restores the firewall policy derived from this appliance's answers."
	case auth.VerbPower:
		return c.Entry(v) + " The power helper performs it."
	case auth.VerbCertificate:
		return c.Entry(v) + " The certificate helper generates the pair. This console reads only the stored public certificate fingerprint."
	case auth.VerbRepairPortalPAM:
		return c.Entry(v) + " The package's pristine copy is restored, and the stack is measured as the mode selector measures it."
	}
	return c.Entry(v) + " " + notImplemented
}

// powerConfirm is the question Run asks after power is selected.
const powerConfirm = "Type reboot or shutdown to ask the power helper for it, " + recoveryAnswer +
	" to recover from a network change whose reply was lost, or anything else to ask for nothing:"

// Power asks the power helper for answer, reboot or shutdown, through the admission's retained gates,
// and returns what the console states about it. Without a qualified sign-in it refuses before
// anything else and asks nothing, whatever the answer. Any other answer asks for nothing. A gate that
// refuses asks nothing, and no network window is exempt: an open one refuses an ordinary act and
// names the recovery route. It reports what the helper answered and nothing more: an absent helper
// performed nothing, and a request whose answer was lost is unknown, never done.
func (c Console) Power(answer string) string {
	if !c.qualified() {
		return "power: " + signInRequired
	}
	if answer != helperschema.PowerReboot && answer != helperschema.PowerShutdown {
		return "Nothing was asked of the power helper."
	}
	if c.helper == nil {
		return "power: no helper is wired to this console, so nothing was asked and nothing was performed."
	}
	if c.admission == nil {
		return "power: refused: the power gates are not composed with this console, so nothing was asked of the power helper."
	}
	// The operator confirmed this act by typing it, so this is where its operation id is minted:
	// one per request, never reused.
	operation, err := helperschema.NewOperationID(c.randomness())
	if err != nil {
		return "power: no operation id could be minted, so nothing was asked and nothing was performed."
	}
	ctx, cancel := context.WithTimeout(context.Background(), powerTimeout)
	defer cancel()
	var response helperschema.Response
	var callErr error
	err = c.admission.Ordinary(ctx, c.qualified, func(ctx context.Context) error {
		response, callErr = c.helper.Call(ctx, helperschema.HelperPower, &helperschema.PowerRequest{Verb: answer, OperationID: operation})
		return nil
	})
	if err != nil {
		return "power: " + refusalText(err) + " Nothing was asked of the power helper."
	}
	return powerAnswer(answer, response, callErr)
}

// randomness is where the console mints operation ids.
func (c Console) randomness() io.Reader {
	if c.random == nil {
		return rand.Reader
	}
	return c.random
}

// powerAnswer states what the power helper answered for verb, and nothing more.
func powerAnswer(verb string, response helperschema.Response, err error) string {
	var failure *helperclient.Error
	switch {
	case errors.As(err, &failure) && failure.Code == helperschema.CodeConsumerUnavailable:
		return "power: the power helper is unavailable (" + failure.Reason + "), so nothing was asked and nothing was performed."
	case errors.As(err, &failure) && failure.Code == helperclient.CodeOutcomeUnknown:
		return "power: the " + verb + " request was sent and no answer came back, so whether the host is going down is unknown."
	case err != nil:
		return "power: the " + verb + " request was not sent, so nothing was performed."
	case response.Result == helperschema.ResultPerformed:
		return "power: the power helper performed " + verb + ": the service manager accepted it and takes the host down."
	case response.Result == helperschema.ResultRefused:
		return "power: the power helper refused " + verb + " (" + response.Code + "), so nothing was performed."
	}
	return "power: the power helper could not perform " + verb + " (" + response.Code + "), so nothing was performed."
}

// The exit codes of Run, in the project's convention: 0 when the operator leaves, 2 on an
// input or output failure. No selection is refused, so no exit is 1.
const (
	exitLeft        = 0
	exitInputOutput = 2
)

// The fixed sentences Run states on its diagnostics when its input or its output fails.
// Neither carries the failure's own text or anything the operator typed.
const (
	inputFailed  = ConsoleName + ": a read failed or a line was too long, so it stopped and asks for nothing more."
	outputFailed = ConsoleName + ": its output could not be written, so it stopped and asks for nothing more."
)

// Run states the console on out, reads selections from in, and returns the process exit
// code. A number selects the verb of that position, "s" signs the operator in, "r" enters E12's
// account-recovery entry, "q" leaves, and the end of the input leaves too. Every other line is
// answered with the fact that it selected nothing. Every line read is the operator's input, so a
// sign-in that has had none for five minutes ends before the line is read as a selection; leaving
// ends the sign-in too, and nothing of it is kept.
//
// Leaving the last door is not a failure, so leaving returns 0. An input or output failure
// — a read that fails, a line longer than the console reads, or output that cannot be
// written — stops the console before it reads again, states one fixed sentence on
// diagnostics, and returns 2. A console that cannot tell the operator what happened never
// reports that it left.
func (c Console) Run(in io.Reader, out, diagnostics io.Writer) int {
	w := &terminal{out: out}
	defer func() { c.signIn = end(c.signIn) }()
	for _, line := range c.Header() {
		w.say("%s", line)
	}
	verbs := Verbs()
	w.say("")
	for i, v := range verbs {
		w.say("  %d) %s", i+1, c.Entry(v))
	}
	w.say("  s) sign in through this host's local-console sign-in service")
	w.say("  r) E12 account recovery: set-admin-password for the human E12 recorded, which grants no act here")
	w.say("  q) leave this console")
	w.say("")
	lines := bufio.NewScanner(in)
	// ask states question and reads the operator's answer; ok is false when the console must stop
	// or leave, with code its exit code.
	ask := func(question string) (answer string, ok bool, code int) {
		w.say("%s", question)
		if w.failed {
			return "", false, stop(diagnostics, outputFailed)
		}
		if !lines.Scan() {
			if lines.Err() != nil {
				return "", false, stop(diagnostics, inputFailed)
			}
			w.say("The input ended. Nothing was asked. This console stays the last door.")
			return "", false, w.left(diagnostics)
		}
		// An answer is input too: it keeps a live sign-in alive, and one idle too long stays ended.
		if session, ok := c.signIn.(*Session); ok {
			session.activity()
		}
		return strings.TrimSpace(lines.Text()), true, 0
	}
	for {
		w.say("Select a verb by its number, s to sign in, r for account recovery, or q to leave:")
		if w.failed {
			return stop(diagnostics, outputFailed)
		}
		if !lines.Scan() {
			if lines.Err() != nil {
				return stop(diagnostics, inputFailed)
			}
			w.say("The input ended. This console stays the last door.")
			return w.left(diagnostics)
		}
		if session, ok := c.signIn.(*Session); ok {
			open := !session.ended
			if !session.activity() {
				c.signIn = end(c.signIn)
				if open {
					w.say("The sign-in ended after five minutes without input: an ordinary act needs a new sign-in (s).")
				}
			}
		}
		selection := strings.TrimSpace(lines.Text())
		switch selection {
		case "q", "Q":
			w.say("Leaving this console.")
			return w.left(diagnostics)
		case "s", "S":
			c.signIn = end(c.signIn)
			login, ok, code := ask("login:")
			if !ok {
				return code
			}
			session, err := c.authority.SignIn(login)
			if err != nil {
				w.say("%s", signInRefused)
				continue
			}
			c.signIn = session
			w.say("Signed in as %s through this host's local-console sign-in service. The sign-in ends when you "+
				"leave, when this console restarts and after five minutes without input.", session.Login())
			continue
		case "r", "R":
			c.signIn = end(c.signIn)
			w.say("%s", c.AccountRecovery())
			continue
		}
		position, err := strconv.Atoi(selection)
		if err != nil || position < 1 || position > len(verbs) {
			w.say("That selected nothing: choose a number between 1 and %d, s, r or q.", len(verbs))
			continue
		}
		verb := verbs[position-1]
		w.say("%s", c.Select(verb))
		if !c.qualified() {
			continue
		}
		var question string
		var act func(string) string
		switch verb {
		case auth.VerbNetwork:
			question, act = "Type restore to request the confirmed baseline of each open network window, "+firewallRestoreAnswer+
				" to restore the firewall policy derived from this appliance's answers, or anything else to request nothing:", c.Network
		case auth.VerbPower:
			question, act = powerConfirm, c.Power
		case auth.VerbCertificate:
			w.say("%s", c.showCertificate())
			question, act = certificateConfirm, c.Certificate
		case auth.VerbRepairPortalPAM:
			question, act = pamConfirm, c.RepairPortalPAM
		default:
			continue
		}
		answer, ok, code := ask(question)
		if !ok {
			return code
		}
		if verb == auth.VerbNetwork && answer == firewallRestoreAnswer {
			plan, statement := c.PlanFirewallRestore()
			w.say("%s", statement)
			if plan == nil {
				continue
			}
			typed, ok, code := ask(firewallRestoreConfirm)
			if !ok {
				return code
			}
			w.say("%s", c.FirewallRestore(*plan, typed))
			continue
		}
		if verb == auth.VerbPower && answer == recoveryAnswer {
			plan, statement := c.RecoveryPlan()
			w.say("%s", statement)
			if plan == nil {
				continue
			}
			typed, ok, code := ask(recoveryConfirm)
			if !ok {
				return code
			}
			w.say("%s", c.RecoveryReboot(*plan, typed))
			continue
		}
		w.say("%s", act(answer))
	}
}

// end ends the sign-in s, when it can be ended, and returns none.
func end(s SignIn) SignIn {
	if ender, ok := s.(interface{ End() }); ok {
		ender.End()
	}
	return nil
}

// terminal writes the console's lines to out and remembers that one failed, so the console
// stops at its next step instead of writing past the failure.
type terminal struct {
	out    io.Writer
	failed bool
}

// say writes one line, unless an earlier line already failed.
func (t *terminal) say(format string, args ...any) {
	if t.failed {
		return
	}
	if _, err := fmt.Fprintf(t.out, format+"\n", args...); err != nil {
		t.failed = true
	}
}

// left is the exit code of a console the operator left: 0, or the output failure when its
// last line could not be written.
func (t *terminal) left(diagnostics io.Writer) int {
	if t.failed {
		return stop(diagnostics, outputFailed)
	}
	return exitLeft
}

// stop states sentence on diagnostics and returns the input-or-output exit code. If the
// diagnostics cannot be written either, the exit code is what remains to say it.
func stop(diagnostics io.Writer, sentence string) int {
	_, _ = fmt.Fprintln(diagnostics, sentence)
	return exitInputOutput
}
