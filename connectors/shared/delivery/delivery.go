// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package delivery exposes the existing reliable HTTP transport to private connectors.
package delivery

import internal "github.com/olivaresai/olivares/connectors/internal/delivery"

type Doer = internal.Doer
type Options = internal.Options
type Client = internal.Client
type Request = internal.Request

func New(doer Doer, opts Options) *Client { return internal.New(doer, opts) }
