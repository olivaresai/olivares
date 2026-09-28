// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// Helper is one closed helper: its name, its admission table, its document and its effect.
type Helper struct {
	// Name is the helper's socket name, one of helperschema.Helpers.
	Name string
	// Rules is the helper's closed admission table.
	Rules []helperschema.Rule
	// NewRequest returns an empty document of the helper's schema.
	NewRequest func() helperschema.Request
	// Perform performs an admitted request and answers it. It is called at most once per
	// connection, and only after admission.
	Perform func(ctx context.Context, peer helperschema.Peer, request helperschema.Request) helperschema.Response
}

// Exit codes, in the project's convention.
const (
	ExitDone    = 0 // performed or answered
	ExitRefused = 1 // refused, with no effect
	ExitFailure = 2 // a usage, setup or input/output failure, with no effect
)

// guestProven records whether the appliance's own kernel has been proven, on the appliance image
// in a guest, to give each per-connection helper instance its peer's pidfd, with a recycled pid
// refused. Per-connection instances (Accept=yes) perform no mutating subcommand before that
// proof, whoever asks: until it is accepted and this constant changes with it, Serve refuses
// every mutating subcommand an admitted invoker asks for with pidfd_unproven. Subcommands that
// change nothing are unaffected.
const guestProven = false

// Serve answers the one connection the service manager passed as fd, read from in and answered
// on out, and returns the exit code. In order: the command line must be empty (--help prints
// the usage), the kernel attests the peer, the document is decoded against the helper's closed
// schema, the peer is admitted for the document's subcommand under the helper's class (a module
// helper admits the Appliance Console alone), a mutating subcommand waits for
// the guest proof (guestProven), and only then is the effect performed. log receives one line
// per decision, never a value of the document other than its operation id.
func Serve(ctx context.Context, h Helper, k Kernel, args []string, fd int, in io.Reader, out, log io.Writer) int {
	return serve(ctx, h, k, guestProven, args, fd, in, out, log)
}

// serve is Serve with the guest proof given, so the admission it performs can be measured on
// both sides of the proof.
func serve(ctx context.Context, h Helper, k Kernel, proven bool, args []string, fd int, in io.Reader, out, log io.Writer) int {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, h.Name+" helper: "+format+"\n", a...) }
	action, err := helperschema.CheckArgs(args)
	if err != nil {
		logf("%v", err)
		return ExitFailure
	}
	if action == helperschema.ArgsHelp {
		if _, err := io.WriteString(out, helperschema.Usage); err != nil {
			return ExitFailure
		}
		return ExitDone
	}
	peer, err := Attest(k, fd)
	if err != nil {
		logf("%v", err)
		return ExitFailure
	}
	request := h.NewRequest()
	if err := helperschema.Decode(in, request); err != nil {
		if errors.Is(err, helperschema.ErrRead) {
			logf("%v", err)
			return ExitFailure
		}
		logf("peer uid %d: %v", peer.UID, err)
		return answer(out, logf, helperschema.Response{Result: helperschema.ResultRefused,
			Code: helperschema.CodeInputRefused, Detail: err.Error()})
	}
	subcommand := request.Subcommand()
	if err := helperschema.AdmitHelper(peer, h.Name, h.Rules, subcommand); err != nil {
		code, detail := helperschema.CodeNotAdmitted, err.Error()
		var refusal *helperschema.Refusal
		if errors.As(err, &refusal) {
			code, detail = refusal.Code, refusal.Reason
		}
		logf("peer uid %d unit %q tty %q attested %t: %s refused: %s", peer.UID, peer.Unit, peer.TTY, peer.Attested, subcommand, code)
		return answer(out, logf, helperschema.Response{Result: helperschema.ResultRefused, Code: code, Detail: detail})
	}
	operation := request.Operation()
	logf("peer uid %d unit %q tty %q attested: %s admitted, operation_id %s", peer.UID, peer.Unit, peer.TTY, subcommand, operation)
	if mutating(h.Rules, subcommand) && !proven {
		logf("%s refused: %s, operation_id %s", subcommand, helperschema.CodePidfdUnproven, operation)
		return answer(out, logf, helperschema.Response{Result: helperschema.ResultRefused, Code: helperschema.CodePidfdUnproven,
			Detail: "per-connection helper instances perform no mutating subcommand until this appliance's kernel is proven to give them the peer's pidfd"})
	}
	response := h.Perform(ctx, peer, request)
	logf("%s: %s %s, operation_id %s", subcommand, response.Result, response.Code, operation)
	return answer(out, logf, response)
}

// mutating reports whether rules mark subcommand as mutating.
func mutating(rules []helperschema.Rule, subcommand string) bool {
	for _, rule := range rules {
		if rule.Subcommand == subcommand {
			return rule.Mutating
		}
	}
	return false
}

// answer writes the response and returns the exit code its result means; an answer that cannot
// be written is an output failure.
func answer(out io.Writer, logf func(string, ...any), response helperschema.Response) int {
	data, err := json.Marshal(response)
	if err == nil {
		_, err = out.Write(append(data, '\n'))
	}
	if err != nil {
		logf("the answer could not be written")
		return ExitFailure
	}
	switch response.Result {
	case helperschema.ResultPerformed, helperschema.ResultAnswered:
		return ExitDone
	case helperschema.ResultRefused:
		return ExitRefused
	}
	return ExitFailure
}
