// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

type EmptyListsSayWhichEmptinessTheyMeanCase struct {
	name string
	args []string
	want string
}

type IdentityPostureTellsOffApartFromUnmeasuredCase struct {
	name     string
	args     []string
	body     string
	wantExit int
	wantText string
}

type PagingFlagsExistOnlyWhereTheEngineReadsThemCase struct {
	path       string
	wantCursor bool
	wantLimit  bool
}

type TheAllowListsAcceptWhatTheEngineAcceptsCase struct {
	name string
	args []string
}

type LocalAllowListsRefuseWithoutSpendingARequestCase struct {
	name string
	args []string
	why  string
}

type PositionalIDsCannotRetargetTheRequestCase struct {
	name   string
	args   []string
	prefix string
}

type RoutesWithARequiredParameterRefuseBeforeTheRequestCase struct {
	name    string
	missing []string
	full    []string
}

type laneReadVerb struct {
	family string
	args   []string
	path   string
}

type pagingCounts struct{ rows, cursor, limit int }
