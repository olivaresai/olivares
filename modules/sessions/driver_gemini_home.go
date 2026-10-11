// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// geminiConfigDirName is the directory Gemini CLI keeps its configuration in,
// inside the home GEMINI_CLI_HOME names. The CLI cannot be pointed at another name.
const geminiConfigDirName = ".gemini"

// geminiSettingsMaxBytes bounds the settings.json the launch reads. A real one is a few KiB.
const geminiSettingsMaxBytes = 1 << 20

// geminiHomeRefusal is the launch-time check of a Gemini CLI profile's home.
//
// The configuration home must be named .gemini, because the CLI reads
// <GEMINI_CLI_HOME>/.gemini and the launch sets GEMINI_CLI_HOME to its parent: any
// other name would leave the child reading a directory nobody validated.
//
// The home must also pre-approve nothing. A user-level `tools.allowed` or a policy
// file lets a tool run with no session/request_permission, so the session's live
// policy never sees it (measured on 0.62.0 with a stand-in API: a shell call under
// `tools.allowed: ["run_shell_command"]` in <home>/.gemini/settings.json ran with no
// request, and a system settings file did not override it). The CLI's policy builder
// (createPolicyEngineConfig, read from the pinned 0.62.0 package, not run) turns more
// of settings.json into allow rules: tools.core, mcp.allowed and a trusted MCP server
// (mcpServers.<name>.trust), and policyPaths and adminPolicyPaths load policy files
// from any path. The child can write its own home, and the setting takes effect at
// the next start, so the check runs on every create and resume. A file that cannot
// be read is refused, never assumed empty.
func geminiHomeRefusal(home *ProviderHomeSnapshot) error {
	if home == nil || home.ConfigHome == "" {
		return badRequest("a Gemini CLI session needs a profile with its configuration home")
	}
	if filepath.Base(home.ConfigHome) != geminiConfigDirName {
		return badRequest("Gemini CLI keeps its configuration in a directory named " + geminiConfigDirName +
			"; give the profile a config_home that ends in it")
	}
	preApproves, err := geminiHomePreApproves(home.ConfigHome)
	if err != nil {
		return conflictErr("the Gemini CLI settings in this profile's home could not be checked, so the session did not start")
	}
	if preApproves {
		return conflictErr("the Gemini CLI settings in this profile's home pre-approve tools (tools.allowed, tools.core, mcp.allowed, " +
			"a trusted MCP server, policyPaths, adminPolicyPaths or a policy file), " +
			"which would skip the session's approval policy; remove them and start the session again")
	}
	return nil
}

// geminiHomePreApproves reports whether the configuration home holds a rule that
// approves a tool without asking.
func geminiHomePreApproves(configHome string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(configHome, "policies"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if len(entries) > 0 {
		return true, nil
	}
	path := filepath.Join(configHome, "settings.json")
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// The child can make its own settings.json a FIFO, which would block the read
	// for ever, or a file of any size.
	if !info.Mode().IsRegular() || info.Size() > geminiSettingsMaxBytes {
		return false, errors.New("settings.json is not a regular file of a readable size")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if preApproves, err := geminiSettingsPreApprove(raw); err == nil {
		return preApproves, nil
	}
	// The CLI accepts comments in settings.json, which this parser does not: a file
	// that names any of these keys is treated as pre-approving, whatever its value,
	// and so is one with a \u escape, which could spell any of them.
	for _, key := range geminiPreApprovingKeys {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			return true, nil
		}
	}
	return bytes.Contains(raw, []byte(`\u`)), nil
}

// geminiSettingsPreApprove reads settings.json the way the CLI's JSON.parse does:
// keys are case-sensitive and the last of a repeated key wins. Decoding into struct
// fields would match "ALLOWED" to "allowed" and let a later empty one hide the real
// list. A shape that is not the expected one is an error, left to the caller's
// fallback.
func geminiSettingsPreApprove(raw []byte) (bool, error) {
	top, err := geminiJSONObject(raw)
	if err != nil {
		return false, err
	}
	for _, path := range [][]string{
		{"tools", "allowed"}, {"tools", "core"}, {"mcp", "allowed"}, {"policyPaths"}, {"adminPolicyPaths"},
	} {
		value, err := geminiJSONLookup(top, path...)
		if err != nil {
			return false, err
		}
		var list []json.RawMessage
		if len(value) > 0 {
			if err := json.Unmarshal(value, &list); err != nil {
				return false, err
			}
		}
		if len(list) > 0 {
			return true, nil
		}
	}
	servers, err := geminiJSONLookup(top, "mcpServers")
	if err != nil {
		return false, err
	}
	byName, err := geminiJSONObject(servers)
	if err != nil {
		return false, err
	}
	for _, server := range byName {
		fields, err := geminiJSONObject(server)
		if err != nil {
			return false, err
		}
		if geminiJSTruthy(fields["trust"]) {
			return true, nil
		}
	}
	return false, nil
}

func geminiJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(raw) == 0 {
		return nil, nil
	}
	err := json.Unmarshal(raw, &object)
	return object, err
}

// geminiJSONLookup follows keys down nested objects; an absent key is no value.
func geminiJSONLookup(object map[string]json.RawMessage, keys ...string) (json.RawMessage, error) {
	for i, key := range keys {
		value, ok := object[key]
		if !ok || i == len(keys)-1 {
			return value, nil
		}
		var err error
		if object, err = geminiJSONObject(value); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// geminiJSTruthy is JavaScript's truthiness for a decoded JSON value, which is how the
// CLI tests `trust`: `"true"` and `1` trust a server as `true` does. A zero spelled
// 0.0 counts as truthy here, which refuses more and never less.
func geminiJSTruthy(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "false", "0", `""`:
		return false
	}
	return true
}

// geminiPreApprovingKeys are the settings.json keys that, in an unparsable file,
// stand for a pre-approval: tools.allowed and mcp.allowed ("allowed"), tools.core
// ("core"), a trusted MCP server ("trust"), and the policy file paths.
var geminiPreApprovingKeys = []string{"allowed", "core", "trust", "policyPaths", "adminPolicyPaths"}
