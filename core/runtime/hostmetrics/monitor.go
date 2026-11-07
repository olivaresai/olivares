// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostmetrics

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"slices"
	"sync"
	"time"
)

// Monitor takes one sample a minute into a 24 hour history kept in a JSON-lines
// file, and answers a current reading on demand.
//
// The history file holds at most 2*HistoryLen lines (about 300 KB): a line is
// appended per sample, and when the file reaches that size it is rewritten with
// only the HistoryLen newest samples. The history is per host and survives a
// restart; a line that does not parse or is older than 24 hours is dropped on load.
type Monitor struct {
	file        string
	sessionRoot string
	log         *slog.Logger
	now         func() time.Time

	mu      sync.Mutex // prev, history, lines
	prev    cpuTimes
	history []Sample
	lines   int

	foldersMu sync.Mutex // one measurement at a time; it holds the cache
	folders   Folders
}

// New returns a Monitor that keeps its history in historyFile and measures the
// folders under sessionRoot ("" measures none). It reads the history and the
// first CPU reading now, so the first request has an interval to report.
func New(historyFile, sessionRoot string, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	m := &Monitor{file: historyFile, sessionRoot: sessionRoot, log: log, now: time.Now}
	if cpu, err := readCPU(); err == nil {
		m.prev = cpu
	}
	m.history, m.lines = loadHistory(historyFile, m.now())
	return m
}

// Run samples every SampleInterval until ctx ends. It returns at once where the
// host cannot be read.
func (m *Monitor) Run(ctx context.Context) {
	if _, err := readCPU(); err != nil {
		return
	}
	t := time.NewTicker(SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.tick(); err != nil {
				m.log.Warn("host metrics sample failed", "error", err)
			}
		}
	}
}

func (m *Monitor) tick() error {
	cpu, err := readCPU()
	if err != nil {
		return err
	}
	load, err := readLoad()
	if err != nil {
		return err
	}
	mem, swap, err := readMemory()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Sample{At: m.now().UTC().Truncate(time.Second), CPUPercent: round2(cpuPercent(m.prev, cpu)),
		Load1: load.One, MemoryUsedBytes: mem.UsedBytes, SwapUsedBytes: swap.UsedBytes}
	m.prev = cpu
	m.history = append(m.history, s)
	if len(m.history) > HistoryLen {
		m.history = slices.Clone(m.history[len(m.history)-HistoryLen:])
	}
	return m.persist(s)
}

// HostStatus reads the host now. The CPU figure is the average since the last
// sample, so a request never reports a sub-second blip.
func (m *Monitor) HostStatus(ctx context.Context) (Status, error) {
	cpu, err := readCPU()
	if err != nil {
		return Status{}, err
	}
	load, err := readLoad()
	if err != nil {
		return Status{}, err
	}
	mem, swap, err := readMemory()
	if err != nil {
		return Status{}, err
	}
	fsys, err := readFilesystems()
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	pct := cpuPercent(m.prev, cpu)
	hist := slices.Clone(m.history)
	m.mu.Unlock()
	if hist == nil {
		hist = []Sample{}
	}
	return Status{CollectedAt: m.now().UTC().Truncate(time.Second), Load: load, CPUPercent: round2(pct),
		Memory: mem, Swap: swap, Filesystems: fsys, SessionFolders: m.sessionFolders(ctx), History: hist}, nil
}

// sessionFolders returns the cached measurement while it is under FolderCacheTTL
// old. The walk is bounded in entries and time, and it finishes even when the
// request that started it is cancelled, so its result can be cached.
func (m *Monitor) sessionFolders(ctx context.Context) Folders {
	m.foldersMu.Lock()
	defer m.foldersMu.Unlock()
	now := m.now()
	if !m.folders.MeasuredAt.IsZero() && now.Sub(m.folders.MeasuredAt) < FolderCacheTTL {
		return m.folders
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), FolderWalkLimit)
	defer cancel()
	m.folders = measureFolders(wctx, m.sessionRoot, now.UTC().Truncate(time.Second))
	return m.folders
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// persist appends s, and compacts the file once it holds twice the history.
func (m *Monitor) persist(s Sample) error {
	if m.file == "" {
		return nil
	}
	if m.lines+1 > 2*HistoryLen {
		if err := writeHistory(m.file, m.history); err != nil {
			return err
		}
		m.lines = len(m.history)
		return nil
	}
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	m.lines++
	return f.Close()
}

// writeHistory replaces the file with samples, through a temporary file and a rename.
func writeHistory(path string, samples []Sample) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, s := range samples {
		if err = enc.Encode(s); err != nil {
			break
		}
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// loadHistory reads the newest HistoryLen samples from the last 24 hours and the
// number of lines the file holds. An absent file is an empty history.
func loadHistory(path string, now time.Time) ([]Sample, int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	var out []Sample
	lines := 0
	cutoff := now.Add(-24 * time.Hour)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines++
		var s Sample
		if json.Unmarshal(sc.Bytes(), &s) == nil && s.At.After(cutoff) {
			out = append(out, s)
		}
	}
	if len(out) > HistoryLen {
		out = slices.Clone(out[len(out)-HistoryLen:])
	}
	return out, lines
}
