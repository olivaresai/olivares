// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// guide-docs — the generator and the gate for the three guide surfaces that had no
// coverage at all: the CONSOLE, the UPGRADE path, and the gRPC mirror.
//
// WHAT WAS MEASURED, and it is why this exists (an internal design note (not shipped):74):
// "Consola: 0 de 57 rutas con guía … Sin guía de actualización de ninguna clase, con
// `olivares upgrade` y /v1/console/update-check ya existiendo. Sin referencia gRPC (28
// rpc)." Three surfaces, three zeros, and the same structural cause the same document
// names at :79 — there is a coverage gate for modules and there was none for screens,
// upgrades or rpc. A page written by hand is stale the day the 58th route lands, so what
// has a denominator is ENUMERATED from the tree and the page is REGENERATED from it.
//
// THE THREE SURFACES ARE NOT THE SAME KIND OF THING, and this program says which is
// which rather than pretending they are uniform:
//
//	console   GENERATED, 57 of 57. The roster is web/src/features/route-census.json —
//	          append-only and pinned against the BUILT router by
//	          registry.route-conservation.test.ts, so it is the one list in the tree that
//	          a deletion has no reason to touch. Per-screen metadata (stable id, RBAC
//	          permission, help page) comes from web/src/features/registry.tsx through the
//	          TypeScript compiler (stage 1, console-dump.mjs). The English label and
//	          one-line description are the CONSOLE'S OWN strings from
//	          web/src/lib/i18n/locales/en/nav.json — the product's words, already
//	          translated into seven locales, not a second description invented here.
//
//	grpc      GENERATED, 28 of 28. The roster is the grpc.ServiceDesc literals in the
//	          generated *_grpc.pb.go — the registration table the server actually hands
//	          grpc-go — read with go/parser. Not the .proto text: a .proto edited and not
//	          regenerated describes a service the binary does not serve, and this gate
//	          reports that disagreement as a finding of its own.
//
//	upgrade   WRITTEN, with its claims CHECKED. An upgrade runbook is prose: judgement
//	          about when to take a security channel is not enumerable and this program
//	          does not invent it. What IS enumerable is checked — the release channels
//	          (core/release.Channels) are rendered into the page, and every `--flag` the
//	          page prints on an `olivares upgrade` command line must be a flag that
//	          cmd/olivares/cmd_upgrade.go actually registers. A guide that documents a
//	          flag the binary does not have is a lie with a shell prompt in front of it.
//
// THREE ANSWERS, never two: 0 clean · 1 the tree and the pages disagree, every
// difference printed · 2 CANNOT LOOK. A roster that could not be enumerated is never
// reported as "nothing to change" — the shape scripts/check-migrations.sh established.
//
// DETERMINISTIC BY CONSTRUCTION: every roster is sorted by a stable key before it is
// rendered, nothing reads a clock, a directory or an environment, and two runs over one
// tree produce byte-identical pages.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// dumpSchema is the contract with stage 1. A dump that declares anything else is
	// CANNOT LOOK rather than a best-effort read: a field whose meaning changed must
	// not be consumed by a gate that still assumes the old one.
	dumpSchema = "olivares.console.routes/1"

	consolePageRel = "docs-site/src/content/docs/reference/console.md"
	grpcPageRel    = "docs-site/src/content/docs/reference/grpc.md"
	upgradePageRel = "docs-site/src/content/docs/how-to/upgrade-and-rollback.md"

	consoleCatalogRel = "scripts/console-guide-catalog.tsv"
	grpcCatalogRel    = "scripts/grpc-ref-catalog.tsv"

	navEnRel      = "web/src/lib/i18n/locales/en/nav.json"
	censusRel     = "web/src/features/route-census.json"
	releaseRel    = "core/release/manifest.go"
	cmdUpgradeRel = "cmd/olivares/cmd_upgrade.go"

	// The generated *_grpc.pb.go files, which carry the registration table. Both are
	// required: a missing one is a service surface this gate did not look at.
	coreGRPCRel = "core/api/genpb/apiv1/api_grpc.pb.go"
	sdkGRPCRel  = "sdk/plugin/genpb/olivaresv1/v1_grpc.pb.go"

	// And their .proto sources, read ONLY for the regeneration cross-check below.
	coreProtoRel = "core/api/proto/apiv1/api.proto"
	sdkProtoRel  = "sdk/plugin/proto/olivaresv1/v1.proto"
)

