//go:build contract && linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package protocolcontract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func isolatedEnv(root string) []string {
	return []string{
		"HOME=" + filepath.Join(root, "home"), "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TERM=dumb",
		"TMPDIR=" + root, "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "claude"),
		"CODEX_HOME=" + filepath.Join(root, "codex"), "GROK_HOME=" + filepath.Join(root, "grok"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "GROK_DISABLE_AUTOUPDATER=1",
		"OPENCODE_DISABLE_AUTOUPDATE=true",
	}
}

// capBuffer prevents a changed vendor protocol from filling memory or disk.
// Keep the buffer named: embedding it promotes ReadFrom, which lets io.Copy
// and os/exec bypass Write and retain an uncapped stderr stream.
type capBuffer struct{ buf bytes.Buffer }

func (b *capBuffer) Len() int      { return b.buf.Len() }
func (b *capBuffer) Bytes() []byte { return b.buf.Bytes() }

func (b *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if space := (4 << 20) - b.buf.Len(); space > 0 {
		if len(p) > space {
			p = p[:space]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

type child struct {
	ctx            context.Context
	cmd            *exec.Cmd
	in             io.WriteCloser
	lines          chan json.RawMessage
	done           chan error
	stdout, stderr capBuffer
	stop           func()
}

func startChild(t *testing.T, exe string, args []string, outputDir, name string) *child {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"home", "claude", "codex", "grok", "config", "cache", "data", "state"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return startChildAt(t, exe, args, outputDir, name, root)
}

// A restart may reuse only the home this test created, never a host account.
func startChildAt(t *testing.T, exe string, args []string, outputDir, name, root string) *child {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	p := &child{ctx: ctx, lines: make(chan json.RawMessage, 32), done: make(chan error, 1)}
	p.cmd = exec.CommandContext(ctx, exe, args...)
	p.cmd.Dir = filepath.Join(root, "home")
	p.cmd.Env = isolatedEnv(root)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.cmd.Cancel = func() error { return syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL) }
	p.cmd.WaitDelay = 2 * time.Second
	p.cmd.Stderr = &p.stderr
	var err error
	p.in, err = p.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err = p.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		s := bufio.NewScanner(io.TeeReader(out, &p.stdout))
		s.Buffer(make([]byte, 4096), 2<<20)
		for s.Scan() {
			line := append(json.RawMessage(nil), s.Bytes()...)
			select {
			case p.lines <- line:
			case <-ctx.Done():
				_ = out.Close()
				p.done <- p.cmd.Wait()
				close(p.lines)
				return
			}
		}
		_ = out.Close()
		waitErr := p.cmd.Wait()
		if s.Err() != nil {
			waitErr = s.Err()
		}
		close(p.lines)
		p.done <- waitErr
	}()
	var stopped sync.Once
	p.stop = func() {
		stopped.Do(func() {
			_ = p.in.Close()
			// Stop the entire owned process group even if the vendor forks helpers.
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			cancel()
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				t.Error("protocol child did not stop")
				return
			}
			for suffix, data := range map[string][]byte{"stdout.ndjson": p.stdout.Bytes(), "stderr.log": p.stderr.Bytes()} {
				if err := os.WriteFile(filepath.Join(outputDir, name+"-"+suffix), data, 0600); err != nil {
					t.Error(err)
				}
			}
		})
	}
	t.Cleanup(p.stop)
	return p
}

func (p *child) send(value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = p.in.Write(append(b, '\n'))
	return err
}

func (p *child) read(match func(map[string]json.RawMessage) bool) (map[string]json.RawMessage, error) {
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				return nil, fmt.Errorf("protocol stdout closed before response")
			}
			var frame map[string]json.RawMessage
			if err := json.Unmarshal(line, &frame); err != nil || frame == nil {
				return nil, fmt.Errorf("stdout is not an NDJSON object")
			}
			if match(frame) {
				return frame, nil
			}
		case <-p.ctx.Done():
			return nil, fmt.Errorf("handshake response timed out")
		}
	}
}

func (p *child) call(id int, method string, params any, acp bool) (map[string]json.RawMessage, error) {
	request := map[string]any{"id": id, "method": method, "params": params}
	if acp {
		request["jsonrpc"] = "2.0"
	}
	if err := p.send(request); err != nil {
		return nil, err
	}
	return p.read(func(f map[string]json.RawMessage) bool { return string(f["id"]) == fmt.Sprint(id) })
}

func textField(object map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(object[name], &value)
	return value
}

func objectField(object map[string]json.RawMessage, name string) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(object[name], &value)
	return value
}
