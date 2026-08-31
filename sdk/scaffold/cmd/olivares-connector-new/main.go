// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command olivares-connector-new scaffolds a complete, compiling,
// boundary-clean out-of-tree connector repository (S142) — the starting point
// of "build your first connector". It is a thin stdlib-flag front end over
// scaffold.Generate; all validation (deny-closed, precise refusals) lives
// there so library and CLI behave identically.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/olivaresai/olivares/sdk/scaffold"
)

func main() {
	fs := flag.NewFlagSet("olivares-connector-new", flag.ExitOnError)
	dir := fs.String("dir", "", "target directory for the generated repo (created if absent; a non-empty dir is refused)")
	name := fs.String("name", "", `connector name: "<vendor>.<connector>", two lowercase [a-z0-9-] parts (e.g. acme.widget-audit)`)
	module := fs.String("module", "", "Go module path of the generated repo (e.g. github.com/acme/olivares-connector-widget-audit)")
	kind := fs.String("kind", scaffold.KindSource, `connector kind: "source" (gathers facts) or "output" (delivers notifications)`)
	withPlugin := fs.Bool("plugin", false, "also emit cmd/<vendor-connector>/main.go and the sdk/plugin dependency (ship as a go-plugin binary)")
	sdkPath := fs.String("sdk-path", "", "DEV: path to a local checkout of the upstream repo's sdk/ — emits replace directives so the repo builds immediately")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: olivares-connector-new -dir DIR -name VENDOR.CONNECTOR -module MODULE [-kind source|output] [-plugin] [-sdk-path PATH]

Generates a complete, compiling, boundary-clean out-of-tree Olivares AI
connector repository: the connector, its lifecycle test, a README covering
build/test/sign/distribute/operate/certify, a standalone license-boundary
check, and (with -plugin) the go-plugin main.

Example:
  olivares-connector-new \
    -dir ./widget-audit \
    -name acme.widget-audit \
    -module github.com/acme/olivares-connector-widget-audit \
    -kind source -plugin \
    -sdk-path ~/src/olivares/sdk

Flags:
`)
		fs.PrintDefaults()
	}
	// ExitOnError: a flag-parse failure prints usage and exits 2 on its own.
	_ = fs.Parse(os.Args[1:])

	opts := scaffold.Options{
		Dir:        *dir,
		Name:       *name,
		Module:     *module,
		Kind:       *kind,
		WithPlugin: *withPlugin,
		SDKPath:    *sdkPath,
	}
	if err := scaffold.Generate(opts); err != nil {
		fmt.Fprintln(os.Stderr, "olivares-connector-new:", err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s connector %s in %s\n", opts.Kind, opts.Name, opts.Dir)
	if opts.SDKPath == "" {
		// No -sdk-path: go.mod carries placeholder requires with no replace, so
		// `go test`/`go mod tidy` cannot resolve the SDK until the author adds a
		// replace to a local checkout (no public tags yet). Steer them there
		// first rather than to a command that fails.
		fmt.Println("Next: add the replace directive(s) for the SDK to go.mod (see go.mod / README.md),")
		fmt.Println("      then: go test ./...  &&  ./scripts/check-boundary.sh")
	} else {
		fmt.Println("Next: go test ./...  &&  ./scripts/check-boundary.sh  — then read README.md.")
	}
}
