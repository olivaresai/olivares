// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package api

import "errors"

// ErrOrganizationsUnavailable marks organization management absent from this edition.
// The single organization created by setup, stored tenants and their exports remain available.
var ErrOrganizationsUnavailable = errors.New("api: organizations unavailable")
