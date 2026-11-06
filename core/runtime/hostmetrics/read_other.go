//go:build !linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostmetrics

import (
	"context"
	"time"
)

type cpuTimes struct{ idle, total uint64 }

func readCPU() (cpuTimes, error)                                { return cpuTimes{}, ErrUnavailable }
func readLoad() (Load, error)                                   { return Load{}, ErrUnavailable }
func readMemory() (Memory, Swap, error)                         { return Memory{}, Swap{}, ErrUnavailable }
func readFilesystems() ([]Filesystem, error)                    { return nil, ErrUnavailable }
func cpuPercent(_, _ cpuTimes) float64                          { return 0 }
func measureFolders(context.Context, string, time.Time) Folders { return Folders{} }
