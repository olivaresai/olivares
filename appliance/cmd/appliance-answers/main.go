// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// appliance-answers validates an explicitly selected document or prints a read-only plan.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/olivaresai/olivares/appliance/answers"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 3 || (args[0] != "validate" && args[0] != "plan") || args[1] != "--input" || args[2] == "" {
		_, _ = fmt.Fprintln(stderr, "usage: appliance-answers <validate|plan> --input <FILE|->; select exactly one input")
		return 2
	}
	input := stdin
	if args[2] != "-" {
		file, err := os.Open(args[2])
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "cannot open answers input")
			return 2
		}
		defer file.Close()
		input = file
	}
	plan, err := answers.Build(input)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		var invalid *answers.ValidationError
		if errors.As(err, &invalid) {
			return 1
		}
		return 2
	}
	output := []byte("valid appliance answers; no installation performed\n")
	if args[0] == "plan" {
		output, err = plan.JSON()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "cannot encode plan")
			return 2
		}
	}
	if n, err := stdout.Write(output); err != nil || n != len(output) {
		_, _ = fmt.Fprintln(stderr, "cannot write result")
		return 2
	}
	return 0
}
