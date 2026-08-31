// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEnvelopeFileKprobe(t *testing.T) {
	line := jsonLine(t, fileKprobe(procFix{
		execID: "x1", pid: 100, binary: "/usr/bin/cat", args: "cat /etc/passwd", node: "n1",
	}, "/etc/passwd", mayRead, ts(0)))

	env, err := parseEnvelope(line)
	require.NoError(t, err)
	require.NotNil(t, env.ProcessKprobe)
	assert.Equal(t, "security_file_permission", env.ProcessKprobe.FunctionName)
	assert.Equal(t, "/usr/bin/cat", env.ProcessKprobe.Process.Binary)
	assert.Equal(t, "n1", env.NodeName)

	fa := firstFileArg(env.ProcessKprobe.Args)
	require.NotNil(t, fa)
	assert.Equal(t, "/etc/passwd", fa.Path)

	mask, ok := firstIntArg(env.ProcessKprobe.Args)
	assert.True(t, ok)
	assert.Equal(t, mayRead, mask)
}

func TestParseEnvelopeConnectWithSNI(t *testing.T) {
	line := jsonLine(t, connectKprobe(procFix{binary: "/usr/bin/curl", node: "n1"}, "93.184.216.34", 443, "example.com", ts(0)))
	env, err := parseEnvelope(line)
	require.NoError(t, err)
	require.NotNil(t, env.ProcessKprobe)

	tup, ok := tupleFromArgs(env.ProcessKprobe.Args)
	require.True(t, ok)
	assert.Equal(t, "93.184.216.34", tup.dstIP)
	assert.Equal(t, uint32(443), tup.dport)
	assert.Equal(t, "example.com", tup.sni)
}

func TestParseEnvelopeTolerantToUnknownFields(t *testing.T) {
	// A future Tetragon field must not break decoding.
	raw := []byte(`{"process_kprobe":{"process":{"binary":"/bin/ls","future_field":123},"function_name":"security_file_permission","args":[{"file_arg":{"path":"/tmp"}}],"unknown":true},"time":"` + ts(0) + `","node_name":"n1","extra":{"x":1}}`)
	env, err := parseEnvelope(raw)
	require.NoError(t, err)
	require.NotNil(t, env.ProcessKprobe)
	assert.Equal(t, "/bin/ls", env.ProcessKprobe.Process.Binary)
}

func TestParseEnvelopeMalformed(t *testing.T) {
	_, err := parseEnvelope([]byte(`{not json`))
	assert.Error(t, err)
}

func TestEventTime(t *testing.T) {
	now := func() time.Time { return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) }

	t.Run("top-level time", func(t *testing.T) {
		env := tetragonEnvelope{Time: ts(0)}
		got := env.eventTime(now)
		assert.Equal(t, time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC), got)
	})
	t.Run("fallback to exit time", func(t *testing.T) {
		env := tetragonEnvelope{ProcessExit: &tetragonExit{Time: ts(time.Second)}}
		got := env.eventTime(now)
		assert.Equal(t, time.Date(2026, 6, 3, 12, 0, 1, 0, time.UTC), got)
	})
	t.Run("fallback to now", func(t *testing.T) {
		env := tetragonEnvelope{}
		assert.Equal(t, now(), env.eventTime(now))
	})
}
