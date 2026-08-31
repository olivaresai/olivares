// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.sun.net.httpserver.Headers;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.jupiter.api.Test;

/**
 * Integration tests for the Java client against a local fake control plane
 * ({@code com.sun.net.httpserver}, JDK built-in). Run from clients/java:
 * {@code mvn test} ({@code task sdk:test:java} from the repo root).
 *
 * <p>JUnit creates a fresh instance per test, so {@link #seen}/{@link #slept}
 * start empty each time; each test starts a focused server (Go-style) and stops
 * it in a finally block.
 */
class ClientTest {

    private record Seen(String method, String url, Headers headers, byte[] body) {
    }

    private final List<Seen> seen = Collections.synchronizedList(new ArrayList<>());
    private final List<Long> slept = Collections.synchronizedList(new ArrayList<>());

    // --- harness -------------------------------------------------------------

    private HttpServer start(HttpHandler handler) throws IOException {
        HttpServer s = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        s.createContext("/", handler);
        s.start();
        return s;
    }

    private Client client(HttpServer s) {
        return client(s, b -> {
        });
    }

    private Client client(HttpServer s, Consumer<ClientOptions.Builder> extra) {
        ClientOptions.Builder b = ClientOptions.builder()
                .endpoint("http://127.0.0.1:" + s.getAddress().getPort())
                .token("olvk_test_secret")
                .tenant("t-default")
                .retrySleep(slept::add);
        extra.accept(b);
        return new Client(b.build());
    }

    private void record(HttpExchange ex) throws IOException {
        String url = ex.getRequestURI().getRawPath();
        String q = ex.getRequestURI().getRawQuery();
        if (q != null) {
            url += "?" + q;
        }
        Headers copy = new Headers();
        copy.putAll(ex.getRequestHeaders());
        seen.add(new Seen(ex.getRequestMethod(), url, copy, ex.getRequestBody().readAllBytes()));
    }

    private static void json(HttpExchange ex, int status, String body, String... headers) throws IOException {
        ex.getResponseHeaders().add("Content-Type", "application/json");
        send(ex, status, body, headers);
    }

    private static void send(HttpExchange ex, int status, String body, String... headers) throws IOException {
        Headers h = ex.getResponseHeaders();
        for (int i = 0; i + 1 < headers.length; i += 2) {
            h.add(headers[i], headers[i + 1]);
        }
        byte[] b = body.getBytes(StandardCharsets.UTF_8);
        if (b.length == 0) {
            ex.sendResponseHeaders(status, -1);
        } else {
            ex.sendResponseHeaders(status, b.length);
            try (OutputStream os = ex.getResponseBody()) {
                os.write(b);
            }
        }
        ex.close();
    }

    // --- request shape -------------------------------------------------------

