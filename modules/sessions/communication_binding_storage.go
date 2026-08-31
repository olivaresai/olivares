// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "github.com/olivaresai/olivares/core/store"

// protocolBindingSchemaInvariants pins the K5 database guards that protect
// immutable spec generations, monotonic binding settlement, and hard-delete
// refusal even when a writer bypasses the Go service.
func protocolBindingSchemaInvariants() map[store.Engine][]store.SchemaTrigger {
	return map[store.Engine][]store.SchemaTrigger{
		store.EnginePostgres: {
			{
				Name: "sessions_communication_binding_spec_guard", Table: protocolBindingSpecTable,
				DefinitionSHA256: "ced05e725b95f2068d31e884c703c2f7860ece0a279419087a4a5c27a7e89ace",
			},
			{
				Name: "sessions_communication_binding_guard", Table: protocolBindingTable,
				DefinitionSHA256: "451edeadd34a7254a94c219aa847d122b507db3ad3b40cffa01bcb9601732f61",
			},
			{
				Name: "sessions_communication_binding_spec_no_delete", Table: protocolBindingSpecTable,
				DefinitionSHA256: "2d864f9fcbb4266e7749dd41a4654cf73e4e9cc4a2b89b28d83ad837b4e92546",
			},
			{
				Name: "sessions_communication_binding_no_delete", Table: protocolBindingTable,
				DefinitionSHA256: "40b4d13f9c9f5c6bc7b12fc01ca0fc61f9a40042ea90ad2252faf218e3ead529",
			},
		},
		store.EngineSQLite: {
			{
				Name: "sessions_communication_binding_spec_guard_ins", Table: protocolBindingSpecTable,
				DefinitionSHA256: "073f5f92b9c063fac226343de82b7c32bf290b4c6feb62106ef2cb9340e2a843",
			},
			{
				Name: "sessions_communication_binding_spec_guard_upd", Table: protocolBindingSpecTable,
				DefinitionSHA256: "c3141a9eac0352b6cab7a3a25c6677a1d6e3195c000723e1ee738676a439e573",
			},
			{
				Name: "sessions_communication_binding_guard_ins", Table: protocolBindingTable,
				DefinitionSHA256: "a8342f44ab73d858cd46c6ce08959ff6b54bcac6d60cb815fcb4b7115b74c00b",
			},
			{
				Name: "sessions_communication_binding_guard_upd", Table: protocolBindingTable,
				DefinitionSHA256: "7237ccb1354a7bdf95f2c85de19ad19f907396497c15e75ae079159352a84ecb",
			},
			{
				Name: "sessions_communication_binding_spec_no_delete", Table: protocolBindingSpecTable,
				DefinitionSHA256: "b142f9a6c538f9d44b9ff80329ab2fb66c09f63439a3fa2af242543cd0f827ba",
			},
			{
				Name: "sessions_communication_binding_no_delete", Table: protocolBindingTable,
				DefinitionSHA256: "0b6f09a1b454f6732c8c8d995c0d3000ad6e6851a4c3da0b112146bf230627c9",
			},
		},
	}
}
