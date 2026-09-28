// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package localframe

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
)

type receiveRange struct {
	data, control []byte
	flags         int
}
type rangeSource struct {
	ranges []receiveRange
	asked  []int
	flags  []int
}

func (s *rangeSource) receive(p, oob []byte, flags int) (int, int, int, error) {
	s.asked = append(s.asked, len(p))
	s.flags = append(s.flags, flags)
	if len(s.ranges) == 0 {
		return 0, 0, 0, io.EOF
	}
	r := s.ranges[0]
	s.ranges = s.ranges[1:]
	if len(r.data) > len(p) {
		panic("reader consumed next frame")
	}
	return copy(p, r.data), copy(oob, r.control), r.flags, nil
}
func wire(body string) []byte {
	b := make([]byte, 8+len(body))
	copy(b, "OLL1")
	binary.BigEndian.PutUint32(b[4:8], uint32(len(body)))
	copy(b[8:], body)
	return b
}
func credentials(pid int32, uid uint32) []byte {
	return syscall.UnixCredentials(&syscall.Ucred{Pid: pid, Uid: uid, Gid: 1000})
}
func source(body string) *rangeSource {
	b := wire(body)
	c := credentials(42, 1000)
	return &rangeSource{ranges: []receiveRange{{data: b[:3], control: c}, {data: b[3:8], control: c}, {data: b[8:], control: c}}}
}

func TestReader_AuthenticatesEveryRangeWithoutReadAhead(t *testing.T) {
	s := source(`{"t":"ready","v":1}`)
	polls := 0
	r, err := next(s.receive, Expect{UID: 1000, PID: 42}, func() error { polls++; return nil }, 65534, 65534)
	if err != nil || r.Type != "ready" || polls != 3 {
		t.Fatalf("%#v polls=%d %v", r, polls, err)
	}
	for _, flag := range s.flags {
		if flag != syscall.MSG_CMSG_CLOEXEC {
			t.Fatalf("flags=%d", flag)
		}
	}
	if len(s.asked) != 3 || s.asked[0] != 8 || s.asked[1] != 5 || s.asked[2] != len(`{"t":"ready","v":1}`) {
		t.Fatalf("reads=%v", s.asked)
	}
}

func TestReader_RefusesBoundsAndClosedSchema(t *testing.T) {
	for _, body := range []string{`{"t":"ready","v":1,"extra":1}`, `{"t":"ready","v":1,"v":1}`, `{"t":"ready","v":null}`, `{"t":"request","op":"task.plan","body":{"a":{"b":{"c":{"d":{"e":{"f":{"g":{}}}}}}}}}`, strings.Repeat(" ", 16385)} {
		s := source(body)
		if _, err := next(s.receive, Expect{UID: 1000}, nil, 65534, 65534); err == nil {
			t.Fatalf("accepted %.100s", body)
		}
	}
}

func TestReader_RefusesForeignTruncatedAndChangingCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		control []byte
		flags   int
	}{
		{"missing", nil, 0}, {"foreign", credentials(42, 999), 0}, {"other process", credentials(99, 1000), 0}, {"sentinel", credentials(0, 1000), 0},
		{"duplicate", append(credentials(42, 1000), credentials(42, 1000)...), 0}, {"control truncation", credentials(42, 1000), syscall.MSG_CTRUNC}, {"data truncation", credentials(42, 1000), syscall.MSG_TRUNC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := source(`{"t":"ready","v":1}`)
			s.ranges[1].control = tc.control
			s.ranges[1].flags = tc.flags
			if _, err := next(s.receive, Expect{UID: 1000, PID: 42}, nil, 65534, 65534); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	s := source(`{"t":"ready","v":1}`)
	s.ranges[1].control = credentials(43, 1000)
	if _, err := next(s.receive, Expect{UID: 1000}, nil, 65534, 65534); err == nil {
		t.Fatal("record changed sender")
	}
	s = source(`{"t":"ready","v":1}`)
	if _, err := next(s.receive, Expect{UID: 1000}, func() error { return errors.New("exited") }, 65534, 65534); err == nil {
		t.Fatal("owner exited")
	}
}

func TestReader_ClosesDescriptorsEvenWhenControlIsTruncated(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	fd, err := syscall.Dup(int(r.Fd()))
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	s := source(`{"t":"ready","v":1}`)
	s.ranges[0].control = append(credentials(42, 1000), syscall.UnixRights(fd)...)
	s.ranges[0].flags = syscall.MSG_CTRUNC
	if _, err := next(s.receive, Expect{UID: 1000}, nil, 65534, 65534); err == nil {
		t.Fatal("descriptor accepted")
	}
	if err := syscall.Close(fd); err != syscall.EBADF {
		t.Fatalf("descriptor leaked: %v", err)
	}
}
