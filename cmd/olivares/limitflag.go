// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// limitZeroWarning is the one stderr line an explicit --limit 0 prints where 0 is the
// flag's own default, the engine's default page size. 26.10 documented that spelling, so
// a 26.10 patch keeps it working; it becomes a usage error in 26.11.
const limitZeroWarning = "WARNING: --limit 0 is deprecated: leave --limit out for the engine's default"

// positiveLimit wraps an int --limit so a negative value is refused while the flags are
// parsed: before any hook, request or output. `olivares users ls --limit -1` returned
// the whole list and exited 0 (ID's sweep of 09b). An explicit 0 keeps its 26.10
// meaning: the engine's default where 0 is the flag's default (with limitZeroWarning),
// and the command's own range check elsewhere.
type positiveLimit struct {
	pflag.Value
	cmd           *cobra.Command
	zeroIsDefault bool
}

func (p positiveLimit) Set(s string) error {
	// Read the value as pflag's int does (base 0): -0x1, -0b1 and -0o1 are -1 to the flag,
	// so they are to this check (SR4 on 2e20b4d660).
	if n, err := strconv.ParseInt(s, 0, 64); err == nil {
		switch {
		case n < 0:
			return errors.New("must be 1 or more; leave --limit out for the default")
		case n == 0 && p.zeroIsDefault:
			fmt.Fprintln(p.cmd.ErrOrStderr(), limitZeroWarning)
		}
	}
	return p.Value.Set(s)
}

// enforcePositiveLimits applies positiveLimit to every command with an int --limit.
// A refused value is a flag error, so it exits 2 like every refusal of the invocation.
func enforcePositiveLimits(root *cobra.Command) {
	walkCommands(root, func(c *cobra.Command) {
		if f := c.Flags().Lookup("limit"); f != nil && f.Value.Type() == "int" {
			f.Value = positiveLimit{Value: f.Value, cmd: c, zeroIsDefault: f.DefValue == "0"}
		}
	})
}
