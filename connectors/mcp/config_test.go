// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

func TestParseInlineServers(t *testing.T) {
	specs, err := parseInlineServers(`[{"name":"a","command":"npx","args":["x"]},{"name":"b","transport":"http","url":"https://h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Name != "a" || specs[0].Command != "npx" || specs[1].URL != "https://h" {
		t.Errorf("specs = %+v", specs)
	}
	if _, err := parseInlineServers("not json"); err == nil {
		t.Error("invalid JSON must error")
	}
}

func TestParseMCPConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	content := `{"mcpServers":{"zeta":{"command":"npx","args":["s"]},"alpha":{"type":"http","url":"https://a"}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	specs, err := parseMCPConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by name: alpha before zeta.
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "zeta" {
		t.Errorf("specs = %+v", specs)
	}
	if specs[0].Transport != "http" || specs[0].URL != "https://a" {
		t.Errorf("alpha = %+v", specs[0])
	}
	if specs[1].Command != "npx" {
		t.Errorf("zeta = %+v", specs[1])
	}
}

func TestParseMCPConfigFileMissing(t *testing.T) {
	if _, err := parseMCPConfigFile("/no/such/file.json"); err == nil {
		t.Error("missing file must error")
	}
}

func TestLoadConfig(t *testing.T) {
	cfg := sdk.Config{Settings: map[string]string{
		cfgServers: `[{"name":"a","command":"npx"}]`,
		cfgTimeout: "10s",
	}}
	c, err := loadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.servers) != 1 || c.timeout != 10*time.Second {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadConfigNoServers(t *testing.T) {
	if _, err := loadConfig(sdk.Config{}); err == nil {
		t.Error("no servers configured must error")
	}
}

func TestLoadConfigUnnamedServer(t *testing.T) {
	cfg := sdk.Config{Settings: map[string]string{cfgServers: `[{"command":"npx"}]`}}
	if _, err := loadConfig(cfg); err == nil {
		t.Error("a server with no name must error")
	}
}
