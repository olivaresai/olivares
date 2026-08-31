// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command checkinstallmanifest is the content-contract half of `task
// manifests:check` (C5): it parses deploy/manifests/install.yaml and
// asserts the safe single-node shape — exactly ServiceAccount + Service +
// StatefulSet, replicas=1, and the C1-correct HTTPS /livez + /readyz probes —
// so an over-broad chart edit cannot silently ship in the no-Helm install path.
//
// It lives in the operator module (not scripts/) because the gate must be
// hermetic: Go is the ONE toolchain every environment that runs the gate is
// guaranteed to have — the dev container ships no pip/PyYAML, and runner images
// change their preinstalled Python packages without notice. Kubernetes
// manifests are the operator's domain and its graph already carries the YAML
// parser. It imports neither /core nor /sdk (the operator boundary rule).
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	yaml "go.yaml.in/yaml/v3"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: checkinstallmanifest <install.yaml>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "check-install-manifest: FAIL — "+err.Error())
		os.Exit(1)
	}
	fmt.Println("check-install-manifest: OK (SA+Service+StatefulSet, replicas=1, probes /livez+/readyz HTTPS)")
}

func run(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	var docs []map[string]any
	for {
		var d map[string]any
		if derr := dec.Decode(&d); derr != nil {
			if errors.Is(derr, io.EOF) {
				break
			}
			return fmt.Errorf("parse %s: %w", path, derr)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}

	kinds := make([]string, 0, len(docs))
	var statefulSet map[string]any
	for _, d := range docs {
		kind, _ := d["kind"].(string)
		kinds = append(kinds, kind)
		if kind == "StatefulSet" {
			statefulSet = d
		}
	}
	sort.Strings(kinds)
	if len(kinds) != 3 || kinds[0] != "Service" || kinds[1] != "ServiceAccount" || kinds[2] != "StatefulSet" {
		return fmt.Errorf("unexpected kinds %v — expected exactly ServiceAccount + Service + StatefulSet (safe single-node; no DaemonSet/Job/NetworkPolicy/ServiceMonitor/PDB)", kinds)
	}

	spec := dig(statefulSet, "spec")
	if spec == nil {
		return errors.New("StatefulSet has no spec")
	}
	if replicas, ok := spec["replicas"].(int); !ok || replicas != 1 {
		return fmt.Errorf("StatefulSet replicas = %v, want 1 (single-node default)", spec["replicas"])
	}

	podSpec := dig(spec, "template", "spec")
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		return errors.New("StatefulSet template has no containers")
	}
	container, _ := containers[0].(map[string]any)
	for _, probe := range []struct{ name, path string }{
		{"livenessProbe", "/livez"},
		{"readinessProbe", "/readyz"},
	} {
		httpGet := dig(container, probe.name, "httpGet")
		p, _ := httpGet["path"].(string)
		scheme, _ := httpGet["scheme"].(string)
		if p != probe.path || scheme != "HTTPS" {
			return fmt.Errorf("%s httpGet = %v, want %s over HTTPS (C1)", probe.name, httpGet, probe.path)
		}
	}
	return nil
}

// dig walks nested string-keyed mappings; a missing or mistyped step returns
// nil, so callers' type assertions fail closed into an honest error.
func dig(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if m == nil {
			return nil
		}
		m, _ = m[k].(map[string]any)
	}
	return m
}
