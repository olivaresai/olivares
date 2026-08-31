// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"path"
	"strings"
)

// procInfo is the connector's normalized view of a process: the actor attribution
// the kernel provides (README.md module III). The command line is held here, in
// memory, only for classification — it is NEVER emitted raw (docs/SECURITY-HARDENING.md).
type procInfo struct {
	execID    string
	pid       uint32
	binary    string
	exeBase   string
	args      []string
	container string
	node      string
	parentID  string
}

// procFromTetragon builds a procInfo from a Tetragon process and the event's node
// name. The Tetragon `arguments` string is split on whitespace — a faithful argv
// reconstruction is unnecessary because args are used only to classify (agent or
// not), never emitted.
func procFromTetragon(p *tetragonProcess, node string) procInfo {
	if p == nil {
		return procInfo{node: node}
	}
	pi := procInfo{
		execID:    p.ExecID,
		pid:       p.Pid,
		binary:    p.Binary,
		exeBase:   path.Base(p.Binary),
		args:      strings.Fields(p.Arguments),
		container: containerID(p),
		node:      node,
		parentID:  p.ParentExecID,
	}
	return pi
}

// containerID returns the most specific container identifier available — the
// Kubernetes pod container id, else the Docker id — or "" for a host process.
func containerID(p *tetragonProcess) string {
	if p.Pod != nil && p.Pod.Container != nil && p.Pod.Container.ID != "" {
		return p.Pod.Container.ID
	}
	return p.Docker
}

// originRef returns the stable workload identity the kernel attributes the access
// to: a container/host scope joined with the executable base name. It deliberately
// omits the pid and start time so repeated accesses by the same workload+binary
// de-duplicate to one edge per (origin, resource, mode). This is OriginKind
// "identity" (a non-human runtime identity), never "agent" — agent resolution is
// job (see doc.go).
func (pi procInfo) originRef() string {
	scope := "host"
	switch {
	case pi.container != "":
		scope = "container:" + shortID(pi.container)
	case pi.node != "":
		scope = "host:" + pi.node
	}
	base := pi.exeBase
	if base == "" {
		base = "unknown"
	}
	return scope + "/" + base
}

// processKey returns a stable per-process-instance key for FindingReport hashing.
// Tetragon's exec_id is unique and stable; when absent, a composite of the
// workload identity and pid is used.
func (pi procInfo) processKey() string {
	if pi.execID != "" {
		return pi.execID
	}
	return pi.originRef() + "#" + itoa(pi.pid)
}

// shortID truncates a long container id to its first 12 hex chars, the customary
// short form, so the origin reference stays readable and stable.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// itoa renders a uint32 without importing strconv at every call site.
func itoa(n uint32) string {
	if n == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// classifier decides whether a process is a cooperative agent that is EXPECTED to
// emit telemetry — used solely by the off-by-default anti-evasion detector, never
// to set an edge's OriginKind. Matching is case-sensitive and anchored to limit
// false positives: a signature matches the executable base name, or any path
// component of an argv token (so `node /opt/.../claude-code/cli.js` matches), where
// a "match" is an exact equality or a prefix followed by a separator (-, _, .).
// A bare path substring is NOT matched, so "claude" matches neither "claudette"
// nor "python-claude". This heuristic only affects the off-by-default detector and
// its signatures are operator-configurable.
type classifier struct {
	signatures []string
}

// newClassifier builds a classifier from the configured signatures.
func newClassifier(signatures []string) *classifier {
	return &classifier{signatures: signatures}
}

// isAgent reports whether pi matches any configured agent signature.
func (c *classifier) isAgent(pi procInfo) bool {
	if c == nil || len(c.signatures) == 0 {
		return false
	}
	for _, sig := range c.signatures {
		if sig == "" {
			continue
		}
		if matchName(pi.exeBase, sig) {
			return true
		}
		for _, a := range pi.args {
			for _, comp := range strings.Split(a, "/") {
				if matchName(comp, sig) {
					return true
				}
			}
		}
	}
	return false
}

// matchName reports whether name equals sig or begins with sig followed by a
// separator (so "claude" matches "claude-code" but not "claudette").
func matchName(name, sig string) bool {
	if name == sig {
		return true
	}
	if strings.HasPrefix(name, sig) {
		rest := name[len(sig):]
		return rest != "" && (rest[0] == '-' || rest[0] == '_' || rest[0] == '.')
	}
	return false
}
