// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"os"
	"path/filepath"
	"strings"

	coreconfine "github.com/olivaresai/olivares/core/runtime/confine"
)

// systemReadOnly are the directories any program needs to load and run. A
// protected path inside one of them is carved out like any other grant.
var systemReadOnly = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
	"/etc", "/opt", "/proc", "/sys", "/snap", "/nix",
}

// deviceFiles are read and written by ordinary programs (and shells).
var deviceFiles = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty"}

// deviceDirs hold pseudo-terminals and shared memory.
var deviceDirs = []string{"/dev/pts", "/dev/shm"}

// defaultWritable are the paths the helper grants writable to every child.
func defaultWritable() []string {
	return append(append([]string{}, deviceDirs...), deviceFiles...)
}

var probe = func() State {
	s := coreconfine.Probe()
	return State{Mode: Mode(s.Mode), ABI: s.ABI, Reason: s.Reason}
}

func sessionPolicy(p Policy, program string) coreconfine.Policy {
	q := coreconfine.Policy{ReadWrite: append([]string{}, p.ReadWrite...), ReadOnly: append([]string{}, p.ReadOnly...), Protect: p.Protect, Sealed: p.Sealed, Limits: sessionLimits()}
	q.ReadOnly = append(q.ReadOnly, systemReadOnly...)
	q.ReadOnly = append(q.ReadOnly, filepath.Dir(program), filepath.Dir(realPath(program)))
	if tree := packageTree(realPath(program)); tree != "" {
		q.ReadOnly = append(q.ReadOnly, tree)
	}
	if lib := programLibrary(realPath(program)); lib != "" {
		q.ReadOnly = append(q.ReadOnly, lib)
	}
	if self, err := os.Executable(); err == nil {
		q.ReadOnly = append(q.ReadOnly, self)
	}
	for _, path := range deviceFiles {
		q.Devices = append(q.Devices, coreconfine.Device{Path: path, Write: true, Execute: true, IOCTL: true})
	}
	q.ReadWrite = append(q.ReadWrite, deviceDirs...)
	return q
}

// programLibrary is the lib/<program> directory beside the bin that holds path,
// or "". A managed release (<tools>/<driver>/<version>/bin/<program>) loads its
// runtime from there: Gemini's bundle, Ollama's libraries. Only a real directory
// reached without a link is granted: the helper resolves a link when it grants,
// so a lib or lib/<program> link would grant its target.
func programLibrary(path string) string {
	if !filepath.IsAbs(path) || filepath.Base(filepath.Dir(path)) != "bin" {
		return ""
	}
	lib := filepath.Join(filepath.Dir(filepath.Dir(path)), "lib", filepath.Base(path))
	if resolved, err := filepath.EvalSymlinks(lib); err != nil || resolved != lib {
		return ""
	}
	if fi, err := os.Stat(lib); err != nil || !fi.IsDir() {
		return ""
	}
	return lib
}

// packageTree is the outermost node_modules directory that holds path, or "".
// A tool installed by npm, pnpm or yarn is the whole tree: Codex's launcher
// runs its native binary from a sibling package, and Node resolves a package
// from any node_modules above it.
func packageTree(path string) string {
	if i := strings.Index(path, "/node_modules/"); i >= 0 {
		return path[:i+len("/node_modules")]
	}
	return ""
}
