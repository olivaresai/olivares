// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// sessionListenPorts lists the loopback TCP ports the session rooted at root
// listens on, read in the session's own network namespace. When the session has
// its own network, every listener there is the session's; on the engine's
// network, only sockets the session's process tree holds are. Processes that
// run the engine's binary are the session's network bridge and confinement
// helpers, never the agent's app, so their sockets never count; nor do the
// tool's own (OpenCode's server, say): they are its control surface, which the
// engine already governs, not the app the agent builds.
func sessionListenPorts(root int) ([]listenPort, error) {
	self, err := os.Stat("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	return listenPorts("/proc", root, func(pid int) bool {
		exe, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
		return err != nil || os.SameFile(exe, self)
	})
}

// listenPorts is sessionListenPorts over procfs; helper reports the processes
// that run the engine's binary.
func listenPorts(procfs string, root int, helper func(int) bool) ([]listenPort, error) {
	tree, parent, err := processTree(procfs, root)
	if err != nil {
		return nil, err
	}
	isHelper := map[int]bool{}
	for _, pid := range tree {
		isHelper[pid] = helper != nil && helper(pid)
	}
	session, excluded := map[string]bool{}, map[string]bool{}
	for _, pid := range tree {
		held := session
		// The tool is the topmost process that is not a helper: the root, or a
		// helper's child (in the session's own network the root is the bridge).
		if isHelper[pid] || pid == root || isHelper[parent[pid]] {
			held = excluded
		}
		dir := filepath.Join(procfs, strconv.Itoa(pid), "fd")
		entries, _ := os.ReadDir(dir) // a process that exited holds nothing
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join(dir, e.Name()))
			if inode, ok := strings.CutPrefix(target, "socket:["); err == nil && ok {
				held[strings.TrimSuffix(inode, "]")] = true
			}
		}
	}
	// A dev server started in the background may outlive its parent and leave
	// the tree. In the session's own network it is still the session's.
	ownNetwork := false
	if theirs, err := os.Readlink(filepath.Join(procfs, strconv.Itoa(root), "ns", "net")); err == nil {
		ours, err := os.Readlink(filepath.Join(procfs, "self", "ns", "net"))
		ownNetwork = err == nil && theirs != ours
	}
	var out []listenPort
	for _, table := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(procfs, strconv.Itoa(root), "net", table))
		if err != nil {
			if table == "tcp6" && os.IsNotExist(err) {
				continue // IPv6 is disabled on this host
			}
			return nil, err
		}
		lines := bufio.NewScanner(f)
		lines.Scan() // header
		for lines.Scan() {
			// sl local rem st queues tr retrnsmt uid timeout inode
			fields := strings.Fields(lines.Text())
			if len(fields) < 10 || fields[3] != "0A" || excluded[fields[9]] || !(ownNetwork || session[fields[9]]) {
				continue
			}
			if p, ok := dialablePort(fields[1]); ok && !slices.ContainsFunc(out, func(o listenPort) bool { return o.Port == p.Port }) {
				out = append(out, p)
			}
		}
		err = lines.Err()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, func(a, b listenPort) int { return a.Port - b.Port })
	return out, nil
}

// processTree is root and its descendants, by parent links in procfs, plus the
// members of root's process group (procrunner starts each session as one): a
// server started in the background stays in it after its parent exits.
// parent holds every process's parent pid.
func processTree(procfs string, root int) (tree []int, parent map[int]int, err error) {
	children, group, parent := map[int][]int{}, []int{}, map[int]int{}
	entries, err := os.ReadDir(procfs)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procfs, e.Name(), "stat"))
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...; comm may hold spaces and parentheses.
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			continue
		}
		// state ppid pgrp ...
		fields := strings.Fields(string(stat[i+1:]))
		if len(fields) < 3 {
			continue
		}
		if ppid, err := strconv.Atoi(fields[1]); err == nil {
			children[ppid] = append(children[ppid], pid)
			parent[pid] = ppid
		}
		if pgrp, err := strconv.Atoi(fields[2]); err == nil && pgrp == root && pid != root {
			group = append(group, pid)
		}
	}
	seen := map[int]bool{}
	add := func(pids ...int) {
		for _, p := range pids {
			if !seen[p] {
				seen[p] = true
				tree = append(tree, p)
			}
		}
	}
	add(root)
	add(group...)
	for i := 0; i < len(tree); i++ {
		add(children[tree[i]]...)
	}
	return tree, parent, nil
}

// dialablePort decodes a /proc/net/tcp{,6} local address (hex, each 32-bit
// word in host order) and returns the loopback address that reaches it. A
// socket bound to a non-loopback address is not the session's local app.
func dialablePort(local string) (listenPort, bool) {
	hexIP, hexPort, ok := strings.Cut(local, ":")
	raw, err := hex.DecodeString(hexIP)
	port, perr := strconv.ParseUint(hexPort, 16, 16)
	if !ok || err != nil || perr != nil || port == 0 || (len(raw) != 4 && len(raw) != 16) {
		return listenPort{}, false
	}
	ip := make(net.IP, len(raw))
	for i := 0; i < len(raw); i += 4 {
		binary.NativeEndian.PutUint32(ip[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	var dial net.IP
	switch {
	case ip.IsLoopback():
		dial = ip
	case ip.IsUnspecified() && len(raw) == 4:
		dial = net.IPv4(127, 0, 0, 1)
	case ip.IsUnspecified():
		dial = net.IPv6loopback
	default:
		return listenPort{}, false
	}
	return listenPort{Port: int(port), Address: net.JoinHostPort(dial.String(), strconv.Itoa(int(port)))}, true
}
