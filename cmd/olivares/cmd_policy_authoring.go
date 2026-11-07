// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"net/http"

	"github.com/spf13/cobra"
)

// The policy API owns validation for each linked kind. The CLI sends the same
// document as the console and adds no local list of permitted policy kinds.
func addPolicyAuthoringCommands(root *cobra.Command, flags *authClientFlags) {
	c := modelstackClient{flags: flags, base: "/v1/m/governance", family: "policy"}
	root.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{Use: "ls", Aliases: []string{"list"}, Short: "List governance policies", Long: "List this tenant's governance policies and their enabled state.", Example: "  olivares policy ls --all -o json", Target: modelstackTarget{Collection: "/policies"}, EmptyNote: "no governance policies in this tenant", Paginated: true, Columns: []modelstackColumn{{Header: "ID", Key: "id"}, {Header: "NAME", Key: "name"}, {Header: "KIND", Key: "kind"}, {Header: "ENABLED", Key: "enabled"}}}),
		newModelstackGetCmd(c, modelstackGetSpec{Use: "get <policy-id>", Short: "Read a governance policy", Long: "Read one governance policy with its complete specification.", Example: "  olivares policy get 018f2a10-0000-7000-8000-000000000001 -o json", Target: modelstackTarget{Collection: "/policies", IDs: 1}}),
		newModelstackWriteCmd(c, modelstackWriteSpec{ChangesData: "write", Use: "create", Short: "Create a governance policy", Long: "Create a policy from a JSON document. The engine validates its kind and specification.", Example: "  olivares policy create --data @policy.json", Method: http.MethodPost, Target: modelstackTarget{Collection: "/policies"}, Body: modelstackBodyRequired}),
		newModelstackWriteCmd(c, modelstackWriteSpec{ChangesData: "write", Use: "update <policy-id>", Short: "Replace a governance policy", Long: "Replace a policy from a JSON document. Its kind is immutable; enabled controls whether it applies.", Example: "  olivares policy update 018f2a10-0000-7000-8000-000000000001 --data @policy.json", Method: http.MethodPut, Target: modelstackTarget{Collection: "/policies", IDs: 1}, Body: modelstackBodyRequired}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{Use: "rm <policy-id>", Short: "Remove a governance policy", Long: "Remove one governance policy after confirmation. Its restrictions stop applying immediately.", Example: "  olivares policy rm 018f2a10-0000-7000-8000-000000000001 --yes", Target: modelstackTarget{Collection: "/policies", IDs: 1}, Noun: "governance policy", Blast: "the policy's restrictions will no longer apply"}),
	)
}
