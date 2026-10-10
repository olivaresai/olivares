// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.io.ByteArrayInputStream;
import java.io.InputStream;
import java.io.UncheckedIOException;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpHeaders;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.Stream;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

class ClientRedirectTest {
    private record Seen(String method, String auth, String tenant, String body) {
    }

    @TestFactory
    Stream<DynamicTest> defaultRedirectRefusalCompatibility() {
        return Stream.of(301, 302, 303, 307, 308).flatMap(status ->
                Stream.of("same_origin", "other_port", "localhost_vs_127").map(destination ->
                        DynamicTest.dynamicTest(status + "/" + destination,
                                () -> redirect(status, destination, null))));
    }

    @TestFactory
    Stream<DynamicTest> suppliedClientRedirectCompatibility() {
        HttpClient supplied = HttpClient.newBuilder()
                .followRedirects(HttpClient.Redirect.ALWAYS)
                .connectTimeout(Duration.ofSeconds(2)).build();
        return Stream.of(301, 302, 303, 307, 308).flatMap(status ->
                Stream.of("same_origin", "other_port", "localhost_vs_127").map(destination ->
                        DynamicTest.dynamicTest(status + "/" + destination,
                                () -> redirect(status, destination, supplied))));
    }

    private void redirect(int status, String destination, HttpClient supplied) throws IOException {
        List<Seen> seen = Collections.synchronizedList(new ArrayList<>());
        HttpHandler answer = ex -> {
            record(ex, seen);
            send(ex, 200, "{\"redirected\":true}");
        };
        HttpServer target = start(answer);
        String newOrigin = address(target);
        if (destination.equals("localhost_vs_127")) {
            newOrigin = newOrigin.replace("127.0.0.1", "localhost");
        }
        String location = destination.equals("same_origin")
                ? "/destination" : newOrigin + "/destination";
        HttpServer source = start(ex -> {
            if (ex.getRequestURI().getPath().equals("/destination")) {
                answer.handle(ex);
                return;
            }
            record(ex, seen);
            ex.getResponseHeaders().set("Location", location);
            send(ex, status, "");
        });
        try {
            ClientOptions.Builder options = ClientOptions.builder().endpoint(address(source))
                    .token("synthetic-client-token").tenant("synthetic-tenant")
                    .timeout(Duration.ofSeconds(2));
            if (supplied != null) {
                options.httpClient(supplied);
            }
            Client client = new Client(options.build());
            if (supplied == null) {
                OlivaresApiException error = assertThrows(OlivaresApiException.class,
                        () -> client.postV1AuthLogin(Map.of("sentinel", "synthetic-body")));
                assertEquals(status, error.getStatus());
                assertEquals(1, seen.size(), "default redirect refusal must not replay a body");
            } else {
                if (destination.equals("same_origin")) {
                    assertEquals(true,
                            client.postV1AuthLogin(Map.of("sentinel", "synthetic-body")).get("redirected"));
                } else {
                    IllegalStateException error = assertThrows(IllegalStateException.class,
                            () -> client.postV1AuthLogin(Map.of("sentinel", "synthetic-body")));
                    assertEquals("olivares: the server redirected to " + newOrigin
                            + "; set the client's base URL to it", error.getMessage());
                }
                // The supplied client's policy still applies before the SDK sees
                // its response. This is final-answer refusal, not pre-send proof.
                assertEquals(2, seen.size());
                Seen redirected = seen.get(1);
                if (destination.equals("same_origin")) {
                    assertEquals("Bearer synthetic-client-token", redirected.auth());
                    assertEquals("synthetic-tenant", redirected.tenant());
                }
                assertEquals(status < 307 ? "GET" : "POST", redirected.method());
                if (status < 307) {
                    assertEquals("", redirected.body());
                } else {
                    assertTrue(redirected.body().contains("synthetic-body"));
                }
            }
            assertEquals("Bearer synthetic-client-token", seen.get(0).auth());
            assertEquals("synthetic-tenant", seen.get(0).tenant());
            assertTrue(seen.get(0).body().contains("synthetic-body"));
        } finally {
            source.stop(0);
            target.stop(0);
        }
    }

    private record FinalUriCase(String name, String endpoint, String finalUri, String refusedOrigin) {
    }

