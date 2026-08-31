// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command checkciports enforces the CI port policy of this repository:
//
//	No fixed HOST ports in any workflow. Every entry of
//	jobs.<id>.services.<sid>.ports[] and jobs.<id>.container.ports[] must be a BARE
//	container port: after decoding to a scalar and trimming an optional /tcp, /udp or
//	/sctp suffix it must match ^[0-9]{1,5}$ and parse to 1-65535. Everything else is
//	refused.
//
// WHY. `5432:5432` reserved the HOST's port 5432, and the moment a second job ran on the
// same self-hosted runner host it died in "Initialize containers" with "Bind for :::5432
// failed: port is already allocated" (measured: run 30541550811, job 90901999419 — that
// host runs three runner installations of this repository, so the repo trips over
// itself). A bare container port lets Docker pick a free ephemeral host port, which each
// job resolves from job.services.<sid>.ports['<port>'] in its first step. This gate is
// what keeps the fixed mapping from coming back — including "harmless" fixed ports like
// 55432: the kernel's default ephemeral range is 32768-60999, so a fixed bind there can
// lose a race against any random allocation and reintroduce the failure intermittently.
//
// ALLOWLIST, NOT DENYLIST. The set of bad shapes is open (`5432:5432`, `"5432:5432"`,
// `127.0.0.1:5432:5432`, `5432:5432/tcp`, flow sequences, anchors/aliases, `${{ … }}`
// expressions that could expand to a mapping); the set of good shapes is closed. So the
// gate accepts the closed set and refuses everything else, including constructs it does
// not model: YAML merge keys inside the walked mappings, non-scalar ports entries, and
// unparseable documents are all findings, never silence.
//
// WHY A YAML WALK AND NOT A GREP (precedent measured in this tree: the header of
// cmd/olivares/tools/checkcosignpins documents two text-scanning gates that passed this
// repository's own tree while accepting valid, dangerous YAML). The symmetric risk here
// is a FALSE POSITIVE: e2e-operator-kind.yml carries Kubernetes `ports:` keys inside a
// `run: |` heredoc. A structural walker never sees them — a block scalar is a value, not
// a mapping — while a key scanner would flag them and the gate would be born with an
// exception, which is how these gates die.
//
// It lives in the cmd/olivares module because that module already depends on yaml.v3
// (gopkg.in/yaml.v3, same as checkcosignpins); no new workspace module is introduced,
// and it is not imported by the CLI.
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
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var barePort = regexp.MustCompile(`^[0-9]{1,5}$`)

type finding struct {
	file string
	line int
	msg  string
}

func main() {
	root := os.Getenv("CI_PORTS_ROOT")
	if root == "" {
		fmt.Fprintln(os.Stderr, "checkciports: CI_PORTS_ROOT is not set (scripts/check-ci-ports.sh sets it); refusing to guess which tree to inspect")
		os.Exit(2)
	}

	files, err := collect(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "checkciports: %v\n", err)
		os.Exit(2)
	}
	if len(files) == 0 {
		// A gate pointed at the wrong directory must not pass vacuously.
		fmt.Fprintf(os.Stderr, "checkciports: no workflow files under %s/.github/workflows — wrong CI_PORTS_ROOT? refusing to report a vacuous pass\n", root)
		os.Exit(2)
	}

	var findings []finding
	entries := 0
	for _, f := range files {
		fnd, n := checkFile(f)
		findings = append(findings, fnd...)
		entries += n
	}
	if len(findings) > 0 {
		sort.Slice(findings, func(i, j int) bool {
			if findings[i].file != findings[j].file {
				return findings[i].file < findings[j].file
			}
			return findings[i].line < findings[j].line
		})
		for _, f := range findings {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", f.file, f.line, f.msg)
		}
		fmt.Fprintf(os.Stderr, "checkciports: %d finding(s). Policy: no fixed host ports in workflows — publish a bare container port and resolve the ephemeral host port from job.services.<id>.ports in a step.\n", len(findings))
		os.Exit(1)
	}
	fmt.Printf("checkciports: OK — %d file(s), %d port entries, all bare container ports\n", len(files), entries)
}

