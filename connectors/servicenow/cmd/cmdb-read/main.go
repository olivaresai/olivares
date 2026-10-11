// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// cmdb-read is an optional managed stdio MCP adapter. Credentials enter through
// the gateway's env_secret_refs, never command-line arguments.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/olivaresai/olivares/connectors/servicenow"
	"github.com/olivaresai/olivares/sdk"
)

func main() {
	settings := map[string]string{}
	for _, key := range []string{"instance_url", "auth_mode", "username", "password", "token", "source_ref", "sys_id"} {
		if value, ok := os.LookupEnv("SERVICENOW_" + strings.ToUpper(key)); ok {
			settings[key] = value
		}
	}
	reader, err := servicenow.NewCMDBReader(sdk.Config{Settings: settings})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if reader.ServeCMDBMCP(context.Background(), os.Stdin, os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, "servicenow cmdb: protocol input or output unavailable")
		os.Exit(1)
	}
}
