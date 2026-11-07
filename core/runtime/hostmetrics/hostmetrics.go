// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package hostmetrics reads the load, CPU, memory, swap and filesystem use of
// the host the engine runs on, and the disk use of each session folder, and
// keeps a 24 hour history at one sample a minute. It reads /proc and statfs, so
// only Linux answers; every other platform reports ErrUnavailable.
package hostmetrics

import (
	"errors"
	"time"
)

// ErrUnavailable is returned where host metrics cannot be read: a platform that
// is not Linux, or a Linux whose /proc is not mounted.
var ErrUnavailable = errors.New("host metrics are not available on this platform")

const (
	// SampleInterval is the history resolution.
	SampleInterval = time.Minute
	// HistoryLen is the number of samples kept: 24 hours at SampleInterval.
	HistoryLen = 24 * 60
	// FolderCacheTTL is how long a session-folder measurement is reused.
	FolderCacheTTL = 60 * time.Second
	// MaxFolderEntries bounds the directory entries one measurement visits; a
	// measurement that reaches it is reported as truncated.
	MaxFolderEntries = 1_000_000
	// FolderWalkLimit bounds the time one measurement may take.
	FolderWalkLimit = 10 * time.Second
	// MaxFolders bounds the folders listed; the largest come first.
	MaxFolders = 1000
)

// Load is the 1, 5 and 15 minute load average.
type Load struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// Memory is physical memory in bytes. Used is Total minus Available.
type Memory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

// Swap is swap space in bytes; both are zero on a host without swap.
type Swap struct {
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

// Filesystem is one mounted filesystem.
type Filesystem struct {
	Mount      string `json:"mount"`
	Device     string `json:"device"`
	Type       string `json:"type"`
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

// Folder is the disk use of one session folder, in bytes of allocated blocks.
type Folder struct {
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
}

// Folders is the disk use of the session folders. Truncated is true when the
// measurement stopped at a bound, so the sizes are a floor.
type Folders struct {
	Root       string    `json:"root"`
	MeasuredAt time.Time `json:"measured_at"`
	TotalBytes uint64    `json:"total_bytes"`
	Folders    []Folder  `json:"folders"`
	Truncated  bool      `json:"truncated"`
}

// Sample is one history point.
type Sample struct {
	At              time.Time `json:"at"`
	CPUPercent      float64   `json:"cpu_percent"`
	Load1           float64   `json:"load_1"`
	MemoryUsedBytes uint64    `json:"memory_used_bytes"`
	SwapUsedBytes   uint64    `json:"swap_used_bytes"`
}

// Status is what the API returns.
type Status struct {
	CollectedAt    time.Time    `json:"collected_at"`
	Load           Load         `json:"load"`
	CPUPercent     float64      `json:"cpu_percent"`
	Memory         Memory       `json:"memory"`
	Swap           Swap         `json:"swap"`
	Filesystems    []Filesystem `json:"filesystems"`
	SessionFolders Folders      `json:"session_folders"`
	History        []Sample     `json:"history"`
}
