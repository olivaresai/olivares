// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

func TestManagedStdioNpxCacheStaysOutOfSessionFolder(t *testing.T) {
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx is not installed")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	folder, pkg := t.TempDir(), t.TempDir()
	manifest := `{"name":"mc-local-stdio-cache-proof","version":"1.0.0","bin":{"mc-local-stdio-cache-proof":"server.js"}}`
	server := `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const readline = require('node:readline');
const home = process.env.HOME, tmp = process.env.TMPDIR;
fs.writeFileSync(path.join(home, '.mc-cache-proof'), 'private child home');
readline.createInterface({input: process.stdin}).on('line', line => {
  const req = JSON.parse(line);
  if (req.id === undefined) return;
  const result = req.method === 'initialize'
    ? {protocolVersion:'2025-11-25', capabilities:{tools:{}}, serverInfo:{name:'npx-cache-proof',version:'1'}}
    : {tools:[{name:'private_home', description:JSON.stringify({home,tmp}), inputSchema:{type:'object'}}]};
  process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:req.id,result})+'\n');
});
`
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "server.js"), []byte(server), 0o700); err != nil {
		t.Fatal(err)
	}
	m := &mcpManagement{eng: &engine{sessionsMod: sessions.New(sessions.WithConfinement([]string{t.TempDir()}, false))}}
	spec := sessions.LaunchSpec{Program: "npx", Args: []string{"--offline", "--yes", "--package", pkg, "mc-local-stdio-cache-proof"}, Dir: folder, Isolation: sessions.IsolationNative, WaitDelay: time.Second}
	spec.Confinement = m.eng.sessionsMod.ConfinementPolicy([]string{folder}, []string{pkg})
	if err := m.confineLocalServer(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Confinement == nil {
		t.Fatal("the real npx fixture must use the engine's confinement policy")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client, err := launchManagedStdio(ctx, sessions.NewProcRunner(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("actual npx stdio initialization: %v", err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil || len(tools) != 1 {
		t.Fatalf("actual npx stdio tools: %v", err)
	}
	var dirs struct{ Home, Tmp string }
	if err := json.Unmarshal([]byte(tools[0].Description), &dirs); err != nil {
		t.Fatal(err)
	}
	if dirs.Home == "" || dirs.Home == folder || dirs.Home != dirs.Tmp {
		t.Fatal("MCP child did not receive its private home and scratch directory")
	}
	if _, err := os.Stat(filepath.Join(dirs.Home, ".npm", "_npx")); err != nil {
		t.Fatalf("npx did not create its cache in the private home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs.Home, ".mc-cache-proof")); err != nil {
		t.Fatal("stdio fixture did not run in the private home")
	}
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != 0 {
		t.Fatalf("npx polluted the session folder: %v (entries %d)", err, len(entries))
	}
	client.Close()
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(dirs.Home)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stopping the MCP child retained its private cache directory")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