// collect returns every workflow file plus every action manifest, so a job-shaped
// document in .github/actions cannot smuggle a fixed mapping either.
func collect(root string) ([]string, error) {
	var files []string
	for _, pat := range []string{
		filepath.Join(root, ".github", "workflows", "*.yml"),
		filepath.Join(root, ".github", "workflows", "*.yaml"),
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			return nil, err
		}
		files = append(files, m...)
	}
	actions := filepath.Join(root, ".github", "actions")
	err := filepath.WalkDir(actions, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		if base == "action.yml" || base == "action.yaml" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func checkFile(path string) ([]finding, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return []finding{{path, 0, fmt.Sprintf("unreadable: %v", err)}}, 0
	}
	var findings []finding
	entries := 0
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		derr := dec.Decode(&doc)
		if errors.Is(derr, io.EOF) {
			break
		}
		if derr != nil {
			// Fail closed: what cannot be decoded cannot be certified.
			findings = append(findings, finding{path, 0, fmt.Sprintf("cannot decode YAML: %v", derr)})
			break
		}
		fnd, n := walkDoc(path, &doc)
		findings = append(findings, fnd...)
		entries += n
	}
	return findings, entries
}

// deref follows alias nodes with cycle detection, so `ports: *x` and `- *p` are
// inspected at their resolved values instead of slipping past as AliasNodes.
func deref(path string, n *yaml.Node) (*yaml.Node, *finding) {
	seen := map[*yaml.Node]bool{}
	for n != nil && n.Kind == yaml.AliasNode {
		if seen[n] {
			return nil, &finding{path, n.Line, "alias cycle; refusing to certify what cannot be resolved"}
		}
		seen[n] = true
		n = n.Alias
	}
	return n, nil
}

// pairs enumerates a mapping's key/value pairs. A merge key (`<<`) inside a mapping this
// gate interprets is refused rather than modeled: merged content could carry a ports
// block this walk never visits, and an unmodelled construct must be a finding, not a
// blind spot.
func pairs(path string, m *yaml.Node) ([][2]*yaml.Node, []finding) {
	var out [][2]*yaml.Node
	var findings []finding
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, kerr := deref(path, m.Content[i])
		if kerr != nil {
			findings = append(findings, *kerr)
			continue
		}
		if k.Value == "<<" {
			findings = append(findings, finding{path, k.Line, "YAML merge key inside a jobs/services/container mapping is not modeled by the port gate — write the mapping (and its ports) explicitly"})
			continue
		}
		out = append(out, [2]*yaml.Node{k, m.Content[i+1]})
	}
	return out, findings
}

func walkDoc(path string, n *yaml.Node) ([]finding, int) {
	var findings []finding
	entries := 0
	n, derr := deref(path, n)
	if derr != nil {
		return []finding{*derr}, 0
	}
	if n == nil {
		return nil, 0
	}
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			fnd, cnt := walkDoc(path, c)
			findings = append(findings, fnd...)
			entries += cnt
		}
		return findings, entries
	}
	if n.Kind != yaml.MappingNode {
		return nil, 0
	}
	rootPairs, fnd := pairsTop(path, n)
	findings = append(findings, fnd...)
	for _, kv := range rootPairs {
		if kv[0].Value != "jobs" {
			continue
		}
		jobs, derr := deref(path, kv[1])
		if derr != nil {
			findings = append(findings, *derr)
			continue
		}
		if jobs == nil || jobs.Kind != yaml.MappingNode {
			continue // nothing walkable; actionlint owns workflow validity
		}
		jobPairs, fnd := pairs(path, jobs)
		findings = append(findings, fnd...)
		for _, jkv := range jobPairs {
			job, derr := deref(path, jkv[1])
			if derr != nil {
				findings = append(findings, *derr)
				continue
			}
			if job == nil || job.Kind != yaml.MappingNode {
				continue
			}
			fnd, cnt := walkJob(path, job)
			findings = append(findings, fnd...)
			entries += cnt
		}
	}
	return findings, entries
}

