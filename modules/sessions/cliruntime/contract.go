// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import (
	"context"
	"errors"
	"time"
)

// Official CLI kinds. These strings match the sessions module driver keys.
const (
	KindClaude = "claude"
	KindCodex  = "codex"
	KindGrok   = "grok"
)

// Kinds lists the official CLI kinds this package declares a launch form for,
// in a stable order. It is the ONE list the argv table, the transport table and
// their batteries agree on: a kind that stops appearing in all three at once is
// a kind that would launch with no argv or refuse to launch at all.
//
// A returned slice is a fresh copy, so a caller cannot edit the declaration.
func Kinds() []string {
	return []string{KindClaude, KindCodex, KindGrok}
}

// Stream names on a multiplexed output frame.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// Driver launches one owned official-CLI child. Registering a kind is what
// makes that vendor program operable on this node.
type Driver interface {
	Kind() string
	OfficialProgram() string
	Launch(ctx context.Context, req LaunchRequest) (Session, error)
}

// Resumer continues an exact conversation in a new process generation.
// A failed resume must not start a different conversation.
type Resumer interface {
	Driver
	Resume(ctx context.Context, req ResumeRequest) (Session, error)
}

// Session is one owned process generation and its bridged I/O.
type Session interface {
	Ref() string
	ConversationID() string
	Generation() int64
	PID() int
	Send(ctx context.Context, line []byte) error
	Attach(fromSeq int64) (<-chan Frame, func())
	Reconnect(fromSeq int64) (<-chan Frame, func(), error)
	Stop(ctx context.Context) (Result, error)
	Wait() (Result, error)
}

// LaunchRequest is non-secret launch context. Credential values never belong
// here; the parent runtime injects them as child environment when authorized.
//
// Not every field reaches every kind: the permission mode, the tool allowlist,
// the appended instructions and the session name are Claude Code's own flags,
// and the argv of a kind that has no such flag never receives them. That is the
// point of one table — a field a vendor does not have is dropped HERE, once,
// instead of being invented per caller.
type LaunchRequest struct {
	WorkDir        string
	ConfigHome     string
	UserHome       string
	Model          string
	Effort         string
	PermissionMode string
	ResumeID       string
	// AllowedTools is the workspace template's governed tool allowlist. It
	// AUTO-APPROVES what it names and confines nothing by itself: what denies
	// everything else is the permission mode the same merge pins beside it.
	AllowedTools []string
	// ToolSurface is the set of BUILT-IN tools the child may have at all, and
	// ToolSurfaceDeclared says the server decided one. It is a different axis from
	// AllowedTools above: that one auto-approves what the child may already use,
	// this one decides what the child HAS. The provider profile declares it, and an
	// undeclared profile yields a DECLARED EMPTY surface, which is the deny-closed
	// answer (an internal design note (not shipped)).
	ToolSurface         []string
	ToolSurfaceDeclared bool
	// Instructions is appended to the child's system prompt.
	Instructions string
	// Name labels a lifecycle-only remote-control child. The stdio launch forms
	// have no such flag and drop it.
	Name string
	Env  []EnvVar
}

// ResumeRequest names the conversation a new generation must continue.
type ResumeRequest struct {
	LaunchRequest
	ConversationID string
}

// EnvVar is one explicit, non-secret environment entry.
type EnvVar struct {
	Name  string
	Value string
}

// Frame is one sequenced output chunk. Seq starts at 1.
type Frame struct {
	Seq    int64
	Stream string
	Data   []byte
	At     time.Time
}

// Result is the observed process outcome. ExitCode is meaningful when
// ProcessExited is true. A stop that could not reap the child reports
// ProcessExited false rather than inventing an exit code.
type Result struct {
	ExitCode      int
	ProcessExited bool
	Generation    int64
}

// Sentinel errors. Callers classify with errors.Is.
var (
	// ErrProcessGone means the OS process is not live. Reconnect after process
	// loss returns this instead of launching a replacement. Resume is the
	// operation that starts a new generation.
	ErrProcessGone = errors.New("cliruntime: the owned process is gone")
	// ErrUnknownKind means the driver kind is not claude, codex or grok.
	ErrUnknownKind = errors.New("cliruntime: unknown official CLI kind")
	// ErrNoProgram means Launch was asked to spawn an empty executable name.
	ErrNoProgram = errors.New("cliruntime: launch has no program")
	// ErrResumeRequired means Resume was called without the exact conversation id.
	ErrResumeRequired = errors.New("cliruntime: resume requires the exact conversation id")
)

// ConfigHomeEnv is the vendor variable that selects that CLI's configuration
// home on the child. Empty for an unknown kind.
func ConfigHomeEnv(kind string) string {
	switch kind {
	case KindClaude:
		return "CLAUDE_CONFIG_DIR"
	case KindCodex:
		return "CODEX_HOME"
	case KindGrok:
		return "GROK_HOME"
	default:
		return ""
	}
}

// OfficialProgram is the vendor executable name for a kind.
func OfficialProgram(kind string) string {
	switch kind {
	case KindClaude:
		return "claude"
	case KindCodex:
		return "codex"
	case KindGrok:
		return "grok"
	default:
		return ""
	}
}
