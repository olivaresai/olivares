// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package agenttoolsapi

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ollamaListenerHeldBy confirms that the socket listening on addr in the engine's own
// network is one that process pid holds open. A 200 on the loopback port alone may come
// from any local process that bound it first: the appliance policy lets every installed
// tool bind the model runtime ports.
func ollamaListenerHeldBy(pid int, addr string) error {
	return listenerHeldBy("/proc", pid, addr)
}

// listenerHeldBy is ollamaListenerHeldBy over procfs. Every listener that can take a
// connection to addr (its own address or the wildcard, on its port) must be one of
// pid's sockets, and there must be one.
func listenerHeldBy(procfs string, pid int, addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	want := net.ParseIP(host)
	port, perr := strconv.ParseUint(portText, 10, 16)
	if err != nil || perr != nil || want == nil {
		return fmt.Errorf("%s is not an IP address and port", addr)
	}
	held := map[string]bool{}
	dir := filepath.Join(procfs, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.New("the started Ollama's open sockets could not be read: " + err.Error())
	}
	var unread error
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			if unread == nil && !os.IsNotExist(err) { // a descriptor closed since ReadDir is not hidden
				unread = err
			}
			continue
		}
		if inode, ok := strings.CutPrefix(target, "socket:["); ok {
			held[strings.TrimSuffix(inode, "]")] = true
		}
	}
	if len(held) == 0 && unread != nil {
		return errors.New("the started Ollama's descriptors could not be read: " + unread.Error())
	}
	found := false
	for _, table := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(procfs, "self", "net", table))
		if err != nil {
			if table == "tcp6" && os.IsNotExist(err) {
				continue // IPv6 is disabled on this host
			}
			return errors.New("the engine's listening sockets could not be read: " + err.Error())
		}
		lines := bufio.NewScanner(f)
		lines.Scan() // header
		for lines.Scan() {
			// sl local rem st queues tr retrnsmt uid timeout inode
			fields := strings.Fields(lines.Text())
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			ip, p, ok := procNetAddress(fields[1])
			if !ok || p != port || !(ip.Equal(want) || ip.IsUnspecified()) {
				continue
			}
			if !held[fields[9]] {
				_ = f.Close()
				return fmt.Errorf("another process listens on %s", addr)
			}
			found = true
		}
		err = lines.Err()
		_ = f.Close()
		if err != nil {
			return errors.New("the engine's listening sockets could not be read: " + err.Error())
		}
	}
	if !found {
		return fmt.Errorf("the started Ollama does not listen on %s", addr)
	}
	return nil
}

// procNetAddress decodes a /proc/net/tcp{,6} local address: hex, each 32-bit word in
// host order, then the port.
func procNetAddress(local string) (net.IP, uint64, bool) {
	hexIP, hexPort, ok := strings.Cut(local, ":")
	raw, err := hex.DecodeString(hexIP)
	port, perr := strconv.ParseUint(hexPort, 16, 16)
	if !ok || err != nil || perr != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0, false
	}
	ip := make(net.IP, len(raw))
	for i := 0; i < len(raw); i += 4 {
		binary.NativeEndian.PutUint32(ip[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	return ip, port, true
}
