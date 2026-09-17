// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The OPTIONAL Runner inspection seam.
//
// ⛔ IT IS OPTIONAL, AND THAT IS THE WHOLE DESIGN. Runner keeps exactly ONE
// required method — Launch — so an external Runner that predates this file, or
// one nobody controls, keeps working untouched and is never required to change.
// A Runner that does not implement RunnerInspector answers `unknown` to the
// launch-readiness read, and unknown is honored as unknown: the console may
// still ask for a launch under a declared incomplete check, and nothing here
// promotes that unknown to ready or presents it as "not installed".
//
// ⛔ AND IT NEVER RUNS ANYTHING. The whole point of a separate method is that
// the program is examined by METADATA — resolution plus a stat — and not by
// spawning it, not even with `--version`. Finding an executable proves it is an
// executable file the Runner would resolve; it proves nothing about the vendor,
// the version, the compatibility or whether the CLI works. Those are the
// provider handshake's answers and they stay in remaining_checks.
//
// The observation returns CLASSIFIED states rather than an error, because the
// distinction the caller needs is exactly the one an error loses: "there is no
// such program" is a configuration fact, while "I could not look" is not.

// RunnerInspection is the references-only question. It carries the effective
// program the module resolved (never an argv, never an environment) and the
// isolation posture the caller is asking about.
type RunnerInspection struct {
	// Program is the executable this Runner would be asked to launch.
	Program string
	// Isolation is the containment posture the caller is asking about.
	Isolation Isolation
}

// RunnerIsolationState is a Runner's answer about one isolation posture.
type RunnerIsolationState string

// The three isolation answers.
const (
	// RunnerIsolationSupported means this Runner runs that posture.
	RunnerIsolationSupported RunnerIsolationState = "supported"
	// RunnerIsolationUnsupported means this Runner REFUSES that posture — the
	// native runner's refusal to run unisolated while reporting containment.
	RunnerIsolationUnsupported RunnerIsolationState = "unsupported"
	// RunnerIsolationUnknown means the Runner did not decide.
	RunnerIsolationUnknown RunnerIsolationState = ""
)

// RunnerProgramState is a Runner's METADATA verdict about the executable.
type RunnerProgramState string

// The four program answers. Missing and NotExecutable are configuration facts;
// Unknown is the honest answer of an inspection that could not complete, and it
// is deliberately NOT folded into Missing.
const (
	RunnerProgramExecutable    RunnerProgramState = "executable"
	RunnerProgramMissing       RunnerProgramState = "missing"
	RunnerProgramNotExecutable RunnerProgramState = "not_executable"
	RunnerProgramUnknown       RunnerProgramState = ""
)

// RunnerObservation is one Runner's dated, effect-free answer.
type RunnerObservation struct {
	Isolation RunnerIsolationState
	Program   RunnerProgramState
}

// RunnerInspector is the optional half of a Runner that can describe what it
// would resolve WITHOUT launching it. An implementation must not spawn, connect,
// mint or write anything, and must not return a path, an argv or a raw OS error
// — the caller publishes closed codes and cannot redact what it never receives.
type RunnerInspector interface {
	InspectLaunch(ctx context.Context, in RunnerInspection) (RunnerObservation, error)
}

// LaunchInspectionAvailable reports whether the WIRED Runner can be inspected
// without launching. It exists for the composition root, not for a test: a
// deployment whose Runner lacks the optional seam will answer `unknown` for the
// runner and the program on EVERY profile, forever and silently, and an
// operator reading a console full of "check incomplete" deserves to find the
// reason in the boot log rather than deduce it.
func (m *Module) LaunchInspectionAvailable() bool {
	if _, unwired := m.rt.runner.(unwiredRunner); unwired {
		return false
	}
	_, ok := m.rt.runner.(RunnerInspector)
	return ok
}

// InspectLaunch is the NATIVE implementation: the same resolution rules
// procRunner.Launch uses (exec.Command's: a name with a separator is a path, a
// bare name is resolved through PATH) and the same isolation refusal, with the
// spawn removed.
func (pr *procRunner) InspectLaunch(_ context.Context, in RunnerInspection) (RunnerObservation, error) {
	out := RunnerObservation{Isolation: RunnerIsolationSupported}
	// Exactly the refusal Launch makes: this runner runs a plain host child and
	// cannot honor a container/sandbox request, so it says so instead of running
	// unisolated under a row that claims containment.
	if in.Isolation == IsolationContainer || in.Isolation == IsolationSandbox {
		out.Isolation = RunnerIsolationUnsupported
	}
	out.Program = inspectProgramMetadata(in.Program)
	return out, nil
}

