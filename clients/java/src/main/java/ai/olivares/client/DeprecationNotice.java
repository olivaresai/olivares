// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

/**
 * One deprecated-endpoint signal, parsed from the stability policy's response
 * headers (RFC 9745 {@code Deprecation}, RFC 8594 {@code Sunset}, and the
 * {@code Link rel="deprecation"} migration-guide target). Surfaced at most once
 * per endpoint per client.
 *
 * @param method      the request method
 * @param path        the request path (concrete, not the route template)
 * @param deprecation the raw {@code Deprecation} value, e.g. {@code "@1780272000"}
 * @param sunset      the raw {@code Sunset} value (HTTP-date), or {@code ""} if unscheduled
 * @param link        the migration-guide URL from {@code Link rel="deprecation"}, or {@code ""}
 */
public record DeprecationNotice(String method, String path, String deprecation, String sunset, String link) {
}
