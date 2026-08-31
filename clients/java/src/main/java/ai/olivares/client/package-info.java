// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

/**
 * First-party Java client for the Olivares AI control plane REST API (the
 * published {@code /v1} contract).
 *
 * <p>The package has two layers:
 *
 * <ul>
 *   <li>A hand-written core ({@link ai.olivares.client.ClientCore}): endpoint/auth/
 *       tenancy wiring for the opaque bearer tokens ({@code olvs_}/{@code olvk_}),
 *       the single error envelope mapped to {@link ai.olivares.client.OlivaresApiException},
 *       cursor pagination, Retry-After-aware retries for rate-limited calls, and the
 *       stability policy's deprecation signal (RFC 9745 {@code Deprecation} / RFC 8594
 *       {@code Sunset} → {@link ai.olivares.client.DeprecationNotice}, once per endpoint),
 *       plus the dependency-free JSON codec ({@link ai.olivares.client.Json}).</li>
 *   <li>A generated operation layer ({@link ai.olivares.client.Client},
 *       {@link ai.olivares.client.ApiMetadata}), regenerated from the committed OpenAPI
 *       snapshots by {@code task sdk:generate} and drift-checked by {@code task sdk:check}.
 *       Operations represent published JSON schemas with generic values
 *       ({@code Object} / {@code Map}) and preserve raw request media types.</li>
 * </ul>
 *
 * <p>JDK standard library only ({@code java.net.http}); it never links the AGPL
 * engine. Governing policy:
 * <a href="https://olivares.ai/docs">api-stability</a>.
 */
package ai.olivares.client;
