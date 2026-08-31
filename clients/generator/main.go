// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command generator is the OpenAPI→SDK pipeline (EXT-4): it reads the
// committed OpenAPI snapshot (web/openapi/openapi.json — the same artifact the
// web client codegen consumes) and regenerates the operation layer of the four
// first-party client SDKs (clients/go, clients/python, clients/typescript and
// clients/java).
//
// Design rules:
//   - Deterministic: same spec bytes → byte-identical output (sorted paths,
//     fixed method order, no timestamps). `task sdk:check` diffs the committed
//     output against a fresh run, exactly like openapi:check.
//   - Hermetic: stdlib only — the generator EMITS Java/Python/TS as text and
//     never invokes their toolchains, and makes no network calls, so the whole
//     pipeline (including the Java target) runs inside the ordinary Go build gate.
//   - Thin by design: the generated layer represents published JSON schemas with
//     generic language values rather than generated DTOs, and sends raw request
//     bytes under their exact declared media type. Everything contractual (auth,
//     tenancy, error envelope, pagination, Retry-After, deprecation signals)
//     lives in each SDK's hand-written core, which this tool never touches.
//   - Deprecations travel: an operation marked deprecated in the spec (from
//     core/api/stability.go) is emitted with the language-native deprecation
//     marker, so integrators see the sunset in their IDE, not in an outage.
//
// The SDKs cover the UNION of the two published contracts: the STABLE core
// document (-spec) and the BETA module-route document (-beta). Every operation
// keeps its tier (x-stability), and the beta operations are emitted with each
// language's stability annotation, so an integrator sees "Stability: beta" in
// their IDE — never a beta route masquerading as part of the stable contract.
//
// Usage: go run ./clients/generator -spec web/openapi/openapi.json \
//
//	-beta web/openapi/openapi.beta.json -out clients
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	spec := flag.String("spec", "web/openapi/openapi.json", "path to the STABLE OpenAPI 3.1 snapshot")
	beta := flag.String("beta", "web/openapi/openapi.beta.json",
		`path to the BETA module-route snapshot ("" to generate the stable contract only)`)
	out := flag.String("out", "clients", "clients root to write generated files into")
	flag.Parse()

	if err := run(*spec, *beta, *out); err != nil {
		fmt.Fprintln(os.Stderr, "sdk generator:", err)
		os.Exit(1)
	}
}

func run(specPath, betaPath, outRoot string) error {
	doc, err := loadUnion(specPath, betaPath)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	goOps, err := emitGo(doc)
	if err != nil {
		return err
	}
	files[filepath.Join("go", "operations.gen.go")] = goOps
	goVer, err := emitGoVersion(doc)
	if err != nil {
		return err
	}
	files[filepath.Join("go", "version.gen.go")] = goVer
	files[filepath.Join("python", "src", "olivares_client", "_operations.py")] = emitPython(doc)
	files[filepath.Join("typescript", "src", "operations.gen.ts")] = emitTypeScript(doc)
	files[filepath.Join("typescript", "src", "version.gen.ts")] = emitTypeScriptVersion(doc)
	javaPkg := filepath.Join("java", "src", "main", "java", "ai", "olivares", "client")
	files[filepath.Join(javaPkg, "Client.java")] = emitJava(doc)
	files[filepath.Join(javaPkg, "ApiMetadata.java")] = emitJavaVersion(doc)

	for rel, content := range files {
		dst := filepath.Join(outRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// loadUnion loads the stable document and, when betaPath is set, merges the beta
// module-route document into it. Each file is validated independently by load
// (paths, summaries, intra-file name collisions); the union adds a CROSS-file
// collision check, because a beta operation whose derived name clashed with a
// stable one would silently shadow it in Python/TS. The stable document's
// APIVersion is authoritative (both majors are v1); the SpecHash binds BOTH
// snapshots so the SDK records the exact pair it was generated from.
func loadUnion(stablePath, betaPath string) (*Document, error) {
	doc, err := loadWithPolicy(stablePath, false)
	if err != nil {
		return nil, err
	}
	if betaPath == "" {
		return doc, nil
	}
	betaDoc, err := loadWithPolicy(betaPath, true)
	if err != nil {
		return nil, err
	}
	// The union carries ONE APIVersion (the stable major); a beta document under a
	// different major could not ride the same SDK. Fail closed, like the collision
	// check below, rather than silently labeling its operations with stable's major.
	if betaDoc.APIVersion != doc.APIVersion {
		return nil, fmt.Errorf("beta document major %q differs from stable %q — the union SDK cannot carry two majors under one APIVersion",
			betaDoc.APIVersion, doc.APIVersion)
	}
	seen := make(map[string]Operation, len(doc.Operations))
	for _, op := range doc.Operations {
		seen[op.pyName()] = op
	}
	for _, op := range betaDoc.Operations {
		if prev, dup := seen[op.pyName()]; dup {
			return nil, fmt.Errorf("operation name collision across specs: %s %s and %s %s both derive %q — rename one route",
				prev.Method, prev.Path, op.Method, op.Path, op.pyName())
		}
		seen[op.pyName()] = op
	}
	doc.Operations = append(doc.Operations, betaDoc.Operations...)
	// Bind both snapshots: a change to EITHER file rotates SpecHash, so the SDK's
	// recorded provenance (and the sdk:check gate) tracks the union, not just stable.
	sum := sha256.Sum256([]byte(doc.SpecHash + ":" + betaDoc.SpecHash))
	doc.SpecHash = hex.EncodeToString(sum[:])
	return doc, nil
}
