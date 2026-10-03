// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bufio"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// THE REAL OPENCODE, WHOLE SESSION, ZERO REQUESTS TO ANY HOST BUT THE BOUND ONE (HU2 019).
//
// It runs the installed OpenCode (OLIVARES_TEST_OPENCODE_BIN; 1.18.34 is the version the
// product installs) with the environment the driver builds for a record-bound launch, drives
// one turn over ACP and keeps it alive for the background work that follows a turn (title,
// summary). Every host it tries is counted on two channels:
//   - HTTP(S)_PROXY points at a local proxy that refuses every request and records its
//     target, so an attempt counts even though nothing leaves;
//   - the remote address of every TCP socket the OpenCode process tree holds is read from
//     /proc every 50 ms, so a request that ignores the proxy counts too.
//
// A key's own API host is the one allowed host (the proxy refuses it: the bound endpoint is
// down). A local model allows only loopback, with its endpoint down (a closed port) and up
// (a loopback OpenAI-compatible endpoint that answers, and records each request).
func TestRealOpenCodeBoundSessionReachesNoOtherHost(t *testing.T) {
	bin := os.Getenv("OLIVARES_TEST_OPENCODE_BIN")
	if bin == "" {
		t.Skip("OLIVARES_TEST_OPENCODE_BIN is not set: this regression runs the installed OpenCode")
	}
	key := DriverLaunch{BoundProvider: BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://api.anthropic.com"}, Preset: PresetAsk}
	keyEnv := map[string]string{"ANTHROPIC_API_KEY": "fh-fixture-not-a-key-0000"}
	t.Run("anthropic key, endpoint down", func(t *testing.T) {
		got := oneRealOpenCodeTurn(t, bin, t.TempDir(), key, keyEnv)
		got.requireOnly(t, "anthropic", "api.anthropic.com:443")
	})
	t.Run("local model, endpoint down", func(t *testing.T) {
		closed := closedLoopbackPort(t)
		got := oneRealOpenCodeTurn(t, bin, t.TempDir(), DriverLaunch{LocalModelEndpoint: "http://" + closed + "/v1", LocalModels: []string{"qwen3:8b"}, Preset: PresetAsk}, nil)
		got.requireOnly(t, openCodeLocalProviderID)
	})
	t.Run("local model, endpoint up", func(t *testing.T) {
		fake := newFakeChatEndpoint(t)
		got := oneRealOpenCodeTurn(t, bin, t.TempDir(), DriverLaunch{LocalModelEndpoint: fake.url + "/v1", LocalModels: []string{"qwen3:8b"}, Preset: PresetAsk}, nil)
		got.requireOnly(t, openCodeLocalProviderID)
		if got.stop != "end_turn" || !strings.Contains(got.text, "hi") {
			t.Fatalf("turn on the answering endpoint = stop %q, text %q, error %s", got.stop, got.text, got.turnErr)
		}
		if n := fake.count(); n == 0 {
			t.Fatal("the local endpoint received no request")
		} else {
			t.Logf("the local endpoint answered %d requests (the turn and its background work)", n)
		}
	})
	// SR5C on a8ac380a: a baseURL saved for the bound provider in the profile's own
	// configuration or in the project must not carry the key and the prompt elsewhere.
	t.Run("anthropic key, a foreign baseURL saved in the profile and the project", func(t *testing.T) {
		foreign := newFakeChatEndpoint(t)
		home := t.TempDir()
		saved := `{"provider":{"anthropic":{"options":{"baseURL":"` + foreign.url + `/v1"}}}}`
		for _, dir := range []string{filepath.Join(home, ".config", "opencode"), filepath.Join(home, "project")} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(saved), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got := oneRealOpenCodeTurn(t, bin, home, key, keyEnv)
		got.requireOnly(t, "anthropic", "api.anthropic.com:443")
		if n := foreign.count(); n != 0 {
			t.Fatalf("the saved foreign endpoint received %d requests", n)
		}
	})
	// SR5C on 06a18e46: OpenCode merges a host-managed configuration AFTER the launch's own
	// (OPENCODE_TEST_MANAGED_CONFIG_DIR is OpenCode's own way to move that directory). This
	// case shows the tool doing it, which is why the record mint refuses such a host
	// (openCodeManagedProviderOverride); if it ever stops, the refusal can go.
	t.Run("a managed configuration on the host wins over the launch pin", func(t *testing.T) {
		foreign := newFakeChatEndpoint(t)
		managed := t.TempDir()
		if err := os.WriteFile(filepath.Join(managed, "opencode.json"),
			[]byte(`{"provider":{"anthropic":{"options":{"baseURL":"`+foreign.url+`/v1"}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		env := map[string]string{"ANTHROPIC_API_KEY": "fh-fixture-not-a-key-0000", "OPENCODE_TEST_MANAGED_CONFIG_DIR": managed}
		oneRealOpenCodeTurn(t, bin, t.TempDir(), key, env)
		if n := foreign.count(); n == 0 {
			t.Fatal("the managed baseURL received nothing: OpenCode no longer lets a managed file replace the launch pin")
		} else {
			t.Logf("the managed baseURL received %d requests despite the launch pin", n)
		}
	})
	// SR5C on a8ac380a: a session that was shared before keeps syncing to the share service
	// on resume unless sharing is switched off natively.
	t.Run("anthropic key, resume of a session shared before", func(t *testing.T) {
		home := t.TempDir()
		proxy := newRefusingProxy(t)
		first := startRealOpenCode(t, bin, home, key, keyEnv, proxy)
		sid, _, _ := first.newSession()
		first.finish()
		markOpenCodeSessionShared(t, filepath.Join(home, ".local", "share", "opencode", "opencode.db"), sid)
		second := startRealOpenCode(t, bin, home, key, keyEnv, proxy)
		second.resume(sid)
		got := second.turn(sid)
		got.currentModel, got.offered = "anthropic/", map[string]bool{"anthropic": true}
		got.requireOnly(t, "anthropic", "api.anthropic.com:443")
	})
}

type realOpenCodeResult struct {
	currentModel string
	offered      map[string]bool
	proxyHosts   map[string]int
	direct       map[string]bool
	stop, text   string
	turnErr      string
}

func (r realOpenCodeResult) requireOnly(t *testing.T, provider string, allowedHosts ...string) {
	t.Helper()
	t.Logf("current model %s; offered providers %v; proxied %v; direct %v; turn stop %q error %s",
		r.currentModel, r.offered, r.proxyHosts, r.direct, r.stop, r.turnErr)
	if !strings.HasPrefix(r.currentModel, provider+"/") {
		t.Errorf("current model = %q, want one of %s", r.currentModel, provider)
	}
	for p := range r.offered {
		if p != provider {
			t.Errorf("OpenCode offers a model of %s; only %s may be offered", p, provider)
		}
	}
	allowed := map[string]bool{}
	for _, h := range allowedHosts {
		allowed[h] = true
	}
	for host, n := range r.proxyHosts {
		if !allowed[host] {
			t.Errorf("%d request(s) to %s, a host other than the bound provider", n, host)
		}
	}
	for addr := range r.direct {
		t.Errorf("a direct connection to %s (not loopback, not through the proxy)", addr)
	}
}

// oneRealOpenCodeTurn runs one session with one turn and its background work.
func oneRealOpenCodeTurn(t *testing.T, bin, home string, launch DriverLaunch, keyEnv map[string]string) realOpenCodeResult {
	t.Helper()
	o := startRealOpenCode(t, bin, home, launch, keyEnv, newRefusingProxy(t))
	sid, current, offered := o.newSession()
	got := o.turn(sid)
	got.currentModel, got.offered = current, offered
	return got
}

// realOpenCode is one OpenCode child driven over ACP, its egress observed.
type realOpenCode struct {
	t       *testing.T
	cmd     *exec.Cmd
	stdin   io.Writer
	lines   chan map[string]any
	id      int
	text    strings.Builder
	proxy   *refusingProxy
	project string
	mu      sync.Mutex
	direct  map[string]bool
	done    chan struct{}
}

func startRealOpenCode(t *testing.T, bin, home string, launch DriverLaunch, keyEnv map[string]string, proxy *refusingProxy) *realOpenCode {
	t.Helper()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	launch.WorkDir = project
	env := []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost"}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		env = append(env, name+"="+proxy.url)
	}
	for _, item := range openCodeXDGMapping(home) {
		env = append(env, item.Name+"="+item.Value)
	}
	for _, item := range (openCodeDriver{}).LaunchEnv(launch) {
		env = append(env, item.Name+"="+item.Value)
	}
	for name, value := range keyEnv {
		env = append(env, name+"="+value)
	}
	cmd := exec.Command(bin, (openCodeDriver{}).LaunchArgs(launch)...)
	cmd.Dir, cmd.Env, cmd.Stderr = project, env, io.Discard
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start OpenCode: %v", err)
	}
	o := &realOpenCode{t: t, cmd: cmd, stdin: stdin, lines: make(chan map[string]any, 256), proxy: proxy, project: project,
		direct: map[string]bool{}, done: make(chan struct{})}
	t.Cleanup(o.finish)
	go func() {
		for {
			select {
			case <-o.done:
				return
			case <-time.After(50 * time.Millisecond):
			}
			for addr := range processTreeRemotes(cmd.Process.Pid) {
				o.mu.Lock()
				o.direct[addr] = true
				o.mu.Unlock()
			}
		}
	}()
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			m := map[string]any{}
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				o.lines <- m
			}
		}
		close(o.lines)
	}()
	if r := o.call("initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]any{"name": "olivares-egress-test", "version": "0"},
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false}}, 90*time.Second); r["result"] == nil {
		t.Fatalf("initialize = %v", r)
	}
	return o
}

func (o *realOpenCode) call(method string, params any, timeout time.Duration) map[string]any {
	o.id++
	id := o.id
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	_, _ = o.stdin.Write(append(raw, '\n'))
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-o.lines:
			if !ok {
				return map[string]any{"error": "OpenCode ended"}
			}
			if m["method"] == "session/update" {
				if u, _ := m["params"].(map[string]any)["update"].(map[string]any); u["sessionUpdate"] == "agent_message_chunk" {
					if c, _ := u["content"].(map[string]any); c != nil {
						o.text.WriteString(fmt.Sprint(c["text"]))
					}
				}
			}
			if m["method"] == "session/request_permission" {
				reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}})
				_, _ = o.stdin.Write(append(reply, '\n'))
			}
			if fmt.Sprint(m["id"]) == strconv.Itoa(id) && m["method"] == nil {
				return m
			}
		case <-deadline:
			return map[string]any{"error": "timeout"}
		}
	}
}

// newSession opens a conversation and returns its id, current model and offered providers.
func (o *realOpenCode) newSession() (string, string, map[string]bool) {
	r := o.call("session/new", map[string]any{"cwd": o.project, "mcpServers": []any{}}, 90*time.Second)
	res, _ := r["result"].(map[string]any)
	if res == nil {
		o.t.Fatalf("session/new = %v", r)
	}
	current, offered := "", map[string]bool{}
	options, _ := res["configOptions"].([]any)
	for _, raw := range options {
		opt, _ := raw.(map[string]any)
		if opt["id"] != "model" {
			continue
		}
		current = fmt.Sprint(opt["currentValue"])
		values, _ := opt["options"].([]any)
		for _, v := range values {
			value := fmt.Sprint(v.(map[string]any)["value"])
			offered[strings.SplitN(value, "/", 2)[0]] = true
		}
	}
	return fmt.Sprint(res["sessionId"]), current, offered
}

// resume reopens a stored conversation the way the driver does (session/resume).
func (o *realOpenCode) resume(sid string) {
	if r := o.call("session/resume", map[string]any{"sessionId": sid, "cwd": o.project, "mcpServers": []any{}}, 90*time.Second); r["result"] == nil {
		o.t.Fatalf("session/resume = %v", r)
	}
}

// turn sends one prompt, waits for its end and for the background work after it, and
// returns what was reached.
func (o *realOpenCode) turn(sid string) realOpenCodeResult {
	var out realOpenCodeResult
	r := o.call("session/prompt", map[string]any{"sessionId": sid, "prompt": []any{map[string]any{"type": "text", "text": "Say hi."}}}, 150*time.Second)
	if tr, _ := r["result"].(map[string]any); tr != nil {
		out.stop = fmt.Sprint(tr["stopReason"])
	}
	if r["error"] != nil {
		raw, _ := json.Marshal(r["error"])
		out.turnErr = string(raw)
	}
	time.Sleep(12 * time.Second) // the background work after a turn (title, summary, share sync)
	out.text = o.text.String()
	out.proxyHosts = o.proxy.hosts()
	o.mu.Lock()
	out.direct = map[string]bool{}
	for addr := range o.direct {
		out.direct[addr] = true
	}
	o.mu.Unlock()
	return out
}

func (o *realOpenCode) finish() {
	select {
	case <-o.done:
		return
	default:
		close(o.done)
	}
	_ = o.cmd.Process.Kill()
	_ = o.cmd.Wait()
}

// markOpenCodeSessionShared records a share for sid in OpenCode's own store, as a session
// shared from another client would have one.
func markOpenCodeSessionShared(t *testing.T, path, sid string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UnixMilli()
	if _, err := db.Exec("INSERT INTO session_share (session_id, id, secret, url, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)",
		sid, "fhshare1", "fh-share-secret", "https://opncd.ai/s/fhshare1", now, now); err != nil {
		t.Fatalf("mark the session shared: %v", err)
	}
}

// refusingProxy answers every proxied request 403 and records its target host.
type refusingProxy struct {
	url  string
	mu   sync.Mutex
	seen map[string]int
}

func newRefusingProxy(t *testing.T) *refusingProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &refusingProxy{url: "http://" + l.Addr().String(), seen: map[string]int{}}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				parts := strings.Fields(line)
				if len(parts) < 2 {
					return
				}
				host := parts[1]
				if i := strings.Index(host, "://"); i >= 0 {
					host = strings.SplitN(host[i+3:], "/", 2)[0]
				}
				p.mu.Lock()
				p.seen[host]++
				p.mu.Unlock()
				_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			}()
		}
	}()
	return p
}

func (p *refusingProxy) hosts() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for h, n := range p.seen {
		out[h] = n
	}
	return out
}

// fakeChatEndpoint is a loopback OpenAI-compatible endpoint that answers every request.
type fakeChatEndpoint struct {
	url string
	mu  sync.Mutex
	n   int
}

func newFakeChatEndpoint(t *testing.T) *fakeChatEndpoint {
	t.Helper()
	f := &fakeChatEndpoint{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.n++
		f.mu.Unlock()
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":0,"model":"qwen3:8b","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"qwen3:8b","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
			`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"qwen3:8b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		} {
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	f.url = "http://" + l.Addr().String()
	return f
}

func (f *fakeChatEndpoint) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// closedLoopbackPort is a loopback address nothing listens on.
func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// processTreeRemotes returns the non-loopback remote addresses of the TCP sockets held
// by root and its descendants.
func processTreeRemotes(root int) map[string]bool {
	pids := map[int]bool{root: true}
	entries, _ := os.ReadDir("/proc")
	for changed := true; changed; {
		changed = false
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pids[pid] {
				continue
			}
			stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
			if err != nil {
				continue
			}
			fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
			if len(fields) > 1 {
				if ppid, err := strconv.Atoi(fields[1]); err == nil && pids[ppid] {
					pids[pid], changed = true, true
				}
			}
		}
	}
	inodes := map[string]bool{}
	for pid := range pids {
		fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		for _, fd := range fds {
			link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
			if err == nil && strings.HasPrefix(link, "socket:[") {
				inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
			}
		}
	}
	out := map[string]bool{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, row := range strings.Split(string(raw), "\n")[1:] {
			cols := strings.Fields(row)
			if len(cols) < 10 || !inodes[cols[9]] {
				continue
			}
			ipPort := strings.SplitN(cols[2], ":", 2)
			b, err := hex.DecodeString(ipPort[0])
			if err != nil {
				continue
			}
			ip := make(net.IP, len(b))
			for i := 0; i < len(b); i += 4 {
				ip[i], ip[i+1], ip[i+2], ip[i+3] = b[i+3], b[i+2], b[i+1], b[i]
			}
			if ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			port, _ := strconv.ParseUint(ipPort[1], 16, 16)
			out[net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10))] = true
		}
	}
	return out
}
