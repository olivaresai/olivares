// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package localframe

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type receiver func([]byte, []byte, int) (int, int, int, error)

// Next authenticates every recvmsg range before any JSON is decoded. onRange is
// the server's held-pidfd liveness check; the client supplies nil.
func Next(c *net.UnixConn, e Expect, onRange func() error) (Record, error) {
	uid, err := overflowID("/proc/sys/kernel/overflowuid")
	if err != nil {
		return Record{}, fault("credentials_sentinel")
	}
	gid, err := overflowID("/proc/sys/kernel/overflowgid")
	if err != nil {
		return Record{}, fault("credentials_sentinel")
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return Record{}, err
	}
	return next(rawReceiver(raw), e, onRange, uid, gid)
}

func rawReceiver(raw syscall.RawConn) receiver {
	return func(p, oob []byte, flags int) (n, on, outflags int, err error) {
		var receiveErr error
		err = raw.Read(func(fd uintptr) bool {
			n, on, outflags, _, receiveErr = syscall.Recvmsg(int(fd), p, oob, flags)
			return receiveErr != syscall.EAGAIN && receiveErr != syscall.EWOULDBLOCK && receiveErr != syscall.EINTR
		})
		if err == nil {
			err = receiveErr
		}
		return
	}
}

func next(receive receiver, e Expect, onRange func() error, overflowUID, overflowGID uint32) (Record, error) {
	var first *syscall.Ucred
	total := 0
	read := func(dst []byte) error {
		for offset := 0; offset < len(dst); {
			// Separate staging storage prevents unauthenticated bytes reaching the frame.
			buf := make([]byte, len(dst)-offset)
			control := make([]byte, syscall.CmsgSpace(syscall.SizeofUcred)+syscall.CmsgSpace(253*4))
			n, on, flags, err := receive(buf, control, syscall.MSG_CMSG_CLOEXEC)
			if on < 0 || on > len(control) || n < 0 || n > len(buf) {
				return fault("record_malformed")
			}
			cred, controlErr := ancillary(control[:on], flags, e, overflowUID, overflowGID)
			if n == 0 {
				if on > 0 && controlErr != nil {
					return controlErr
				}
				if total == 0 {
					return fault("eof")
				}
				return fault("record_truncated")
			}
			if controlErr != nil {
				return controlErr
			}
			if first == nil {
				copy := cred
				first = &copy
			} else if *first != cred {
				return fault("credentials_changed")
			}
			if onRange != nil && onRange() != nil {
				return fault("owner_exited")
			}
			copy(dst[offset:], buf[:n])
			offset += n
			total += n
			if err != nil {
				return fault("record_truncated")
			}
		}
		return nil
	}
	var header [8]byte
	if err := read(header[:]); err != nil {
		return Record{}, err
	}
	if string(header[:4]) != "OLL1" {
		return Record{}, fault("record_malformed")
	}
	size := binary.BigEndian.Uint32(header[4:])
	if size < 2 || size > 16384 {
		return Record{}, fault("record_too_large")
	}
	body := make([]byte, int(size))
	if err := read(body); err != nil {
		return Record{}, err
	}
	// All range callbacks completed before the parser observes the body.
	return parseRecord(body)
}

func ancillary(control []byte, flags int, e Expect, overflowUID, overflowGID uint32) (syscall.Ucred, error) {
	var cred syscall.Ucred
	reason := ""
	count := 0
	// Parse one message at a time so a malformed later message cannot hide
	// descriptors delivered by an earlier SCM_RIGHTS. The kernel produces these
	// headers; every complete descriptor is closed even on another refusal path.
	for offset := 0; offset+syscall.SizeofCmsghdr <= len(control); {
		length := binary.NativeEndian.Uint64(control[offset : offset+8])
		if length < uint64(syscall.CmsgLen(0)) || length > uint64(len(control)-offset) {
			reason = "credentials_malformed"
			break
		}
		messages, err := syscall.ParseSocketControlMessage(control[offset : offset+int(length)])
		if err != nil || len(messages) != 1 {
			reason = "credentials_malformed"
			break
		}
		m := messages[0]
		if m.Header.Level == syscall.SOL_SOCKET && m.Header.Type == syscall.SCM_RIGHTS {
			for i := 0; i+4 <= len(m.Data); i += 4 {
				_ = syscall.Close(int(int32(binary.NativeEndian.Uint32(m.Data[i : i+4]))))
			}
			reason = "descriptors_received"
		} else if m.Header.Level == syscall.SOL_SOCKET && m.Header.Type == syscall.SCM_CREDENTIALS {
			count++
			if int(m.Header.Len) != syscall.CmsgLen(syscall.SizeofUcred) {
				reason = "credentials_malformed"
			} else if value, err := syscall.ParseUnixCredentials(&m); err != nil {
				reason = "credentials_malformed"
			} else {
				cred = *value
			}
		} else {
			reason = "ancillary_unexpected"
		}
		offset += syscall.CmsgSpace(int(length) - syscall.CmsgLen(0))
	}
	switch {
	case flags&syscall.MSG_CTRUNC != 0:
		reason = "ancillary_truncated"
	case flags&syscall.MSG_TRUNC != 0:
		reason = "data_truncated"
	case flags&(syscall.MSG_OOB|syscall.MSG_ERRQUEUE) != 0:
		reason = "ancillary_unexpected"
	case reason != "":
	case count == 0:
		reason = "credentials_missing"
	case count != 1:
		reason = "credentials_malformed"
	case cred.Pid <= 0 || cred.Uid == overflowUID || cred.Gid == overflowGID:
		reason = "credentials_sentinel"
	case cred.Uid != e.UID:
		reason = "credentials_wrong_account"
	case e.PID != 0 && cred.Pid != e.PID:
		reason = "credentials_wrong_process"
	}
	if reason != "" {
		return syscall.Ucred{}, fault(reason)
	}
	return cred, nil
}

// Write sends exactly one frame and no ancillary data. The receiver obtains the
// sender's credentials from its SO_PASSCRED option.
func Write(c *net.UnixConn, r Record) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := parseRecord(body); err != nil {
		return err
	}
	if len(body) > 16384 {
		return fault("record_too_large")
	}
	frame := make([]byte, 8+len(body))
	copy(frame, "OLL1")
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(body)))
	copy(frame[8:], body)
	for len(frame) > 0 {
		n, err := c.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func overflowID(path string) (uint32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	return uint32(n), err
}
