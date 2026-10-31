// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// On the Server the hook runs in olivares_hook_t, whose port grant SELinux cannot bind to an
// address: in that domain the hook client itself refuses an endpoint off loopback,
// before it dials, and anywhere else it keeps today's behavior (a PEP behind an ingress).

// countingPEP serves on host and counts the requests that reach it.
func countingPEP(t *testing.T, host string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	var hits atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String() + "/", &hits
}

// nonLoopbackIPv4 is an address of this host that is not loopback, standing in for a remote PEP.
// redirectingPEP serves a loopback redirect to target and counts requests that reach the first hop.
func redirectingPEP(t *testing.T, target string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	var hits atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String() + "/", &hits
}

func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
				return n.IP.String()
			}
		}
	}
	t.Skip("this host has no non-loopback IPv4 address")
	return ""
}

func runHookIn(t *testing.T, selinux func() (bool, string, error), endpoint string) (error, string) {
	t.Helper()
	restore := hookSELinuxContext
	hookSELinuxContext = selinux
	t.Cleanup(func() { hookSELinuxContext = restore })
	cmd := newClaudeHookCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"true"}}`))
	cmd.SetArgs([]string{"--endpoint", endpoint, "--token", "fixture-token", "--tenant", "fixture-tenant", "--timeout", "3s"})
	err := cmd.Execute()
	return err, out.String()
}

func inDomain(domain string) func() (bool, string, error) {
	return func() (bool, string, error) { return true, "system_u:system_r:" + domain + ":s0", nil }
}

func TestClaudeHookInTheHookDomainRefusesARemoteEndpointWithoutConnecting(t *testing.T) {
	for _, domain := range []string{"olivares_hook_t", "olivares_container_hook_t"} {
		t.Run(domain, func(t *testing.T) {
			endpoint, hits := countingPEP(t, nonLoopbackIPv4(t))
			err, out := runHookIn(t, inDomain(domain), endpoint)
			if n := hits.Load(); n != 0 {
				t.Fatalf("from %s the hook client connected to %s (%d request(s)); it must refuse an endpoint off loopback before dialing", domain, endpoint, n)
			}
			if exitcode.From(err) != 2 || out != "" {
				t.Fatalf("refusal must exit 2 with nothing on stdout (deny closed); exit %d, err %v, stdout %q", exitcode.From(err), err, out)
			}
		})
	}
}

func TestClaudeHookDoesNotFollowARedirectOffLoopback(t *testing.T) {
	for _, domain := range []string{"olivares_hook_t", "olivares_container_hook_t"} {
		t.Run(domain, func(t *testing.T) {
			remote, remoteHits := countingPEP(t, nonLoopbackIPv4(t))
			endpoint, firstHits := redirectingPEP(t, remote)
			err, out := runHookIn(t, inDomain(domain), endpoint)
			if remoteHits.Load() != 0 || firstHits.Load() != 1 || !strings.Contains(out, `"permissionDecision":"deny"`) {
				t.Fatalf("a loopback redirect must be denied without reaching a remote PEP: first hops %d, remote hops %d, err %v, stdout %q", firstHits.Load(), remoteHits.Load(), err, out)
			}
		})
	}
}

func TestClaudeHookInTheHookDomainDeniesWhenItsContextCannotBeRead(t *testing.T) {
	endpoint, hits := countingPEP(t, "127.0.0.1")
	err, _ := runHookIn(t, func() (bool, string, error) { return true, "", errors.New("permission denied") }, endpoint)
	if hits.Load() != 0 || exitcode.From(err) != 2 {
		t.Fatalf("SELinux enabled and the context unreadable must deny closed (exit 2) without dialing; exit %d, requests %d", exitcode.From(err), hits.Load())
	}
}

func TestClaudeHookKeepsItsEndpointOutsideTheHookDomain(t *testing.T) {
	remote := nonLoopbackIPv4(t)
	for name, c := range map[string]struct {
		selinux func() (bool, string, error)
		host    string
	}{
		"hook domain, loopback PEP":   {inDomain("olivares_hook_t"), "127.0.0.1"},
		"tool domain, remote PEP":     {inDomain("olivares_tool_t"), remote},
		"no SELinux, remote PEP":      {func() (bool, string, error) { return false, "", nil }, remote},
		"unconfined host, remote PEP": {inDomain("unconfined_t"), remote},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint, hits := countingPEP(t, c.host)
			err, _ := runHookIn(t, c.selinux, endpoint)
			if hits.Load() < 1 || err != nil {
				t.Fatalf("the hook client must still ask %s (requests %d, err %v)", endpoint, hits.Load(), err)
			}
		})
	}
}

// the regression review: the transition into olivares_hook_t follows the program's label, so any
// subcommand would run there with the hook domain's network grant. In that domain the program
// runs only as `olivares claude-hook`; every other invocation is refused before it runs.
func TestHookDomainRunsOnlyClaudeHook(t *testing.T) {
	for name, c := range map[string]struct {
		selinux func() (bool, string, error)
		args    []string
		refused bool
	}{
		"hook domain, claude-hook":    {inDomain("olivares_hook_t"), []string{"claude-hook", "--hook-event", "PreToolUse"}, false},
		"container hook, claude-hook": {inDomain("olivares_container_hook_t"), []string{"claude-hook", "--hook-event", "PreToolUse"}, false},
		"container hook, codex-hook":  {inDomain("olivares_container_hook_t"), []string{"codex-hook"}, true},
		"container hook, version":     {inDomain("olivares_container_hook_t"), []string{"version"}, true},
		"container hook, helper":      {inDomain("olivares_container_hook_t"), []string{"__olivares_confine", "--", "/bin/true"}, true},
		"hook domain, codex-hook":     {inDomain("olivares_hook_t"), []string{"codex-hook"}, true},
		"hook domain, version":        {inDomain("olivares_hook_t"), []string{"version"}, true},
		"hook domain, confine helper": {inDomain("olivares_hook_t"), []string{"__olivares_confine", "--", "/bin/true"}, true},
		"hook domain, flag first":     {inDomain("olivares_hook_t"), []string{"--server", "http://10.0.0.1:9", "claude-hook"}, true},
		"hook domain, no arguments":   {inDomain("olivares_hook_t"), nil, true},
		"hook domain, context unreadable (EACCES), codex-hook": {func() (bool, string, error) {
			return true, "", errors.New("permission denied")
		}, []string{"codex-hook"}, true},
		"hook domain, mounts unreadable, confine helper": {func() (bool, string, error) {
			return true, "", errors.New("permission denied")
		}, []string{"__olivares_confine", "--", "/bin/true"}, true},
		"hook domain, context unreadable, claude-hook": {func() (bool, string, error) {
			return true, "", errors.New("permission denied")
		}, []string{"claude-hook"}, false},
		"command domain, codex-hook":      {inDomain("olivares_cmd_t"), []string{"codex-hook"}, false},
		"no SELinux, codex-hook":          {func() (bool, string, error) { return false, "", nil }, []string{"codex-hook"}, false},
		"unconfined administrator, serve": {inDomain("unconfined_t"), []string{"serve"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			restore := hookSELinuxContext
			hookSELinuxContext = c.selinux
			t.Cleanup(func() { hookSELinuxContext = restore })
			err := hookDomainArgs(c.args)
			if (err != nil) != c.refused {
				t.Fatalf("args %q: refused=%v, want %v (%v)", c.args, err != nil, c.refused, err)
			}
			if err != nil && exitcode.From(err) != exitcode.Usage {
				t.Fatalf("a refusal must exit %d (deny closed), got %d", exitcode.Usage, exitcode.From(err))
			}
		})
	}
}
