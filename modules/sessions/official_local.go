// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// officialLocal is the Community implementation of cliruntime.Driver: it
// spawns the vendor executable on the transport that vendor's owned launch
// form requires and sequences I/O. Session rows for console operate still go
// through Module.createRun; this type is the process contract the conformance
// battery and journeys J01–J08 drive.
type officialLocal struct {
	kind    string
	program string
	runner  Runner
	seq     atomic.Int64
	gen     atomic.Int64
}

// NewOfficialLocal returns the local driver for kind. program is the executable
// (a fixture script in tests; the vendor binary when on PATH).
//
// ⛔ THE TRANSPORT IS THE LAUNCH FORM'S, NOT THE HOST'S. Every owned operate
// argv in cliruntime.LaunchArgs is a stdio protocol, so the child gets pipes.
// Giving Claude Code a terminal on stdin makes its `--print` stream-json form
// refuse before it prints anything (cliruntime.LaunchTransport carries the
// measurement). The terminal runner is selected here, and only here, when a
// launch form declares that it needs one.
func NewOfficialLocal(kind, program string) (cliruntime.Driver, error) {
	runner, err := NewOfficialRunner(kind)
	if err != nil {
		return nil, err
	}
	return newOfficialLocalOn(kind, program, runner)
}

// newOfficialLocalOn binds one kind to one runner. It is the seam the PTY
// conformance battery drives, so the terminal runner keeps its contract
// coverage while no launch form selects it.
func newOfficialLocalOn(kind, program string, runner Runner) (cliruntime.Driver, error) {
	if cliruntime.OfficialProgram(kind) == "" {
		return nil, cliruntime.ErrUnknownKind
	}
	if program == "" {
		return nil, cliruntime.ErrNoProgram
	}
	return &officialLocal{kind: kind, program: program, runner: runner}, nil
}

func (d *officialLocal) Kind() string            { return d.kind }
func (d *officialLocal) OfficialProgram() string { return cliruntime.OfficialProgram(d.kind) }

func (d *officialLocal) Launch(ctx context.Context, req cliruntime.LaunchRequest) (cliruntime.Session, error) {
	args, err := cliruntime.LaunchArgs(d.kind, req)
	if err != nil {
		return nil, err
	}
	env := cliruntime.HomeEnv(d.kind, req.UserHome, req.ConfigHome)
	env = append(env, req.Env...)
	spec := LaunchSpec{
		Program:   d.program,
		Args:      args,
		Dir:       req.WorkDir,
		Env:       toSessionEnv(env),
		Isolation: IsolationNative,
		WaitDelay: 2 * time.Second,
	}
	proc, err := d.runner.Launch(ctx, spec)
	if err != nil {
		return nil, err
	}
	n := d.seq.Add(1)
	gen := d.gen.Add(1)
	meta := cliruntime.HostedMeta{
		Ref:            fmt.Sprintf("%s-%d", d.kind, n),
		Kind:           d.kind,
		ConversationID: req.ResumeID,
		Generation:     gen,
	}
	return cliruntime.Host(adaptProcess(proc), meta), nil
}

func (d *officialLocal) Resume(ctx context.Context, req cliruntime.ResumeRequest) (cliruntime.Session, error) {
	if req.ConversationID == "" {
		return nil, cliruntime.ErrResumeRequired
	}
	req.LaunchRequest.ResumeID = req.ConversationID
	return d.Launch(ctx, req.LaunchRequest)
}

func toSessionEnv(in []cliruntime.EnvVar) []EnvVar {
	out := make([]EnvVar, 0, len(in))
	for _, e := range in {
		out = append(out, EnvVar{Name: e.Name, Value: e.Value})
	}
	return out
}

type processPump struct {
	p  Process
	ch chan cliruntime.Frame
}

func adaptProcess(p Process) *processPump {
	pp := &processPump{p: p, ch: make(chan cliruntime.Frame, 256)}
	go func() {
		defer close(pp.ch)
		for f := range p.Output() {
			pp.ch <- cliruntime.Frame{Stream: f.Stream, Data: append([]byte(nil), f.Data...)}
		}
	}()
	return pp
}

func (p *processPump) Send(ctx context.Context, line []byte) error { return p.p.Send(ctx, line) }
func (p *processPump) Frames() <-chan cliruntime.Frame             { return p.ch }
func (p *processPump) Wait() (int, error)                          { return p.p.Wait() }
func (p *processPump) Stop(ctx context.Context) error              { return p.p.Stop(ctx) }
func (p *processPump) PID() int                                    { return p.p.PID() }

var (
	_ cliruntime.Driver  = (*officialLocal)(nil)
	_ cliruntime.Resumer = (*officialLocal)(nil)
	_ cliruntime.Pump    = (*processPump)(nil)
)
