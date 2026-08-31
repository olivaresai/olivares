// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHardenedProfileIsMaximal proves the canonical profile turns on every
// defense-in-depth control (no single-frontier reliance).
func TestHardenedProfileIsMaximal(t *testing.T) {
	p := HardenedProfile()
	if !p.ReadonlyRoot || !p.DropAllCaps || !p.NoNewPrivileges || !p.NoNetwork {
		t.Fatalf("hardened profile missing a control: %+v", p)
	}
	if p.UID == 0 || p.GID == 0 {
		t.Fatalf("hardened profile runs as root: uid=%d gid=%d", p.UID, p.GID)
	}
	if p.Seccomp == "" || len(p.TmpfsMounts) == 0 || p.MemoryBytes == 0 || p.PidsLimit == 0 {
		t.Fatalf("hardened profile under-specified: %+v", p)
	}
}

// TestBuildOCISpecHardenedShape decodes the generated config.json and asserts the
// full hardened shape: read-only root, empty capability sets (cap-drop ALL),
// no-new-privileges, a non-root uid, an own network namespace (no NIC), a bounded
// tmpfs, pid/mem limits, and masked paths.
func TestBuildOCISpecHardenedShape(t *testing.T) {
	jobMount := ociMount{Destination: "/sandbox/job.json", Type: "bind", Source: "/host/job.json", Options: []string{"ro", "bind"}}
	raw, err := buildOCISpec(HardenedProfile(), "/rootfs", []string{"/sandbox-harness", "/sandbox/job.json"}, proxyEnv("127.0.0.1:1234"), jobMount)
	if err != nil {
		t.Fatal(err)
	}
	var spec ociSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("config.json not valid: %v", err)
	}
	if !spec.Root.Readonly {
		t.Fatal("root not read-only")
	}
	if !spec.Process.NoNewPrivileges {
		t.Fatal("no-new-privileges not set")
	}
	if spec.Process.User.UID == 0 {
		t.Fatal("process runs as uid 0")
	}
	caps := spec.Process.Capabilities
	if len(caps.Bounding)+len(caps.Effective)+len(caps.Permitted)+len(caps.Inheritable)+len(caps.Ambient) != 0 {
		t.Fatalf("capabilities not fully dropped: %+v", caps)
	}
	if !hasNamespace(spec.Linux.Namespaces, "network") {
		t.Fatal("no network namespace (instance would have a NIC)")
	}
	if !hasNamespace(spec.Linux.Namespaces, "pid") || !hasNamespace(spec.Linux.Namespaces, "mount") {
		t.Fatal("missing pid/mount namespace")
	}
	// tmpfs mount present + bounded; the job bind mount is read-only.
	var sawTmpfs, sawJobRO bool
	for _, m := range spec.Mounts {
		if m.Type == "tmpfs" {
			sawTmpfs = true
			if !hasOpt(m.Options, "noexec") || !hasSizeOpt(m.Options) {
				t.Fatalf("tmpfs not bounded/noexec: %+v", m)
			}
		}
		if m.Destination == "/sandbox/job.json" {
			if !hasOpt(m.Options, "ro") {
				t.Fatalf("job bind mount not read-only: %+v", m)
			}
			sawJobRO = true
		}
	}
	if !sawTmpfs || !sawJobRO {
		t.Fatalf("missing tmpfs (%v) or read-only job mount (%v)", sawTmpfs, sawJobRO)
	}
	if spec.Linux.Resources == nil || spec.Linux.Resources.Memory == nil || spec.Linux.Resources.Pids == nil {
		t.Fatal("resource limits (mem/pids) not set")
	}
	if len(spec.Linux.MaskedPaths) == 0 {
		t.Fatal("no masked paths")
	}
}

// TestSeccompIsDenyByDefault proves the pinned profile denies by default and the
// curated allowlist excludes dangerous syscalls.
func TestSeccompIsDenyByDefault(t *testing.T) {
	sc := buildSeccomp()
	if sc.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Fatalf("default action = %q, want SCMP_ACT_ERRNO (deny)", sc.DefaultAction)
	}
	if len(sc.Syscalls) == 0 || len(sc.Syscalls[0].Names) == 0 {
		t.Fatal("empty allowlist")
	}
	allowed := map[string]bool{}
	for _, s := range sc.Syscalls {
		if s.Action != "SCMP_ACT_ALLOW" {
			t.Fatalf("non-allow rule in profile: %+v", s)
		}
		for _, n := range s.Names {
			allowed[n] = true
		}
	}
	for _, danger := range []string{"ptrace", "mount", "umount2", "bpf", "kexec_load", "init_module", "reboot", "setns", "unshare"} {
		if allowed[danger] {
			t.Fatalf("dangerous syscall %q is on the allowlist", danger)
		}
	}
	// Sanity: ordinary I/O and the egress connect path are permitted.
	for _, need := range []string{"read", "write", "socket", "connect", "exit_group"} {
		if !allowed[need] {
			t.Fatalf("required syscall %q missing from allowlist", need)
		}
	}
}

// TestProxyEnvForcesEgressThroughProxy proves the workload env points every HTTP
// scheme at the proxy and leaves no NO_PROXY bypass.
func TestProxyEnvForcesEgressThroughProxy(t *testing.T) {
	env := proxyEnv("127.0.0.1:9")
	joined := strings.Join(env, "\n")
	for _, want := range []string{"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("proxy env missing %q in:\n%s", want, joined)
		}
	}
	if proxyEnv("") != nil {
		t.Fatal("empty proxy addr should yield no env")
	}
}

func hasNamespace(ns []ociNamespace, t string) bool {
	for _, n := range ns {
		if n.Type == t {
			return true
		}
	}
	return false
}

func hasOpt(opts []string, want string) bool {
	for _, o := range opts {
		if o == want {
			return true
		}
	}
	return false
}

func hasSizeOpt(opts []string) bool {
	for _, o := range opts {
		if strings.HasPrefix(o, "size=") {
			return true
		}
	}
	return false
}
