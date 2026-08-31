// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command checkcosignpins is the STATIC HALF of this repository's cosign control. It
// enforces exactly one thing, and the precision of that sentence matters:
//
//	Every `sigstore/cosign-installer` step declared in an EXECUTABLE step sequence of this
//	repository's workflows and composite actions is the reviewed action revision and
//	requests the approved cosign version as a literal — and no OTHER cosign acquisition
//	path that this gate models appears anywhere in those files.
//
// IT DOES NOT PROVE THAT AN UNAPPROVED COSIGN CANNOT BE OBTAINED. It cannot: `run:` is a
// shell, `uses:` can reach code nobody here can read, and four rounds of adversarial review
// each found another valid construct that a finite YAML model misses — aliases, matrix
// data, service entrypoints, invoked repository scripts, renamed images. Proving that
// negative by reading YAML is proving a negative over a Turing-complete language.
//
// THE LOAD-BEARING CONTROL IS scripts/assert-cosign-binary.sh, which runs immediately after
// every installer step, plus scripts/cosign-verified.sh, through which every signer
// actually executes: together they require the bytes to hash to a digest published in the
// upstream release, re-checked immediately before each invocation. That question IS
// decidable — it does not care how cosign arrived — where this gate's question is not.
//
// It is still not a proof over time. Hashing a file and then executing that path leaves a
// window, however small, and only descriptor-bound execution would close it; that residual
// is stated in scripts/cosign-verified.sh rather than papered over. So: this gate is fast
// feedback on a pull request, the execution-time pair is the control, and neither is a
// complete proof. Both say so themselves.
//
// WHY IT IS A YAML WALK AND NOT A TEXT SCAN. Until 2026-07-25 nothing pinned cosign, so
// each installer revision supplied its own default: the build and Docker Hub jobs got
// cosign v3.0.6 while the OTA job got v2.5.2 — two generations with incompatible signing
// contracts in one release pipeline, discoverable only when a v* tag was cut. Four
// successive gates were written to stop that and an adversarial review broke each one:
//
//   - a line-window scanner credited a pin belonging to the NEXT action and accepted
//     `env:` in place of `with:`;
//   - a "canonical shape, fail closed" scanner missed a YAML-ESCAPED action name that
//     GitHub executes but a substring search cannot see, and credited a fake
//     `cosign-release:` line inside a block scalar under the real `install-dir:` input;
//   - a struct-decoding version modeled only `steps[].uses`, compared AGGREGATE raw and
//     structured mention counts (so two unrelated miscounts canceled), stripped `#`
//     comments blind to quoting (so `run: "printf '#'; curl …/cosign-installer/… | sh"`
//     was erased), and used yaml.Unmarshal, which decodes only the FIRST document;
//   - a yaml.Node walk that treated EVERY mapping as a step, so `strategy.matrix` DATA
//     satisfied the installer count while the matrix-selected container image went
//     unexamined; that walk also never followed an ALIAS, so `uses: *installer` reached
//     the action's default version without being inspected, and it resolved local
//     `uses: ./path` targets without containing them to the repository.
//
// Every one of those was valid YAML that `actionlint` accepted. So this version decodes
// each file into a yaml.Node tree, dereferences aliases at every semantic use site,
// identifies installer steps ONLY inside the step sequences GitHub actually executes, and
// requires that no other node in the tree can obtain cosign by any modeled means.
//
// ROLE-AWARENESS IS FAIL-CLOSED, NOT A NARROWING. An installer reference that appears
// OUTSIDE an executable step sequence is not silently ignored: it stops being a validated
// installer and becomes a finding through the scalar backstop. Data cannot buy credit.
//
// WHAT IT DOES NOT COVER, STATED PLAINLY. A third-party action pinned by SHA can install
// anything inside its own implementation, and no static reading of this repository can see
// that. Neither can it see a repository script invoked from `run:` or a custom
// `defaults.run.shell`, a command assembled from separate env/matrix fields, or a renamed
// image whose contents include cosign. This gate therefore enforces the policy over:
// workflow and composite-action steps, `run:` scripts, `docker://` step images,
// job/service/action container images, `strategy.matrix` values, job-level
// reusable-workflow calls (local ones are resolved; external ones must be allowlisted),
// and local `uses: ./path` actions (resolved with containment, symlink canonicalization
// and cycle detection). Everything else is governed by the execution-time invariant.
// Saying so is the point: an unverified "cannot happen" inside a gate is
// indistinguishable from a hole.
//
// It lives in the cmd/olivares module because that module already depends on yaml.v3; no
// new workspace module is introduced, and it is not imported by the CLI.
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