// pairsTop is pairs without the merge-key refusal: the document root is not a mapping
// this gate interprets member-by-member, so a top-level anchor library stays legal.
func pairsTop(path string, m *yaml.Node) ([][2]*yaml.Node, []finding) {
	var out [][2]*yaml.Node
	var findings []finding
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, kerr := deref(path, m.Content[i])
		if kerr != nil {
			findings = append(findings, *kerr)
			continue
		}
		out = append(out, [2]*yaml.Node{k, m.Content[i+1]})
	}
	return out, findings
}

func walkJob(path string, job *yaml.Node) ([]finding, int) {
	var findings []finding
	entries := 0
	jobPairs, fnd := pairs(path, job)
	findings = append(findings, fnd...)
	for _, kv := range jobPairs {
		switch kv[0].Value {
		case "services":
			services, derr := deref(path, kv[1])
			if derr != nil {
				findings = append(findings, *derr)
				continue
			}
			if services == nil || services.Kind != yaml.MappingNode {
				continue
			}
			svcPairs, fnd := pairs(path, services)
			findings = append(findings, fnd...)
			for _, skv := range svcPairs {
				svc, derr := deref(path, skv[1])
				if derr != nil {
					findings = append(findings, *derr)
					continue
				}
				if svc == nil || svc.Kind != yaml.MappingNode {
					continue // `svc: image-string` shorthand has no ports
				}
				fnd, cnt := checkPortsOf(path, svc)
				findings = append(findings, fnd...)
				entries += cnt
			}
		case "container":
			container, derr := deref(path, kv[1])
			if derr != nil {
				findings = append(findings, *derr)
				continue
			}
			if container == nil || container.Kind != yaml.MappingNode {
				continue // `container: image-string` shorthand has no ports
			}
			fnd, cnt := checkPortsOf(path, container)
			findings = append(findings, fnd...)
			entries += cnt
		}
	}
	return findings, entries
}

func checkPortsOf(path string, m *yaml.Node) ([]finding, int) {
	var findings []finding
	entries := 0
	mPairs, fnd := pairs(path, m)
	findings = append(findings, fnd...)
	for _, kv := range mPairs {
		if kv[0].Value != "ports" {
			continue
		}
		ports, derr := deref(path, kv[1])
		if derr != nil {
			findings = append(findings, *derr)
			continue
		}
		if ports == nil || ports.Kind != yaml.SequenceNode {
			line := kv[0].Line
			if ports != nil {
				line = ports.Line
			}
			findings = append(findings, finding{path, line, "ports must be a sequence of bare container ports"})
			continue
		}
		for _, item := range ports.Content {
			entry, derr := deref(path, item)
			if derr != nil {
				findings = append(findings, *derr)
				continue
			}
			entries++
			if entry.Kind != yaml.ScalarNode {
				findings = append(findings, finding{path, entry.Line, "ports entry is not a scalar; only a bare container port is allowed"})
				continue
			}
			v := entry.Value
			if entry.Tag == "!!null" || v == "" {
				findings = append(findings, finding{path, entry.Line, "empty ports entry; only a bare container port is allowed"})
				continue
			}
			base := v
			for _, suf := range []string{"/tcp", "/udp", "/sctp"} {
				if strings.HasSuffix(base, suf) {
					base = strings.TrimSuffix(base, suf)
					break
				}
			}
			if !barePort.MatchString(base) {
				findings = append(findings, finding{path, entry.Line, fmt.Sprintf("fixed host-port mapping or unmodelled form %q; only a bare container port is allowed", v)})
				continue
			}
			if p, err := strconv.Atoi(base); err != nil || p < 1 || p > 65535 {
				findings = append(findings, finding{path, entry.Line, fmt.Sprintf("port %q is outside 1-65535; only a bare container port is allowed", v)})
			}
		}
	}
	return findings, entries
}
