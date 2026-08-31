// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command ldap-source ships the AD/LDAP identity connector as a standalone
// go-plugin binary: the engine launches it and talks to it over gRPC. The
// connector code is identical to the in-process case (rt.AddSource(ldap.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/ldap"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(ldap.New())
}
