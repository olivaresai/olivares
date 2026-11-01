// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// runtime_worktree_setup.go runs the approved setup command of a new worktree once,
// before the session's first turn: on the first start of a worktree run, whether that
// start is a create, an approved queued start or a resume of a run whose first start
// failed. A resume of a run that has started never runs it.
//
// The command is the text a person approved (workspace_setup.go, workspaceSetupStartError),
// read from the same snapshot of the workspace the launch resolved, so a text changed
// after its approval never runs. It runs in a process of its own under the folder's
// confinement and the git mount protection of the session, with the session's network
// policy. It receives none of the session's environment, no account home and no
// credential file: only the runner's safe base environment, with the session's
// profile programs ahead of the host's own PATH on it, and read access to their
// directories.
//
// A non-zero exit, a stop at the time limit or a command that cannot start stops the
// start. Its last output is kept as a worktree_setup event on the run's ledger and in the
// refusal, so the person sees what failed. A setup that finishes is recorded the same way.
//
// A request waits for a quick setup. A setup still running when that wait ends belongs
// to the session, which answers pending until the start settles (runtime_launch_background.go).
const (
	worktreeSetupStage      = "worktree setup command"
	worktreeSetupEvent      = "worktree_setup"
	worktreeSetupOutputMark = ". Last output:\n"
	// worktreeSetupLines and worktreeSetupBytes bound the output kept for the ledger and
	// the refusal: the last lines of it, and no more than the last bytes.
	worktreeSetupLines = 20
	worktreeSetupBytes = 4 << 10
	worktreeSetupShell = "/bin/sh"
)

// worktreeSetupTimeout bounds the command when the session owns it (the start continues
// after its request answered, runtime_launch_background.go): a networked installer such
// as npm ci, pnpm install or pip install may need minutes. A setup a request waits for
// to its end (a resume) is held to the shorter worktreeSetupInlineWait instead.
// ponytail: a fixed ceiling, no per-workspace setting; upgrade trigger: a setup that
// legitimately needs longer than ten minutes.
var worktreeSetupTimeout = 10 * time.Minute

// errWorktreeSetupTimeout distinguishes the setup's own limit from a parent request
// deadline: only the former may be reported as "time limit" to the person.
var errWorktreeSetupTimeout = errors.New("worktree setup time limit")

