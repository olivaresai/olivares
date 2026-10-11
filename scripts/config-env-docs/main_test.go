// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryConfigurationReferenceMatchesSource(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if got := run(root, false, false, &out, &errOut); got != exitClean {
		t.Fatalf("configuration reference exited %d, want %d:\n%s%s", got, exitClean, &out, &errOut)
	}
}

func TestConfigurationReferenceDescribesCurrentComponents(t *testing.T) {
	region := renderRegion(nil, nil)
	for _, component := range []string{"Kubernetes operator", "Terraform provider"} {
		if strings.Contains(region, component) {
			t.Errorf("configuration reference claims to cover %s, which is in the Business source tree", component)
		}
	}
}
