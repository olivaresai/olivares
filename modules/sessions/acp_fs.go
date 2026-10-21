// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// fs/write_text_file is how OpenCode writes an edit once the person (or the live
// policy) approved it through session/request_permission: the agent asks its CLIENT
// to write the file. The engine is not confined the way the child is, so this write
// may reach what the child itself may write in its session folder and not more: the
// folder jail of the governed file API (workspace_fs.go), minus what the child cannot
// change (the Git hooks and configuration, read-only folders, engine folders). The
// scope is decided at launch (newFileScope). A link in the path is judged by where it
// leads, before the file is opened and again on the open descriptor.
//
// A write refused or failed after the open removes the file and the folders it
// created. The removal is best effort: a racing swap of a folder for a link can leave
// an empty folder in a path the child cannot change, never content.
//
// ponytail: whole-file writes in place (a rename would break a write-without-delete
// folder), no per-path audit row and no check that an approval was granted for the
// path: the scope is what the confined child could write itself. A write that fails
// midway (disk full) leaves an existing file truncated, as the child's own edit would,
// and nothing is synced. A path is compared as written, so a case-folding filesystem
// could name a protected path another way. Add either when a provider needs an atomic
// or audited client write.

// maxQueuedFileWrites bounds the writes waiting for the one that runs. The engine's
// memory holds each request until its turn, and the child sends them as fast as the
// pump reads.
const maxQueuedFileWrites = 16

var (
	errFileBusy      = errors.New("too many file writes are waiting")
	errSessionClosed = errors.New("the session is closed")
)

// fileScope is where the engine may write a file for one session's agent.
type fileScope struct {
	root   string   // the session folder
	allow  []string // the workspace's allowed subpaths; none means the whole folder
	denied []string // paths inside root the child cannot change

	// queue counts the writes running or waiting; mu lets one run at a time, so two
	// requests for one file do not interleave.
	queue chan struct{}
	mu    sync.Mutex
	// afterJudged runs between the look at the path and the open; a test swaps a
	// folder for a link there. Nil in production.
	afterJudged func()
}

// newFileScope is nil, which keeps every file request refused, when the session may
// not write its folder or the engine cannot reach it: a read-only session or
// workspace, a container whose folder is not the engine's path, a platform where the
// open file cannot be asked for its path.
func newFileScope(dir string, container bool, preset string, ws *resolvedWorkspace, policy *confine.Policy, mounts confine.Mounts) *fileScope {
	if !fileScopeSupported || dir == "" || container || preset == PresetReadOnly {
		return nil
	}
	scope := &fileScope{root: dir, queue: make(chan struct{}, maxQueuedFileWrites), denied: append([]string(nil), mounts.ReadOnly...)}
	if ws != nil {
		if ws.mountMode == mountRO {
			return nil
		}
		scope.allow = append([]string(nil), ws.allowSubpaths...)
		scope.denied = append(scope.denied, ws.readOnlyFolders...)
	}
	if policy != nil {
		scope.denied = append(scope.denied, policy.Protect...)
	}
	return scope
}

// fileRefusal is a request the scope understood and will not carry out.
type fileRefusal string

func (e fileRefusal) Error() string { return string(e) }

// writeFile replaces the content of the file at the absolute path, creating it and
// its folders when they are missing. live runs once the write holds its turn and is
// the last word on whether the session may still write.
func (f *fileScope) writeFile(path, content string, live func() error) error {
	select {
	case f.queue <- struct{}{}:
		defer func() { <-f.queue }()
	default:
		return errFileBusy
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if live != nil {
		if err := live(); err != nil {
			return err
		}
	}
	rootReal, rel, err := f.target(path)
	if err != nil {
		return err
	}
	if f.afterJudged != nil {
		f.afterJudged()
	}
	root, err := os.OpenRoot(rootReal)
	if err != nil {
		return err
	}
	defer root.Close()

	// Folders this write makes, deepest first, to take away again if it fails.
	var made []string
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		if _, err := root.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			break
		}
		made = append(made, dir)
	}
	created, done := false, false
	defer func() {
		if done {
			return
		}
		if created {
			_ = root.Remove(rel)
		}
		for _, dir := range made {
			_ = root.Remove(dir)
		}
	}()
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	file, created, err := openForWrite(root, rel)
	if err != nil {
		return asRefusal(err)
	}
	defer file.Close()
	// The answer to "is this the file I judged" comes from the descriptor, after a
	// swap of a folder for a link could have happened. O_NONBLOCK keeps a pipe the
	// session made from holding the open; only a regular file is written.
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fileRefusal("the path is not a regular file")
	}
	opened, err := openedPath(file)
	if err == nil {
		err = f.judge(rootReal, opened)
	}
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	done = true
	return nil
}

// target judges the path the agent named and returns the session folder and the
// file's path below it, links resolved.
func (f *fileScope) target(path string) (rootReal, rel string, err error) {
	if !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return "", "", fileRefusal("the path must be absolute")
	}
	// Stricter than the jail: Clean would fold "link/.." where the kernel follows the link.
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return "", "", fileRefusal("the path must not contain ..")
		}
	}
	if rootReal, err = filepath.EvalSymlinks(f.root); err != nil {
		return "", "", err
	}
	rel, ok := relativeTo(path, f.root, rootReal)
	if !ok {
		return "", "", fileRefusal("the path is outside the session folder")
	}
	abs, err := resolveWithin(rootReal, rel, false)
	if errors.Is(err, errTraversal) {
		return "", "", fileRefusal("the path is outside the session folder or runs through a file")
	}
	if err != nil {
		return "", "", err
	}
	// From here on only the real path is used: a link in the path the agent named is
	// judged by where it leads, and the file is opened where it leads.
	real := realTarget(abs)
	if err := f.judge(rootReal, real); err != nil {
		return "", "", err
	}
	// Whatever link is left at the end of a resolved path leads nowhere.
	if info, err := os.Lstat(real); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", fileRefusal("the path is a link that leads nowhere")
	}
	if rel, err = filepath.Rel(rootReal, real); err != nil {
		return "", "", err
	}
	return rootReal, rel, nil
}

