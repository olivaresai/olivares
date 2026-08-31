// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command gcpkms-source ships the Google Cloud KMS / Secret Manager audit
// connector as a standalone go-plugin binary.
package main

import (
	"github.com/olivaresai/olivares/connectors/gcpkms"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(gcpkms.New())
}
