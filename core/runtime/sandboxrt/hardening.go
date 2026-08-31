// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"encoding/json"
	"sort"
	"strconv"
)

// This file is the DEFENSE-IN-DEPTH hardening profile (ARCHITECTURE.md, docs/SECURITY-HARDENING.md):
// the canonical, maximal-isolation Profile every run applies, plus the OCI
// runtime-spec and pinned deny-by-default seccomp builders the gVisor backend
// feeds runsc (and the Firecracker backend mirrors in its VM config). It is pure
// construction: it RUNS and is TESTED for shape (readonly root, caps dropped,
// no-new-privs, seccomp deny-default, own network namespace = no NIC, tmpfs,
// non-root uid, pid/mem bounds) without needing the isolation binary present.

// defaultNonRootUID / GID is the unprivileged identity the workload runs as. It is
// the conventional "nobody"-class high uid used by distroless/runsc examples — the
// instance never runs as uid 0.
const (
	defaultNonRootUID = 65532
	defaultNonRootGID = 65532
	// defaultTmpfsBytes bounds the single writable tmpfs so a run cannot exhaust
	// host memory; the rest of the filesystem is read-only.
	defaultTmpfsBytes = 64 << 20 // 64 MiB
	// defaultPidsLimit caps the process count inside the instance (fork-bomb guard).
	defaultPidsLimit = 256
	// defaultMemoryBytes caps the instance memory.
	defaultMemoryBytes = 256 << 20 // 256 MiB
	// seccompBaseline is the pinned profile name recorded in the attestation.
	seccompBaseline = "olivares-sandbox-deny-by-default"
)

// Profile is the hardening profile applied to an ephemeral instance. The zero
// value is intentionally NOT safe — callers use HardenedProfile() so a backend
// can never accidentally run un-hardened.
type Profile struct {
	ReadonlyRoot    bool
	TmpfsMounts     []string // writable tmpfs mount points (everything else read-only)
	TmpfsBytes      int64
	DropAllCaps     bool
	NoNewPrivileges bool
	Seccomp         string // the pinned seccomp profile name ("" ⇒ none)
	UID             int
	GID             int
	PidsLimit       int64
	MemoryBytes     int64
	NoNetwork       bool // the instance gets its OWN (empty) network namespace — no NIC
}

// HardenedProfile returns the canonical maximal-isolation profile: read-only root
// + a single bounded tmpfs, cap-drop ALL, no-new-privileges, the pinned
// deny-by-default seccomp profile, a non-root uid, pid/mem bounds, and no network
// interface of its own (egress only via the engine's out-of-process proxy).
func HardenedProfile() Profile {
	return Profile{
		ReadonlyRoot:    true,
		TmpfsMounts:     []string{"/tmp"},
		TmpfsBytes:      defaultTmpfsBytes,
		DropAllCaps:     true,
		NoNewPrivileges: true,
		Seccomp:         seccompBaseline,
		UID:             defaultNonRootUID,
		GID:             defaultNonRootGID,
		PidsLimit:       defaultPidsLimit,
		MemoryBytes:     defaultMemoryBytes,
		NoNetwork:       true,
	}
}

// --- OCI runtime spec (the subset runsc / an OCI runtime consumes) --------------

// ociSpec is the minimal subset of the OCI runtime spec the gVisor backend writes
// to a bundle's config.json. Only the hardening-relevant fields are modeled; an
// unset field takes the runtime's default.
type ociSpec struct {
	OCIVersion string     `json:"ociVersion"`
	Process    ociProcess `json:"process"`
	Root       ociRoot    `json:"root"`
	Hostname   string     `json:"hostname,omitempty"`
	Mounts     []ociMount `json:"mounts,omitempty"`
	Linux      ociLinux   `json:"linux"`
}

type ociProcess struct {
	Terminal        bool            `json:"terminal"`
	User            ociUser         `json:"user"`
	Args            []string        `json:"args"`
	Env             []string        `json:"env,omitempty"`
	Cwd             string          `json:"cwd"`
	Capabilities    ociCapabilities `json:"capabilities"`
	NoNewPrivileges bool            `json:"noNewPrivileges"`
}

type ociUser struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

// ociCapabilities with every set empty encodes cap-drop ALL — the process keeps
// no Linux capability in any set.
type ociCapabilities struct {
	Bounding    []string `json:"bounding"`
	Effective   []string `json:"effective"`
	Inheritable []string `json:"inheritable"`
	Permitted   []string `json:"permitted"`
	Ambient     []string `json:"ambient"`
}

type ociRoot struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}

type ociMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options,omitempty"`
}

type ociLinux struct {
	Namespaces    []ociNamespace `json:"namespaces"`
	Seccomp       *ociSeccomp    `json:"seccomp,omitempty"`
	Resources     *ociResources  `json:"resources,omitempty"`
	MaskedPaths   []string       `json:"maskedPaths,omitempty"`
	ReadonlyPaths []string       `json:"readonlyPaths,omitempty"`
}

type ociNamespace struct {
	Type string `json:"type"`
}

type ociResources struct {
	Memory *ociMemory `json:"memory,omitempty"`
	Pids   *ociPids   `json:"pids,omitempty"`
}

type ociMemory struct {
	Limit int64 `json:"limit"`
}

type ociPids struct {
	Limit int64 `json:"limit"`
}

// ociSeccomp is the pinned deny-by-default seccomp profile: the default action is
// ERRNO (deny) and only a curated allowlist of syscalls is permitted.
type ociSeccomp struct {
	DefaultAction string              `json:"defaultAction"`
	Architectures []string            `json:"architectures"`
	Syscalls      []ociSeccompSyscall `json:"syscalls"`
}

type ociSeccompSyscall struct {
	Names  []string `json:"names"`
	Action string   `json:"action"`
}

