// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package audit is the Appliance Console's local audit spool: where an act is recorded while
// the product's ledger cannot take it, until the product ingests it, once.
//
// The spool is two append-only files in one directory private to the console's account:
// records.jsonl, one record per line, and ingested.jsonl, the id of each record the product
// took, one per line. Nothing is rewritten or removed: a restart reads both files and finds the
// records still to ingest. A line left incomplete by a crash is never a record, and the next
// append starts on a line of its own. A record holds what the act was, never a secret.
package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	recordsFile  = "records.jsonl"
	ingestedFile = "ingested.jsonl"
	// maxLine bounds one line of either file.
	maxLine = 16 * 1024
)

// Record is one act, in the fields every surface records.
type Record struct {
	// ID is 128 random bits the spool assigns; the product deduplicates ingestion by it.
	ID string `json:"id"`
	// Time is when the spool recorded the act, UTC.
	Time string `json:"time"`
	// Mode is the sign-in mode or the surface that asked: product-up, product-down-repair, tty1.
	Mode string `json:"mode"`
	// Surface is web, cli, tui or tty1.
	Surface string `json:"surface"`
	// Actor is the human or unit that asked, as the mode names them.
	Actor string `json:"actor"`
	// Verb is the act asked for.
	Verb string `json:"verb"`
	// Outcome is what the owner answered: performed, refused, failed or unknown.
	Outcome string `json:"outcome"`
}

// Spool is one spool directory.
type Spool struct {
	dir string
	// Now is the spool's clock; tests inject one.
	Now func() time.Time
	// Random is the source of record ids; tests inject one.
	Random io.Reader
}

// Open opens the spool in dir, which must be a directory of this account that no one else
// can read or write.
func Open(dir string) (*Spool, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ownedBySelf(info) {
		return nil, errors.New("the audit spool is not a directory private to this account")
	}
	return &Spool{dir: dir, Now: time.Now, Random: rand.Reader}, nil
}

// Append records r, assigning its id and time, and returns the id once the record is durable.
func (s *Spool) Append(r Record) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(s.Random, raw[:]); err != nil {
		return "", errors.New("no random bytes for a record id")
	}
	r.ID = hex.EncodeToString(raw[:])
	r.Time = s.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	if err := s.appendLine(recordsFile, line); err != nil {
		return "", err
	}
	return r.ID, nil
}

// Pending returns the records not yet ingested, in the order they were recorded.
func (s *Spool) Pending() ([]Record, error) {
	ingested, err := s.ingested()
	if err != nil {
		return nil, err
	}
	var pending []Record
	err = s.eachLine(recordsFile, func(line []byte) {
		var r Record
		if json.Unmarshal(line, &r) != nil || r.ID == "" || ingested[r.ID] {
			return
		}
		pending = append(pending, r)
	})
	return pending, err
}

// Ingest delivers each pending record to deliver, in order, and records each one deliver
// took before it offers the next; it stops at the first refusal and returns how many were
// taken. A record deliver took is never offered again, across restarts included.
func (s *Spool) Ingest(ctx context.Context, deliver func(context.Context, Record) error) (int, error) {
	pending, err := s.Pending()
	if err != nil {
		return 0, err
	}
	taken := 0
	for _, r := range pending {
		if err := deliver(ctx, r); err != nil {
			return taken, err
		}
		line, err := json.Marshal(r.ID)
		if err != nil {
			return taken, err
		}
		if err := s.appendLine(ingestedFile, line); err != nil {
			return taken, err
		}
		taken++
	}
	return taken, nil
}

func (s *Spool) ingested() (map[string]bool, error) {
	ids := map[string]bool{}
	err := s.eachLine(ingestedFile, func(line []byte) {
		var id string
		if json.Unmarshal(line, &id) == nil && id != "" {
			ids[id] = true
		}
	})
	return ids, err
}

// appendLine appends line and a newline to name, durably. When the file does not end in a
// newline, a crash cut its last line short, and that line is closed first so it never joins
// the new one.
func (s *Spool) appendLine(name string, line []byte) error {
	if len(line) >= maxLine || bytes.IndexByte(line, '\n') >= 0 {
		return errors.New("a record does not fit one spool line")
	}
	path := filepath.Join(s.dir, name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	var out []byte
	if size := info.Size(); size > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, size-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			out = append(out, '\n')
		}
	}
	out = append(append(out, line...), '\n')
	if _, err := f.Write(out); err != nil {
		return err
	}
	return f.Sync()
}

// eachLine calls fn with each complete line of name; a missing file has none, and a last line
// without its newline is not complete.
func (s *Spool) eachLine(name string, fn func([]byte)) error {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if cut := bytes.LastIndexByte(data, '\n'); cut >= 0 {
		data = data[:cut+1]
	} else {
		data = nil
	}
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(make([]byte, 0, 4096), maxLine+1)
	for lines.Scan() {
		if len(lines.Bytes()) > 0 {
			fn(lines.Bytes())
		}
	}
	return lines.Err()
}

func ownedBySelf(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
