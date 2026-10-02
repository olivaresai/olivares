// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/spf13/pflag"

// connectionFlags reach an engine without a saved sign-in. A person signs in once with
// `olivares login`; the flags are documented there and hidden from the help of every
// other command, where they keep working for scripts. The CLI audit of 09b counted them
// as 9 of the 12-18 flags in the help of every basic command.
var connectionFlags = []string{"server", "tenant", "token", "token-file", "ca-cert", "pin-sha256", "insecure", "timeout", "allow-cleartext"}

// hideConnectionFlags hides the connection flags of one client flag set. It is called
// where the set is registered, so a flag of the same name with another meaning (serve's
// --insecure, a probe's --timeout) is never touched.
func hideConnectionFlags(fs *pflag.FlagSet) {
	for _, name := range connectionFlags {
		if fs.Lookup(name) != nil {
			_ = fs.MarkHidden(name)
		}
	}
}

// showConnectionFlags is login's side: the one help that lists them. --token stays
// hidden there too: it is deprecated for --token-file, and using it prints that
// (cliTokenArgvWarning).
func showConnectionFlags(fs *pflag.FlagSet) {
	for _, name := range connectionFlags {
		if f := fs.Lookup(name); f != nil && name != "token" {
			f.Hidden = false
		}
	}
}
