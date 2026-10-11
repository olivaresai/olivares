// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package plugjail

import (
	"bufio"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/olivaresai/olivares/core/runtime/confine"
)

// applyOS applies the Linux confinement controls to cmd and records ONLY what
// VERIFIABLY applied in att. Env scoping (C1) is already done by Apply. Here:
//   - C3: a dedicated, PER-LAUNCH non-root UID/GID with all supplementary groups
//     dropped (only when the engine can change credentials; otherwise honestly
//     degraded). A distinct uid per plugin blocks cross-plugin /proc + ptrace reads.
//   - C2: a per-plugin cgroup v2 with memory/pids/cpu ceilings that are READ BACK to
//     confirm they took — a ceiling that silently no-ops (controller not delegated) is
//     recorded as degraded, never asserted.
//
// The shared helper applies Landlock and no_new_privs before original-path exec.
// Successful handshake confirms that it reached exec. Bounding capabilities,
// seccomp and active health monitoring are not enforced in this release.
func applyOS(cmd *exec.Cmd, c Confinement, att *Attestation) (Cleanup, error) {
	var scratch string
	removeScratch := noopCleanup
	if c.Landlock {
		state := confine.Probe()
		if state.Mode != confine.ModeLandlock {
			att.Degraded = append(att.Degraded, "landlock: "+state.Reason+"; no_new_privs was not applied")
		} else {
			scratch = c.WritableScratch
			if scratch == "" {
				var err error
				// /tmp is only the parent of this owned directory, never a grant.
				// Unlike an engine-private TMPDIR, it is traversable after UID drop.
				scratch, err = os.MkdirTemp("/tmp", "olivares-plugin-")
				if err != nil {
					return nil, fmt.Errorf("plugin scratch: %w", err)
				}
				var once sync.Once
				removeScratch = func() { once.Do(func() { _ = os.RemoveAll(scratch) }) }
			}
			program := cmd.Path
			if !filepath.IsAbs(program) && cmd.Dir != "" {
				program = filepath.Join(cmd.Dir, program)
			}
			policy, err := pluginPolicy(program, c.ReadableRoots, scratch)
			if err == nil {
				_, err = confine.Wrap(cmd, policy)
			}
			if err != nil {
				removeScratch()
				return nil, err
			}
			cmd.Env = append(cmd.Env, "TMPDIR="+scratch)
		}
	}
	sysAttr := &syscall.SysProcAttr{Setpgid: true} // own process group ⇒ group-kill on teardown

	// C3: run as a dedicated, per-launch non-root uid/gid with NO supplementary groups.
	var releaseUID int
	if os.Geteuid() == 0 {
		uid, distinct := uidAlloc.acquire(c.UID)
		if distinct {
			att.DedicatedUID = true
			releaseUID = uid // freed when the plugin is torn down (added to cleanup below)
		} else {
			// The per-launch uid pool is exhausted by co-resident plugins. Still drop to a
			// non-root uid so env scoping (C1) holds, but DO NOT assert a distinct uid — the
			// cross-plugin /proc isolation is honestly degraded, never falsely attested.
			uid = defaultPluginUID
			if c.UID > 0 {
				uid = c.UID
			}
			att.Degraded = append(att.Degraded,
				"uid: per-launch uid pool exhausted by co-resident plugins; this plugin shares a uid — cross-plugin /proc/ptrace isolation is NOT guaranteed")
		}
		sysAttr.Credential = &syscall.Credential{
			Uid: uint32(uid), Gid: uint32(c.GID),
			// NoSetGroups=false + empty Groups ⇒ the child calls setgroups([]) and drops
			// EVERY supplementary group the (root) engine held, leaving only the primary GID.
			NoSetGroups: false,
			Groups:      []uint32{},
		}
		att.UID = uid
		if scratch != "" {
			if err := os.Chown(scratch, uid, c.GID); err != nil {
				if releaseUID > 0 {
					uidAlloc.release(releaseUID)
				}
				removeScratch()
				return nil, fmt.Errorf("plugin scratch owner: %w", err)
			}
		}
	} else {
		att.Degraded = append(att.Degraded,
			"uid: engine is not privileged enough to drop to a dedicated non-root uid; plugin runs at the ENGINE's uid — same-uid process-memory isolation is not guaranteed; unsupported Landlock or an explicit /proc grant can expose /proc/<engine>/{environ,mem}")
	}
	// CapsDropped is a STRONG claim (bounding set cleared + no-new-privs so a setuid/
	// setcap binary cannot regain privilege). That is the re-exec launcher's job and is
	// NOT applied this release, so it is never asserted here — a non-root uid alone does
	// not satisfy it. DedicatedUID carries the honest "runs unprivileged" fact.
	if os.Geteuid() == 0 {
		att.Degraded = append(att.Degraded,
			"caps: the plugin runs non-root but the bounding capability set is not cleared; no_new_privs alone does not clear it")
	}

	// C2: per-plugin cgroup v2, spawned into via CgroupFD, with EACH ceiling read back to
	// confirm it took. Anything that could not be enforced is degraded, not asserted.
	cleanup := noopCleanup
	if fd, dir, eff, ok := makeCgroup(c); ok {
		sysAttr.UseCgroupFD = true
		sysAttr.CgroupFD = fd
		if eff.memory {
			att.MemoryBytes = c.MemoryBytes
		} else if c.MemoryBytes > 0 {
			att.Degraded = append(att.Degraded, "cgroup.memory: memory.max not enforced (controller not delegated)")
		}
		if eff.pids {
			att.PidsMax = c.PidsMax
		} else if c.PidsMax > 0 {
			att.Degraded = append(att.Degraded, "cgroup.pids: pids.max not enforced (controller not delegated)")
		}
		if !eff.cpu && c.CPUMaxPercent > 0 {
			att.Degraded = append(att.Degraded, "cgroup.cpu: cpu.max not enforced (controller not delegated)")
		}
		// Cgroup is "applied" only if AT LEAST the fork-bomb/OOM guards took; a cgroup dir
		// with no effective controller offers no protection and must not be asserted.
		att.Cgroup = eff.memory || eff.pids
		cleanup = cgroupCleanup(dir)
		if !att.Cgroup {
			att.Degraded = append(att.Degraded, "cgroup: created but no resource controller was enforceable; ceilings NOT in effect")
		}
	} else {
		att.Degraded = append(att.Degraded, "cgroup: cgroup v2 unavailable/undelegated; resource ceilings not enforced")
	}

	if c.Seccomp {
		att.Degraded = append(att.Degraded, "seccomp: deny-by-default syscall filter is a declared strong-Linux follow-up (re-exec launcher)")
	}
	// C6: an ACTIVE post-handshake health/kill budget is not enforced this release; the
	// cgroup OOM/pids guards (when effective) are the real resource kills.
	att.Degraded = append(att.Degraded, "health-budget: active post-handshake health-timeout kill is a declared follow-up; resource kills rely on the cgroup guards above")

	// Release the per-launch uid back to the allocator on teardown so a long-lived engine does
	// not leak uids out of the pool (F8). Order matters: reap the cgroup subtree FIRST (its
	// cgroup.kill terminates the plugin AND any forked orphans still at this uid), THEN return the
	// uid — never free a uid while a process at it may be live, or a racing launch reuses it as a
	// co-resident uid and the isolation breaks. released.Do keeps it idempotent (Cleanup is
	// documented safe to call more than once) so a double-call can't re-free a uid another plugin
	// has since acquired.
	if releaseUID > 0 {
		inner := cleanup
		var released sync.Once
		cleanup = func() {
			if inner != nil {
				inner()
			}
			released.Do(func() { uidAlloc.release(releaseUID) })
		}
	}

	inner := cleanup
	cleanup = func() { inner(); removeScratch() }
	cmd.SysProcAttr = sysAttr
	return cleanup, nil
}