// approvedCosign is the version whose contract is proven by
// scripts/check-cosign-contract.sh. Do NOT bump it without running that fixture against
// the new binary. v2.6.4 (2026-07-17) is the maintained v2 line: v2.5.2 and earlier fall
// inside GHSA-whqx-f9j3-ch6m and GHSA-w6c6-c85g-mmv6.
const approvedCosign = "v2.6.4"

// approvedActionSHA is the ONE reviewed sigstore/cosign-installer revision. Pinning the
// version input alone is not enough: a different installer revision is different code
// running in the release job.
const approvedActionSHA = "6f9f17788090df1f26f669e9d70d6ae9567deba6"

const installerRepo = "sigstore/cosign-installer"

// approvedReusableWorkflows are the external reusable workflows this repository is allowed
// to call. A reusable workflow runs arbitrary jobs under our identity, so an unreviewed one
// is an unbounded acquisition surface. Local calls are resolved instead of allowlisted.
//
// RESIDUAL, OWNED AND STATED: these are mutable `v2.1.0` tags, not commit SHAs, because
// the SLSA generators refuse to run when called by digest (they resolve their own
// trusted builder by tag). "Reviewed" therefore does not mean "immutable" here, and the
// execution-time invariant does not extend into another repository's jobs.
var approvedReusableWorkflows = map[string]bool{
	"slsa-framework/slsa-github-generator/.github/workflows/generator_generic_slsa3.yml@v2.1.0":   true,
	"slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@v2.1.0": true,
}

// acquisitionPatterns match scalar content that OBTAINS a cosign binary, as opposed to
// merely invoking one that the reviewed installer already placed on PATH. Each pattern is
// anchored on an acquisition verb or a distribution path so that ordinary use
// (`cosign verify-blob …`, `scripts/check-cosign-contract.sh`) does not trip it.
//
// The package-manager and pip patterns stop at a command separator (`;`, `&&`, `|`) so
// that a legitimate `apt-get install -y jq && cosign verify …` is not reported: the
// install verb and the word cosign must belong to the SAME command.
var acquisitionPatterns = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`(?i)sigstore/cosign-installer`), "the cosign installer action referenced outside a reviewed step"},
	{regexp.MustCompile(`(?i)\bgo\s+(install|run)\b[^\n]*sigstore/cosign`), "`go install`/`go run` of cosign"},
	{regexp.MustCompile(`(?i)https?://[^\s"']*sigstore/cosign[^\s"']*releases`), "a download of a cosign release asset"},
	{regexp.MustCompile(`(?i)\b(curl|wget)\b[^\n]*sigstore/cosign`), "a download of cosign from its upstream repository"},
	{regexp.MustCompile(`(?i)\bgh\s+release\s+download\b[^\n]*\bcosign\b`), "a `gh release download` of cosign"},
	{regexp.MustCompile(`(?i)\bgit\s+clone\b[^\n]*sigstore/cosign`), "a clone of the cosign source"},
	{regexp.MustCompile(`(?i)\b(apt-get|apt|apk|dnf|yum|zypper|brew|choco|scoop|winget|port)\b[^\n]*\b(install|add)\b[^\n;&|]*\bcosign\b`), "a package-manager installation of cosign"},
	{regexp.MustCompile(`(?i)\b(pacman|nix-env)\b[^\n;&|]*\bcosign\b`), "a package-manager installation of cosign"},
	{regexp.MustCompile(`(?i)\bpip3?\s+install\b[^\n;&|]*\bcosign\b`), "a pip installation of cosign"},
}

// imageCosign matches a container image reference that ships cosign. A job that runs in
// such an image has cosign on PATH without any installer step. It is a NAME heuristic, not
// proof of image contents: a renamed or toolbox image defeats it, which is one more reason
// the execution-time invariant is the control.
var imageCosign = regexp.MustCompile(`(?i)(^|[/:@])cosign([:@/]|$)`)

