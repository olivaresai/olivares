// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Fake is an in-process Driver. It never executes a vendor binary. Conformance
// always runs against it; a PATH probe of claude/codex/grok is a separate test.
type Fake struct {
	kind string
	seq  atomic.Int64
	gen  atomic.Int64
}

// NewFake returns a Fake for kind (claude, codex or grok).
func NewFake(kind string) *Fake {
	if OfficialProgram(kind) == "" {
		kind = KindClaude
	}
	return &Fake{kind: kind}
}

func (f *Fake) Kind() string            { return f.kind }
func (f *Fake) OfficialProgram() string { return OfficialProgram(f.kind) }

func (f *Fake) Launch(_ context.Context, req LaunchRequest) (Session, error) {
	id := f.seq.Add(1)
	conv := req.ResumeID
	if conv == "" {
		conv = "fake-conv-" + strconv.FormatInt(id, 10)
	}
	gen := f.gen.Add(1)
	s := newFakeSession(f.kind, conv, gen, 42000+int(id))
	s.ring.append(StreamStdout, []byte(`{"type":"system","subtype":"init","session_id":"`+conv+`"}`), time.Now())
	s.ring.append(StreamStderr, []byte("fake-ready kind="+f.kind), time.Now())
	return s, nil
}

func (f *Fake) Resume(ctx context.Context, req ResumeRequest) (Session, error) {
	if req.ConversationID == "" {
		return nil, ErrResumeRequired
	}
	req.LaunchRequest.ResumeID = req.ConversationID
	return f.Launch(ctx, req.LaunchRequest)
}

type fakeSession struct {
	kind   string
	ref    string
	conv   string
	gen    int64
	pid    int
	ring   *seqRing
	mu     sync.Mutex
	gone   bool
	result Result
	echo   int
	waitCh chan struct{}
}

func newFakeSession(kind, conv string, gen int64, pid int) *fakeSession {
	return &fakeSession{
		kind:   kind,
		ref:    fmt.Sprintf("%s-gen-%d", conv, gen),
		conv:   conv,
		gen:    gen,
		pid:    pid,
		ring:   newSeqRing(64, 1<<20),
		waitCh: make(chan struct{}),
		result: Result{Generation: gen},
	}
}

func (s *fakeSession) Ref() string            { return s.ref }
func (s *fakeSession) ConversationID() string { return s.conv }
func (s *fakeSession) Generation() int64      { return s.gen }
func (s *fakeSession) PID() int               { return s.pid }

func (s *fakeSession) Send(ctx context.Context, line []byte) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return ErrProcessGone
	}
	s.echo++
	n := s.echo
	s.ring.append(StreamStdout, []byte(`{"type":"assistant","echo":true,"n":`+strconv.Itoa(n)+`}`), time.Now())
	s.ring.append(StreamStderr, []byte("stderr-echo "+strconv.Itoa(n)), time.Now())
	_ = line
	return nil
}

func (s *fakeSession) Attach(fromSeq int64) (<-chan Frame, func()) {
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(stop) }) }
	return followRing(s.ring, fromSeq, stop), cancel
}

func (s *fakeSession) Reconnect(fromSeq int64) (<-chan Frame, func(), error) {
	s.mu.Lock()
	gone := s.gone
	s.mu.Unlock()
	if gone {
		return nil, func() {}, ErrProcessGone
	}
	ch, cancel := s.Attach(fromSeq)
	return ch, cancel, nil
}

func (s *fakeSession) Stop(context.Context) (Result, error) {
	s.finish(0, true)
	return s.result, nil
}

func (s *fakeSession) Wait() (Result, error) {
	<-s.waitCh
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, nil
}

func (s *fakeSession) finish(code int, exited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return
	}
	s.gone = true
	s.result = Result{ExitCode: code, ProcessExited: exited, Generation: s.gen}
	s.ring.close()
	close(s.waitCh)
}

var (
	_ Driver  = (*Fake)(nil)
	_ Resumer = (*Fake)(nil)
	_ Session = (*fakeSession)(nil)
)
