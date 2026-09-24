// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command cedar-measure takes one bounded measurement of the cedar-go policy
// library and prints exactly one JSON record on standard output.
//
// It is started by supervisor.py, one process at a time, inside a fresh cgroup
// with hard time, memory, swap and task limits. It never prints policy text:
// inputs are identified by their SHA-256 digest.
//
// Modes:
//
//	cedar-measure child    -matrix MATRIX.json -id CELL/OP/POINT -token T [-control NAME]
//	cedar-measure validate -matrix MATRIX.json -token T
//	cedar-measure oframe   -corpus corpus-tests.tar.gz -batch I -batches N -token T
//	cedar-measure oframe   -edges -token T
//
// Exit status: 0 observed, 1 mismatch, 2 inability.
package main

import (
	"fmt"
	"os"
)

const (
	exitObserved  = 0
	exitMismatch  = 1
	exitInability = 2
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "cedar-measure: missing mode (child, validate or oframe)")
		os.Exit(exitInability)
	}
	var code int
	switch os.Args[1] {
	case "child":
		code = runChild(os.Args[2:])
	case "validate":
		code = runValidate(os.Args[2:])
	case "oframe":
		code = runOFrame(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "cedar-measure: unknown mode")
		code = exitInability
	}
	os.Exit(code)
}