    @TestFactory
    Stream<DynamicTest> suppliedSubclassFinalUriAndBodyOwnership() {
        return Stream.of(
                new FinalUriCase("same_origin_case_and_default_port", "https://engine.example",
                        "HTTPS://ENGINE.EXAMPLE:443/destination", null),
                new FinalUriCase("http_to_https", "http://engine.example",
                        "https://engine.example/destination", "https://engine.example"),
                new FinalUriCase("other_port_error_not_retried", "https://engine.example",
                        "https://engine.example:444/destination", "https://engine.example:444"),
                new FinalUriCase("other_host_origin_only_diagnostic", "https://engine.example",
                        "https://synthetic-user:synthetic-password@other.example/destination?synthetic-secret#fragment",
                        "https://other.example"),
                new FinalUriCase("same_ipv6_origin", "https://[::1]",
                        "https://[0:0:0:0:0:0:0:1]:443/destination", null),
                new FinalUriCase("same_named_zone_without_local_interface", "http://[FE80::1%CENPKG_NoLocalInterface]",
                        "http://[fe80:0:0:0:0:0:0:1%CENPKG_NoLocalInterface]:80/destination", null),
                new FinalUriCase("different_zone_case", "http://[fe80::1%CENPKG_NoLocalInterface]",
                        "http://[fe80::1%cenpkg_nolocalinterface]/destination", "http://[fe80:0:0:0:0:0:0:1%cenpkg_nolocalinterface]"),
                new FinalUriCase("different_zone_name", "http://[fe80::1%CENPKG_NoLocalInterface]",
                        "http://[fe80::1%CENPKG_OtherInterface]/destination", "http://[fe80:0:0:0:0:0:0:1%CENPKG_OtherInterface]"),
                new FinalUriCase("same_numeric_zone", "http://[fe80::1%7]",
                        "http://[fe80:0:0:0:0:0:0:1%7]:80/destination", null),
                new FinalUriCase("different_numeric_zone", "http://[fe80::1%7]",
                        "http://[fe80::1%8]/destination", "http://[fe80:0:0:0:0:0:0:1%8]"),
                new FinalUriCase("same_ipv4_mapped_ipv6_origin", "http://[::ffff:127.0.0.1]",
                        "http://[0:0:0:0:0:ffff:7f00:1]:80/destination", null),
                new FinalUriCase("ipv4_is_not_ipv4_mapped_ipv6", "http://[::ffff:127.0.0.1]",
                        "http://127.0.0.1/destination", "http://127.0.0.1"))
                .map(c -> DynamicTest.dynamicTest(c.name(), () -> {
                    AtomicInteger reads = new AtomicInteger();
                    AtomicInteger closes = new AtomicInteger();
                    InputStream body = new ByteArrayInputStream("synthetic-answer".getBytes(StandardCharsets.UTF_8)) {
                        @Override public synchronized int read(byte[] bytes, int offset, int length) {
                            reads.incrementAndGet();
                            return super.read(bytes, offset, length);
                        }
                        @Override public void close() throws IOException {
                            closes.incrementAndGet();
                            super.close();
                        }
                    };
                    int status = c.name().equals("other_port_error_not_retried") ? 503 : 200;
                    StubHttpClient supplied = new StubHttpClient(new Answer(URI.create(c.finalUri()), body, status));
                    Client client = new Client(ClientOptions.builder().endpoint(c.endpoint())
                            .httpClient(supplied).retrySleep(ms -> {
                                throw new AssertionError("origin refusal must not retry");
                            }).build());
                    if (c.refusedOrigin() == null) {
                        assertEquals("synthetic-answer", client.getMetrics());
                        assertTrue(reads.get() > 0);
                    } else {
                        IllegalStateException error = assertThrows(IllegalStateException.class, client::getMetrics);
                        assertEquals("olivares: the server redirected to " + c.refusedOrigin()
                                + "; set the client's base URL to it", error.getMessage());
                        assertEquals(0, reads.get(), "refused final body must not be consumed");
                    }
                    assertEquals(1, supplied.calls.get(), "preserve the supplied subclass's send method");
                    assertEquals(1, closes.get(), "the SDK owns closing the response body");
                }));
    }

    private record Answer(URI uri, InputStream body, int statusCode) implements HttpResponse<InputStream> {
        @Override public HttpRequest request() { return HttpRequest.newBuilder(uri).build(); }
        @Override public Optional<HttpResponse<InputStream>> previousResponse() { return Optional.empty(); }
        @Override public HttpHeaders headers() { return HttpHeaders.of(Map.of(), (name, value) -> true); }
        @Override public Optional<javax.net.ssl.SSLSession> sslSession() { return Optional.empty(); }
        @Override public HttpClient.Version version() { return HttpClient.Version.HTTP_1_1; }
    }

