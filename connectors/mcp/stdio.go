// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// stdioWaitDelay bounds how long Close's Wait may block after the process is
// signaled, so an unclosed pipe (e.g. held by an orphaned grandchild) cannot
// wedge shutdown forever.
const stdioWaitDelay = 5 * time.Second

// stdioTransport speaks MCP over a subprocess's stdin/stdout as newline-delimited
// JSON-RPC (the MCP stdio transport). The subprocess is bound to the context it
// was created with, so a server-level timeout terminates it and unblocks reads.
type stdioTransport struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader

	// obs records governance-relevant server-initiated messages the read loop
	// skips while waiting for a response (deprecation posture, observer.go).
	obs requestObserver

	mu     sync.Mutex
	nextID int64
}

// newStdioTransport launches the configured MCP server as a subprocess and
// returns a transport over its stdio. The process inherits the connector's
// environment plus the spec's env (a server needs PATH to find its runtime); the
// spec env is used only to connect and is never persisted.
func newStdioTransport(ctx context.Context, spec serverSpec) (*stdioTransport, error) {
	if spec.Command == "" {
		return nil, fmt.Errorf("mcp: stdio server %q has no command", spec.Name)
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...) // #nosec G204 -- spec.Command/Args come from operator-owned MCP server config
	cmd.Env = append(os.Environ(), envPairs(spec.Env)...)
	cmd.Stderr = io.Discard // MCP stdio servers log to stderr; not our signal
	cmd.WaitDelay = stdioWaitDelay
	// Run the server in its own process group and make context cancellation kill
	// the whole group, so a server-level timeout reliably terminates the server
	// and any grandchildren (which would otherwise hold the pipe open). On
	// non-Unix this is a no-op and the default per-process kill applies.
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %q: %w", spec.Command, err)
	}
	return &stdioTransport{cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout)}, nil
}

// roundTrip writes one request and reads until the matching response, skipping
// any server notifications or logs interleaved on the stream.
func (t *stdioTransport) roundTrip(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	req.ID = t.nextID
	if err := t.write(req); err != nil {
		return nil, err
	}
	return t.readResponse(req.ID)
}

// notify writes a notification (no response is read).
func (t *stdioTransport) notify(_ context.Context, method string, params any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.write(rpcRequest{Method: method, Params: params, isNotification: true})
}

// setProtocolVersion is a no-op for stdio (the version is not carried on the wire).
func (t *stdioTransport) setProtocolVersion(string) {}

// observedServerRequests returns the server-initiated messages the read loop saw
// (introspect.go stamps them onto the catalog).
func (t *stdioTransport) observedServerRequests() []serverRequestObservation {
	return t.obs.observations()
}

// Close terminates the subprocess (its whole process group on Unix) and reaps it.
// WaitDelay bounds the Wait so an unclosed pipe cannot block shutdown forever.
func (t *stdioTransport) Close() error {
	_ = t.stdin.Close()
	_ = terminate(t.cmd)
	_ = t.cmd.Wait()
	return nil
}

// write frames and writes one message (one JSON object per line, no embedded
// newlines, per the stdio transport spec — json.Marshal never emits a newline).
func (t *stdioTransport) write(req rpcRequest) error {
	b, err := req.marshal()
	if err != nil {
		return err
	}
	if _, err := t.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("mcp: write: %w", err)
	}
	return nil
}

// readResponse reads newline-delimited messages until the response to id arrives,
// skipping anything else (server notifications, log lines that parse as JSON).
// Skipped server-initiated messages are OBSERVED (never answered) — the
// deprecation-posture seam.
func (t *stdioTransport) readResponse(id int64) (json.RawMessage, error) {
	for {
		line, err := t.out.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return nil, fmt.Errorf("mcp: read: %w", err)
		}
		var msg rpcMessage
		if jerr := json.Unmarshal(trimLine(line), &msg); jerr != nil {
			if err != nil {
				return nil, fmt.Errorf("mcp: read: %w", err)
			}
			continue // not a JSON-RPC message (stray stdout); skip
		}
		t.obs.observe(msg)
		if msg.isResponseTo(id) {
			if msg.Error != nil {
				return nil, msg.Error
			}
			return msg.Result, nil
		}
		if err != nil {
			return nil, fmt.Errorf("mcp: stream ended before response: %w", err)
		}
	}
}

// trimLine drops a trailing CR/LF from a read line.
func trimLine(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// envPairs renders a string map as "k=v" entries for exec.Cmd.Env.
func envPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
