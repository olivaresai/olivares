// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package confine applies an explicit child-process filesystem policy. It adds
// no roots, devices or resource limits: those decisions belong to the caller.
// Linux uses the engine's re-exec helper, Landlock and no_new_privs, and clears
// the child's capability sets; it empties the bounding set only when the helper
// holds CAP_SETPCAP (DropCapabilities). It does not restrict network access or
// install seccomp.
package confine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HelperArg selects the engine's confinement helper before normal startup.
const HelperArg = "__olivares_confine"

const policyArg = "--policy-v1"

// Policy grants only the paths and devices explicitly supplied by a caller.
// Sealed paths cannot overlap any writable grant; Protect paths are carved out
// of directory grants. Limits is optional and never supplied by the mechanism.
type Policy struct {
	ReadWrite, ReadOnly, Protect, Sealed []string
	// SealedFiles are borrowed canonical directory handles; the caller closes them after launch.
	SealedFiles []*os.File `json:"-"`
	Devices     []Device
	Limits      *Limits
	// Ready is an optional inherited launch handshake. It receives a positive
	// marker only after confinement is installed, then closes on successful exec.
	Ready *os.File `json:"-"`
}

// Device grants file rights without implicitly granting execute or ioctl.
type Device struct {
	Path                  string
	Write, Execute, IOCTL bool
}

// Limits lowers the child's core/file size ceilings and adds Nice to its nice
// value. It never raises an existing limit and imposes no memory ceiling.
type Limits struct {
	CoreSize, FileSize uint64
	Nice               int
}

type Mode string

const (
	ModeLandlock Mode = "landlock"
	ModeNone     Mode = "none"
)

// State reports host support, rather than whether a child has already entered
// its Landlock domain.
type State struct {
	Mode   Mode
	ABI    int
	Reason string
}

func (s State) String() string {
	if s.Mode == ModeLandlock {
		return fmt.Sprintf("landlock (ABI %d)", s.ABI)
	}
	return "none: " + s.Reason
}

func Probe() State { return probe() }

type request struct {
	Policy  Policy
	Program string
	Handles []directoryHandle `json:",omitempty"`
}

type directoryHandle struct {
	FD   int
	Path string
}

// IsExplicitPolicy distinguishes the shared helper payload from the published
// session helper's legacy four-field flags.
func IsExplicitPolicy(args []string) bool { return len(args) > 0 && args[0] == policyArg }

// IsWrapped reports whether Wrap replaced cmd's executable with the helper.
func IsWrapped(cmd *exec.Cmd) bool {
	return len(cmd.Args) > 2 && cmd.Args[1] == HelperArg && IsExplicitPolicy(cmd.Args[2:])
}

// Wrap preserves the caller's environment, directory, streams, cancellation
// and native credentials/cgroup settings. Borrowed sealed handles are appended
// to ExtraFiles; existing descriptor positions stay intact. Unsupported hosts
// leave cmd unchanged and say why.
func Wrap(cmd *exec.Cmd, p Policy) (State, error) {
	state := Probe()
	if err := p.validateHeldABI(state); err != nil {
		return state, err
	}
	if state.Mode != ModeLandlock {
		return state, nil
	}
	if err := p.validate(); err != nil {
		return state, err
	}
	self, err := os.Executable()
	if err != nil {
		return state, fmt.Errorf("confine: locate the engine binary: %w", err)
	}
	req := request{Policy: p, Program: cmd.Path}
	for i, file := range p.SealedFiles {
		req.Handles = append(req.Handles, directoryHandle{FD: 3 + len(cmd.ExtraFiles) + i, Path: file.Name()})
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return state, err
	}
	argv := append([]string{self, HelperArg, policyArg, string(payload), "--"}, cmd.Args...)
	cmd.Path, cmd.Args = self, argv
	cmd.ExtraFiles = append(cmd.ExtraFiles, p.SealedFiles...)
	return state, nil
}

