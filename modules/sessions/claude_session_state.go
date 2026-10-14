// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// Claude Code keeps one conversation as two things under its configuration home:
//
//	projects/<project>/<session id>.jsonl   the transcript
//	projects/<project>/<session id>/        subagents, tool results, workflows
//
// <project> is the working directory with every non-alphanumeric rune replaced by
// "-", shortened by the tool when it is very long. It is therefore FOUND by listing
// the projects directory for the id, never computed. Resuming by id needs exactly
// these files in the home the tool runs under, which is all a move carries: the
// account's login, settings and every other conversation stay where they are.
const (
	claudeProjectsDir = "projects"
	// The bounds keep one move from filling the target's disk or walking a tree an
	// operator never meant to be a conversation. A real session is far below both.
	claudeStateMaxFiles = 100_000
	claudeStateMaxBytes = 8 << 30
	claudeStateMaxDepth = 16
	claudeStateTmpMark  = ".olv-move-"
	claudeStateChunk    = 64 << 10
)

// claudeSessionIDPattern is what a session id must look like before it becomes a
// path element. The id is captured from the child's own stream, so it is never
// trusted as a path: no dot, no separator.
var claudeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

var (
	errClaudeStateNotFound = &runErr{http.StatusConflict,
		"this session's conversation was not found in its account home, so there is nothing to carry; start a new session under the target profile"}
	errClaudeStateDiverged = &runErr{http.StatusConflict,
		"the target profile already holds a different conversation with this id; refusing to overwrite it"}
	errClaudeStateSpecial = &runErr{http.StatusUnprocessableEntity,
		"the conversation state holds a link or special file; refusing to carry it"}
	errClaudeStateTooLarge = &runErr{http.StatusUnprocessableEntity,
		"the conversation state is larger than a move carries; start a new session under the target profile"}
	// errClaudeStateChanged is what a file that moved under the copy answers: the
	// planned file is gone, shorter, longer, rewritten or no longer a regular file. A stopped
	// session's files do not change, so this is a tool or person writing in the home
	// meanwhile, and the same request can be retried.
	errClaudeStateChanged = &runErr{http.StatusConflict,
		"the conversation's files changed while they were being carried; nothing was kept, try again"}
)

// claudeStateCopy is what one move carried.
type claudeStateCopy struct {
	Files int
	Bytes int64
	// created are the files this copy made where none existed, relative to the
	// target home. A move that fails after the copy removes exactly these; a file it
	// replaced (an older copy of the same conversation) is not restorable and stays.
	created []string
}

// claudeStateEntry is one file the move will write, planned before any write.
type claudeStateEntry struct {
	rel     string // relative to the configuration home
	size    int64
	skip    bool // the target already holds exactly this file
	replace bool // the target holds an older copy of it
	// info is what the planner saw. The copy compares its identity, size and
	// modification time with the descriptor it opens, so a name swapped for another
	// file, or a file rewritten, after the plan is refused.
	info fs.FileInfo
}