// dynamicExpr matches a GitHub Actions expression. A container image built from one cannot
// be resolved by any static reading, so this gate refuses it rather than pretending to
// have checked it — that pretense is exactly how matrix-selected images went unexamined.
var dynamicExpr = regexp.MustCompile(`\$\{\{`)

// maxAliasDepth bounds alias dereferencing. yaml.v3 cannot construct a cycle (an alias
// must name an already-closed anchor), but a bound costs nothing and turns a hypothetical
// pathological input into a finding instead of a hang.
const maxAliasDepth = 100

type finding struct {
	file string
	line int
	msg  string
}

type checker struct {
	root         string // canonical: absolute and symlink-resolved
	findings     []finding
	installers   int
	scanned      map[string]bool // canonical path -> scanned, for cycle detection
	scannedFiles []string        // every file actually scanned, entry or resolved
}

func main() {
	root := os.Getenv("COSIGN_PINS_ROOT")
	if root == "" {
		var err error
		root, err = repoRoot()
		if err != nil {
			fatalf("cannot locate the repository root: %v", err)
		}
	}
	canonRoot, err := canonical(root)
	if err != nil {
		fatalf("cannot canonicalize the repository root %s: %v", root, err)
	}

	c := &checker{root: canonRoot, scanned: map[string]bool{}}
	entry, err := c.collect()
	if err != nil {
		fatalf("scanning %s: %v", canonRoot, err)
	}
	if len(entry) == 0 {
		fatalf("no workflow or action metadata found under %s/.github — either the layout moved (re-scope this gate) or it is pointed at the wrong tree", canonRoot)
	}
	for _, f := range entry {
		c.scanFile(f)
	}

	if len(c.findings) > 0 {
		for _, f := range c.findings {
			rel, relErr := filepath.Rel(c.root, f.file)
			if relErr != nil {
				rel = f.file
			}
			if f.line > 0 {
				fmt.Fprintf(os.Stderr, "::error::cosign-pins: %s:%d — %s\n", rel, f.line, f.msg)
			} else {
				fmt.Fprintf(os.Stderr, "::error::cosign-pins: %s — %s\n", rel, f.msg)
			}
		}
		fmt.Fprintf(os.Stderr, "\ncosign-pins: %d problem(s). cosign may be obtained ONLY as:\n", len(c.findings))
		fmt.Fprintf(os.Stderr, "    - uses: %s@%s\n      with:\n        cosign-release: '%s'\n", installerRepo, approvedActionSHA, approvedCosign)
		fmt.Fprintf(os.Stderr, "To change the version: run scripts/check-cosign-contract.sh against the new binary\nfirst, then update approvedCosign here and APPROVED_COSIGN in that script.\n")
		os.Exit(1)
	}

	if c.installers == 0 {
		fatalf("no %s step found anywhere under .github — either the release pipeline stopped installing cosign (revisit this gate and scripts/check-cosign-contract.sh) or the scan has drifted", installerRepo)
	}

	fmt.Printf("cosign-pins: OK (%d installer step(s) across %d file(s), each on revision %s with cosign-release %s)\n",
		c.installers, len(c.scannedFiles), approvedActionSHA[:12], approvedCosign)
	fmt.Printf("cosign-pins: STATIC half — installer steps declared in executable step sequences are\n")
	fmt.Printf("cosign-pins: pinned, and the modeled alternative paths (run scripts, container images,\n")
	fmt.Printf("cosign-pins: matrix values, local actions, reusable workflow calls) are absent. This does\n")
	fmt.Printf("cosign-pins: NOT prove that no unapproved cosign can be obtained. What does the load-bearing\n")
	fmt.Printf("cosign-pins: work is scripts/assert-cosign-binary.sh plus scripts/cosign-verified.sh, which\n")
	fmt.Printf("cosign-pins: authenticate the BYTES by digest before each signing invocation — and which\n")
	fmt.Printf("cosign-pins: state their own residual (a hash-then-exec window) rather than claim a proof.\n")
}

func (c *checker) add(file string, line int, format string, a ...any) {
	c.findings = append(c.findings, finding{file: file, line: line, msg: fmt.Sprintf(format, a...)})
}