// inspectProgramMetadata resolves a program the way exec.Command would and
// classifies it by metadata alone.
//
// ⛔ IT ASKS THE STANDARD LIBRARY, IT DOES NOT REIMPLEMENT IT, and the reason is
// a measured false verdict. This function used to end in its own predicate —
// "some 0111 bit is set, therefore executable" — and that predicate does not
// answer the question a launch asks. An independent review reproduced it over
// real HTTP on one profile whose only change was the file mode: a file owned by
// the effective uid at mode 0401 has an execute bit (for others), is perfectly
// statable, and is UNAMBIGUOUSLY denied to the very user who would run it. The
// read answered 200 ready / program_present for a program that cannot start.
//
// `os/exec` already answers exactly this, on every supported platform, and
// answers it by METADATA: os.Stat, then `unix.Eaccess(file, X_OK)` — the
// effective-identity access check — falling back to the permission bits only
// when the kernel says the check is unavailable (ENOSYS, or EPERM under a
// seccomp policy). See the installed toolchain's `src/os/exec/lp_unix.go`
// findExecutable; Windows has its own PATHEXT rules in `lp_windows.go`, and
// deferring is what keeps both correct here without this file guessing at
// either. NOTHING below runs anything: LookPath is stat plus faccessat.
func inspectProgramMetadata(program string) RunnerProgramState {
	program = strings.TrimSpace(program)
	switch program {
	case "", ".", "..":
		// Launch refuses a spec with no program, and exec's own validateLookPath
		// refuses these three names outright. That is a wiring absence, not a
		// missing binary, so it is not reported as one.
		return RunnerProgramUnknown
	}
	// exec.Command's own rule, mirrored byte for byte (`filepath.Base(name) ==
	// name`): a bare name is resolved through PATH, anything else is used as
	// written.
	if filepath.Base(program) != program {
		return classifyProgramCandidate(program)
	}
	if _, err := exec.LookPath(program); err == nil {
		return RunnerProgramExecutable
	} else if !errors.Is(err, exec.ErrNotFound) {
		// LookPath refused for a reason of its own — ErrDot is the one that
		// happens: the name DID resolve, to a relative path the policy declines to
		// run, so the launch would fail too. "I could not resolve it to something
		// this runtime would start" is not "it is not installed", and it is not
		// ready either. Unknown is both, and this arm is unchanged on purpose.
		return RunnerProgramUnknown
	}
	// LookPath found nothing EXECUTABLE on PATH, and that single answer covers
	// three different worlds: absent everywhere, present and denied, and not
	// examinable. Re-walk the same candidates it walked and keep the cause it
	// discards — which is the whole defect on the bare-name path, where a file
	// that is statable and denied used to be reported as an absence.
	//
	// Reading PATH here is not the module re-deriving configuration from the
	// environment — the rule that forbids that is about the PROGRAM, which the
	// caller resolved from m.rt.program/driverProgram. This is the resolution
	// exec.LookPath just performed, repeated with the classification it discards.
	sawNotExecutable, sawUnknown := false, false
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			// Unix shell semantics, the same line lookPath has.
			dir = "."
		}
		switch classifyProgramCandidate(filepath.Join(dir, program)) {
		case RunnerProgramNotExecutable:
			sawNotExecutable = true
		case RunnerProgramUnknown:
			sawUnknown = true
		}
	}
	switch {
	case sawNotExecutable:
		// A candidate exists and cannot be executed BY THIS PROCESS: a nameable,
		// fixable cause, and never an absence.
		return RunnerProgramNotExecutable
	case sawUnknown:
		return RunnerProgramUnknown
	default:
		return RunnerProgramMissing
	}
}

// classifyProgramCandidate is the metadata verdict about ONE concrete candidate,
// and it is deliberately built from TWO observations rather than one, because
// one cannot separate the two things this contract must never confuse.
//
//   - `exec.LookPath` gives the VERDICT — the same effective-access resolution
//     the launch will perform, symlinks followed, PATHEXT applied where the
//     platform has it. But its refusal is ambiguous: it answers fs.ErrPermission
//     both when the file is examinable and denied, and when a directory on the
//     way could not be searched at all.
//   - our own `os.Stat` disambiguates exactly that, in the order `findExecutable`
//     itself uses: if the stat fails, the inspection could not look; if it
//     succeeds, whatever LookPath refuses afterwards is a statement ABOUT THE
//     FILE.
//
// Known denial is not_executable, an inspection that could not look is unknown,
// and only a proven absence is missing.
func classifyProgramCandidate(path string) RunnerProgramState {
	// LookPath first: on Windows it may resolve to another name entirely
	// (`claude` → `claude.exe`), so a stat of the literal path is not the
	// question and must not be allowed to answer it.
	_, err := exec.LookPath(lookPathDirect(path))
	if err == nil {
		return RunnerProgramExecutable
	}
	st, statErr := os.Stat(path)
	switch {
	case statErr != nil && errors.Is(statErr, fs.ErrNotExist):
		return RunnerProgramMissing
	case statErr != nil:
		// Permission denied on a directory of its path, a symlink loop, an I/O
		// error: this inspection could not look, and "not installed" would be a
		// claim nobody made.
		return RunnerProgramUnknown
	case st.IsDir():
		return RunnerProgramNotExecutable
	case errors.Is(err, fs.ErrNotExist):
		// It was there for our stat and gone (or extension-less on a platform that
		// needs one) for the resolution.
		return RunnerProgramMissing
	case errors.Is(err, fs.ErrPermission), errors.Is(err, exec.ErrNotFound):
		// Examinable, and this process may not execute it.
		return RunnerProgramNotExecutable
	default:
		return RunnerProgramUnknown
	}
}

// lookPathDirect makes a candidate unambiguous to exec.LookPath's "is this a
// path or a bare name?" test, so a candidate that happens to have no separator
// (`filepath.Join(".", "cli")` cleans to `cli`) is examined AS THAT FILE instead
// of triggering a second PATH search. The `.`+separator form is exactly what the
// standard library uses for the same purpose in lookExtensions.
func lookPathDirect(p string) string {
	if filepath.Base(p) == p {
		return "." + string(filepath.Separator) + p
	}
	return p
}
