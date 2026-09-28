// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"slices"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

// powerArgv is the one command each verb runs: the service manager's own verbs, by absolute
// path, with no shell and nothing from the request but the choice between the two.
var powerArgv = map[string][]string{
	helperschema.PowerReboot:   {"/usr/bin/systemctl", "reboot"},
	helperschema.PowerShutdown: {"/usr/bin/systemctl", "poweroff"},
}

// runner runs an argument vector.
type runner func(ctx context.Context, argv []string) error

// helper is the power helper over run.
func helper(run runner) invocation.Helper {
	return invocation.Helper{
		Name:       helperschema.HelperPower,
		Rules:      helperschema.PowerRules(),
		NewRequest: func() helperschema.Request { return &helperschema.PowerRequest{} },
		Perform: func(ctx context.Context, _ helperschema.Peer, request helperschema.Request) helperschema.Response {
			verb := request.Subcommand()
			argv, ok := powerArgv[verb]
			if !ok {
				return helperschema.Response{Result: helperschema.ResultRefused, Code: helperschema.CodeInputRefused,
					Detail: "not a verb of this helper"}
			}
			if err := run(ctx, slices.Clone(argv)); err != nil {
				return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed,
					Detail: "the service manager did not accept the " + verb + " request; nothing was performed"}
			}
			return helperschema.Response{Result: helperschema.ResultPerformed,
				Detail: "the service manager accepted the " + verb + " request; the host goes down under its control"}
		},
	}
}