// copyClaudeSessionState copies one conversation's files from one configuration
// home to another. Every refusal is decided in a planning pass that writes nothing;
// each file is then written beside its name and renamed into place, so an
// interrupted move never leaves a half transcript that looks whole. Source files
// are only read, and each is checked again as it is opened: the plan used Lstat, the
// copy follows the descriptor it holds, which must be a regular file of the planned
// size. A file the target holds is replaced only when it is an older copy of the
// same bytes (a conversation moved away and back); anything else is a different
// conversation and refuses the move. A copy that fails removes the files it made.
//
// ponytail: a process killed mid-copy leaves its beside-the-name temporary file; it
// is skipped by later plans and never mistaken for state, and is not swept.
func copyClaudeSessionState(ctx context.Context, srcHome, dstHome, sid string) (claudeStateCopy, error) {
	if !claudeSessionIDPattern.MatchString(sid) {
		return claudeStateCopy{}, badRequest("the session's provider id is not a plain identifier; refusing to use it as a path")
	}
	src, err := os.OpenRoot(srcHome)
	if err != nil {
		return claudeStateCopy{}, fmt.Errorf("sessions: open source home: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenRoot(dstHome)
	if err != nil {
		return claudeStateCopy{}, fmt.Errorf("sessions: open target home: %w", err)
	}
	defer dst.Close()

	plan, err := planClaudeStateCopy(ctx, src, dst, sid)
	if err != nil {
		return claudeStateCopy{}, err
	}
	var out claudeStateCopy
	fail := func(err error) (claudeStateCopy, error) {
		removeCreated(dst, out.created)
		return claudeStateCopy{}, carryFailure(err)
	}
	for _, e := range plan {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if e.skip {
			continue
		}
		if err := writeClaudeStateFile(ctx, src, dst, e); err != nil {
			return fail(err)
		}
		if !e.replace {
			out.created = append(out.created, e.rel)
		}
		out.Files++
		out.Bytes += e.size
	}
	return out, nil
}

// carryFailure turns what a vanished or shortened file raises into the one answer
// a person can act on; context errors and typed answers pass through unchanged.
func carryFailure(err error) error {
	var typed *runErr
	switch {
	case errors.As(err, &typed), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return errClaudeStateChanged
	}
	return err
}

// removeClaudeStateFiles removes files a copy created in a home, for a move whose
// binding then failed. It is best effort and bounded to what the copy listed.
func removeClaudeStateFiles(dstHome string, rels []string) {
	if len(rels) == 0 {
		return
	}
	dst, err := os.OpenRoot(dstHome)
	if err != nil {
		return
	}
	defer dst.Close()
	removeCreated(dst, rels)
}

func removeCreated(dst *os.Root, rels []string) {
	for _, rel := range rels {
		_ = dst.Remove(rel)
	}
}

// planClaudeStateCopy lists every file of the conversation in src and decides, for
// each, whether dst needs it. It writes nothing.
func planClaudeStateCopy(ctx context.Context, src, dst *os.Root, sid string) ([]claudeStateEntry, error) {
	projects, skippedLinks, err := claudeSessionProjects(src, sid)
	if err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		if skippedLinks {
			// A conversation may live behind a link; saying "not found" would be a lie.
			return nil, errClaudeStateSpecial
		}
		return nil, errClaudeStateNotFound
	}
	var plan []claudeStateEntry
	var total int64
	entries := 0
	add := func(rel string, info fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !singleLink(info) {
			return errClaudeStateSpecial
		}
		if len(plan) >= claudeStateMaxFiles || total+info.Size() > claudeStateMaxBytes {
			return errClaudeStateTooLarge
		}
		skip, replace, err := classifyClaudeStateTarget(ctx, dst, src, rel, info.Size())
		if err != nil {
			return err
		}
		total += info.Size()
		plan = append(plan, claudeStateEntry{rel: rel, size: info.Size(), skip: skip, replace: replace, info: info})
		return nil
	}
	for _, project := range projects {
		base := path.Join(claudeProjectsDir, project)
		transcript, err := src.Lstat(path.Join(base, sid+".jsonl"))
		if err != nil {
			return nil, err
		}
		if err := add(path.Join(base, sid+".jsonl"), transcript); err != nil {
			return nil, err
		}
		tree := path.Join(base, sid)
		info, err := src.Lstat(tree)
		if errors.Is(err, fs.ErrNotExist) {
			continue // a conversation with no subagents or stored results has no directory
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errClaudeStateSpecial
		}
		err = fs.WalkDir(src.FS(), tree, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if entries++; entries > claudeStateMaxFiles {
				return errClaudeStateTooLarge
			}
			if d.IsDir() {
				if strings.Count(strings.TrimPrefix(p, tree), "/") > claudeStateMaxDepth {
					return errClaudeStateTooLarge
				}
				return nil
			}
			if strings.Contains(d.Name(), claudeStateTmpMark) {
				return nil // a copy's own leftover, never conversation state
			}
			fi, err := src.Lstat(p)
			if err != nil {
				return err
			}
			return add(p, fi)
		})
		if err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// claudeSessionProjects lists the project directories of src that hold a transcript
// of sid, in a stable order. A symlinked project directory is not followed; whether
// one was passed over is reported, so an empty answer is never "not found" when a
// link may hide the conversation. An unreadable project is an error, not an absence.
func claudeSessionProjects(src *os.Root, sid string) (found []string, skippedLinks bool, err error) {
	entries, err := fs.ReadDir(src.FS(), claudeProjectsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			skippedLinks = true
			continue
		}
		if !e.IsDir() {
			continue
		}
		fi, err := src.Lstat(path.Join(claudeProjectsDir, e.Name(), sid+".jsonl"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, false, err
		case fi.Mode().IsRegular():
			found = append(found, e.Name())
		}
	}
	slices.Sort(found)
	return found, skippedLinks, nil
}

var (
	errNoClaudeSessionID       = errors.New("the session has not reported its conversation id")
	errClaudeTranscriptMissing = errors.New("the session's transcript is not in its account home")
)

// claudeTranscript is a session's stored transcript, open for reading in the home it
// ran under. rel is its path inside that home.
type claudeTranscript struct {
	home *os.Root
	rel  string
	f    *os.File
	info fs.FileInfo
}

func (t claudeTranscript) Close() {
	t.f.Close()
	t.home.Close()
}

// openClaudeTranscript opens the transcript of a Claude Code session after its home
// is proven to be the one its profile registered. A move carries every project
// directory that holds the id, so the most recently written copy is the one read.
// A session that has not reported its id yet answers errNoClaudeSessionID, and one
// whose transcript is not in the home errClaudeTranscriptMissing.
func (m *Module) openClaudeTranscript(ctx context.Context, tenant model.TenantID, rec model.Record, src ProviderHomeSnapshot) (claudeTranscript, error) {
	sid := rec.String(colClaudeSessionID)
	if sid == "" || !claudeSessionIDPattern.MatchString(sid) {
		return claudeTranscript{}, errNoClaudeSessionID
	}
	if err := m.provenSourceHome(ctx, tenant, src); err != nil {
		return claudeTranscript{}, err
	}
	home, err := os.OpenRoot(src.ConfigHome)
	if err != nil {
		return claudeTranscript{}, err
	}
	tr, err := openClaudeTranscriptIn(home, sid)
	if err != nil {
		home.Close()
	}
	return tr, err
}

func openClaudeTranscriptIn(home *os.Root, sid string) (claudeTranscript, error) {
	projects, linked, err := claudeSessionProjects(home, sid)
	if err != nil {
		return claudeTranscript{}, err
	}
	if len(projects) == 0 {
		if linked {
			return claudeTranscript{}, errClaudeStateSpecial
		}
		return claudeTranscript{}, errClaudeTranscriptMissing
	}
	var rel string
	var newest fs.FileInfo
	for _, project := range projects {
		candidate := path.Join(claudeProjectsDir, project, sid+".jsonl")
		fi, err := home.Lstat(candidate)
		if err != nil {
			return claudeTranscript{}, err
		}
		if newest == nil || fi.ModTime().After(newest.ModTime()) {
			rel, newest = candidate, fi
		}
	}
	f, err := openStateFile(home, rel)
	if err != nil {
		return claudeTranscript{}, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || !singleLink(info)) {
		err = errClaudeStateSpecial
	}
	if err != nil {
		f.Close()
		return claudeTranscript{}, err
	}
	return claudeTranscript{home: home, rel: rel, f: f, info: info}, nil
}

// classifyClaudeStateTarget decides whether dst already holds rel. skip means the
// same bytes are there; replace means an older copy of the source is, to be
// replaced; an absent file is neither. Anything else is a different conversation
// and refuses.
func classifyClaudeStateTarget(ctx context.Context, dst, src *os.Root, rel string, srcSize int64) (skip, replace bool, err error) {
	fi, err := dst.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !fi.Mode().IsRegular() {
		return false, false, errClaudeStateSpecial
	}
	if fi.Size() > srcSize {
		return false, false, errClaudeStateDiverged
	}
	prefix, err := sameLeadingBytes(ctx, dst, src, rel, fi.Size())
	if err != nil {
		return false, false, err
	}
	if !prefix {
		return false, false, errClaudeStateDiverged
	}
	if fi.Size() == srcSize {
		return true, false, nil
	}
	return false, true, nil
}

// sameLeadingBytes reports whether the first n bytes of rel are equal in both roots.
func sameLeadingBytes(ctx context.Context, a, b *os.Root, rel string, n int64) (bool, error) {
	fa, err := openStateFile(a, rel)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := openStateFile(b, rel)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	bufA, bufB := make([]byte, claudeStateChunk), make([]byte, claudeStateChunk)
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		step := min(int64(len(bufA)), n)
		if _, err := io.ReadFull(fa, bufA[:step]); err != nil {
			return false, err
		}
		if _, err := io.ReadFull(fb, bufB[:step]); err != nil {
			return false, err
		}
		if !bytes.Equal(bufA[:step], bufB[:step]) {
			return false, nil
		}
		n -= step
	}
	return true, nil
}

// writeClaudeStateFile copies one planned file next to its final name and renames
// it into place, keeping the source's modification time so the tool's own recency
// ordering of conversations is unchanged.
func writeClaudeStateFile(ctx context.Context, src, dst *os.Root, e claudeStateEntry) (err error) {
	in, err := openStateFile(src, e.rel)
	if err != nil {
		return err
	}
	defer in.Close()
	// The descriptor, not the name, is what is trusted: the name may have been
	// swapped for a link or another file since the plan. It must be the file that
	// was planned, still a regular file with one name, of the planned size and as
	// last written when planned. Device and inode alone do not say so: a file
	// rewritten in place keeps both, and a freed inode can be handed to a new file.
	//
	// ponytail: a write within the clock tick of the planned modification time, one
	// that restores it, and one that lands while the bytes are read are not seen;
	// hash the content at plan time if a tool is measured doing that to a stopped
	// session's files.
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || !singleLink(st) || st.Size() != e.size || !os.SameFile(e.info, st) ||
		!st.ModTime().Equal(e.info.ModTime()) {
		return errClaudeStateChanged
	}
	if err := dst.MkdirAll(path.Dir(e.rel), 0o700); err != nil {
		return err
	}
	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	tmp := e.rel + claudeStateTmpMark + hex.EncodeToString(salt[:])
	out, err := dst.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = dst.Remove(tmp)
		}
	}()
	buf := make([]byte, claudeStateChunk)
	left := e.size
	for left > 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, rerr := in.Read(buf[:min(int64(len(buf)), left)])
		if n > 0 {
			if _, err = out.Write(buf[:n]); err != nil {
				return err
			}
			left -= int64(n)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) && left == 0 {
				break
			}
			// A short file is a file that shrank under the copy.
			err = errClaudeStateChanged
			if !errors.Is(rerr, io.EOF) {
				err = rerr
			}
			return err
		}
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = dst.Chtimes(tmp, st.ModTime(), st.ModTime()); err != nil {
		return err
	}
	// Rename replaces whatever is there. The classification was made at plan time, so
	// what is there now must still be what it was: nothing, or an older copy.
	cur, lerr := dst.Lstat(e.rel)
	switch {
	case errors.Is(lerr, fs.ErrNotExist):
		if e.replace {
			err = errClaudeStateChanged
		}
	case lerr != nil:
		err = lerr
	case !e.replace || !cur.Mode().IsRegular():
		err = errClaudeStateChanged
	}
	if err != nil {
		return err
	}
	err = dst.Rename(tmp, e.rel)
	return err
}
