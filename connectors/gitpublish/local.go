// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// LocalGitRunner runs local Git inside the caller's session confinement. It must
// disable hooks, fsmonitor, transports, signing and inherited Git configuration,
// pin the worktree and Git directories, and bound execution time and output.
// input is stdin; env contains only the profile's author/committer identity.
type LocalGitRunner func(ctx context.Context, args []string, input string, env []string) (string, error)

// LocalRepository supplies the local work preceding publication through this
// connector. No remote, publication credential, or host API is needed here.
type LocalRepository struct{ Run LocalGitRunner }

// ErrLocalInput identifies a refused path, message or branch name.
var ErrLocalInput = errors.New("invalid Git input")

// LocalCommandExit retains an actual Git exit code while preserving the
// confined runner's explanation. Validation must not hide infrastructure errors.
type LocalCommandExit struct {
	Code int
	Err  error
}

func (e *LocalCommandExit) Error() string { return e.Err.Error() }
func (e *LocalCommandExit) Unwrap() error { return e.Err }

// LocalFile is Git's two-column porcelain status for one literal path.
type LocalFile struct {
	Path     string `json:"path"`
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
}

// LocalStatus is the local working tree, index and branch roster.
type LocalStatus struct {
	Branch    string      `json:"branch"`
	Branches  []string    `json:"branches"`
	Files     []LocalFile `json:"files"`
	Truncated bool        `json:"truncated"`
}

// run refuses settings that can dispatch repository-controlled programs. This
// check itself also runs within the session's OS boundary; none is engine-side.
func (l LocalRepository) run(ctx context.Context, args []string, input string, env []string) (string, error) {
	if l.Run == nil {
		return "", errors.New("session Git runner unavailable")
	}
	// --get-regexp exits 1 for an empty result, so --list supplies the keys without
	// turning the normal empty answer into an execution failure.
	config, err := l.Run(ctx, []string{"config", "--includes", "--name-only", "--list"}, "", nil)
	if err != nil {
		return "", err
	}
	for _, key := range strings.Split(config, "\n") {
		key = strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") ||
			(strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".smudge") || strings.HasSuffix(key, ".process"))) {
			return "", fmt.Errorf("%w: session Git refuses command filters and included repository configuration", ErrLocalInput)
		}
	}
	return l.Run(ctx, args, input, env)
}

// Status reads NUL-delimited, rename-free porcelain: paths containing spaces,
// tabs or newlines are kept literally, without parsing Git's quoted display.
func (l LocalRepository) Status(ctx context.Context) (LocalStatus, error) {
	out := LocalStatus{Branches: []string{}, Files: []LocalFile{}}
	raw, err := l.run(ctx, []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all"}, "", nil)
	if err != nil {
		return out, err
	}
	for _, record := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' || !utf8.ValidString(record) {
			return out, errors.New("Git returned an unreadable status path")
		}
		if len(out.Files) == 200 {
			out.Truncated = true
			break
		}
		out.Files = append(out.Files, LocalFile{Path: record[3:], Index: record[:1], Worktree: record[1:2]})
	}
	branch, err := l.run(ctx, []string{"branch", "--show-current"}, "", nil)
	if err != nil {
		return out, err
	}
	out.Branch = strings.TrimSpace(branch)
	branches, err := l.run(ctx, []string{"for-each-ref", "--count=201", "--sort=refname", "--format=%(refname:strip=2)", "refs/heads/"}, "", nil)
	if err != nil {
		return out, err
	}
	if names := strings.TrimSpace(branches); names != "" {
		out.Branches = strings.Split(names, "\n")
	}
	if len(out.Branches) > 200 {
		out.Branches = out.Branches[:200]
		out.Truncated = true
	}
	// An unborn branch has a symbolic HEAD but no ref yet.
	if out.Branch != "" && len(out.Branches) == 0 {
		out.Branches = append(out.Branches, out.Branch)
	}
	return out, nil
}

// Stage changes only the named index entries. Unstage also works before the
// first commit and never discards the files in the working tree.
func (l LocalRepository) Stage(ctx context.Context, paths []string, unstage bool) error {
	if len(paths) == 0 || len(paths) > 200 {
		return fmt.Errorf("%w: choose between 1 and 200 files", ErrLocalInput)
	}
	for _, p := range paths {
		if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsRune(p, 0) || path.IsAbs(p) || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") || p == ".git" || strings.HasPrefix(p, ".git/") {
			return fmt.Errorf("%w: paths must name files inside the session folder", ErrLocalInput)
		}
	}
	args := []string{"add", "--"}
	if unstage {
		head, err := l.run(ctx, []string{"status", "--porcelain=v2", "--branch", "--untracked-files=no", "--ignore-submodules=all"}, "", nil)
		if err != nil {
			return err
		}
		if strings.HasPrefix(head, "# branch.oid (initial)\n") {
			args = []string{"rm", "--cached", "--force", "--quiet", "--ignore-unmatch", "--"}
		} else {
			args = []string{"reset", "--quiet", "HEAD", "--"}
		}
	}
	_, err := l.run(ctx, append(args, paths...), "", nil)
	return err
}

// Commit uses the authenticated user's profile for BOTH identities, via the
// child environment. The message travels on stdin, never on the command line.
func (l LocalRepository) Commit(ctx context.Context, message, name, email string) error {
	if strings.TrimSpace(message) == "" || len(message) > 8192 || strings.ContainsRune(message, 0) || !utf8.ValidString(message) {
		return fmt.Errorf("%w: a commit message of at most 8192 bytes is required", ErrLocalInput)
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" || strings.ContainsAny(name+email, "\r\n\x00<>") {
		return fmt.Errorf("%w: your profile needs a name and email to commit", ErrLocalInput)
	}
	_, err := l.run(ctx, []string{"commit", "--no-gpg-sign", "--no-verify", "--file=-"}, message, []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email})
	return err
}

// Branch validates with Git and switches without force, remote guessing or
// tracking. Git refuses a dirty checkout that would lose work.
func (l LocalRepository) Branch(ctx context.Context, name string, create bool) error {
	if name == "" || len(name) > 512 || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\r\n\x00") {
		return fmt.Errorf("%w: choose a local branch name", ErrLocalInput)
	}
	if _, err := l.run(ctx, []string{"check-ref-format", "refs/heads/" + name}, "", nil); err != nil {
		var exit *LocalCommandExit
		if errors.As(err, &exit) && exit.Code == 1 {
			return fmt.Errorf("%w: Git does not accept that branch name", ErrLocalInput)
		}
		return err
	}
	args := []string{"switch", "--no-guess", "--no-recurse-submodules"}
	if create {
		args = append(args, "--no-track", "-c", name)
	} else {
		// check-ref-format and the leading-dash refusal above make this literal.
		args = append(args, name)
	}
	_, err := l.run(ctx, args, "", nil)
	return err
}
