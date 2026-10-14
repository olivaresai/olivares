//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

func editionEmptyListsSayWhichEmptinessTheyMeanCases() []EmptyListsSayWhichEmptinessTheyMeanCase {
	return []EmptyListsSayWhichEmptinessTheyMeanCase{
		{"accessmap-graph", []string{"accessmap", "graph"}, "no access edges match this query"},
	}
}

func editionIdentityPostureTellsOffApartFromUnmeasuredCases() []IdentityPostureTellsOffApartFromUnmeasuredCase {
	return nil
}

func editionPagingFlagsExistOnlyWhereTheEngineReadsThemCases() []PagingFlagsExistOnlyWhereTheEngineReadsThemCase {
	return []PagingFlagsExistOnlyWhereTheEngineReadsThemCase{
		{"olivares accessmap attack-paths escalation", false, false},
		{"olivares accessmap attack-paths exfil", false, false},
		{"olivares accessmap attack-paths reachability", false, false},
		{"olivares accessmap attack-paths summary", false, false},
		{"olivares accessmap drift", true, true},
		{"olivares accessmap graph", true, true},
		{"olivares accessmap neighbors", false, false},
	}
}

func editionTheAllowListsAcceptWhatTheEngineAcceptsCases() []TheAllowListsAcceptWhatTheEngineAcceptsCase {
	return []TheAllowListsAcceptWhatTheEngineAcceptsCase{
		{"accessmap-direction-incoming", []string{"accessmap", "neighbors", "--id", "ag-7", "--direction", "incoming"}},
	}
}

func editionLocalAllowListsRefuseWithoutSpendingARequestCases() []LocalAllowListsRefuseWithoutSpendingARequestCase {
	return []LocalAllowListsRefuseWithoutSpendingARequestCase{
		{"accessmap-direction", []string{"accessmap", "neighbors", "--id", "ag-7", "--direction", "sideways"},
			"a traversal direction that does not exist"},
	}
}

func editionPositionalIDsCannotRetargetTheRequestCases(evil string) []PositionalIDsCannotRetargetTheRequestCase {
	return nil
}

func editionRoutesWithARequiredParameterRefuseBeforeTheRequestCases() []RoutesWithARequiredParameterRefuseBeforeTheRequestCase {
	return []RoutesWithARequiredParameterRefuseBeforeTheRequestCase{
		{
			"accessmap-neighbors-id",
			[]string{"accessmap", "neighbors"},
			[]string{"accessmap", "neighbors", "--id", "ag-7"},
		},
		{
			"accessmap-reachability-agent-id",
			[]string{"accessmap", "attack-paths", "reachability"},
			[]string{"accessmap", "attack-paths", "reachability", "--agent-id", "ag-7"},
		},
		{
			"accessmap-escalation-agent-id",
			[]string{"accessmap", "attack-paths", "escalation"},
			[]string{"accessmap", "attack-paths", "escalation", "--agent-id", "ag-7"},
		},
		{
			"accessmap-exfil-resource-id",
			[]string{"accessmap", "attack-paths", "exfil"},
			[]string{"accessmap", "attack-paths", "exfil", "--resource-id", "res-3"},
		},
	}
}

func editionLaneReadVerbs() []laneReadVerb {
	return []laneReadVerb{{"accessmap", []string{"accessmap", "graph"}, "/v1/m/accessmap/graph"}}
}

func editionPagingCounts() pagingCounts {
	if thisEdition.name == "enterprise" {
		return pagingCounts{107, 21, 23}
	}
	return pagingCounts{105, 21, 23}
}

func editionIdentityVerbCount() int { return 1 }

func editionLaneVerbCounts() map[string]int { return map[string]int{"accessmap": 7} }

func editionLaneTotal() int {
	if thisEdition.name == "enterprise" {
		return 73
	}
	return 71
}

func editionVerEngineRoutes() []string {
	return []string{
		"GET /v1/m/accessmap/graph",
		"GET /v1/m/accessmap/neighbors",
		"GET /v1/m/accessmap/drift",
		"GET /v1/m/accessmap/attack-paths/reachability",
		"GET /v1/m/accessmap/attack-paths/escalation",
		"GET /v1/m/accessmap/attack-paths/exfil",
		"GET /v1/m/accessmap/attack-paths/summary",
	}
}

func editionVerLeaves() []verLeaf {
	return []verLeaf{
		{[]string{"accessmap", "attack-paths", "escalation", "--agent-id", "ag-7"}, "GET /v1/m/accessmap/attack-paths/escalation"},
		{[]string{"accessmap", "attack-paths", "exfil", "--resource-id", "r-1"}, "GET /v1/m/accessmap/attack-paths/exfil"},
		{[]string{"accessmap", "attack-paths", "reachability", "--agent-id", "ag-7"}, "GET /v1/m/accessmap/attack-paths/reachability"},
		{[]string{"accessmap", "attack-paths", "summary"}, "GET /v1/m/accessmap/attack-paths/summary"},
		{[]string{"accessmap", "drift"}, "GET /v1/m/accessmap/drift"},
		{[]string{"accessmap", "graph"}, "GET /v1/m/accessmap/graph"},
		{[]string{"accessmap", "neighbors", "--id", "ag-7"}, "GET /v1/m/accessmap/neighbors"},
	}
}

func editionVerCount() int {
	if thisEdition.name == "enterprise" {
		return 70
	}
	return 68
}
