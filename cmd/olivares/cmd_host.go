// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/runtime/hostmetrics"
	"github.com/spf13/cobra"
)

func newHostCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "host", Short: "Read the host the engine runs on", Long: "Read the CPU, memory, load and filesystem use of the host the engine runs on, and the disk use of each session folder. Requires system administration.", Example: "  olivares host\n  olivares host -o json"}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "host"}
	root.AddCommand(&cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "status", Short: "Show host load, CPU, memory, filesystems and session-folder disk use", Long: "Read the host's load average, CPU percent, memory, swap, each mounted filesystem and the disk use of each session folder from the running engine, with the last 24 hours of one-minute samples in -o json. Linux only: another platform answers that host metrics are not available.", Example: "  olivares host status\n  olivares host status -o json", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		raw, err := client.expect(cmd, http.MethodGet, "/v1/system/host", nil, http.StatusOK)
		if err != nil {
			return err
		}
		var st hostmetrics.Status
		if err = decodeBootstrapJSON("host", raw, &st); err != nil {
			return err
		}
		return renderOut(cmd, func(out io.Writer) error { return writeHostStatus(out, st) }, st)
	}})
	return root
}

func writeHostStatus(out io.Writer, st hostmetrics.Status) error {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	p("CPU: %.1f%%  Load: %.2f %.2f %.2f\n", st.CPUPercent, st.Load.One, st.Load.Five, st.Load.Fifteen)
	p("Memory: %s of %s used\n", hostBytes(st.Memory.UsedBytes), hostBytes(st.Memory.TotalBytes))
	p("Swap: %s of %s used\n", hostBytes(st.Swap.UsedBytes), hostBytes(st.Swap.TotalBytes))
	p("Filesystems:\n")
	for _, f := range st.Filesystems {
		p("  %s (%s): %s of %s used\n", termSafe(f.Mount), f.Type, hostBytes(f.UsedBytes), hostBytes(f.TotalBytes))
	}
	sf := st.SessionFolders
	trunc := ""
	if sf.Truncated {
		trunc = " (at least: the measurement hit its bound)"
	}
	p("Session folders: %d, %s%s\n", len(sf.Folders), hostBytes(sf.TotalBytes), trunc)
	for _, f := range sf.Folders[:min(len(sf.Folders), 10)] {
		p("  %s: %s\n", termSafe(f.Name), hostBytes(f.Bytes))
	}
	return nil
}

// hostBytes writes n in the largest binary unit that keeps it at or above 1.
func hostBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, suffix := float64(n), "KMGTPE"
	i := -1
	for ; v >= unit && i < len(suffix)-1; i++ {
		v /= unit
	}
	return fmt.Sprintf("%.1f %ciB", v, suffix[i])
}

// newHostMetrics keeps the history next to the session folders' parent, in the data
// directory. Without a data directory the history is not persisted and no session
// folder is measured.
func newHostMetrics(dataDir string, log *slog.Logger) *hostmetrics.Monitor {
	if strings.TrimSpace(dataDir) == "" {
		return hostmetrics.New("", "", log)
	}
	dataDir = absOrSame(dataDir)
	return hostmetrics.New(filepath.Join(dataDir, "host-metrics.jsonl"), sessionWorkspaceRootFor(dataDir), log)
}
