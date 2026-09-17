// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"context"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// probeSpec is one bounded execution of a tool.
type probeSpec struct {
	Exe  string
	Args []string
	// Env is the complete environment; nothing is inherited.
	Env []string
	Dir string
	// Budget is the time the tool has to exit on its own; after it the process
	// group receives SIGTERM, and Grace later SIGKILL.
	Budget time.Duration
	Grace  time.Duration
}

const probeOutputCap = 64 << 10

// cappedBuffer keeps the first probeOutputCap bytes and discards the rest, so a
// tool that floods stdout cannot grow the receipt or the process.
type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := probeOutputCap - c.buf.Len()
	if room <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		c.truncated = true
		c.buf.Write(p[:room])
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string {
	s := c.buf.String()
	if c.truncated {
		s += "…[truncated]"
	}
	return s
}

func envNames(env []string) []string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func newProbeCmd(ctx context.Context, spec probeSpec, out *cappedBuffer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, spec.Exe, spec.Args...) // #nosec G204 -- spec.Exe is the absolute path of the staged file whose size and SHA-256 were just verified, or a detected path the operator named explicitly with --probe; args are fixed literals per provider
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Stdin = nil
	return cmd
}
