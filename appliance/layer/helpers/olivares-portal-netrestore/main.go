// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"os"
)

func restoreRules() []helperschema.Rule {
	return []helperschema.Rule{{Subcommand: "restore", Mutating: true, Invokers: []helperschema.Invoker{helperschema.NetGuard, helperschema.RepairConsole}}}
}
func main() {
	h := invocation.Helper{Name: "netrestore", Rules: restoreRules(), NewRequest: func() helperschema.Request { return &netguard.RestoreRequest{} }, Perform: func(ctx context.Context, _ helperschema.Peer, r helperschema.Request) helperschema.Response {
		if err := netguard.RunRootRestore(ctx, *r.(*netguard.RestoreRequest)); err != nil {
			return helperschema.Response{Result: helperschema.ResultAnswered, Code: "network_restoration_unresolved", Detail: "Read the durable call receipts; this response does not settle an owner call."}
		}
		return helperschema.Response{Result: helperschema.ResultAnswered, Code: "network_restore_calls_recorded", Detail: "The guard must settle every call and measure the restored state."}
	}}
	os.Exit(invocation.Serve(context.Background(), h, invocation.Linux{}, os.Args[1:], 0, os.Stdin, os.Stdout, os.Stderr))
}
