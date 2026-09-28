// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package netguard

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type BootClock struct{ boot string }

func NewBootClock() (*BootClock, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	boot := strings.TrimSpace(string(b))
	if len(boot) != 36 {
		return nil, errors.New("boot_identity_unavailable")
	}
	return &BootClock{boot: boot}, nil
}
func (c *BootClock) Now() (string, time.Duration, error) {
	var ts syscall.Timespec
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 7, uintptr(unsafe.Pointer(&ts)), 0)
	if errno != 0 {
		return "", 0, errno
	}
	return c.boot, time.Duration(ts.Sec)*time.Second + time.Duration(ts.Nsec), nil
}

type processFD struct {
	fd       int
	identity ProcessIdentity
}

func processPoll(fd int) Death {
	p := struct {
		FD               int32
		Events, Returned int16
	}{FD: int32(fd), Events: 1}
	zero := syscall.Timespec{}
	_, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&p)), 1, uintptr(unsafe.Pointer(&zero)), 0, 0, 0)
	if errno != 0 || p.Returned&32 != 0 {
		return DeathUnknown
	}
	if p.Returned&(1|16) != 0 {
		return DeathProven
	}
	return DeathAlive
}
func procVisible() bool {
	self, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return false
	}
	var init string
	if os.Geteuid() == 0 {
		init, err = os.Readlink("/proc/1/ns/pid")
	} else {
		var proof namespaceProof
		gid, lookupErr := staticGroup("olivares-net-guard")
		if lookupErr != nil {
			return false
		}
		err = protectedJSON(namespaceProofPath, 0, gid, 0640, 1024, &proof)
		clock, clockErr := NewBootClock()
		if clockErr != nil || proof.BootID != clock.boot {
			return false
		}
		init = proof.Namespace
	}
	if err != nil || self != init {
		return false
	}
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 7 || f[4] != "/proc" {
			continue
		}
		found = true
		for _, word := range strings.Fields(strings.ReplaceAll(line, ",", " ")) {
			if strings.HasPrefix(word, "hidepid=") && word != "hidepid=0" {
				return false
			}
		}
	}
	return found
}
func procStart(pid int) (uint64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(b), ')')
	if end < 0 {
		return 0, errors.New("process_stat_invalid")
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) <= 19 {
		return 0, errors.New("process_stat_invalid")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}
func credentialProcess(fd int, unique, boot string) (*processFD, error) {
	reject := func() (*processFD, error) {
		syscall.Close(fd)
		return nil, errors.New("network_process_identity_unavailable")
	}
	if fd < 0 || !procVisible() || processPoll(fd) != DeathAlive {
		return reject()
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil || target != "anon_inode:[pidfd]" {
		return reject()
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_EXCL != 0 {
		return reject()
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return reject()
	}
	pid := 0
	for _, line := range strings.Split(string(b), "\n") {
		if value, ok := strings.CutPrefix(line, "Pid:"); ok {
			pid, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if pid <= 0 {
		return reject()
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return reject()
	}
	tgid := 0
	for _, line := range strings.Split(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "Tgid:"); ok {
			tgid, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	start, err := procStart(pid)
	if err != nil {
		return reject()
	}
	identity, err := IdentifyProcess(ProcessFacts{FromCredentials: true, WholeProcess: true, Alive: processPoll(fd) == DeathAlive, Visible: true, PID: pid, TGID: tgid, StartTime: start, BootID: boot, Unique: unique})
	if err != nil {
		return reject()
	}
	return &processFD{fd: fd, identity: identity}, nil
}
func originalDeath(id ProcessIdentity, held *processFD, boot string) Death {
	if boot != "" && boot != id.BootID {
		return DeathProven
	}
	if held != nil && held.fd >= 0 && held.identity == id {
		return processPoll(held.fd)
	}
	if !procVisible() {
		return DeathUnknown
	}
	start, err := procStart(id.PID)
	if errors.Is(err, os.ErrNotExist) {
		return DeathProven
	}
	if err != nil || start == 0 {
		return DeathUnknown
	}
	if start != id.StartTime {
		return DeathProven
	}
	return DeathAlive
}

const NetworkLockPath = "/run/olivares-network/network.lock"

type FileLock struct {
	filename string
	gid      uint32
	mu       sync.Mutex
	inode    os.FileInfo
}

func NewFileLock() (*FileLock, error) {
	group, err := user.LookupGroup("olivares-net-guard")
	if err != nil {
		return nil, err
	}
	gid, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil {
		return nil, err
	}
	return &FileLock{filename: NetworkLockPath, gid: uint32(gid)}, nil
}
func (l *FileLock) Acquire() (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	parent, err := os.Lstat(filepath.Dir(l.filename))
	if err != nil {
		return nil, err
	}
	ps, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || parent.Mode().Perm() != 0750 || ps.Uid != 0 || ps.Gid != l.gid {
		return nil, errors.New("network_lock_custody_refused")
	}
	f, err := os.OpenFile(l.filename, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	reject := func() (func(), error) { f.Close(); return nil, errors.New("network_lock_custody_refused") }
	info, err := f.Stat()
	if err != nil {
		return reject()
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0640 || st.Uid != 0 || st.Gid != l.gid || st.Nlink != 1 {
		return reject()
	}
	named, err := os.Lstat(l.filename)
	if err != nil || !os.SameFile(named, info) || l.inode != nil && !os.SameFile(l.inode, info) {
		return reject()
	}
	l.inode = info
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("network_lock_busy")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func closeProcess(p *processFD) { _ = syscall.Close(p.fd); p.fd = -1 }
