// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// mqtt-temperature is an optional stdio MCP adapter. Register it as a managed
// MCP server and use env_secret_refs for MQTT_PASSWORD and any private TLS key.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/olivaresai/olivares/connectors/mqtt"
	"github.com/olivaresai/olivares/sdk"
)

func main() {
	settings := map[string]string{}
	for _, key := range []string{"broker", "topic", "max_age", "timeout", "username", "password",
		"tls", "tls_ca_file", "tls_cert_file", "tls_key_file"} {
		if value, ok := os.LookupEnv("MQTT_" + strings.ToUpper(key)); ok {
			settings[key] = value
		}
	}
	reader, err := mqtt.NewTemperatureReader(sdk.Config{Settings: settings})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if reader.ServeTemperatureMCP(context.Background(), os.Stdin, os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, "mqtt temperature: protocol input or output unavailable")
		os.Exit(1)
	}
}
