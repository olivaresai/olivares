// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command checkcosignwiring enforces that the execution-time cosign control is actually
// LOAD-BEARING — that nothing signs except through the launcher which re-authenticates the
// binary's bytes immediately beforehand.
//
// WHY THIS EXISTS AS A SEPARATE, STRUCTURAL TOOL. An adversarial review found the invariant
// (scripts/assert-cosign-binary.sh) correct and simultaneously not reaching the signing:
// GoReleaser and the chart publisher re-resolved `cosign` from PATH, so a verified binary
// was authenticated and a different one could sign. Verifying one thing and using another
// is a control that passes its own tests and protects nothing.
//
// The first attempt to test that seam was a set of greps, and the SAME review broke it in
// two ways that matter, because both are ordinary edits rather than exotic YAML:
//
//   - it counted three `cmd:` fields and three launcher strings ANYWHERE in
//     .goreleaser.yaml, without associating them. Changing every `cmd: bash` to `cmd: true`
//     left the counts intact and the battery green while nothing could execute.
//   - it searched workflow `run:` scripts for the literal token `cosign`. Writing
//     `"$OLIVARES_COSIGN_BIN" sign --yes …` is a genuine bypass — it uses the pathname
//     authenticated minutes earlier, skipping the per-invocation re-hash — and contains no
//     such token, so the battery stayed green.
//
// A counting check cannot express "this command runs that wrapper". This one is structural:
// it reads the YAML and asserts the RELATIONSHIP.
//
// WHAT IT ENFORCES
//  1. every `signs[]` / `docker_signs[]` item has `cmd: bash` AND `args[0]` equal to the
//     launcher path — the pair, inside one item;
//  2. no workflow `run:` script invokes a publication-capable cosign subcommand through any
//     spelling (bare `cosign`, `$OLIVARES_COSIGN_BIN`, `${OLIVARES_COSIGN_BIN}`) except
//     through the launcher;
//  3. within each job, a cosign-installer step is accompanied by an assertion step, and if
//     the installer is conditional the assertion carries the SAME condition — otherwise the
//     assertion runs in a job that deliberately did nothing, and fails it.
//
// It deliberately does NOT re-check what checkcosignpins covers.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const launcher = "scripts/cosign-verified.sh"
const installerRepo = "sigstore/cosign-installer"
const assertScript = "scripts/assert-cosign-binary.sh"

// publisherCall matches an invocation of a publication-capable cosign subcommand through
// any spelling of the executable — including the verified PATHNAME, which is a bypass of
// the per-invocation re-check even though it looks like an improvement.
var publisherCall = regexp.MustCompile(
	`(^|[\s;&|(])("?\$\{?OLIVARES_COSIGN_BIN[^"\s]*\}?"?|cosign)\s+(sign|sign-blob|attest|attest-blob|copy|upload|attach)([\s]|$)`)

type finding struct {
	file string
	line int
	msg  string
}

var findings []finding

func add(file string, line int, format string, a ...any) {
	findings = append(findings, finding{file: file, line: line, msg: fmt.Sprintf(format, a...)})
}

func main() {
	root := os.Getenv("COSIGN_WIRING_ROOT")
	if root == "" {
		var err error
		root, err = repoRoot()
		if err != nil {
			fatalf("cannot locate the repository root: %v", err)
		}
	}

	checkGoReleaser(root)
	checked := checkWorkflows(root)

	if len(findings) > 0 {
		sort.SliceStable(findings, func(i, j int) bool {
			if findings[i].file != findings[j].file {
				return findings[i].file < findings[j].file
			}
			return findings[i].line < findings[j].line
		})
		for _, f := range findings {
			rel, err := filepath.Rel(root, f.file)
			if err != nil {
				rel = f.file
			}
			if f.line > 0 {
				fmt.Fprintf(os.Stderr, "::error::cosign-wiring: %s:%d — %s\n", rel, f.line, f.msg)
			} else {
				fmt.Fprintf(os.Stderr, "::error::cosign-wiring: %s — %s\n", rel, f.msg)
			}
		}
		fmt.Fprintf(os.Stderr, "\ncosign-wiring: %d problem(s). Everything that runs cosign must run it as:\n", len(findings))
		fmt.Fprintf(os.Stderr, "    bash %s <subcommand> …\n", launcher)
		fmt.Fprintf(os.Stderr, "which re-authenticates the binary's bytes immediately before the call. Using the\n")
		fmt.Fprintf(os.Stderr, "verified PATHNAME directly is also a bypass: a path is not an executable identity.\n")
		os.Exit(1)
	}

	fmt.Printf("cosign-wiring: OK (3 GoReleaser signing items and %d workflow(s) all execute cosign\n", checked)
	fmt.Printf("cosign-wiring: through %s, and every installer site has a\n", launcher)
	fmt.Printf("cosign-wiring: matching assertion under the same condition)\n")
}