// canonical makes a path absolute AND resolves symlinks, so that two spellings of the same
// file converge and a link out of the tree becomes visible rather than being followed
// silently. A path that does not exist yet is returned in its absolute, lexically clean
// form: the caller reports the absence, and contain() still refuses it if it points out.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return filepath.Clean(abs), nil //nolint:nilerr // absence is the caller's to report
	}
	return resolved, nil
}

// contain canonicalizes a candidate path and REFUSES it unless it lies inside the
// repository root. A gate whose verdict depends on files outside the tree it is gating is
// not gating that tree: `../x`, `./../x` and a symlinked directory all end up outside, and
// all three must be findings rather than reads.
func (c *checker) contain(p string) (string, error) {
	canon, err := canonical(p)
	if err != nil {
		return "", fmt.Errorf("cannot canonicalize %s: %w", p, err)
	}
	rel, err := filepath.Rel(c.root, canon)
	if err != nil {
		return "", fmt.Errorf("cannot relate %s to the repository root: %w", canon, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("resolves to %s, which is OUTSIDE the repository root %s", canon, c.root)
	}
	return canon, nil
}

// scanFile decodes one YAML file and applies the acquisition policy to its whole node
// tree. Files are scanned at most once, keyed by CANONICAL path, so two spellings or two
// symlinks to one file cannot make it look like two independent inputs — nor recurse.
func (c *checker) scanFile(path string) {
	canon, err := c.contain(path)
	if err != nil {
		c.add(path, 0, "%v", err)
		return
	}
	if c.scanned[canon] {
		return
	}
	c.scanned[canon] = true
	c.scannedFiles = append(c.scannedFiles, canon)

	raw, err := os.ReadFile(canon) //nolint:gosec // contained, repository-local path
	if err != nil {
		c.add(canon, 0, "cannot read: %v", err)
		return
	}

	// EXACTLY ONE DOCUMENT, then EOF. yaml.Unmarshal decodes only the first document, so
	// an earlier version of this gate reported clean after checking document one while a
	// second document carried an unpinned installer.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty or comment-only file cannot itself obtain cosign, but a policy
			// input this gate cannot interpret must never pass silently: GitHub would
			// reject it too, and silence here is how "the file was checked" becomes a
			// claim nobody can support.
			c.add(canon, 0, "empty or comment-only YAML: this gate reports what it cannot interpret rather than accepting it silently")
			return
		}
		c.add(canon, 0, "cannot parse as YAML, so its cosign usage cannot be verified: %v", err)
		return
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		c.add(canon, extra.Line, "multi-document YAML: this gate verifies one document per file, so later documents would go unchecked")
		return
	} else if !errors.Is(err, io.EOF) {
		c.add(canon, 0, "cannot parse the document stream: %v", err)
		return
	}

	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = deref(root.Content[0])
	}
	if root == nil || root.Kind != yaml.MappingNode {
		c.add(canon, doc.Line, "document root is not a mapping, so it is neither a workflow nor action metadata this gate can interpret")
		return
	}

	c.checkDuplicateKeys(canon, root, map[*yaml.Node]bool{})

	validated := map[*yaml.Node]bool{}
	c.checkExecutableSteps(canon, root, validated)
	c.checkJobLevelUses(canon, root)
	c.checkContainers(canon, root)
	c.checkLocalActionRuntime(canon, root)
	c.checkScalars(canon, root, validated, map[*yaml.Node]bool{})
}

// deref follows a YAML alias to the node its anchor defines. Decoding into a yaml.Node
// does NOT resolve aliases — it represents them — so a walker that only follows Content
// never sees the value an alias use site actually contributes. That was the round-4
// bypass: an anchored, correctly pinned first `uses` masked a second `uses: *installer`
// which took the action revision's default version.
func deref(n *yaml.Node) *yaml.Node {
	for depth := 0; n != nil && n.Kind == yaml.AliasNode; depth++ {
		if depth >= maxAliasDepth {
			return nil
		}
		n = n.Alias
	}
	return n
}

