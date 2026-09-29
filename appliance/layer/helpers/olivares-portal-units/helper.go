// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

// actAdopted records whether the product's act authorization for the service verb is composed
// with this helper: its consumer, which verifies the act the product issued for this operation
// and journals it. Until it is, and this constant changes with it, every act (start, stop,
// restart, reload, enable, disable and logs) is refused with act_not_adopted, whoever asks, and
// neither the service manager nor the journal reader is reached. list and status change nothing
// and are unaffected.
const actAdopted = false

// dialer opens a connection to the service manager and returns its close.
type dialer func() (services.Bus, func(), error)

// helper is the units helper over dial and journal.
func helper(dial dialer, journal services.Runner) invocation.Helper {
	return helperWith(actAdopted, dial, journal)
}

// helperWith is helper with the adoption given, so that both sides of it can be measured.
func helperWith(adopted bool, dial dialer, journal services.Runner) invocation.Helper {
	return invocation.Helper{
		Name:       services.HelperName,
		Rules:      services.Rules(),
		NewRequest: func() helperschema.Request { return &services.Request{} },
		Perform: func(ctx context.Context, _ helperschema.Peer, request helperschema.Request) helperschema.Response {
			req, ok := request.(*services.Request)
			if !ok {
				return helperschema.Response{Result: helperschema.ResultRefused, Code: helperschema.CodeInputRefused,
					Detail: "not a document of this helper"}
			}
			if services.IsAct(req.Op) && !adopted {
				return helperschema.Response{Result: helperschema.ResultRefused, Code: services.CodeActNotAdopted,
					Detail: "the act authorization for the service verb is not composed with this helper; nothing was asked"}
			}
			bus, closeBus, err := dial()
			if err != nil {
				return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed,
					Detail: "the service manager's bus could not be reached; nothing was asked"}
			}
			defer closeBus()
			return services.Answer(ctx, services.Executor{Bus: bus, Journal: journal, JobWait: services.DefaultJobWait}, req)
		},
	}
}