    /**
     * The API has repeatable query parameters (GET /v1/audit's exclude_action is the
     * first). {@code query} replaces, so it cannot express one; {@code queryAdd}
     * appends, and both occurrences have to reach the wire — a builder that kept only
     * the last would drop every filter after the first, silently.
     */
    @Test
    void repeatsAQueryParameterAddedMoreThanOnce() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 200, "{\"items\":[],\"has_more\":false}");
        });
        try {
            Client c = client(s);
            c.getV1Agents(RequestOptions.builder()
                    .query("limit", "5")
                    .queryAdd("tag", "a")
                    .queryAdd("tag", "b")
                    .build());
            assertEquals("/v1/agents?limit=5&tag=a&tag=b", seen.get(0).url());
        } finally {
            s.stop(0);
        }
    }

    @Test
    void sendsAuthTenantOverrideUserAgentAndQuery() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 200, "{\"items\":[{\"id\":\"a\"},{\"id\":\"b\"}],\"has_more\":false}");
        });
        try {
            Client c = client(s);
            Map<String, Object> out = c.getV1Agents(
                    RequestOptions.builder().query("limit", "5").tenant("t-override").build());
            List<String> ids = new ArrayList<>();
            for (Object it : (List<?>) out.get("items")) {
                ids.add((String) ((Map<?, ?>) it).get("id"));
            }
            assertEquals(List.of("a", "b"), ids);

            Seen r = seen.get(0);
            assertEquals("/v1/agents?limit=5", r.url());
            assertEquals("Bearer olvk_test_secret", r.headers().getFirst("Authorization"));
            assertEquals("t-override", r.headers().getFirst("X-Olivares-Tenant"));
            String ua = r.headers().getFirst("User-Agent");
            assertTrue(ua.startsWith("olivares-client-java/"), ua);
            assertTrue(ua.contains("(api " + ApiMetadata.API_VERSION + ")"), ua);
        } finally {
            s.stop(0);
        }
    }

    @Test
    void preservesDeclaredRawRequestContentType() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 200, "{}");
        });
        try {
            Client c = client(s);
            c.doReqRawWithType("POST", "/memory/import", "/memory/import",
                    "{}\n".getBytes(StandardCharsets.UTF_8), "application/x-ndjson",
                    RequestOptions.NONE);
            c.doReqRaw("PUT", "/files/raw", "/files/raw",
                    "raw".getBytes(StandardCharsets.UTF_8), RequestOptions.NONE);
            c.doReqRawWithType("POST", "/memory/import", "/memory/import", null,
                    "application/x-ndjson", RequestOptions.NONE);

            assertEquals("application/x-ndjson",
                    seen.get(0).headers().getFirst("Content-Type"));
            assertEquals("application/octet-stream",
                    seen.get(1).headers().getFirst("Content-Type"));
            assertNull(seen.get(2).headers().getFirst("Content-Type"));
        } finally {
            s.stop(0);
        }
    }

    @Test
    void sendsRequiredJsonNullButOmitsOptionalNull() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 200, "{}");
        });
        try {
            Client c = client(s);
            c.doJsonRequired("POST", "/required", "/required", null, RequestOptions.NONE);
            c.doJson("POST", "/optional", "/optional", null, RequestOptions.NONE);

            assertEquals("null", new String(seen.get(0).body(), StandardCharsets.UTF_8),
                    "required null must be a present JSON body");
            assertEquals("application/json",
                    seen.get(0).headers().getFirst("Content-Type"));
            assertEquals("", new String(seen.get(1).body(), StandardCharsets.UTF_8),
                    "optional null must preserve legacy body omission");
            assertNull(seen.get(1).headers().getFirst("Content-Type"));
        } finally {
            s.stop(0);
        }
    }

    @Test
    void escapesPathParamsInGeneratedOperations() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 404, "{\"error\":{\"code\":\"not_found\",\"message\":\"no\"}}");
        });
        try {
            Client c = client(s);
            assertThrows(OlivaresApiException.class, () -> c.getV1AgentsById("a/b c"));
            assertEquals("/v1/agents/a%2Fb%20c", seen.get(0).url());
        } finally {
            s.stop(0);
        }
    }

    // --- error envelope ------------------------------------------------------

    @Test
    void mapsTheSingleEnvelopeToApiException() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 404, "{\"error\":{\"code\":\"not_found\",\"message\":\"no such agent\"}}",
                    "X-Request-ID", "req-42");
        });
        try {
            Client c = client(s);
            OlivaresApiException e = assertThrows(OlivaresApiException.class,
                    () -> c.getV1AgentsById("missing"));
            assertEquals(404, e.getStatus());
            assertEquals("not_found", e.getCode());
            assertEquals("no such agent", e.getApiMessage());
            assertEquals("req-42", e.getRequestId());
            assertTrue(e.getMessage().contains("no such agent"));
        } finally {
            s.stop(0);
        }
    }

    @Test
    void keepsRawTextWhenErrorBodyIsNotTheEnvelope() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            send(ex, 502, "upstream exploded");
        });
        try {
            Client c = client(s);
            OlivaresApiException e = assertThrows(OlivaresApiException.class, () -> c.getV1Agents());
            assertEquals(502, e.getStatus());
            assertEquals("http_502", e.getCode());
            assertEquals("upstream exploded", e.getApiMessage());
        } finally {
            s.stop(0);
        }
    }

    // --- retry policy --------------------------------------------------------

    @Test
    void retries429HonouringRetryAfterAsLowerBound() throws IOException {
        AtomicInteger calls = new AtomicInteger();
        HttpServer s = start(ex -> {
            record(ex);
            if (calls.incrementAndGet() == 1) {
                json(ex, 429, "{\"error\":{\"code\":\"rate_limited\",\"message\":\"slow down\"}}",
                        "Retry-After", "3");
            } else {
                json(ex, 201, "{\"ok\":true}");
            }
        });
        try {
            Client c = client(s);
            Map<String, Object> out = c.postV1Tokens(Map.of("name", "ci"));
            assertEquals(Boolean.TRUE, out.get("ok"));
            assertEquals(2, calls.get());
            assertEquals(List.of(3000L), slept);
        } finally {
            s.stop(0);
        }
    }

    @Test
    void exhaustsTheRetryBudgetThenSurfaces() throws IOException {
        AtomicInteger calls = new AtomicInteger();
        HttpServer s = start(ex -> {
            record(ex);
            calls.incrementAndGet();
            json(ex, 429, "{\"error\":{\"code\":\"rate_limited\",\"message\":\"still\"}}");
        });
        try {
            Client c = client(s); // default maxRetries = 2
            OlivaresApiException e = assertThrows(OlivaresApiException.class, () -> c.getV1Agents());
            assertEquals("rate_limited", e.getCode());
            assertEquals(3, calls.get()); // initial + 2 retries
        } finally {
            s.stop(0);
        }
    }

    @Test
    void retries503OnlyForGet() throws IOException {
        AtomicInteger calls = new AtomicInteger();
        HttpServer s = start(ex -> {
            record(ex);
            if (calls.incrementAndGet() == 1) {
                json(ex, 503, "{\"error\":{\"code\":\"not_leader\",\"message\":\"handoff\"}}");
            } else {
                json(ex, 200, "{}");
            }
        });
        try {
            // GET retries the 503 handoff and succeeds.
            client(s).getV1ServerInfo();
            assertEquals(2, calls.get());

            // POST must NOT retry a 503 (not known idempotent).
            calls.set(0);
            OlivaresApiException e = assertThrows(OlivaresApiException.class,
                    () -> client(s).postV1Memberships(Map.of()));
            assertEquals(503, e.getStatus());
            assertEquals(1, calls.get());
        } finally {
            s.stop(0);
        }
    }

    @Test
    void neverRetriesA400() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 400, "{\"error\":{\"code\":\"bad_request\",\"message\":\"nope\"}}");
        });
        try {
            Client c = client(s);
            assertThrows(OlivaresApiException.class, () -> c.postV1Memberships(Map.of()));
            assertEquals(1, seen.size());
        } finally {
            s.stop(0);
        }
    }

    // --- stability policy signal ---------------------------------------------

    @Test
    void surfacesDeprecationOncePerEndpoint() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 200, "{}",
                    "Deprecation", "@1780272000",
                    "Sunset", "Thu, 01 Jun 2028 00:00:00 GMT",
                    "Link", "<https://docs.olivares.invalid/how-to/migrate-example/>; rel=\"deprecation\"",
                    "Link", "<https://docs.olivares.invalid/how-to/migrate-example/>; rel=\"sunset\"");
        });
        try {
            List<DeprecationNotice> notices = new ArrayList<>();
            Client c = client(s, b -> b.onDeprecation(notices::add));
            for (int i = 0; i < 3; i++) {
                c.getV1ServerInfo();
            }
            c.getHealthz();
            assertEquals(2, notices.size()); // deduped per endpoint: server-info + healthz
            DeprecationNotice n = notices.get(0);
            assertEquals("GET", n.method());
            assertEquals("/v1/server-info", n.path());
            assertEquals("@1780272000", n.deprecation());
            assertEquals("Thu, 01 Jun 2028 00:00:00 GMT", n.sunset());
            assertEquals("https://docs.olivares.invalid/how-to/migrate-example/", n.link());
        } finally {
            s.stop(0);
        }
    }

    @Test
    void dedupsPerRouteTemplateNotPerResource() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 200, "{}", "Deprecation", "@1780272000");
        });
        try {
            List<DeprecationNotice> notices = new ArrayList<>();
            Client c = client(s, b -> b.onDeprecation(notices::add));
            c.deleteV1TokensById("tok_001");
            c.deleteV1TokensById("tok_002");
            assertEquals(1, notices.size());
            assertEquals("/v1/tokens/tok_001", notices.get(0).path());
        } finally {
            s.stop(0);
        }
    }

    @Test
    void extractsDeprecationLinkFromCoalescedHeader() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            // A proxy may coalesce repeated Link headers into one comma-joined value.
            json(ex, 200, "{}",
                    "Deprecation", "@1780272000",
                    "Link", "<https://cdn.example/x>; rel=\"preload\", "
                            + "<https://docs.olivares.invalid/how-to/migrate-example/>; rel=\"deprecation\"");
        });
        try {
            List<DeprecationNotice> notices = new ArrayList<>();
            client(s, b -> b.onDeprecation(notices::add)).getHealthz();
            assertEquals(1, notices.size());
            assertEquals("https://docs.olivares.invalid/how-to/migrate-example/", notices.get(0).link());
        } finally {
            s.stop(0);
        }
    }

    // --- response shape ------------------------------------------------------

    @Test
    void returnsRawBodyForNonJsonOperations() throws IOException {
        String exposition = "# HELP olivares_requests_total Requests.\nolivares_requests_total 42\n";
        HttpServer s = start(ex -> {
            record(ex);
            send(ex, 200, exposition, "Content-Type", "text/plain; version=0.0.4; charset=utf-8");
        });
        try {
            String got = client(s).getMetrics();
            assertEquals(exposition, got);
            // A raw operation must not demand JSON.
            assertNotEquals("application/json", seen.get(0).headers().getFirst("Accept"));
        } finally {
            s.stop(0);
        }
    }

    @Test
    void rejectsA200WhoseBodyIsNotAJsonObject() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            json(ex, 200, "[1,2]"); // hostile shape: a JSON array, not an object
        });
        try {
            OlivaresApiException e = assertThrows(OlivaresApiException.class, () -> client(s).getV1Users());
            assertEquals("bad_response", e.getCode());
        } finally {
            s.stop(0);
        }
    }

    // --- security: redirects -------------------------------------------------

    @Test
    void refusesToFollowRedirects() throws IOException {
        HttpServer s = start(ex -> {
            record(ex);
            send(ex, 302, "", "Location", "http://attacker.example/v1/audit");
        });
        try {
            OlivaresApiException e = assertThrows(OlivaresApiException.class, () -> client(s).getV1Audit());
            assertEquals(302, e.getStatus());
            // The credentialed request must never follow to the foreign origin.
            assertEquals(1, seen.size());
        } finally {
            s.stop(0);
        }
    }

    // --- pagination ----------------------------------------------------------

    @Test
    void followsItemsCursorHasMoreEnvelope() throws IOException {
        HttpServer s = start(ex -> {
            String q = ex.getRequestURI().getQuery();
            record(ex);
            if (q != null && q.contains("cursor=c1")) {
                json(ex, 200, "{\"items\":[{\"id\":\"c\"}],\"has_more\":false}");
            } else {
                json(ex, 200, "{\"items\":[{\"id\":\"a\"},{\"id\":\"b\"}],\"cursor\":\"c1\",\"has_more\":true}");
            }
        });
        try {
            List<String> ids = new ArrayList<>();
            for (Map<String, Object> item : client(s).paginate("/v1/agents")) {
                ids.add((String) item.get("id"));
            }
            assertEquals(List.of("a", "b", "c"), ids);
        } finally {
            s.stop(0);
        }
    }

    // --- construction --------------------------------------------------------

    @Test
    void rejectsNonAbsoluteEndpoint() {
        for (String bad : new String[]{"", "not-a-url", "/relative"}) {
            assertThrows(IllegalArgumentException.class, () -> Client.of(bad, ""));
        }
    }
}
