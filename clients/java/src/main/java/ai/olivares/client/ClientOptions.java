// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

import java.net.http.HttpClient;
import java.time.Duration;
import java.util.function.Consumer;
import java.util.function.LongConsumer;

/**
 * Construction-time configuration for a {@link Client}. Build with
 * {@link #builder()}; only {@code endpoint} is required.
 *
 * <p>For custom TLS (e.g. the engine's self-signed default certificate), proxies
 * or timeouts, supply your own {@link HttpClient} via {@link Builder#httpClient}
 * — the SDK adds no TLS knobs of its own, keeping the transport injectable and
 * dependency-free.
 */
public final class ClientOptions {

    final String endpoint;
    final String token;
    final String tenant;
    final int maxRetries;
    final Duration timeout;
    final String userAgent;
    final HttpClient httpClient;
    final Consumer<DeprecationNotice> onDeprecation;
    final LongConsumer retrySleep;

    private ClientOptions(Builder b) {
        this.endpoint = b.endpoint;
        this.token = b.token;
        this.tenant = b.tenant;
        this.maxRetries = b.maxRetries;
        this.timeout = b.timeout;
        this.userAgent = b.userAgent;
        this.httpClient = b.httpClient;
        this.onDeprecation = b.onDeprecation;
        this.retrySleep = b.retrySleep;
    }

    public static Builder builder() {
        return new Builder();
    }

    /** Fluent builder for {@link ClientOptions}. */
    public static final class Builder {
        private String endpoint;
        private String token = "";
        private String tenant;
        private int maxRetries = 2;
        private Duration timeout = Duration.ofSeconds(30);
        private String userAgent;
        private HttpClient httpClient;
        private Consumer<DeprecationNotice> onDeprecation;
        private LongConsumer retrySleep;

        /** Absolute base URL, e.g. {@code https://olivares.example:8443} (required). */
        public Builder endpoint(String endpoint) {
            this.endpoint = endpoint;
            return this;
        }

        /** Opaque bearer token ({@code olvs_} session or {@code olvk_} API key). */
        public Builder token(String token) {
            this.token = token == null ? "" : token;
            return this;
        }

        /** Default {@code X-Olivares-Tenant}; override per call via {@link RequestOptions}. */
        public Builder tenant(String tenant) {
            this.tenant = tenant;
            return this;
        }

        /** Retries for retryable statuses (429 always, 503 for GET). 0 disables. Default 2. */
        public Builder maxRetries(int maxRetries) {
            this.maxRetries = maxRetries;
            return this;
        }

        /** Per-request timeout. Default 30s. */
        public Builder timeout(Duration timeout) {
            this.timeout = timeout;
            return this;
        }

        /** Prefixed to the SDK User-Agent (the SDK identity stays appended). */
        public Builder userAgent(String userAgent) {
            this.userAgent = userAgent;
            return this;
        }

        /** Replace the transport (custom TLS, proxies, test fakes). */
        public Builder httpClient(HttpClient httpClient) {
            this.httpClient = httpClient;
            return this;
        }

        /** Replaces the default deprecation handler (a WARNING log); runs at most
         *  once per METHOD+endpoint per client. */
        public Builder onDeprecation(Consumer<DeprecationNotice> onDeprecation) {
            this.onDeprecation = onDeprecation;
            return this;
        }

        /** Retry wait seam, in milliseconds (tests inject a recorder). */
        public Builder retrySleep(LongConsumer retrySleep) {
            this.retrySleep = retrySleep;
            return this;
        }

        public ClientOptions build() {
            return new ClientOptions(this);
        }
    }
}