    // Getters deliberately fail: copying configuration or inspecting redirect
    // policy cannot preserve an arbitrary supplied client's send behavior.
    private static class StubHttpClient extends HttpClient {
        private final HttpResponse<InputStream> response;
        final AtomicInteger calls = new AtomicInteger();
        StubHttpClient(HttpResponse<InputStream> response) { this.response = response; }
        @Override @SuppressWarnings("unchecked")
        public <T> HttpResponse<T> send(HttpRequest request, HttpResponse.BodyHandler<T> handler) {
            calls.incrementAndGet();
            return (HttpResponse<T>) response; // SDK requests BodyHandlers.ofInputStream().
        }
        @Override public Optional<java.net.CookieHandler> cookieHandler() { throw new AssertionError("no clone"); }
        @Override public Optional<Duration> connectTimeout() { throw new AssertionError("no clone"); }
        @Override public Redirect followRedirects() { throw new AssertionError("no policy rejection"); }
        @Override public Optional<java.net.ProxySelector> proxy() { throw new AssertionError("no clone"); }
        @Override public javax.net.ssl.SSLContext sslContext() { throw new AssertionError("no clone"); }
        @Override public javax.net.ssl.SSLParameters sslParameters() { throw new AssertionError("no clone"); }
        @Override public Optional<java.net.Authenticator> authenticator() { throw new AssertionError("no clone"); }
        @Override public Version version() { throw new AssertionError("no clone"); }
        @Override public Optional<java.util.concurrent.Executor> executor() { throw new AssertionError("no clone"); }
        @Override public <T> java.util.concurrent.CompletableFuture<HttpResponse<T>> sendAsync(
                HttpRequest request, HttpResponse.BodyHandler<T> handler) { throw new AssertionError("no async replacement"); }
        @Override public <T> java.util.concurrent.CompletableFuture<HttpResponse<T>> sendAsync(HttpRequest request,
                HttpResponse.BodyHandler<T> handler, HttpResponse.PushPromiseHandler<T> push) {
            throw new AssertionError("no async replacement");
        }
    }

    @TestFactory
    Stream<DynamicTest> truncatedBodiesAreTransportFailuresWithoutRetries() {
        return Stream.of(200, 429, 503).map(status ->
                DynamicTest.dynamicTest(Integer.toString(status), () -> {
                    AtomicInteger calls = new AtomicInteger();
                    HttpServer server = start(ex -> {
                        calls.incrementAndGet();
                        byte[] body = "{\"error\":{\"code\":\"busy\",\"message\":\"try later\"}}"
                                .getBytes(StandardCharsets.UTF_8);
                        ex.getResponseHeaders().set("Connection", "close");
                        ex.sendResponseHeaders(status, body.length + 1);
                        try {
                            ex.getResponseBody().write(body);
                        } finally {
                            ex.close();
                        }
                    });
                    try {
                        Client client = new Client(ClientOptions.builder().endpoint(address(server))
                                .token("synthetic-client-token").timeout(Duration.ofSeconds(2))
                                .retrySleep(ms -> {
                                    throw new AssertionError("short response body must not be retried");
                                }).build());
                        assertThrows(UncheckedIOException.class, client::getMetrics);
                        assertEquals(1, calls.get());
                    } finally {
                        server.stop(0);
                    }
                }));
    }

    private static HttpServer start(HttpHandler handler) throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", handler);
        server.start();
        return server;
    }

    private static String address(HttpServer server) {
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    private static void record(HttpExchange ex, List<Seen> seen) throws IOException {
        seen.add(new Seen(ex.getRequestMethod(), Objects.toString(ex.getRequestHeaders().getFirst("Authorization"), ""),
                Objects.toString(ex.getRequestHeaders().getFirst("X-Olivares-Tenant"), ""),
                new String(ex.getRequestBody().readAllBytes(), StandardCharsets.UTF_8)));
    }

    private static void send(HttpExchange ex, int status, String text) throws IOException {
        byte[] bytes = text.getBytes(StandardCharsets.UTF_8);
        ex.sendResponseHeaders(status, bytes.length == 0 ? -1 : bytes.length);
        try {
            if (bytes.length != 0) {
                ex.getResponseBody().write(bytes);
            }
        } finally {
            ex.close();
        }
    }
}