// Anti-vacuity floors. Without them a broken enumerator emits a tiny region, the tiny
// region matches a page regenerated from the same breakage, and the gate goes green over
// a console with four screens. Each floor sits far below today's population (57 routes,
// 28 methods, 3 channels, 19 flags) so ordinary product work never trips it, and far
// above zero so "I parsed nothing" can never pass for "nothing changed".
const (
	consoleFloor     = 40
	grpcFloor        = 20
	channelFloor     = 2
	upgradeFlagFloor = 10
)

// cannotLook is the exit-2 class: the gate could not look at something, which is never
// the same as having looked and found nothing.
type cannotLook struct{ msg string }

func (e *cannotLook) Error() string { return e.msg }

func cannot(format string, a ...any) error {
	return &cannotLook{msg: fmt.Sprintf(format, a...)}
}

func isCannotLook(err error) bool {
	var c *cannotLook
	return errors.As(err, &c)
}

// surface is one generated (or checked) documentation surface.
type surface struct {
	name string
	// findings are the differences between the tree and the page. Empty means clean.
	findings []string
	// notes are printed on every run, red or green, and never fail the gate.
	notes []string
}

func main() {
	var (
		root     = flag.String("root", ".", "repository root to read and write")
		dumpPath = flag.String("dump", "", "path to the stage-1 console dump (JSON)")
		write    = flag.Bool("write", false, "regenerate the pages instead of checking them")
		list     = flag.Bool("list", false, "print the enumerated rosters and exit")
		selfTest = flag.Bool("self-test", false, "run the red/green battery and exit")
		stage1   = flag.String("stage1", "", "path to console-dump.mjs, so the battery can execute stage 1's own refusals")
	)
	flag.Parse()

	if *selfTest {
		stage1Script = *stage1
		os.Exit(runSelfTest())
	}

	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "guide-docs: CANNOT LOOK — %v\n", err)
		os.Exit(2)
	}

	code, err := run(abs, *dumpPath, *write, *list)
	if err != nil {
		if isCannotLook(err) {
			fmt.Fprintf(os.Stderr, "guide-docs: CANNOT LOOK — %v\n", err)
			fmt.Fprintf(os.Stderr, "  A roster that was not enumerated is not a roster that agrees with the docs.\n")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "guide-docs: %v\n", err)
		os.Exit(2)
	}
	os.Exit(code)
}

func run(root, dumpPath string, write, list bool) (int, error) {
	console, err := loadConsole(root, dumpPath)
	if err != nil {
		return 0, err
	}
	grpc, err := loadGRPC(root)
	if err != nil {
		return 0, err
	}
	upgrade, err := loadUpgrade(root)
	if err != nil {
		return 0, err
	}

	if list {
		console.print(os.Stdout)
		grpc.print(os.Stdout)
		upgrade.print(os.Stdout)
		return 0, nil
	}

	surfaces := []*surface{}

	s, err := applyConsole(root, console, write)
	if err != nil {
		return 0, err
	}
	surfaces = append(surfaces, s)

	s, err = applyGRPC(root, grpc, write)
	if err != nil {
		return 0, err
	}
	surfaces = append(surfaces, s)

	s, err = applyUpgrade(root, upgrade, write)
	if err != nil {
		return 0, err
	}
	surfaces = append(surfaces, s)

	bad := 0
	for _, s := range surfaces {
		for _, n := range s.notes {
			fmt.Printf("guide-docs: NOTE — %s\n", n)
		}
		if len(s.findings) == 0 {
			continue
		}
		bad += len(s.findings)
		for _, f := range s.findings {
			fmt.Fprintf(os.Stderr, "guide-docs: %s: %s\n", s.name, f)
		}
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "guide-docs: %d difference(s) between the tree and the published guides.\n", bad)
		fmt.Fprintf(os.Stderr, "  Regenerate with: bash scripts/check-guide-docs.sh --write\n")
		return 1, nil
	}
	fmt.Printf("guide-docs: OK — console %d/%d routes, gRPC %d/%d methods, upgrade %d channel(s) and %d flag claim(s), all published and in sync.\n",
		len(console.rows), len(console.dump.Census), grpc.methodCount(), grpc.methodCount(),
		len(upgrade.Channels), len(upgrade.claimed))
	return 0, nil
}

// mdCell escapes a value for a Markdown table cell. A pipe inside a cell silently
// splits it into two columns, which is how a generated table starts publishing a
// permission string as if it were a heading.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