// runWorktreeSetup runs the workspace's approved setup command and returns nil when it
// finishes with status 0, or when there is no command to run. spec is the session's own
// launch, already complete, whose confinement the command shares. The request context
// bounds setup, so a client timeout cannot lose its response and then start the session;
// a start the session owns passes a context of its own. limit is the command's time limit.
// A process whose cleanup is unconfirmed is returned with the error, so the caller's
// existing launch-custody path retains it and blocks an overlapping retry.
func (m *Module) runWorktreeSetup(ctx context.Context, tenant model.TenantID, runRef string, p CreateRunParams, spec LaunchSpec, ws *resolvedWorkspace, limit time.Duration) (Process, error) {
	if ws == nil || ws.setupCommand == "" {
		return nil, nil
	}
	if err := workspaceSetupStartError(ws, false); err != nil {
		return nil, err
	}
	// The folder's policy, rebuilt without the session's account homes or the credential
	// files its launch allowed to be read: the setup is not the session and holds none of them.
	// Worktree launches refuse read-only and no-delete folders, so neither grant is needed here.
	// The preset is none whatever the session's permission mode is: the mode governs the
	// tool's turns, while this command is the text a person approved to prepare the folder.
	setup := LaunchSpec{
		Program: worktreeSetupShell, Args: []string{"-c", ws.setupCommand}, Dir: spec.Dir,
		Isolation: spec.Isolation, WaitDelay: spec.WaitDelay, NetworkPolicy: spec.NetworkPolicy, KillTreeOnExit: true,
		Confinement:       m.sessionConfinement(spec.Dir, nil, PresetNone),
		ConfinementMounts: spec.ConfinementMounts, ConfinementMountsNote: spec.ConfinementMountsNote,
		ConfinementRequireTruncateProtection: spec.ConfinementRequireTruncateProtection,
		ConfinementRequired:                  true,
	}
	// The session finds its profile's programs on its PATH; the setup runs the same ones.
	if err := m.applyProfilePrograms(p.profilePrograms, &setup); err != nil {
		return nil, err
	}
	setupCtx, cancel := context.WithTimeoutCause(ctx, limit, errWorktreeSetupTimeout)
	defer cancel()
	proc, err := m.rt.Runner.Launch(setupCtx, setup)
	if err != nil {
		reason := "the setup command could not start: " + redactErr(err)
		status := http.StatusUnprocessableEntity
		var startErr *runErr
		if errors.As(err, &startErr) {
			status = startErr.status
		}
		switch {
		case errors.Is(context.Cause(setupCtx), errWorktreeSetupTimeout):
			reason = fmt.Sprintf("the setup command was stopped at its time limit of %s", limit)
		case setupCtx.Err() != nil:
			reason = "the setup command was cancelled: " + redactErr(context.Cause(setupCtx))
		}
		// A runner may return a live process together with its error: cancel lets the
		// setup guard kill the tree, and Stop reports whether reaping is still unconfirmed.
		cancel()
		var cleanupErr error
		if proc != nil {
			cleanupErr = proc.Stop(context.Background())
			if cleanupErr != nil {
				m.warnf("sessions: a setup process that failed to start could not be stopped", "run_ref", runRef, "err", redactErr(cleanupErr))
			}
		}
		if !childWasReaped(cleanupErr) {
			if cleanupErr != nil {
				reason += "; cleanup was not confirmed: " + redactErr(cleanupErr)
			}
			failure := m.worktreeSetupFailed(ctx, tenant, runRef, p, reason, "", false, status)
			return proc, failure
		}
		code := 0
		if proc != nil {
			code, _ = proc.Wait()
		}
		cleanupUnconfirmed := worktreeSetupCleanupUnconfirmed(proc, code)
		failure := m.worktreeSetupFailed(ctx, tenant, runRef, p, reason, "", cleanupUnconfirmed, status)
		return nil, failure
	}
	tail := collectSetupOutput(proc.Output(), p.outputRedactor())
	code, waitErr := proc.Wait()
	cause, ctxErr := context.Cause(setupCtx), setupCtx.Err()
	// Whatever the command left running in its process group ends with it: Stop on a
	// finished process is what signals the rest of the group.
	stopErr := proc.Stop(context.Background())
	if stopErr != nil {
		m.warnf("sessions: a process the setup left running could not be stopped", "run_ref", runRef, "err", redactErr(stopErr))
	}
	out := lastSetupLines(tail)
	cleanupUnconfirmed := worktreeSetupCleanupUnconfirmed(proc, code)
	reason := ""
	switch {
	case errors.Is(cause, errWorktreeSetupTimeout):
		reason = fmt.Sprintf("the setup command was stopped at its time limit of %s", limit)
	case ctxErr != nil:
		reason = "the setup command was cancelled: " + redactErr(cause)
	case waitErr != nil:
		reason = "the setup command could not be collected: " + redactErr(waitErr)
	case code != 0:
		reason = fmt.Sprintf("the setup command exited with status %d", code)
	}
	addReason := func(detail string) {
		if reason != "" {
			reason += "; "
		}
		reason += detail
	}
	if cleanupUnconfirmed {
		addReason("the setup command's processes could not be stopped; the guard could not confirm that its process tree was empty")
	}
	if stopErr != nil {
		addReason("the setup command's processes could not be stopped: " + redactErr(stopErr))
	}
	if reason != "" {
		failure := m.worktreeSetupFailed(ctx, tenant, runRef, p, reason, out, cleanupUnconfirmed, http.StatusUnprocessableEntity)
		if !childWasReaped(stopErr) {
			return proc, failure
		}
		return nil, failure
	}
	// The same write that records a finished setup says the run no longer waits for one.
	if err := m.recordWorktreeSetup(ctx, tenant, runRef, p, "the setup command finished with status 0", out, func(rec model.Record) {
		rec[colRunSetupPending] = false
		rec[colRunSetupCleanupFailed] = false
	}); err != nil {
		m.warnf("sessions: a finished worktree setup could not be recorded", "run_ref", runRef, "err", redactErr(err))
		return nil, &runErr{http.StatusBadGateway, "worktree setup command: the setup outcome could not be recorded; the session was not started"}
	}
	return nil, nil
}

// worktreeSetupCleanupUnconfirmed reads the guard's typed outcome, never its output.
// A missing verdict is unconfirmed except for a guard that failed before starting
// the command (126), when no process tree can exist.
func worktreeSetupCleanupUnconfirmed(proc Process, code int) bool {
	outcome, ok := proc.(interface{ WorktreeSetupCleanup() (failed, known bool) })
	if !ok {
		return false
	}
	failed, known := outcome.WorktreeSetupCleanup()
	return failed || (!known && code != 126)
}

// worktreeSetupLaunchError carries the guard's durable outcome alongside the safe
// refusal. The final failed transition preserves it even when the setup event write
// failed and the setup never reached the ledger.
type worktreeSetupLaunchError struct {
	cleanupUnconfirmed bool
	cause              error
}

func (e *worktreeSetupLaunchError) Error() string { return e.cause.Error() }
func (e *worktreeSetupLaunchError) Unwrap() error { return e.cause }

