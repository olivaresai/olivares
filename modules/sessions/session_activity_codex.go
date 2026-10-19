// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// Codex owns an internal design note (not shipped)<timestamp>-<thread>.jsonl and moves
// archived rollouts to archived_sessions/. Read only the proven profile home;
// never launch an app-server, follow directory links, or read its account files.
func (m *Module) openCodexActivityTranscript(ctx context.Context, tenant model.TenantID, rec model.Record, src ProviderHomeSnapshot) (claudeTranscript, error) {
	if err := m.provenSourceHome(ctx, tenant, src); err != nil {
		return claudeTranscript{}, err
	}
	sid := rec.String(colClaudeSessionID)
	if sid == "" {
		return claudeTranscript{}, errNoClaudeSessionID
	}
	if !claudeSessionIDPattern.MatchString(sid) {
		return claudeTranscript{}, badRequest("the session's provider id is not a plain identifier")
	}
	home, err := os.OpenRoot(src.ConfigHome)
	if err != nil {
		return claudeTranscript{}, err
	}
	defer home.Close()
	var found claudeTranscript
	visited := 0
	for _, name := range []string{"sessions", "archived_sessions"} {
		dir, err := openRealDir(home, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = findCodexActivityTranscript(ctx, dir, sid, 0, name == "sessions", &visited, &found)
			dir.Close()
		}
		if err != nil {
			if found.f != nil {
				found.Close()
			}
			return claudeTranscript{}, err
		}
	}
	if found.f == nil {
		return claudeTranscript{}, errClaudeTranscriptMissing
	}
	return found, nil
}

var codexActivityDatePart = regexp.MustCompile(`^[0-9]{2,4}$`)

// Every directory is held open, so swapping its name cannot redirect a later read.
// The scan has a shared entry bound and reads names in batches, not an unbounded
// ReadDir of a home that may hold years of sessions. Multiple rollouts for an id
// (e.g. native revert) are refused rather than guessing which branch is current.
func findCodexActivityTranscript(ctx context.Context, dir *os.Root, sid string, depth int, dated bool, visited *int, found *claudeTranscript) error {
	listing, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer listing.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := listing.ReadDir(256)
		for _, e := range entries {
			*visited++
			if *visited > claudeStateMaxFiles {
				return errActivityTooLarge
			}
			name := e.Name()
			if dated && depth < 3 && codexActivityDatePart.MatchString(name) {
				child, err := openRealDir(dir, name)
				if err != nil {
					return err
				}
				err = findCodexActivityTranscript(ctx, child, sid, depth+1, true, visited, found)
				child.Close()
				if err != nil {
					return err
				}
				continue
			}
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") ||
				(!strings.HasSuffix(name, "-"+sid+".jsonl") && !strings.Contains(name, "-"+sid+"_")) {
				continue
			}
			if found.f != nil {
				return conflictErr("this session has multiple native rollouts; its current record cannot be chosen safely")
			}
			f, err := openStateFile(dir, name)
			if err != nil {
				return errClaudeStateSpecial
			}
			info, err := f.Stat()
			if err == nil && (!info.Mode().IsRegular() || !singleLink(info)) {
				err = errClaudeStateSpecial
			}
			if err == nil && info.Size() > activityFileMax {
				err = errActivityTooLarge
			}
			if err != nil {
				f.Close()
				return err
			}
			own, err := dir.OpenRoot(".")
			if err != nil {
				f.Close()
				return err
			}
			*found = claudeTranscript{home: own, rel: name, f: f, info: info}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