// pluginPolicy supplies SDK grants explicitly; it never borrows session defaults.
// Runtime inspection happens in the parent, before credentials change. A 0711
// executable need not be readable by the plugin UID or by the Landlock helper.
func pluginPolicy(program string, readable []string, scratch string) (confine.Policy, error) {
	program, err := filepath.Abs(program)
	if err != nil {
		return confine.Policy{}, err
	}
	p := confine.Policy{ReadWrite: []string{scratch}, ReadOnly: append([]string{}, readable...),
		Devices: []confine.Device{{Path: "/dev/null", Write: true}, {Path: "/dev/urandom"}}}
	p.ReadOnly = append(p.ReadOnly, filepath.Dir(program), filepath.Dir(canonicalPath(program)))
	p.ReadOnly = append(p.ReadOnly, "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf")
	for _, bundle := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/ca-bundle.pem", "/etc/pki/tls/cacert.pem", "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", "/etc/ssl/cert.pem"} {
		if info, err := os.Stat(bundle); err == nil && info.Mode().IsRegular() {
			p.ReadOnly = append(p.ReadOnly, bundle)
			break
		}
	}
	interp, dynamic, err := executableRuntime(program)
	if err != nil {
		return p, err
	}
	if interp != "" {
		p.ReadOnly = append(p.ReadOnly, interp, canonicalPath(interp))
	}
	if dynamic {
		p.ReadOnly = append(p.ReadOnly, "/etc/ld.so.cache")
		switch runtime.GOARCH {
		case "amd64":
			p.ReadOnly = append(p.ReadOnly, "/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu", "/lib64", "/usr/lib64")
		case "arm64":
			p.ReadOnly = append(p.ReadOnly, "/lib/aarch64-linux-gnu", "/usr/lib/aarch64-linux-gnu", "/lib64", "/usr/lib64")
		}
	}
	return p, nil
}

func canonicalPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// executableRuntime names a shebang interpreter or ELF PT_INTERP, without
// changing execution to that interpreter or changing the plugin's original path.
func executableRuntime(program string) (interpreter string, dynamic bool, err error) {
	f, err := os.Open(program)
	if err != nil {
		return "", false, nil
	} // preserve ordinary exec/checksum refusal
	defer f.Close()
	var header [4]byte
	_, _ = f.ReadAt(header[:], 0)
	if string(header[:]) == "\x7fELF" {
		image, err := elf.NewFile(f)
		if err != nil {
			return "", false, nil // leave malformed files to the original checksum/exec gates
		}
		for _, prog := range image.Progs {
			if prog.Type == elf.PT_INTERP {
				data, err := io.ReadAll(io.LimitReader(prog.Open(), 4096))
				if err != nil {
					return "", false, nil
				}
				return strings.TrimRight(string(data), "\x00"), true, nil
			}
		}
		return "", false, nil
	}
	if string(header[:2]) == "#!" {
		line, _, err := bufio.NewReader(io.LimitReader(f, 256)).ReadLine()
		if err != nil {
			return "", false, nil
		}
		fields := strings.Fields(strings.TrimPrefix(string(line), "#!"))
		if len(fields) > 0 {
			interp := fields[0]
			image, openErr := elf.Open(interp)
			dynamic := false
			if openErr == nil {
				for _, prog := range image.Progs {
					if prog.Type == elf.PT_INTERP {
						dynamic = true
						break
					}
				}
				_ = image.Close()
			}
			return interp, dynamic, nil
		}
	}
	return "", false, nil
}

// uidAlloc hands out a per-launch uid DISTINCT among currently-live plugins so co-resident
// plugins cannot read one another's memory over /proc / ptrace at a shared uid (F8). The
// old scheme (base + counter mod uidRange) wrapped after uidRange launches — so two CO-RESIDENT
// plugins could share a uid — and could land on reserved ids (nobody/nogroup); the attestation
// nonetheless ASSERTED the isolation. This allocator guarantees distinctness among live plugins,
// skips reserved/system uids, and releases the uid on teardown.
const (
	uidRange     = 4096 // dedicated uids span [base, base+uidRange)
	minPluginUID = 1000 // never hand out a system uid
)

// reservedUIDs are never assigned to a plugin: root, and the nobody/nogroup ids that a default
// base (defaultPluginUID=65533) would otherwise reach at base+1 / base+2.
var reservedUIDs = map[int]bool{0: true, 65534: true, 65535: true}

type uidAllocator struct {
	mu   sync.Mutex
	live map[int]bool
}

var uidAlloc = &uidAllocator{live: map[int]bool{}}

