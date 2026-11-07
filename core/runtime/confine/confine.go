// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package confine applies an explicit child-process filesystem policy. It adds
// no roots, devices or resource limits: those decisions belong to the caller.
// Linux uses the engine's re-exec helper, Landlock and no_new_privs, and clears
// the child's capability sets; it empties the bounding set only when the helper
// holds CAP_SETPCAP (DropCapabilities). It does not restrict network access or
// install seccomp unless Offline is requested.
package confine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// HelperArg selects the engine's confinement helper before normal startup.
const HelperArg = "__olivares_confine"

const policyArg = "--policy-v1"

// A distinct marker makes an older on-disk engine refuse an offline launch,
// rather than ignore a JSON field it does not understand after an upgrade.
const offlinePolicyArg = "--offline-policy-v1"

// Policy grants only the paths and devices explicitly supplied by a caller.
// Sealed paths cannot overlap any writable grant; Protect paths are carved out
// of directory grants. Limits is optional and never supplied by the mechanism.
// NoDelete folders are writable without the rights to remove, rename or move a
// name: the child may create and change entries and never lose one. Like Sealed
// paths they cannot overlap a writable grant, which would give the rights back.
// KeepWritable names the one kind of exception: a path inside a NoDelete folder that
// keeps the full write rights (a Git directory, which Git rewrites by renaming a lock
// file over the index). It must lie strictly inside a NoDelete folder: the folder
// itself would give every right back.
type Policy struct {
	// Offline denies sockets, namespace entry and descriptor theft in the child.
	Offline                                                      bool `json:",omitempty"`
	ReadWrite, ReadOnly, Protect, Sealed, NoDelete, KeepWritable []string
	// ReadFiles are files the child may read, each one a regular file of its own that the caller
	// already checked (ReadFileOf). Unlike a ReadOnly path, which may be a folder or a link, each is
	// opened again when the grant is made, and the launch is refused unless the path still leads,
	// with no link, to that same file.
	ReadFiles []ReadFile
	// SealedFiles are borrowed canonical directory handles; the caller closes them after launch.
	SealedFiles []*os.File `json:"-"`
	Devices     []Device
	Limits      *Limits
	// Ready is an optional inherited launch handshake. It receives a positive
	// marker only after confinement is installed, then closes on successful exec.
	Ready *os.File `json:"-"`
}

// ReadFile is a file the caller checked: the path where it really is, and the device and inode it
// had when it was checked. A file with another identity at that path is not granted.
type ReadFile struct {
	Path     string
	Dev, Ino uint64
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
	// ReadyFD is the descriptor the helper inherits Policy.Ready on, after the sealed handles.
	ReadyFD int `json:",omitempty"`
}

type directoryHandle struct {
	FD   int
	Path string
}

// IsExplicitPolicy distinguishes the shared helper payload from the published
// session helper's legacy four-field flags.
func IsExplicitPolicy(args []string) bool {
	return len(args) > 0 && (args[0] == policyArg || args[0] == offlinePolicyArg)
}

// IsWrapped reports whether Wrap replaced cmd's executable with the helper. A mount layer
// WrapMounts put in front of the policy (the user manager hidden from a plugin) is looked
// through: the arguments that follow it are Wrap's.
func IsWrapped(cmd *exec.Cmd) bool {
	if len(cmd.Args) < 3 || cmd.Args[1] != HelperArg {
		return false
	}
	args := cmd.Args[2:]
	if args[0] == mountsArg && len(args) > 2 {
		args = args[2:]
	}
	return IsExplicitPolicy(args)
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
	if p.Ready != nil {
		req.ReadyFD = 3 + len(cmd.ExtraFiles) + len(p.SealedFiles)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return state, err
	}
	marker := policyArg
	if p.Offline {
		marker = offlinePolicyArg
	}
	argv := append([]string{self, HelperArg, marker, string(payload), "--"}, cmd.Args...)
	cmd.Path, cmd.Args = self, argv
	cmd.ExtraFiles = append(cmd.ExtraFiles, p.SealedFiles...)
	if p.Ready != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, p.Ready)
	}
	return state, nil
}

// RunHelper applies the explicit policy and execs the original program path.
// A return means the program never ran; successful confinement does not return.
func RunHelper(args []string) int {
	return RunHelperWithReady(args, nil)
}

// RunHelperWithReady preserves the launch handshake through the explicit helper.
func RunHelperWithReady(args []string, ready *os.File) int {
	if IsMountsProbe(args) {
		return RunMountsProbe(args)
	}
	var req request
	// A plugin jail's mount layer (the engine user's user manager hidden from it) comes
	// before the policy, as in the session helper; arguments without one come back as they were.
	args, err := ConsumeMounts(args)
	switch {
	case err != nil:
	case len(args) < 4 || !IsExplicitPolicy(args) || args[2] != "--":
		err = errors.New("invalid explicit confinement payload")
	default:
		if err = json.Unmarshal([]byte(args[1]), &req); err == nil {
			if args[0] == offlinePolicyArg {
				req.Policy.Offline = true
			}
			if ready == nil && req.ReadyFD >= 3 {
				ready = os.NewFile(uintptr(req.ReadyFD), "confine-ready")
			}
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
	readFiles := make([]string, 0, len(p.ReadFiles))
	for _, file := range p.ReadFiles {
		readFiles = append(readFiles, file.Path)
	}
	for _, group := range [][]string{p.ReadWrite, p.ReadOnly, readFiles, p.Protect, p.Sealed, p.NoDelete, p.KeepWritable} {
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
	// A no-delete folder is writable too: a read-only path under it would be changed
	// through it, so it is checked like any writable grant. So is a path it keeps writable.
	for _, sealed := range sealedPaths {
		s := realPath(sealed)
		for _, path := range append(append(append([]string{}, writable...), p.NoDelete...), p.KeepWritable...) {
			w := realPath(path)
			if contains(s, w) || contains(w, s) {
				return fmt.Errorf("confine: the read-only path %q would be writable through %q; the session was not started", sealed, path)
			}
		}
	}
	for _, folder := range p.NoDelete {
		n := realPath(folder)
		for _, path := range writable {
			w := realPath(path)
			if contains(n, w) || contains(w, n) {
				return fmt.Errorf("confine: the no-delete folder %q could delete through %q; the session was not started", folder, path)
			}
		}
	}
	for _, kept := range p.KeepWritable {
		if !slices.ContainsFunc(p.NoDelete, func(folder string) bool {
			f, k := realPath(folder), realPath(kept)
			return f != k && contains(f, k)
		}) {
			return fmt.Errorf("confine: %q is kept writable outside every no-delete folder; the session was not started", kept)
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
