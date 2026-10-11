// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
)

// TestPreviewListenerProcess is the child the port tests start: it listens on
// loopback, prints the port and waits for its stdin to close.
func TestPreviewListenerProcess(t *testing.T) {
	if os.Getenv("OLIVARES_PREVIEW_LISTENER") != "1" {
		t.Skip("helper process")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(l.Addr().(*net.TCPAddr).Port)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

// startPreviewListener starts the listener and returns the session root's pid:
// the listener itself, or a shell whose child it is (as a tool runs a dev server).
func startPreviewListener(t *testing.T, underShell bool) (pid, port int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPreviewListenerProcess$")
	if underShell {
		cmd = exec.Command("/bin/sh", "-c", `"$0" -test.run='^TestPreviewListenerProcess$'; :`, os.Args[0])
	}
	cmd.Env = append(os.Environ(), "OLIVARES_PREVIEW_LISTENER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	port, err = strconv.Atoi(line[:len(line)-1])
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, port
}

func TestSessionListenPortsReadsTheProcessTree(t *testing.T) {
	shell, port := startPreviewListener(t, true)
	want := listenPort{Port: port, Address: "127.0.0.1:" + strconv.Itoa(port)}
	// The tool (the shell here) started the server: the server is the session's.
	ports, err := listenPorts("/proc", shell, nil)
	if err != nil || !slices.Contains(ports, want) {
		t.Fatalf("session ports = %v, %v; want %v among them", ports, err, want)
	}
	// A process running the engine's own binary (here: this test binary) is a
	// bridge or helper, never the session's app.
	if ports, err := sessionListenPorts(shell); err != nil || slices.Contains(ports, want) {
		t.Fatalf("engine-binary ports = %v, %v; want %d not among them", ports, err, port)
	}
	// The tool's own listener is its control surface, not an app.
	tool, port := startPreviewListener(t, false)
	if ports, err := listenPorts("/proc", tool, nil); err != nil || len(ports) != 0 {
		t.Fatalf("the tool's own port %d = %v, %v; want none", port, ports, err)
	}
}

// A dev server the agent starts in the background outlives the shell that
// started it and leaves the process tree. It stays in the session's process
// group, so its port is still the session's.
func TestSessionListenPortsKeepsTheSessionsBackgroundServer(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `exec 3<&0; ( "$0" -test.run='^TestPreviewListenerProcess$' <&3 & ); exec cat >/dev/null`, os.Args[0])
	cmd.Env = append(os.Environ(), "OLIVARES_PREVIEW_LISTENER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // as procrunner starts a session
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(line[:len(line)-1])
	if err != nil {
		t.Fatal(err)
	}
	want := listenPort{Port: port, Address: "127.0.0.1:" + strconv.Itoa(port)}
	ports, err := listenPorts("/proc", cmd.Process.Pid, nil)
	if err != nil || !slices.Contains(ports, want) {
		t.Fatalf("session ports = %v, %v; want %v among them", ports, err, want)
	}
}

// In its own network namespace every listener is the session's, except the
// sockets the engine's bridge and helpers hold; on the engine's network only the
// session's own sockets count. A fake procfs pins both, whatever this host runs.
func TestListenPortsInTheSessionsNetwork(t *testing.T) {
	procfs := t.TempDir()
	file := func(name, body string) {
		t.Helper()
		path := filepath.Join(procfs, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(name, target string) {
		t.Helper()
		path := filepath.Join(procfs, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(path)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	// 100 is the bridge (the engine's binary, the session's root and process
	// group); 101 the tool it started; 102 the dev server the tool started.
	file("100/stat", "100 (olivares) S 1 100 100 0 -1")
	file("101/stat", "101 (opencode) S 100 100 100 0 -1")
	file("102/stat", "102 (python3) S 101 100 100 0 -1")
	link("100/fd/3", "socket:[222]")
	link("101/fd/3", "socket:[444]")
	link("102/fd/3", "socket:[111]")
	row := func(local, inode string) string {
		return "   0: " + local + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 " + inode + " 1 0 100 0 0 10 0\n"
	}
	file("100/net/tcp", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		row("0100007F:0BB8", "111")+ // 3000, the dev server's
		row("0100007F:0FA0", "222")+ // 4000, the bridge's
		row("0100007F:1000", "444")+ // 4096, the tool's own server
		row("0100007F:1388", "333")) // 5000, held by a process outside the tree
	helper := func(pid int) bool { return pid == 100 }
	ports := func() []int {
		t.Helper()
		got, err := listenPorts(procfs, 100, helper)
		if err != nil {
			t.Fatal(err)
		}
		out := []int{}
		for _, p := range got {
			out = append(out, p.Port)
		}
		return out
	}
	link("self/ns/net", "net:[1]")
	link("100/ns/net", "net:[2]")
	if got := ports(); !slices.Equal(got, []int{3000, 5000}) {
		t.Fatalf("own network = %v, want [3000 5000]", got)
	}
	link("100/ns/net", "net:[1]")
	if got := ports(); !slices.Equal(got, []int{3000}) {
		t.Fatalf("engine's network = %v, want [3000]", got)
	}
}

func TestDialablePortKeepsOnlyLoopback(t *testing.T) {
	for local, want := range map[string]string{
		"0100007F:0BB8":                         "127.0.0.1:3000",
		"00000000:1F90":                         "127.0.0.1:8080",
		"00000000000000000000000001000000:0050": "[::1]:80",
		"00000000000000000000000000000000:0050": "[::1]:80",
		"0000000000000000FFFF00000100007F:0050": "127.0.0.1:80",
		"0101A8C0:0050":                         "",
		"0100007F:0000":                         "",
		"zz:0050":                               "",
	} {
		got, ok := dialablePort(local)
		if (want == "") == ok || (ok && got.Address != want) {
			t.Errorf("%s = %v %t, want %q", local, got, ok, want)
		}
	}
}