// RunHelper applies the explicit policy and execs the original program path.
// A return means the program never ran; successful confinement does not return.
func RunHelper(args []string) int {
	return RunHelperWithReady(args, nil)
}

// RunHelperWithReady preserves the launch handshake through the explicit helper.
func RunHelperWithReady(args []string, ready *os.File) int {
	var req request
	var err error
	if len(args) < 4 || !IsExplicitPolicy(args) || args[2] != "--" {
		err = errors.New("invalid explicit confinement payload")
	} else if err = json.Unmarshal([]byte(args[1]), &req); err == nil {
		req.Policy.Ready = ready
		for _, handle := range req.Handles {
			if handle.FD < 3 {
				err = errors.New("invalid sealed directory handle")
				break
			}
			req.Policy.SealedFiles = append(req.Policy.SealedFiles, os.NewFile(uintptr(handle.FD), handle.Path))
		}
		if err == nil {
			err = Exec(req.Policy, req.Program, args[3:])
		}
	}
	if ready != nil {
		_, _ = fmt.Fprintln(ready, "filesystem confinement failed:", err)
	}
	fmt.Fprintln(os.Stderr, "olivares: the program was not started because it could not be confined:", err)
	return 126
}

// Exec enters the caller's domain and replaces the helper with program. The
// program path and argv are separate so argv[0] retains the caller's value.
func Exec(p Policy, program string, argv []string) error {
	if program == "" || len(argv) == 0 {
		return errors.New("confine: a program path and argv are required")
	}
	if err := p.validate(); err != nil {
		return err
	}
	return restrictAndExec(p, program, argv)
}

func (p Policy) validate() error {
	for _, group := range [][]string{p.ReadWrite, p.ReadOnly, p.Protect, p.Sealed} {
		for _, path := range group {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("confine: %q is not an absolute path", path)
			}
		}
	}
	writable := append([]string{}, p.ReadWrite...)
	for _, device := range p.Devices {
		if !filepath.IsAbs(device.Path) {
			return fmt.Errorf("confine: %q is not an absolute path", device.Path)
		}
		if device.Write {
			writable = append(writable, device.Path)
		}
	}
	if p.Limits != nil && (p.Limits.Nice < 0 || p.Limits.Nice > 19) {
		return errors.New("confine: nice increment must be between 0 and 19")
	}
	sealedPaths := append([]string{}, p.Sealed...)
	for _, file := range p.SealedFiles {
		if err := ValidateDirectoryHandle(file); err != nil {
			return err
		}
		sealedPaths = append(sealedPaths, file.Name())
	}
	for _, sealed := range sealedPaths {
		s := realPath(sealed)
		for _, path := range writable {
			w := realPath(path)
			if contains(s, w) || contains(w, s) {
				return fmt.Errorf("confine: the read-only path %q would be writable through %q; the session was not started", sealed, path)
			}
		}
	}
	return nil
}

func (p Policy) validateHeldABI(state State) error {
	if len(p.SealedFiles) > 0 && (state.Mode != ModeLandlock || state.ABI < 3) {
		return errors.New("confine: this kernel cannot block truncation of existing files in read-only folders; use a kernel with Landlock ABI 3 or later, or remove the extra read-only folders")
	}
	return nil
}

func realPath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

func contains(dir, path string) bool {
	return dir == path || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// OpenDirectory holds a canonical directory without following symlinks.
func OpenDirectory(path string) (*os.File, error) { return openDirectory(path) }

// ValidateDirectoryHandle checks the held object, without reopening its pathname.
func ValidateDirectoryHandle(file *os.File) error {
	if file == nil || !filepath.IsAbs(file.Name()) {
		return errors.New("confine: a sealed directory needs an absolute named handle")
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return errors.New("confine: the validated read-only directory handle is unavailable")
	}
	return directoryHandleUnchanged(file)
}