// seccompAllowlist is the curated set of syscalls the deny-by-default profile
// permits — enough for a constrained workload to run and reach the egress proxy
// via ordinary connect(2), while DANGEROUS syscalls (ptrace, mount, kexec, bpf,
// init_module, reboot, …) fall through to the ERRNO default and are denied.
var seccompAllowlist = []string{
	"read", "write", "readv", "writev", "pread64", "pwrite64",
	"open", "openat", "close", "stat", "fstat", "lstat", "newfstatat", "lseek",
	"mmap", "mprotect", "munmap", "brk", "rt_sigaction", "rt_sigprocmask", "rt_sigreturn",
	"sigaltstack", "ioctl", "access", "pipe", "pipe2", "dup", "dup2", "dup3",
	"nanosleep", "clock_gettime", "clock_nanosleep", "gettimeofday", "getpid", "gettid",
	"futex", "sched_yield", "set_tid_address", "set_robust_list", "get_robust_list",
	"epoll_create1", "epoll_ctl", "epoll_pwait", "epoll_wait", "poll", "ppoll", "select", "pselect6",
	"socket", "connect", "getsockname", "getpeername", "setsockopt", "getsockopt",
	"sendto", "recvfrom", "sendmsg", "recvmsg", "shutdown",
	"fcntl", "getdents64", "getrandom", "exit", "exit_group", "arch_prctl",
	"clone3", "rseq", "madvise", "uname", "getcwd", "fchdir", "chdir", "prlimit64",
}

// seccompArches are the architectures the pinned profile covers.
var seccompArches = []string{"SCMP_ARCH_X86_64", "SCMP_ARCH_AARCH64"}

// buildSeccomp builds the pinned deny-by-default seccomp profile. The default
// action is ERRNO so any syscall outside the curated allowlist is refused.
func buildSeccomp() *ociSeccomp {
	names := make([]string, len(seccompAllowlist))
	copy(names, seccompAllowlist)
	sort.Strings(names)
	return &ociSeccomp{
		DefaultAction: "SCMP_ACT_ERRNO",
		Architectures: seccompArches,
		Syscalls:      []ociSeccompSyscall{{Names: names, Action: "SCMP_ACT_ALLOW"}},
	}
}

// proxyEnv is the environment that routes a workload's HTTP(S) egress through the
// engine's out-of-process proxy (the instance has no NIC of its own; the proxy is
// its sole sanctioned path). NO_PROXY is empty so nothing bypasses the gate.
func proxyEnv(proxyAddr string) []string {
	if proxyAddr == "" {
		return nil
	}
	u := "http://" + proxyAddr
	return []string{
		"HTTP_PROXY=" + u, "http_proxy=" + u,
		"HTTPS_PROXY=" + u, "https_proxy=" + u,
		"NO_PROXY=", "no_proxy=",
	}
}

// buildOCISpec assembles the hardened OCI config.json for a bundle: read-only
// root, a single bounded tmpfs, cap-drop ALL, no-new-privileges, the pinned
// seccomp, a non-root uid, an OWN (empty) network namespace (no NIC), and pid/mem
// bounds. rootPath is the bundle's rootfs dir; args is the in-instance entrypoint;
// extraMounts are additional (e.g. read-only bind) mounts the backend needs.
func buildOCISpec(profile Profile, rootPath string, args, env []string, extraMounts ...ociMount) ([]byte, error) {
	spec := ociSpec{
		OCIVersion: "1.1.0",
		Hostname:   "sandbox",
		Process: ociProcess{
			User:            ociUser{UID: profile.UID, GID: profile.GID},
			Args:            args,
			Env:             env,
			Cwd:             "/",
			NoNewPrivileges: profile.NoNewPrivileges,
			// Every capability set empty ⇒ cap-drop ALL.
			Capabilities: ociCapabilities{
				Bounding: []string{}, Effective: []string{}, Inheritable: []string{},
				Permitted: []string{}, Ambient: []string{},
			},
		},
		Root: ociRoot{Path: rootPath, Readonly: profile.ReadonlyRoot},
		Linux: ociLinux{
			Namespaces: []ociNamespace{
				{Type: "pid"}, {Type: "ipc"}, {Type: "uts"}, {Type: "mount"}, {Type: "cgroup"},
			},
			MaskedPaths: []string{
				"/proc/kcore", "/proc/keys", "/proc/timer_list", "/sys/firmware", "/proc/sched_debug",
			},
			ReadonlyPaths: []string{"/proc/sys", "/proc/sysrq-trigger", "/proc/irq", "/proc/bus"},
		},
	}
	// Own (empty) network namespace ⇒ no NIC inside; egress only via the proxy.
	if profile.NoNetwork {
		spec.Linux.Namespaces = append(spec.Linux.Namespaces, ociNamespace{Type: "network"})
	}
	for _, m := range profile.TmpfsMounts {
		spec.Mounts = append(spec.Mounts, ociMount{
			Destination: m, Type: "tmpfs", Source: "tmpfs",
			Options: []string{"nosuid", "nodev", "noexec", "size=" + strconv.FormatInt(profile.TmpfsBytes, 10)},
		})
	}
	spec.Mounts = append(spec.Mounts, extraMounts...)
	if profile.Seccomp != "" {
		spec.Linux.Seccomp = buildSeccomp()
	}
	if profile.MemoryBytes > 0 || profile.PidsLimit > 0 {
		res := &ociResources{}
		if profile.MemoryBytes > 0 {
			res.Memory = &ociMemory{Limit: profile.MemoryBytes}
		}
		if profile.PidsLimit > 0 {
			res.Pids = &ociPids{Limit: profile.PidsLimit}
		}
		spec.Linux.Resources = res
	}
	return json.MarshalIndent(spec, "", "  ")
}