// checkGoReleaser requires the cmd/args PAIR inside each signing item.
func checkGoReleaser(root string) {
	path := filepath.Join(root, ".goreleaser.yaml")
	doc, err := decodeOne(path)
	if err != nil {
		add(path, 0, "cannot read or parse: %v", err)
		return
	}
	for _, key := range []string{"signs", "docker_signs"} {
		seq := mapValue(doc, key)
		if seq == nil {
			add(path, 0, "no `%s:` block at all — if signing moved, this gate must move with it", key)
			continue
		}
		if seq.Kind != yaml.SequenceNode {
			add(path, seq.Line, "`%s:` is not a sequence", key)
			continue
		}
		for i, item := range seq.Content {
			id := "?"
			if n := mapValue(item, "id"); n != nil {
				id = n.Value
			}
			cmd := mapValue(item, "cmd")
			if cmd == nil || cmd.Kind != yaml.ScalarNode {
				add(path, item.Line, "%s[%d] (id %q) has no scalar `cmd:`", key, i, id)
				continue
			}
			if cmd.Value != "bash" {
				add(path, cmd.Line, "%s[%d] (id %q) has cmd %q; it must be `bash` so that %s can be args[0]. GoReleaser does NOT template `cmd`, so a path or a template here either bypasses the launcher or cannot execute at all",
					key, i, id, cmd.Value, launcher)
				continue
			}
			args := mapValue(item, "args")
			if args == nil || args.Kind != yaml.SequenceNode || len(args.Content) == 0 {
				add(path, item.Line, "%s[%d] (id %q) has cmd `bash` but no `args:`, so it runs an interactive shell", key, i, id)
				continue
			}
			first := args.Content[0]
			if first.Kind != yaml.ScalarNode || strings.TrimSpace(first.Value) != launcher {
				add(path, first.Line, "%s[%d] (id %q) runs `bash %s`, want `bash %s` so every signature re-authenticates the binary first",
					key, i, id, first.Value, launcher)
			}
		}
	}
}

// checkWorkflows enforces the run-script rule and the per-job installer/assertion pairing.
func checkWorkflows(root string) int {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			add(dir, 0, "no workflows directory: this gate is pointed at the wrong tree")
			return 0
		}
		add(dir, 0, "cannot read: %v", err)
		return 0
	}
	n := 0
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".yml" || ext == ".yaml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		doc, err := decodeOne(path)
		if err != nil {
			add(path, 0, "cannot read or parse: %v", err)
			continue
		}
		n++
		jobs := mapValue(doc, "jobs")
		if jobs == nil || jobs.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			jobName := jobs.Content[i].Value
			checkJob(path, jobName, jobs.Content[i+1])
		}
	}
	return n
}

func checkJob(file, jobName string, job *yaml.Node) {
	steps := mapValue(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return
	}
	type stepInfo struct {
		line int
		cond string
	}
	var installers, asserts []stepInfo

	for _, step := range steps.Content {
		if step.Kind != yaml.MappingNode {
			continue
		}
		cond := ""
		if n := mapValue(step, "if"); n != nil && n.Kind == yaml.ScalarNode {
			cond = strings.TrimSpace(n.Value)
		}
		if uses := mapValue(step, "uses"); uses != nil && uses.Kind == yaml.ScalarNode &&
			strings.Contains(uses.Value, installerRepo) {
			installers = append(installers, stepInfo{line: uses.Line, cond: cond})
		}
		run := mapValue(step, "run")
		if run == nil || run.Kind != yaml.ScalarNode {
			continue
		}
		if strings.Contains(run.Value, assertScript) {
			asserts = append(asserts, stepInfo{line: run.Line, cond: cond})
		}
		// The run-script rule, line by line so the diagnostic points somewhere useful.
		for off, l := range strings.Split(run.Value, "\n") {
			if strings.Contains(l, launcher) {
				continue
			}
			if strings.Contains(l, "#") {
				if idx := strings.Index(l, "#"); idx >= 0 {
					l = l[:idx]
				}
			}
			if publisherCall.MatchString(l) {
				add(file, run.Line+off, "job %q runs a publication-capable cosign command without the launcher: %q. Even the verified pathname is a bypass — it skips the re-authentication that makes the check load-bearing",
					jobName, strings.TrimSpace(l))
			}
		}
	}

	if len(installers) == 0 {
		return
	}
	if len(asserts) == 0 {
		add(file, installers[0].line, "job %q installs cosign but never runs %s, so nothing authenticates the binary it will use",
			jobName, assertScript)
		return
	}
	// A conditional installer needs an equally conditional assertion: otherwise the
	// assertion runs in a job that deliberately did nothing and fails the release.
	for _, ins := range installers {
		matched := false
		for _, as := range asserts {
			if as.cond == ins.cond {
				matched = true
				break
			}
		}
		if !matched {
			got := "no condition"
			if ins.cond != "" {
				got = fmt.Sprintf("`if: %s`", ins.cond)
			}
			add(file, ins.line, "job %q installs cosign under %s but no assertion step carries the same condition; acquisition, assertion and use must share one predicate or they drift",
				jobName, got)
		}
	}
}

func decodeOne(path string) (*yaml.Node, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // repository-local path
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty document")
		}
		return nil, err
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("document root is not a mapping")
	}
	return root, nil
}

func deref(n *yaml.Node) *yaml.Node {
	for depth := 0; n != nil && n.Kind == yaml.AliasNode; depth++ {
		if depth >= 100 {
			return nil
		}
		n = n.Alias
	}
	return n
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	n = deref(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := deref(n.Content[i]); k != nil && k.Value == key {
			return deref(n.Content[i+1])
		}
	}
	return nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".github")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no ancestor directory contains .github")
		}
		dir = parent
	}
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "::error::cosign-wiring: "+format+"\n", a...)
	os.Exit(1)
}
