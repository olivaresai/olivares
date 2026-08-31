// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Per-call options: query parameters and a tenant override. Immutable; build
 * with {@link #builder()}. Use {@link #NONE} for the default (no query, no
 * override) — the no-options overload of every operation passes it for you.
 */
public final class RequestOptions {

    /** Shared empty instance: no query parameters, no tenant override. */
    public static final RequestOptions NONE = builder().build();

    final Map<String, List<String>> query;
    final String tenant;

    private RequestOptions(Builder b) {
        // Insertion order is preserved so the encoded query string is deterministic.
        // The value is a LIST because the API has repeatable query parameters
        // (/v1/audit's exclude_action): one key can legitimately carry several
        // occurrences, and a Map<String,String> can only hold the last of them.
        Map<String, List<String>> copy = new LinkedHashMap<>();
        for (Map.Entry<String, List<String>> e : b.query.entrySet()) {
            copy.put(e.getKey(), List.copyOf(e.getValue()));
        }
        this.query = Collections.unmodifiableMap(copy);
        this.tenant = b.tenant;
    }

    public static Builder builder() {
        return new Builder();
    }

    /** Fluent builder for {@link RequestOptions}. */
    public static final class Builder {
        private final Map<String, List<String>> query = new LinkedHashMap<>();
        private String tenant;

        /** Add (or replace) one query parameter. */
        public Builder query(String key, String value) {
            this.query.put(key, new ArrayList<>(List.of(value)));
            return this;
        }

        /**
         * Add one more occurrence of a REPEATABLE query parameter, keeping any already
         * set for the same key. {@link #query(String, String)} replaces; this appends,
         * which is the only way to express a filter the API accepts more than once
         * (for example {@code exclude_action} on GET /v1/audit).
         */
        public Builder queryAdd(String key, String value) {
            this.query.computeIfAbsent(key, k -> new ArrayList<>()).add(value);
            return this;
        }

        /** Override the client's default tenant for this call. */
        public Builder tenant(String tenant) {
            this.tenant = tenant;
            return this;
        }

        public RequestOptions build() {
            return new RequestOptions(this);
        }
    }
}