// mapValueRaw returns the value node for a mapping key WITHOUT dereferencing it, so a
// caller can report the diagnostic at the use site rather than at the anchor definition.
func mapValueRaw(n *yaml.Node, key string) *yaml.Node {
	n = deref(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := deref(n.Content[i])
		if k != nil && k.Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// mapValue returns the dereferenced value node for a mapping key, or nil.
func mapValue(n *yaml.Node, key string) *yaml.Node {
	return deref(mapValueRaw(n, key))
}

// lineOf reports the source line of the USE site, falling back to the anchor.
func lineOf(raw, resolved *yaml.Node) int {
	if raw != nil && raw.Line > 0 {
		return raw.Line
	}
	if resolved != nil {
		return resolved.Line
	}
	return 0
}

// checkDuplicateKeys refuses a mapping that declares the same key twice. yaml.v3 is
// first-wins, so an approved `cosign-release` followed by an unapproved one validated the
// first and created no finding for the second. A policy parser must not silently pick.
func (c *checker) checkDuplicateKeys(file string, n *yaml.Node, seen map[*yaml.Node]bool) {
	n = deref(n)
	if n == nil || seen[n] {
		return
	}
	seen[n] = true
	if n.Kind == yaml.MappingNode {
		first := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := deref(n.Content[i])
			if k == nil || k.Kind != yaml.ScalarNode {
				continue
			}
			if line, dup := first[k.Value]; dup {
				c.add(file, k.Line, "duplicate mapping key %q (first declared at line %d): this gate refuses to guess which value executes", k.Value, line)
				continue
			}
			first[k.Value] = k.Line
		}
	}
	for _, child := range n.Content {
		c.checkDuplicateKeys(file, child, seen)
	}
}

// checkExecutableSteps walks ONLY the step sequences GitHub actually executes:
// `jobs.<id>.steps[]` in a workflow and `runs.steps[]` in a composite action.
//
// The previous version treated every mapping ANYWHERE as a step, which let inert
// `strategy.matrix` data satisfy the installer count while the matrix-selected container
// image was never examined. Restricting the walk is fail-closed, not a narrowing: an
// installer reference outside these sequences loses its exemption and is reported by
// checkScalars.
func (c *checker) checkExecutableSteps(file string, root *yaml.Node, validated map[*yaml.Node]bool) {
	if runs := mapValue(root, "runs"); runs != nil {
		c.checkStepSequence(file, mapValue(runs, "steps"), validated)
	}
	jobs := mapValue(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		job := deref(jobs.Content[i+1])
		c.checkStepSequence(file, mapValue(job, "steps"), validated)
	}
}

func (c *checker) checkStepSequence(file string, seq *yaml.Node, validated map[*yaml.Node]bool) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range seq.Content {
		step := deref(item)
		if step == nil || step.Kind != yaml.MappingNode {
			continue
		}
		rawUses := mapValueRaw(step, "uses")
		if rawUses == nil {
			continue
		}
		uses := deref(rawUses)
		if uses == nil || uses.Kind != yaml.ScalarNode {
			c.add(file, lineOf(rawUses, uses), "step `uses:` does not resolve to a plain scalar, so what it runs cannot be verified")
			continue
		}
		c.checkStepUses(file, step, uses, lineOf(rawUses, uses), validated)
	}
}

func (c *checker) checkStepUses(file string, step, uses *yaml.Node, line int, validated map[*yaml.Node]bool) {
	v := strings.TrimSpace(uses.Value)

	// A local action: resolve its metadata and apply the same policy there.
	if strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") {
		c.resolveLocalAction(file, line, v)
		return
	}

	// A container image used directly as a step.
	if strings.HasPrefix(v, "docker://") {
		img := strings.TrimPrefix(v, "docker://")
		switch {
		case dynamicExpr.MatchString(img):
			c.add(file, line, "step image %q is built from an expression; a static gate cannot resolve what it contains, so it is refused rather than assumed clean", v)
		case imageCosign.MatchString(img):
			c.add(file, line, "step runs container image %q, which supplies cosign outside the reviewed installer", v)
		}
		return
	}

	if !strings.Contains(v, installerRepo) {
		return
	}

	c.installers++
	validated[uses] = true

	repo, ref, ok := strings.Cut(v, "@")
	switch {
	case !ok:
		c.add(file, line, "installer step %q has no @ref; pin the reviewed revision %s", v, approvedActionSHA)
		return
	case repo != installerRepo:
		c.add(file, line, "installer step uses %q, want exactly %s", repo, installerRepo)
		return
	case ref != approvedActionSHA:
		c.add(file, line, "installer pinned to revision %s, want the reviewed %s", ref, approvedActionSHA)
		return
	}

	with := mapValue(step, "with")
	if with == nil || with.Kind != yaml.MappingNode {
		c.add(file, line, "installer step has no `with:` mapping, so it takes the action revision's DEFAULT cosign version")
		return
	}
	rawRel := mapValueRaw(with, "cosign-release")
	if rawRel == nil {
		c.add(file, line, "installer step has no `cosign-release` input, so it takes the action revision's DEFAULT cosign version")
		return
	}
	rel := deref(rawRel)
	if rel == nil || rel.Kind != yaml.ScalarNode {
		c.add(file, lineOf(rawRel, rel), "`cosign-release` does not resolve to a plain scalar; this gate accepts only a literal version")
		return
	}
	if rel.Value != approvedCosign {
		c.add(file, lineOf(rawRel, rel), "`cosign-release` is %q, want %q", rel.Value, approvedCosign)
		return
	}
	validated[rel] = true
}

