// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProcFromTetragon(t *testing.T) {
	t.Run("nil process", func(t *testing.T) {
		pi := procFromTetragon(nil, "node-1")
		assert.Equal(t, "node-1", pi.node)
		assert.Empty(t, pi.binary)
	})
	t.Run("docker container", func(t *testing.T) {
		pi := procFromTetragon(&tetragonProcess{
			ExecID: "id-1", Pid: 42, Binary: "/usr/bin/claude", Arguments: "--print hello", Docker: "abc123",
		}, "node-1")
		assert.Equal(t, "claude", pi.exeBase)
		assert.Equal(t, []string{"--print", "hello"}, pi.args)
		assert.Equal(t, "abc123", pi.container)
	})
	t.Run("pod container preferred over docker", func(t *testing.T) {
		pi := procFromTetragon(&tetragonProcess{
			Binary: "/app/agent", Docker: "short",
			Pod: &tetragonPod{Container: &tetragonContainer{ID: "podcontainer123"}},
		}, "")
		assert.Equal(t, "podcontainer123", pi.container)
	})
}

func TestOriginRef(t *testing.T) {
	assert.Equal(t, "container:abc123def456/claude",
		procInfo{exeBase: "claude", container: "abc123def4567890"}.originRef())
	assert.Equal(t, "host:node-1/python3",
		procInfo{exeBase: "python3", node: "node-1"}.originRef())
	assert.Equal(t, "host/bash",
		procInfo{exeBase: "bash"}.originRef())
	assert.Equal(t, "host/unknown",
		procInfo{}.originRef())
}

func TestProcessKey(t *testing.T) {
	assert.Equal(t, "exec-id-9", procInfo{execID: "exec-id-9"}.processKey())
	assert.Equal(t, "host/bash#7", procInfo{exeBase: "bash", pid: 7}.processKey())
}

func TestMatchName(t *testing.T) {
	assert.True(t, matchName("claude", "claude"))
	assert.True(t, matchName("claude-code", "claude"))
	assert.True(t, matchName("claude_wrapper", "claude"))
	assert.False(t, matchName("claudette", "claude"))
	assert.False(t, matchName("xclaude", "claude"))
	assert.False(t, matchName("", "claude"))
}

func TestClassifierIsAgent(t *testing.T) {
	c := newClassifier([]string{"claude", "claude-code"})

	t.Run("by exe base", func(t *testing.T) {
		assert.True(t, c.isAgent(procInfo{exeBase: "claude"}))
		assert.True(t, c.isAgent(procInfo{exeBase: "claude-code"}))
	})
	t.Run("by argv token base", func(t *testing.T) {
		assert.True(t, c.isAgent(procInfo{exeBase: "node", args: []string{"/opt/agents/claude-code/cli.js"}}))
	})
	t.Run("non-agent", func(t *testing.T) {
		assert.False(t, c.isAgent(procInfo{exeBase: "python3", args: []string{"/srv/app/main.py"}}))
		assert.False(t, c.isAgent(procInfo{exeBase: "claudette"}))
	})
	t.Run("no signatures never matches", func(t *testing.T) {
		assert.False(t, newClassifier(nil).isAgent(procInfo{exeBase: "claude"}))
	})
}