// acquire returns the lowest free, non-reserved, non-system uid in [base, base+uidRange) that
// no currently-live plugin holds, marking it live. ok=false ⇒ the whole range is held by live
// plugins (> uidRange co-resident, unrealistic) — the caller degrades honestly rather than
// assert a false isolation.
func (a *uidAllocator) acquire(base int) (uid int, ok bool) {
	if base <= 0 {
		base = defaultPluginUID
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for off := 0; off < uidRange; off++ {
		cand := base + off
		if cand < minPluginUID || reservedUIDs[cand] || a.live[cand] {
			continue
		}
		a.live[cand] = true
		return cand, true
	}
	return 0, false
}

func (a *uidAllocator) release(uid int) {
	a.mu.Lock()
	delete(a.live, uid)
	a.mu.Unlock()
}

// cgroupRoot is the cgroup v2 unified-hierarchy mount point.
const cgroupRoot = "/sys/fs/cgroup"

// pluginsParent is the intermediate cgroup that holds every per-plugin leaf.
const pluginsParent = "olivares-plugins"

var cgroupSeq atomic.Uint64

// effective reports which cgroup controllers were confirmed in effect (read-back).
type effective struct{ memory, pids, cpu bool }

// makeCgroup creates a per-plugin cgroup v2, writes the requested ceilings, and READS
// EACH ONE BACK to confirm it took (a controller the host did not delegate leaves the
// interface file absent, so the write fails silently — that is what the read-back
// catches, so the attestation never claims an unenforced ceiling). It returns ok=false
// (not an error) when no cgroup could be made — cgroup confinement is best-effort defense
// in depth, and its absence is a recorded degrade, not a launch failure.
func makeCgroup(c Confinement) (int, string, effective, bool) {
	var eff effective
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return 0, "", eff, false // not a cgroup v2 unified host
	}
	parent := filepath.Join(cgroupRoot, pluginsParent)
	_ = os.MkdirAll(parent, 0o700) // #nosec G301 -- 0700: only the engine may enter a plugin cgroup
	// Best-effort: delegate the controllers we need to the leaves. If the engine's own
	// cgroup did not delegate them, this write fails and the read-back below degrades.
	_ = os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0o200)

	name := sanitizeCgroupName(c.Name) + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(cgroupSeq.Add(1), 36)
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G301 -- 0700 as above
		return 0, "", eff, false
	}
	if c.MemoryBytes > 0 {
		want := strconv.FormatInt(c.MemoryBytes, 10)
		if os.WriteFile(filepath.Join(dir, "memory.max"), []byte(want), 0o600) == nil && readTrimmed(dir, "memory.max") == want {
			eff.memory = true
		}
	}
	if c.PidsMax > 0 {
		want := strconv.FormatInt(c.PidsMax, 10)
		if os.WriteFile(filepath.Join(dir, "pids.max"), []byte(want), 0o600) == nil && readTrimmed(dir, "pids.max") == want {
			eff.pids = true
		}
	}
	if c.CPUMaxPercent > 0 {
		const period = 100000 // 100ms
		quota := period * c.CPUMaxPercent / 100
		want := strconv.Itoa(quota) + " " + strconv.Itoa(period)
		if os.WriteFile(filepath.Join(dir, "cpu.max"), []byte(want), 0o600) == nil && readTrimmed(dir, "cpu.max") == want {
			eff.cpu = true
		}
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = os.RemoveAll(dir)
		return 0, "", eff, false
	}
	return fd, dir, eff, true
}

func readTrimmed(dir, file string) string {
	b, err := os.ReadFile(filepath.Join(dir, file)) // #nosec G304 -- dir is an engine-created cgroup path, file is a fixed name
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// cgroupCleanup kills every process still in the cgroup and removes the dir. A plugin
// that forked children would otherwise leave orphans that survive the go-plugin
// client.Kill of the direct child; cgroup.kill (Linux ≥5.14) tears the whole subtree
// down, with a cgroup.procs SIGKILL fallback. The CgroupFD is closed separately right
// after the spawn attempt (loader.dispense), so it is not held until teardown.
func cgroupCleanup(dir string) Cleanup {
	var once atomic.Bool
	return func() {
		if !once.CompareAndSwap(false, true) {
			return // idempotent: never double-remove or re-signal
		}
		if os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o200) != nil {
			// Fallback for kernels without cgroup.kill: SIGKILL every pid in the cgroup.
			for _, pid := range readCgroupPids(dir) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		_ = os.RemoveAll(dir)
	}
}

// CloseSpawnFD closes the CgroupFD once the plugin has been spawned into the cgroup (or
// the spawn failed) — the fd is not needed afterwards, so it must not be held for the
// plugin's whole lifetime (fd-exhaustion on a long-lived engine that rotates plugins).
// The caller invokes it right after the go-plugin spawn attempt.
func CloseSpawnFD(cmd *exec.Cmd) {
	if cmd != nil && cmd.SysProcAttr != nil && cmd.SysProcAttr.UseCgroupFD {
		_ = syscall.Close(cmd.SysProcAttr.CgroupFD)
	}
}

func readCgroupPids(dir string) []int {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs")) // #nosec G304 -- engine-created cgroup path, fixed file
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Fields(string(b)) {
		if p, err := strconv.Atoi(line); err == nil && p > 0 {
			pids = append(pids, p)
		}
	}
	return pids
}

// sanitizeCgroupName keeps a plugin name safe as a single cgroup path segment.
func sanitizeCgroupName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "plugin"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