// resolveLocalAction follows `uses: ./path` to its action metadata and scans it, so a
// composite action anywhere in the repository — not only under .github/actions — is
// covered. The target is CONTAINED: a `../` escape or a symlink out of the tree is a
// finding, because a repository gate must not base its verdict on files it does not gate.
// Cycles terminate because scanFile records every canonical file it has seen.
func (c *checker) resolveLocalAction(file string, line int, rel string) {
	dir, err := c.contain(filepath.Join(c.root, rel))
	if err != nil {
		c.add(file, line, "local action %q %v; a repository gate must not read policy input from outside the tree it gates", rel, err)
		return
	}
	var found string
	for _, name := range []string{"action.yml", "action.yaml"} {
		cand := filepath.Join(dir, name)
		if _, statErr := os.Stat(cand); statErr == nil {
			contained, cErr := c.contain(cand)
			if cErr != nil {
				c.add(file, line, "local action metadata for %q %v", rel, cErr)
				return
			}
			found = contained
			break
		}
	}
	if found == "" {
		c.add(file, line, "local action %q has no action.yml/action.yaml under %s, so what it installs cannot be verified", rel, dir)
		return
	}
	c.scanFile(found)
}

// checkLocalActionRuntime classifies action metadata whose implementation is CODE rather
// than YAML. It reads the already-decoded tree instead of re-reading the file: the earlier
// second read could fail or observe different bytes, and it ignored both possibilities.
func (c *checker) checkLocalActionRuntime(file string, root *yaml.Node) {
	runs := mapValue(root, "runs")
	if runs == nil {
		return
	}
	using := mapValue(runs, "using")
	if using == nil || using.Kind != yaml.ScalarNode {
		return
	}
	image := ""
	if img := mapValue(runs, "image"); img != nil && img.Kind == yaml.ScalarNode {
		image = img.Value
	}
	switch {
	case using.Value == "docker":
		c.add(file, using.Line, "local action is a Docker action (`runs.using: docker`, image %q); this gate cannot read what its image installs", image)
	case strings.HasPrefix(using.Value, "node"):
		c.add(file, using.Line, "local action is a JavaScript action (`runs.using: %s`); this gate cannot read what its code installs", using.Value)
	}
}

// checkJobLevelUses models `jobs.<id>.uses:` — a reusable-workflow call, which runs whole
// jobs under this repository's identity. Local calls are resolved and contained; external
// ones must be on the reviewed allowlist.
func (c *checker) checkJobLevelUses(file string, root *yaml.Node) {
	jobs := mapValue(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		job := deref(jobs.Content[i+1])
		rawUses := mapValueRaw(job, "uses")
		if rawUses == nil {
			continue
		}
		uses := deref(rawUses)
		if uses == nil || uses.Kind != yaml.ScalarNode {
			c.add(file, lineOf(rawUses, uses), "job-level `uses:` does not resolve to a plain scalar, so the workflow it calls cannot be verified")
			continue
		}
		line := lineOf(rawUses, uses)
		v := strings.TrimSpace(uses.Value)
		if strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") {
			target, err := c.contain(filepath.Join(c.root, v))
			if err != nil {
				c.add(file, line, "local reusable workflow %q %v", v, err)
				continue
			}
			if _, statErr := os.Stat(target); statErr != nil {
				c.add(file, line, "local reusable workflow %q does not exist, so its steps cannot be verified", v)
				continue
			}
			c.scanFile(target)
			continue
		}
		if !approvedReusableWorkflows[v] {
			c.add(file, line, "calls external reusable workflow %q, which runs arbitrary jobs under this repository's identity; add its exact ref to the reviewed allowlist in this gate or remove the call", v)
		}
	}
}

