// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Bounds and the one program of the logs read.
const (
	// JournalctlPath is the journal reader the helper runs, by absolute path.
	JournalctlPath = "/usr/bin/journalctl"
	// MaxJournalBytes bounds what the helper reads from the journal reader.
	MaxJournalBytes = 8 << 20
	// MaxMessageBytes bounds one entry's message.
	MaxMessageBytes = 512
	// MaxAnswerBytes bounds the data of one helper answer.
	MaxAnswerBytes = 60 << 10
	// journalTimeout bounds one run of the journal reader.
	journalTimeout = 20 * time.Second
)

// ErrJournalLimit reports a journal reader that wrote more than its bound; what was read is kept.
var ErrJournalLimit = errors.New("the journal reader's output exceeds its bound")

// omitted is the message of an entry whose MESSAGE the journal reader did not print: it prints
// null for a field larger than 4096 bytes.
const omitted = "[message not shown: the journal omits fields larger than 4096 bytes]"

// Runner runs a fixed argument vector with no shell and returns at most limit bytes of its
// standard output.
type Runner func(ctx context.Context, argv []string, limit int64) ([]byte, error)

// Entry is one journal entry as the logs read returns it: its time, its priority and its
// message, and no other field.
type Entry struct {
	Timestamp string `json:"timestamp"`
	Priority  string `json:"priority"`
	Message   string `json:"message"`
}

// Logs is the logs answer.
type Logs struct {
	Unit      string  `json:"unit"`
	Lines     int     `json:"lines"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// LogArgv is the journal reader's one argument vector: the program by absolute path, the unit
// from the inventory, JSON output, the bounded count and no pager. Nothing else from a request
// reaches it.
func LogArgv(unit string, lines int) []string {
	return []string{JournalctlPath, "--unit=" + unit, "--output=json", "--lines=" + strconv.Itoa(lines), "--no-pager"}
}

// Logs reads unit's latest journal entries, at most lines of them. The unit must be a unit
// name the class table admits for logs and be in the inventory, and lines must be from 1 to
// MaxLogLines; otherwise nothing runs. The answer keeps each entry's time, priority and message,
// and the newest entries that fit in MaxAnswerBytes.
func (x Executor) Logs(ctx context.Context, unit string, lines int) (Logs, error) {
	if refusal := Check(OpLogs, unit); refusal != nil {
		return Logs{}, refusal
	}
	if lines < 1 || lines > MaxLogLines {
		return Logs{}, &Refusal{Code: CodeInputRefused, Detail: "lines is from 1 to 500"}
	}
	if x.Journal == nil {
		return Logs{}, errors.New("no journal reader")
	}
	_, present, err := x.state(ctx, unit)
	if err != nil {
		return Logs{}, err
	}
	if !present {
		return Logs{}, notInInventory()
	}
	data, err := x.Journal(ctx, LogArgv(unit, lines), MaxJournalBytes)
	over := errors.Is(err, ErrJournalLimit)
	if err != nil && !over {
		return Logs{}, err
	}
	entries, cut := ParseJournal(data, lines)
	entries, trimmed := newestWithin(entries, MaxAnswerBytes-1024)
	return Logs{Unit: unit, Lines: lines, Entries: entries, Truncated: over || cut || trimmed}, nil
}

// ParseJournal reads the journal reader's JSON output, one object per line, and returns the
// last lines entries. A line that is not an object is skipped: the reader's "-- No entries --"
// line silently, and anything else, such as a line cut at the byte bound, as a truncation.
func ParseJournal(data []byte, lines int) ([]Entry, bool) {
	entries := []Entry{}
	truncated := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.HasPrefix(line, []byte("-- ")) {
			continue
		}
		var fields map[string]json.RawMessage
		if line[0] != '{' || json.Unmarshal(line, &fields) != nil {
			truncated = true
			continue
		}
		entries = append(entries, Entry{
			Timestamp: timestamp(fields["__REALTIME_TIMESTAMP"]),
			Priority:  priority(fields["PRIORITY"]),
			Message:   message(fields["MESSAGE"]),
		})
	}
	if len(entries) > lines {
		entries = entries[len(entries)-lines:]
		truncated = true
	}
	return entries, truncated
}

// timestamp is the entry's wall-clock time in RFC 3339, UTC, or "" when the reader gave none.
func timestamp(raw json.RawMessage) string {
	var micros string
	if json.Unmarshal(raw, &micros) != nil {
		return ""
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil || n < 0 {
		return ""
	}
	return time.UnixMicro(n).UTC().Format(time.RFC3339Nano)
}

// priority is the entry's syslog priority, "0" to "7", or "" when the reader gave none.
func priority(raw json.RawMessage) string {
	var p string
	if json.Unmarshal(raw, &p) != nil || len(p) != 1 || p[0] < '0' || p[0] > '7' {
		return ""
	}
	return p
}

// message is the entry's message as printable UTF-8 on one line, at most MaxMessageBytes. The
// reader prints a message as a string, as an array of bytes when it is not UTF-8, or as null
// when it is too large.
func message(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return omitted
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var octets []byte
		var numbers []int
		if json.Unmarshal(raw, &numbers) != nil {
			return omitted
		}
		for _, n := range numbers {
			if n < 0 || n > 255 {
				return omitted
			}
			octets = append(octets, byte(n))
		}
		text = string(octets)
	}
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
	if len(text) > MaxMessageBytes {
		cut := MaxMessageBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}

// newestWithin keeps the newest entries whose JSON fits in budget bytes, in their order.
func newestWithin(entries []Entry, budget int) ([]Entry, bool) {
	size := 0
	first := len(entries)
	for first > 0 {
		data, err := json.Marshal(entries[first-1])
		if err != nil || size+len(data)+1 > budget {
			break
		}
		size += len(data) + 1
		first--
	}
	return entries[first:], first > 0
}

// ExecJournal is the helper's Runner: it runs argv, which must name JournalctlPath, with no
// shell, an empty environment and a time bound, and reads at most limit bytes of its standard
// output. Past the bound it stops the reader and returns what it read with ErrJournalLimit.
func ExecJournal(ctx context.Context, argv []string, limit int64) ([]byte, error) {
	if len(argv) == 0 || argv[0] != JournalctlPath {
		return nil, errors.New("not the journal reader's argument vector")
	}
	ctx, cancel := context.WithTimeout(ctx, journalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	over := int64(len(data)) > limit
	if over {
		data = data[:limit]
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case over:
		return data, ErrJournalLimit
	case readErr != nil:
		return nil, readErr
	case waitErr != nil:
		return nil, waitErr
	}
	return data, nil
}
