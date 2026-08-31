// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// subjectHost is the FindingReport SubjectKind for a procfs that cannot be read.
const subjectHost = "host"

// procInfo is the minimal-data view of one process extracted from procfs. Only
// the program/script basenames (for matching) and identifiers survive; the full
// command line and environment are never retained.
type procInfo struct {
	pid         int
	program     string // basename of argv[0] (or comm fallback)
	script      string // basename of argv[1], if any
	containerID string // short container id from cgroup, if any
}

// containerIDRe matches a 64-hex container id, optionally wrapped in the
// systemd-cgroup scope names used by Docker/containerd/CRI.
var containerIDRe = regexp.MustCompile(`(?:docker[-/]|cri-containerd[-/]|crio[-/])?([0-9a-f]{64})(?:\.scope)?`)

// gatherLinux walks proc_root once and emits a host->process containment edge
// (and a container->process edge when the process is containerized) for every
// process whose program or script basename matches an AI pattern. Processes are
// emitted in ascending pid order for determinism.
//
// A missing proc_root is a configured-but-failing target (enable_linux is on and
// proc_root was given): it yields exactly one health finding. Per-process read
// errors are skipped silently — a process can exit mid-walk, which is normal, not
// a connector fault. This function NEVER reads /proc/<pid>/environ and never
// emits a full command line.
func gatherLinux(ctx context.Context, cfg config, sink sdk.Sink, at time.Time) error {
	entries, err := os.ReadDir(cfg.procRoot)
	if err != nil {
		return sink.Emit(ctx, healthFinding(subjectHost, cfg.host, "procfs not readable", err, at))
	}

	procs := make([]procInfo, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		pid, ok := numericPID(e.Name())
		if !ok {
			continue
		}
		pi, ok := readProc(cfg.procRoot, pid)
		if !ok {
			continue // process vanished or unreadable mid-walk — normal, skip
		}
		procs = append(procs, pi)
	}

	sort.Slice(procs, func(i, j int) bool { return procs[i].pid < procs[j].pid })

	for _, pi := range procs {
		if err := ctx.Err(); err != nil {
			return err
		}
		pattern, matched := matchAI(cfg.aiPatterns, pi.program, pi.script)
		if !matched {
			continue
		}
		procRef := redact.Clean(fmt.Sprintf("%s/%s#%d", cfg.host, pi.program, pi.pid))
		if err := sink.Emit(ctx, hostProcessEdge(cfg.host, procRef, pattern, at)); err != nil {
			return err
		}
		if pi.containerID != "" {
			if err := sink.Emit(ctx, containerProcessEdge(pi.containerID, procRef, at)); err != nil {
				return err
			}
		}
	}
	return nil
}

// numericPID reports whether name is an all-digit directory name (a pid) and
// returns the parsed pid.
func numericPID(name string) (int, bool) {
	pid, err := strconv.Atoi(name)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// readProc reads the minimal public fields for one pid: comm (fallback program
// name), cmdline (NUL-split: argv[0] basename ⇒ program, argv[1] basename ⇒
// script), and cgroup (container id). It deliberately ignores /proc/<pid>/environ
// and does not retain the raw cmdline. status's "Uid:" line is read to record the
// real uid is available, but the uid itself is not emitted (it is not part of the
// resource ref); reading it confirms the process is observable. A pid that cannot
// be identified at all (no comm and no cmdline) is reported as not-ok.
func readProc(root string, pid int) (procInfo, bool) {
	base := filepath.Join(root, strconv.Itoa(pid))
	pi := procInfo{pid: pid}

	if program, script, ok := readCmdline(filepath.Join(base, "cmdline")); ok {
		pi.program = program
		pi.script = script
	}
	if pi.program == "" {
		if comm := readComm(filepath.Join(base, "comm")); comm != "" {
			pi.program = comm
		}
	}
	if pi.program == "" {
		return procInfo{}, false
	}
	pi.containerID = readContainerID(filepath.Join(base, "cgroup"))
	return pi, true
}

// readCmdline reads /proc/<pid>/cmdline, splits it on NUL, and returns the
// basenames of argv[0] (program) and argv[1] (script). The raw cmdline is NOT
// returned to the caller — only the two basenames needed for matching — so a
// secret passed as an argument never leaves this function.
func readCmdline(p string) (program, script string, ok bool) {
	data, err := os.ReadFile(p) //nolint:gosec // public /proc field, read-only
	if err != nil || len(data) == 0 {
		return "", "", false
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", false
	}
	program = path.Base(parts[0])
	if len(parts) > 1 && parts[1] != "" {
		script = path.Base(parts[1])
	}
	return program, script, true
}

// readComm reads /proc/<pid>/comm (the short process name) as a program fallback.
func readComm(p string) string {
	data, err := os.ReadFile(p) //nolint:gosec // public /proc field, read-only
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readContainerID scans /proc/<pid>/cgroup for a 64-hex container id (bare or in
// a docker-/cri-containerd-/crio- scope token) and returns its short (12-char)
// form, or "" when the process is not in a recognizable container.
func readContainerID(p string) string {
	data, err := os.ReadFile(p) //nolint:gosec // public /proc field, read-only
	if err != nil {
		return ""
	}
	if m := containerIDRe.FindStringSubmatch(string(data)); m != nil {
		full := m[1]
		if len(full) >= 12 {
			return full[:12]
		}
		return full
	}
	return ""
}

// matchAI reports the first AI pattern that is a case-insensitive substring of
// the program or script basename. patterns are already lowercased by splitCSV.
func matchAI(patterns []string, program, script string) (string, bool) {
	hay := strings.ToLower(program + " " + script)
	for _, pat := range patterns {
		if pat != "" && strings.Contains(hay, pat) {
			return pat, true
		}
	}
	return "", false
}