// markWorktreeSetupCleanupFailure preserves the typed outcome in the run's final
// failed transition. Create and resume share this one write.
func markWorktreeSetupCleanupFailure(rec model.Record, err error) {
	var setupErr *worktreeSetupLaunchError
	if errors.As(err, &setupErr) && setupErr.cleanupUnconfirmed {
		rec[colRunSetupCleanupFailed] = true
	}
}

// markWorktreeSetupCleanupOutcome classifies a process whose caller-side teardown
// has confirmed reaping. It runs after Wait, never for a live handle.
func markWorktreeSetupCleanupOutcome(rec model.Record, stage string, proc Process) {
	if stage != worktreeSetupStage || proc == nil {
		return
	}
	code, _ := proc.Wait()
	if worktreeSetupCleanupUnconfirmed(proc, code) {
		rec[colRunSetupCleanupFailed] = true
	}
}

// worktreeSetupFailed records why the setup stopped the start and returns the refusal
// that carries the same reason and output to the person. cleanupUnconfirmed is durable:
// after the guard has exited, resume cannot prove a detached process is gone.
func (m *Module) worktreeSetupFailed(ctx context.Context, tenant model.TenantID, runRef string, p CreateRunParams, reason, out string, cleanupUnconfirmed bool, status int) error {
	var mutate func(model.Record)
	if cleanupUnconfirmed {
		mutate = func(rec model.Record) { rec[colRunSetupCleanupFailed] = true }
	}
	if err := m.recordWorktreeSetup(ctx, tenant, runRef, p, reason, out, mutate); err != nil {
		m.warnf("sessions: a failed worktree setup could not be recorded", "run_ref", runRef, "err", redactErr(err))
		msg := "worktree setup command: " + reason + "; the setup outcome could not be recorded; the session was not started"
		if out != "" {
			msg += worktreeSetupOutputMark + out
		}
		return &worktreeSetupLaunchError{cleanupUnconfirmed: cleanupUnconfirmed, cause: &runErr{http.StatusBadGateway, msg}}
	}
	msg := reason + "; the session was not started"
	if out != "" {
		msg += worktreeSetupOutputMark + out
	}
	return &worktreeSetupLaunchError{cleanupUnconfirmed: cleanupUnconfirmed, cause: &runErr{status, msg}}
}

// recordWorktreeSetup writes the setup's outcome as a run event. The row's reason keeps
// the launch's own words; the event carries the detail. The write is not cancelled with
// the request, so a finished setup is always on the ledger before the session starts.
func (m *Module) recordWorktreeSetup(ctx context.Context, tenant model.TenantID, runRef string, p CreateRunParams, reason, out string, mutate func(model.Record)) error {
	detail := reason
	if out != "" {
		detail += ": " + out
	}
	_, err := m.transition(context.WithoutCancel(ctx), tenant, runRef, transitionInput{
		event: worktreeSetupEvent, detail: detail, actor: p.Actor, actorKind: p.ActorKind, keepReason: true, mutate: mutate,
	})
	return err
}

// worktreeSetupPending reports whether a resume still owes the worktree's first setup.
// New rows carry an explicit boolean. A legacy row has none: an empty started_at is the
// remaining evidence that its first launch never happened. Legacy no_setup was not stored,
// so the safe choice is to run the approved setup once rather than skip it silently.
func worktreeSetupPending(rec model.Record) bool {
	if value, present := rec[colRunSetupPending]; present && value != nil {
		return rec.Bool(colRunSetupPending)
	}
	return rec.String(colRunWorktreeBranch) != "" && rec.String(colStartedAt) == ""
}

// collectSetupOutput reads the command's output to its end and returns the last
// worktreeSetupBytes of it. Each frame is redacted before the tail is cut, so a secret
// the cut would split never leaves a fragment behind.
func collectSetupOutput(frames <-chan OutputFrame, redact *secretRedactor) []byte {
	var tail []byte
	for frame := range frames {
		tail = appendSetupOutput(tail, redact.apply(frame.Data))
	}
	return tail
}

// appendSetupOutput keeps the last worktreeSetupBytes of the output, so a chatty command
// cannot grow the engine's memory. The pump hands over each line without its newline.
func appendSetupOutput(tail, data []byte) []byte {
	tail = append(append(tail, data...), '\n')
	if over := len(tail) - worktreeSetupBytes; over > 0 {
		tail = tail[over:]
	}
	return tail
}

// lastSetupLines returns the last worktreeSetupLines lines of the output, with control
// characters turned into spaces, so the ledger and a refusal show plain text.
func lastSetupLines(tail []byte) string {
	lines := strings.Split(strings.TrimSpace(strings.ToValidUTF8(string(tail), "?")), "\n")
	if len(lines) > worktreeSetupLines {
		lines = lines[len(lines)-worktreeSetupLines:]
	}
	for i, line := range lines {
		lines[i] = strings.Map(plainRune, line)
	}
	return strings.Join(lines, "\n")
}
