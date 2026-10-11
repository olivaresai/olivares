// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Read the compiled migration plan and each release's own package configuration.
package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
	"gopkg.in/yaml.v3"
)

func main() {
	versions := map[store.Engine][]int{}
	for _, backend := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		plan, err := engine.CoreMigrationPlanVersions(backend)
		if err != nil {
			log.Fatal(err)
		}
		versions[backend] = plan
	}
	root, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "packaging/nfpm/packages.json"))
	if os.IsNotExist(err) {
		data, err = os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	}
	if err != nil {
		log.Fatal(err)
	}
	var packages struct {
		Nfpms []struct {
			PackageName string `yaml:"package_name"`
			Bindir      string `yaml:"bindir"`
			Contents    []struct{ Dst string }
		}
	}
	if err := yaml.Unmarshal(data, &packages); err != nil || len(packages.Nfpms) == 0 {
		log.Fatal("package configuration unavailable: ", err)
	}
	paths := []string{}
	for _, pkg := range packages.Nfpms {
		if pkg.PackageName == "" || pkg.Bindir == "" || len(pkg.Contents) == 0 {
			log.Fatal("incomplete package configuration")
		}
		paths = append(paths, "package:"+pkg.PackageName, pkg.Bindir+"/olivares")
		for _, item := range pkg.Contents {
			paths = append(paths, item.Dst)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"versions": versions, "paths": paths}); err != nil {
		log.Fatal(err)
	}
}
