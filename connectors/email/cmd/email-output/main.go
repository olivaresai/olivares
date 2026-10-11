// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command email-output ships the email (SMTP+DKIM) output connector as a standalone
// go-plugin binary. The engine runs this connector in-process, so the release does not
// build this binary; build it to run the connector in its own process as an external
// plugin, over gRPC with AutoMTLS. The connector code is identical to the in-process
// case (rt.AddOutput(email.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/email"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeOutput(email.New())
}