// judge refuses a real path the session may not write.
func (f *fileScope) judge(rootReal, real string) error {
	switch {
	case !within(rootReal, real):
		return fileRefusal("the path is outside the session folder")
	case real == rootReal:
		return fileRefusal("the path names the session folder itself")
	case !underAllowedSubpath(rootReal, f.allow, real):
		return fileRefusal("the path is outside the workspace's allowed folders")
	case f.protected(real):
		return fileRefusal("the path is read-only in this session")
	}
	return nil
}

// openForWrite opens the file without truncating it. A missing file is created
// exclusively, which does not follow a link planted at its name.
func openForWrite(root *os.Root, rel string) (file *os.File, created bool, err error) {
	file, err = root.OpenFile(rel, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		file, err = root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NONBLOCK, 0o644)
		created = err == nil
	}
	return file, created, err
}

// asRefusal makes a request the filesystem cannot take (a directory where a file is
// named, a file where a folder is, a link that leaves the folder) a refusal; any
// other failure stays the engine's.
func asRefusal(err error) error {
	switch {
	case errors.Is(err, syscall.EISDIR), errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.ELOOP),
		errors.Is(err, syscall.ENAMETOOLONG):
		return fileRefusal("the path cannot be written as a file")
	// os.Root does not export its escape error.
	case strings.Contains(err.Error(), "path escapes from parent"):
		return fileRefusal("the path is outside the session folder")
	}
	return err
}

// protected reports whether the real path is one the child cannot change. A denied
// path that does not exist yet is judged by its deepest existing ancestor.
func (f *fileScope) protected(real string) bool {
	for _, denied := range f.denied {
		if filepath.IsAbs(denied) && within(realTarget(denied), real) {
			return true
		}
	}
	return false
}

// relativeTo is path below the first root that holds it (the session folder as
// given, or with its links resolved), as a path relative to that root.
func relativeTo(path string, roots ...string) (string, bool) {
	path = filepath.Clean(path)
	for _, root := range roots {
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && within(".", rel) {
			return rel, true
		}
	}
	return "", false
}

// realTarget is abs with the links of its existing ancestors resolved, so a folder
// that is a link into a protected path is judged by where it leads.
func realTarget(abs string) string {
	tail := ""
	for p := abs; ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, tail)
		}
		if filepath.Dir(p) == p {
			return abs
		}
		tail = filepath.Join(filepath.Base(p), tail)
	}
}

type acpWriteTextFileParams struct {
	SessionID string  `json:"sessionId"`
	Path      string  `json:"path"`
	Content   *string `json:"content"`
}

// writeTextFile answers one fs/write_text_file, with an empty result or a protocol
// error. It runs off the pump. It sends nothing when the session is gone, since the
// connection is too.
func (s *acpSession) writeTextFile(id json.RawMessage, raw json.RawMessage) {
	ctx := context.Background()
	refuse := func(code int, log, msg string) {
		s.warn("sessions: a provider file write "+log, "run_ref", s.cfg.RunRef)
		if err := s.conn.respondError(ctx, id, code, msg); err != nil && !errors.Is(err, errRPCClosed) {
			s.warn("sessions: could not answer a provider file write", "run_ref", s.cfg.RunRef, "cause", errorCause(err, "unavailable"))
		}
	}
	var params acpWriteTextFileParams
	if err := json.Unmarshal(raw, &params); err != nil || params.Content == nil {
		refuse(acpErrInvalidParams, "could not be read and was refused", "the write request needs a sessionId, a path and content")
		return
	}
	if session := s.ConversationID(); session == "" || params.SessionID != session {
		refuse(acpErrInvalidParams, "named a foreign conversation and was refused", "the write names another conversation")
		return
	}
	// The write has the last look just before it runs, behind the writes ahead of it.
	live := func() error {
		s.mu.Lock()
		turn, closed := s.promptID, s.closed
		cancelling := turn != "" && s.cancelledTurn == turn
		s.mu.Unlock()
		switch {
		case closed:
			return errSessionClosed
		case turn == "" || cancelling:
			return fileRefusal("the session has no live turn to write for")
		case s.cfg.AuthorityCheck != nil && s.cfg.AuthorityCheck(ctx) != nil:
			return fileRefusal("this session no longer holds its launch authority")
		}
		return nil
	}
	err := s.cfg.files.writeFile(params.Path, *params.Content, live)
	var refusal fileRefusal
	switch {
	case errors.Is(err, errSessionClosed):
	case errors.As(err, &refusal):
		refuse(acpErrInvalidParams, "was refused: "+string(refusal), string(refusal))
	case err != nil:
		refuse(acpErrInternal, "failed ("+fileFailure(err)+")", "the file could not be written")
	default:
		if err := s.conn.respond(ctx, id, struct{}{}); err != nil && !errors.Is(err, errRPCClosed) {
			s.warn("sessions: could not answer a provider file write", "run_ref", s.cfg.RunRef, "cause", errorCause(err, "unavailable"))
		}
	}
}

// fileFailure names why a write failed, without the path or any text of the file.
func fileFailure(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	if errors.Is(err, errFileBusy) {
		return "busy"
	}
	return "unavailable"
}
