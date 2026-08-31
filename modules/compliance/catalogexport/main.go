// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command catalogexport prints the public compliance catalog as JSON on stdout.
//
// It is a BUILD-TIME tool, not a product surface — deliberately not a subcommand of
// `olivares`. A new CLI subcommand would ripple into the generated CLI reference and
// its six translations for a generator nobody but the release pipeline runs.
//
// It lives under modules/compliance/ rather than modules/ because the hub's state script (line 16)
// counts `modules/*/` directories and that count is the public "modules" figure the
// website states and `scripts/check-public-counts.sh` pins. A tools directory one level
// up would have silently added a module to a public claim.
//
//	task compliance:catalog        # regenerate compliance.catalog.json
//	task compliance:catalog:check  # fail if the committed copy is stale
//
// Output is deterministic (sorted nothing, indented two spaces, trailing newline) so a
// diff is the whole staleness check.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olivaresai/olivares/modules/compliance"
)

func main() {
	doc := compliance.PublicCatalog()
	if len(doc.Frameworks) == 0 || len(doc.Capabilities) == 0 {
		// A generator that writes an EMPTY document over a good one is worse than a
		// generator that fails: the diff looks like an intentional retirement.
		fmt.Fprintln(os.Stderr, "catalogexport: refusing to emit an empty catalog "+
			"(frameworks or capabilities came back empty — the module did not load)")
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	// The catalog carries regulation titles with «»/– and control text with non-ASCII
	// punctuation; HTML-escaping them would publish &-style noise and make the
	// document diff against itself on every Go version that changes the escape set.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		fmt.Fprintf(os.Stderr, "catalogexport: %v\n", err)
		os.Exit(1)
	}
}
