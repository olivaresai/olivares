// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package awssig exposes the existing connector signing primitives to private connectors.
package awssig

import internal "github.com/olivaresai/olivares/connectors/internal/awssig"

type Creds = internal.Creds

func URIEncode(s string, encodeSlash bool) string { return internal.URIEncode(s, encodeSlash) }
func HexSHA256(data []byte) string                { return internal.HexSHA256(data) }