// checkContainers models the container surfaces GitHub documents: a job container, a
// service container, and an action's own image — plus `strategy.matrix`, because a matrix
// value is what a dynamic image reference resolves to at run time.
func (c *checker) checkContainers(file string, root *yaml.Node) {
	report := func(n *yaml.Node, where string) {
		if n == nil {
			return
		}
		img := n
		if n.Kind == yaml.MappingNode {
			img = mapValue(n, "image")
		}
		if img == nil || img.Kind != yaml.ScalarNode {
			return
		}
		switch {
		case dynamicExpr.MatchString(img.Value):
			c.add(file, img.Line, "%s image %q is built from an expression; a static gate cannot resolve which image runs, so it is refused rather than assumed clean", where, img.Value)
		case imageCosign.MatchString(img.Value):
			c.add(file, img.Line, "%s uses image %q, which supplies cosign outside the reviewed installer", where, img.Value)
		}
	}

	if runs := mapValue(root, "runs"); runs != nil {
		report(mapValue(runs, "image"), "composite/docker action")
	}
	jobs := mapValue(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		job := deref(jobs.Content[i+1])
		report(mapValue(job, "container"), "job container")
		if svcs := mapValue(job, "services"); svcs != nil && svcs.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(svcs.Content); j += 2 {
				report(deref(svcs.Content[j+1]), "service container")
			}
		}
		c.checkMatrixImages(file, job)
	}
}

// checkMatrixImages tests every `strategy.matrix` value against the image heuristic. A
// dynamic `container: ${{ matrix.image }}` is already refused above; this closes the same
// hole from the other side, where the literal that would have been selected lives.
func (c *checker) checkMatrixImages(file string, job *yaml.Node) {
	strategy := mapValue(job, "strategy")
	if strategy == nil {
		return
	}
	matrix := mapValue(strategy, "matrix")
	if matrix == nil {
		return
	}
	seen := map[*yaml.Node]bool{}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		n = deref(n)
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if n.Kind == yaml.ScalarNode && imageCosign.MatchString(n.Value) {
			c.add(file, n.Line, "matrix value %q names a cosign image; a matrix-selected container would supply cosign outside the reviewed installer", n.Value)
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(matrix)
}

// checkScalars is the backstop: every scalar in the decoded tree that is not part of a
// validated installer step is tested against the acquisition patterns. Because it walks
// DECODED values, a YAML-escaped or quoted action name is already resolved, and because it
// tests node values rather than file lines, no comment-stripping heuristic is involved —
// comments are not part of the decoded tree at all.
func (c *checker) checkScalars(file string, n *yaml.Node, validated, seen map[*yaml.Node]bool) {
	n = deref(n)
	if n == nil || seen[n] {
		return
	}
	seen[n] = true
	if n.Kind == yaml.ScalarNode && !validated[n] {
		for _, p := range acquisitionPatterns {
			if p.re.MatchString(n.Value) {
				c.add(file, n.Line, "%s — cosign may be obtained only through the reviewed installer step", p.what)
				break
			}
		}
	}
	for _, child := range n.Content {
		c.checkScalars(file, child, validated, seen)
	}
}

// collect returns the workflow files and action metadata files to scan. GitHub only
// executes workflows directly under .github/workflows, and action metadata must be named
// action.yml or action.yaml; scanning anything else produced false positives.
func (c *checker) collect() ([]string, error) {
	var out []string

	wfDir := filepath.Join(c.root, ".github", "workflows")
	entries, err := os.ReadDir(wfDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".yml" || ext == ".yaml" {
			out = append(out, filepath.Join(wfDir, e.Name()))
		}
	}

	actDir := filepath.Join(c.root, ".github", "actions")
	err = filepath.WalkDir(actDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if base := filepath.Base(path); base == "action.yml" || base == "action.yaml" {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	sort.Strings(out)
	return out, nil
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
	fmt.Fprintf(os.Stderr, "::error::cosign-pins: "+format+"\n", a...)
	os.Exit(1)
}
