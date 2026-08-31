// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ai.olivares.client;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.UncheckedIOException;
import java.net.URI;
import java.net.URISyntaxException;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpHeaders;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.NoSuchElementException;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Consumer;
import java.util.function.LongConsumer;

/**
 * The hand-written transport core: endpoint/auth/tenancy wiring for the opaque
 * bearer tokens ({@code olvs_}/{@code olvk_}), the single error envelope mapped
 * to {@link OlivaresApiException}, cursor pagination ({@link #paginate}),
 * Retry-After-aware retries for rate-limited calls, and surfacing of the
 * stability policy's deprecation signal (RFC 9745 {@code Deprecation} / RFC 8594
 * {@code Sunset} response headers), once per endpoint.
 *
 * <p>JDK standard library only (the {@code java.net.http} client) — the SDK
 * never links the engine. Use {@link Client}, which adds one method per
 * published operation on top of this core.
 */
public abstract class ClientCore {

    /** The SDK's own semantic version. Pre-1.0 while the product is pre-1.0; from
     *  GA on, the MAJOR tracks the API contract major ({@link ApiMetadata#API_VERSION}). */
    public static final String VERSION = "0.1.0";

    private static final System.Logger LOG = System.getLogger("ai.olivares.client");
    private static final long BODY_LIMIT = 64L << 20; // 64 MiB, like the other SDKs
    private static final String UNRESERVED =
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~";
    private static final char[] HEX = "0123456789ABCDEF".toCharArray();

    private final String endpoint;
    private final String token;
    private final String tenant;
    private final int maxRetries;
    private final java.time.Duration timeout;
    private final String userAgent;
    private final HttpClient httpClient;
    private final Consumer<DeprecationNotice> onDeprecation;
    private final LongConsumer retrySleep;
    // One notice per "METHOD route" per client; thread-safe (a client may be shared).
    private final Set<String> depSeen = ConcurrentHashMap.newKeySet();

    protected ClientCore(ClientOptions options) {
        URI u;
        try {
            u = new URI(options.endpoint == null ? "" : options.endpoint);
        } catch (URISyntaxException e) {
            throw new IllegalArgumentException("endpoint must be an absolute URL: " + options.endpoint);
        }
        if (u.getScheme() == null || u.getHost() == null) {
            throw new IllegalArgumentException("endpoint must be an absolute URL: " + options.endpoint);
        }
        String ep = options.endpoint;
        if (ep.endsWith("/")) {
            ep = ep.substring(0, ep.length() - 1);
        }
        this.endpoint = ep;
        this.token = options.token;
        this.tenant = options.tenant;
        this.maxRetries = options.maxRetries;
        this.timeout = options.timeout;
        String ua = "olivares-client-java/" + VERSION + " (api " + ApiMetadata.API_VERSION + ")";
        this.userAgent = options.userAgent != null ? options.userAgent + " " + ua : ua;
        // A control plane does not 3xx its JSON endpoints; never follow redirects,
        // which would forward the bearer token + tenant header cross-origin. A 3xx
        // surfaces as an OlivaresApiException (code http_3xx) instead.
        this.httpClient = options.httpClient != null ? options.httpClient
                : HttpClient.newBuilder()
                        .followRedirects(HttpClient.Redirect.NEVER)
                        .connectTimeout(options.timeout)
                        .build();
        this.onDeprecation = options.onDeprecation != null ? options.onDeprecation
                : ClientCore::defaultDeprecationLog;
        this.retrySleep = options.retrySleep != null ? options.retrySleep : ClientCore::defaultSleep;
    }

    // --- request execution ---------------------------------------------------

    /**
     * One JSON operation. {@code route} is the spec path template (the
     * deprecation-dedup key, e.g. {@code /v1/agents/{id}}); {@code path} is the
     * concrete escaped request path; {@code body} is JSON-encoded (or {@code null}).
     */
    protected Map<String, Object> doJson(String method, String route, String path,
                                         Object body, RequestOptions options) {
        String raw = execute(method, route, path, true, body, null, false, options);
        return decodeJsonObject(raw, method, path);
    }

    /**
     * Execute an operation whose JSON request body is required. A {@code null}
     * input is serialized as the present JSON value {@code null}; the legacy
     * {@link #doJson} seam retains its null-means-absent behavior.
     */
    protected Map<String, Object> doJsonRequired(String method, String route, String path,
                                                 Object body, RequestOptions options) {
        String raw = execute(method, route, path, true, body, null, true, options);
        return decodeJsonObject(raw, method, path);
    }

    /** Compatibility seam for operation layers generated before request media
     * types were preserved. Those operations were octet-stream only. */
    protected Map<String, Object> doReqRaw(String method, String route, String path,
                                           byte[] body, RequestOptions options) {
        return doReqRawWithType(method, route, path, body, "application/octet-stream", options);
    }

    /** Send raw request bytes under the operation's exact declared media type. */
    protected Map<String, Object> doReqRawWithType(String method, String route, String path,
                                                    byte[] body, String contentType,
                                                    RequestOptions options) {
        String raw = execute(method, route, path, true, body, contentType, false, options);
        return decodeJsonObject(raw, method, path);
    }

    /**
     * An operation whose success body is NOT JSON (e.g. {@code /metrics}, an audit
     * export, an SSE stream); errors still arrive in the JSON envelope and throw
     * {@link OlivaresApiException}. Returned as UTF-8 text.
     */
    protected String doRaw(String method, String route, String path, RequestOptions options) {
        return execute(method, route, path, false, null, null, false, options);
    }

    private Map<String, Object> decodeJsonObject(String raw, String method, String path) {
        if (raw.isBlank()) {
            return Collections.emptyMap();
        }
        Object parsed;
        try {
            parsed = Json.parse(raw);
        } catch (Json.JsonException e) {
            throw new OlivaresApiException(0, "bad_response",
                    "response is not a JSON object (" + method + " " + path + ")", "", 0);
        }
        if (!(parsed instanceof Map)) {
            throw new OlivaresApiException(0, "bad_response",
                    "response is not a JSON object (" + method + " " + path + ")", "", 0);
        }
        @SuppressWarnings("unchecked")
        Map<String, Object> out = (Map<String, Object>) parsed;
        return out;
    }

    /**
     * The policy-aware retry loop: 429 is always retryable (the limiter rejects
     * before execution and {@code Retry-After} is a safe lower bound), 503 only
     * for GET (the not_leader HA handoff — idempotent reads only). Everything
     * else surfaces immediately. Transport failures ({@link UncheckedIOException})
     * are not retried.
     */
    private String execute(String method, String route, String path, boolean wantJson,
                           Object body, String rawRequestContentType, boolean requiredJsonBody,
                           RequestOptions options) {
        for (int attempt = 0; ; attempt++) {
            try {
                return once(method, route, path, wantJson, body, rawRequestContentType,
                        requiredJsonBody, options);
            } catch (OlivaresApiException e) {
                boolean retryable = e.getStatus() == 429
                        || (e.getStatus() == 503 && method.equals("GET"));
                if (!retryable || attempt >= maxRetries) {
                    throw e;
                }
                long ms = e.getRetryAfterSeconds() > 0
                        ? e.getRetryAfterSeconds() * 1000L
                        : (attempt + 1L) * 500L;
                retrySleep.accept(ms);
            }
        }
    }

    private String once(String method, String route, String path, boolean wantJson,
                        Object body, String rawRequestContentType, boolean requiredJsonBody,
                        RequestOptions options) {
        String url = endpoint + path;
        if (!options.query.isEmpty()) {
            url += "?" + encodeQuery(options.query);
        }

        HttpRequest.BodyPublisher publisher;
        if (rawRequestContentType != null) {
            publisher = body == null
                    ? HttpRequest.BodyPublishers.noBody()
                    : HttpRequest.BodyPublishers.ofByteArray((byte[]) body);
        } else if (requiredJsonBody || body != null) {
            publisher = HttpRequest.BodyPublishers.ofString(Json.write(body), StandardCharsets.UTF_8);
        } else {
            publisher = HttpRequest.BodyPublishers.noBody();
        }

        HttpRequest.Builder rb = HttpRequest.newBuilder(URI.create(url))
                .timeout(timeout)
                .method(method, publisher)
                .header("User-Agent", userAgent);
        if (wantJson) {
            rb.header("Accept", "application/json");
        }
        if (rawRequestContentType != null) {
            if (body != null) {
                rb.header("Content-Type", rawRequestContentType);
            }
        } else if (requiredJsonBody || body != null) {
            rb.header("Content-Type", "application/json");
        }
        if (!token.isEmpty()) {
            rb.header("Authorization", "Bearer " + token);
        }
        // Coalesce-on-empty (matching the canonical Go core's cmpOr): an empty
        // per-call override means "no override" and falls back to the client default.
        String t = (options.tenant != null && !options.tenant.isEmpty()) ? options.tenant : tenant;
        if (t != null && !t.isEmpty()) {
            rb.header("X-Olivares-Tenant", t);
        }

        HttpResponse<InputStream> resp;
        try {
            resp = httpClient.send(rb.build(), HttpResponse.BodyHandlers.ofInputStream());
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException("request interrupted", e);
        }

        byte[] raw;
        try (InputStream in = resp.body()) {
            raw = readLimited(in);
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
        noticeDeprecation(method, route, path, resp.headers());

        int status = resp.statusCode();
        String text = new String(raw, StandardCharsets.UTF_8);
        if (status >= 200 && status < 300) {
            return text;
        }
        throw apiError(status, text, resp.headers());
    }

    private static byte[] readLimited(InputStream in) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        long total = 0;
        while (total < BODY_LIMIT) {
            int want = (int) Math.min(buf.length, BODY_LIMIT - total);
            int n = in.read(buf, 0, want);
            if (n == -1) {
                break;
            }
            out.write(buf, 0, n);
            total += n;
        }
        return out.toByteArray();
    }

    private static String encodeQuery(Map<String, List<String>> query) {
        StringBuilder sb = new StringBuilder();
        boolean first = true;
        for (Map.Entry<String, List<String>> e : query.entrySet()) {
            // One occurrence per value: a repeatable parameter is N occurrences of the
            // same key, never one key holding a joined string.
            for (String value : e.getValue()) {
                if (!first) {
                    sb.append('&');
                }
                first = false;
                sb.append(URLEncoder.encode(e.getKey(), StandardCharsets.UTF_8))
                        .append('=')
                        .append(URLEncoder.encode(value, StandardCharsets.UTF_8));
            }
        }
        return sb.toString();
    }

    private static OlivaresApiException apiError(int status, String raw, HttpHeaders headers) {
        String code = "http_" + status;
        String message = raw.strip();
        try {
            Object env = Json.parse(raw);
            if (env instanceof Map) {
                Object err = ((Map<?, ?>) env).get("error");
                if (err instanceof Map) {
                    Object c = ((Map<?, ?>) err).get("code");
                    if (c instanceof String && !((String) c).isEmpty()) {
                        code = (String) c;
                        Object m = ((Map<?, ?>) err).get("message");
                        message = m instanceof String ? (String) m : "";
                    }
                }
            }
        } catch (Json.JsonException ignored) {
            // non-envelope body: keep the raw text as the message
        }
        String requestId = headers.firstValue("X-Request-ID").orElse("");
        int retryAfter = parseRetryAfter(headers.firstValue("Retry-After").orElse(""));
        return new OlivaresApiException(status, code, message, requestId, retryAfter);
    }

    /**
     * Parse {@code Retry-After} delta-seconds (the only form the engine emits).
     * Only ASCII digits are accepted — Unicode digit forms ({@code '²'}, {@code '１２３'})
     * that {@code Integer.parseInt} would otherwise accept fall back to 0, not a value
     * Go/TS could not parse.
     */
    private static int parseRetryAfter(String value) {
        if (value.isEmpty()) {
            return 0;
        }
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            if (c < '0' || c > '9') {
                return 0;
            }
        }
        try {
            int secs = Integer.parseInt(value);
            return secs >= 0 ? secs : 0;
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    // --- stability policy signal ---------------------------------------------

    private void noticeDeprecation(String method, String route, String path, HttpHeaders headers) {
        String dep = headers.firstValue("Deprecation").orElse("");
        if (dep.isEmpty()) {
            return;
        }
        // Dedup per ENDPOINT (the route template, matching the server-side
        // declaration): a deprecated /v1/agents/{id} warns once, not once per
        // agent, and the set stays bounded by the published surface.
        if (!depSeen.add(method + " " + route)) {
            return;
        }
        DeprecationNotice notice = new DeprecationNotice(
                method, path, dep,
                headers.firstValue("Sunset").orElse(""),
                deprecationLink(headers.allValues("Link")));
        onDeprecation.accept(notice);
    }

    /** Extract the {@code rel="deprecation"} target from Link header values. Each
     *  value may itself be a comma-separated list (proxies coalesce repeats). */
    private static String deprecationLink(List<String> links) {
        for (String raw : links) {
            for (String part : raw.split(",")) {
                if (!part.contains("rel=\"deprecation\"")) {
                    continue;
                }
                int i = part.indexOf('<');
                int j = part.indexOf('>');
                if (i >= 0 && j > i) {
                    return part.substring(i + 1, j);
                }
            }
        }
        return "";
    }

    private static void defaultDeprecationLog(DeprecationNotice n) {
        String msg = "olivares: " + n.method() + " " + n.path() + " is deprecated (" + n.deprecation()
                + (n.sunset().isEmpty() ? "" : ", sunset " + n.sunset())
                + (n.link().isEmpty() ? ")" : "); see " + n.link());
        LOG.log(System.Logger.Level.WARNING, msg);
    }

    private static void defaultSleep(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException("retry sleep interrupted", e);
        }
    }

    // --- pagination ----------------------------------------------------------

    /** Iterate a cursor-paginated collection endpoint with default options. */
    public Iterable<Map<String, Object>> paginate(String path) {
        return paginate(path, RequestOptions.NONE);
    }

    /**
     * Iterate a cursor-paginated collection endpoint (the
     * {@code items}/{@code cursor}/{@code has_more} envelope), yielding each item
     * lazily — one page is fetched at a time, the next only when the current is
     * exhausted. Query/tenant options apply to every page request. Errors surface
     * as {@link OlivaresApiException} from the iterator.
     */
    public Iterable<Map<String, Object>> paginate(String path, RequestOptions options) {
        return () -> new PageIterator(path, options);
    }

    private final class PageIterator implements Iterator<Map<String, Object>> {
        private final String path;
        private final RequestOptions base;
        private Iterator<Map<String, Object>> pageItems = Collections.emptyIterator();
        private String cursor = "";
        private boolean done;

        PageIterator(String path, RequestOptions base) {
            this.path = path;
            this.base = base;
        }

        @Override
        public boolean hasNext() {
            while (!pageItems.hasNext()) {
                if (done) {
                    return false;
                }
                fetchPage();
            }
            return true;
        }

        @Override
        public Map<String, Object> next() {
            if (!hasNext()) {
                throw new NoSuchElementException();
            }
            return pageItems.next();
        }

        private void fetchPage() {
            RequestOptions.Builder b = RequestOptions.builder();
            // queryAdd, not query: the base options may carry a repeatable parameter
            // with several occurrences, and replacing per key would silently page with
            // only the last of them.
            for (Map.Entry<String, List<String>> e : base.query.entrySet()) {
                for (String value : e.getValue()) {
                    b.queryAdd(e.getKey(), value);
                }
            }
            if (!cursor.isEmpty()) {
                b.query("cursor", cursor);
            }
            if (base.tenant != null) {
                b.tenant(base.tenant);
            }
            Map<String, Object> page = doJson("GET", path, path, null, b.build());
            pageItems = extractItems(page).iterator();
            Object next = page.get("cursor");
            cursor = next instanceof String ? (String) next : "";
            if (!Boolean.TRUE.equals(page.get("has_more")) || cursor.isEmpty()) {
                done = true;
            }
        }

        private List<Map<String, Object>> extractItems(Map<String, Object> page) {
            List<Map<String, Object>> out = new ArrayList<>();
            Object items = page.get("items");
            if (items instanceof List) {
                for (Object it : (List<?>) items) {
                    if (it instanceof Map) {
                        @SuppressWarnings("unchecked")
                        Map<String, Object> m = (Map<String, Object>) it;
                        out.add(m);
                    }
                }
            }
            return out;
        }
    }

    // --- helpers -------------------------------------------------------------

    /**
     * Escape one path segment per RFC 3986 (everything outside the unreserved set
     * {@code A-Za-z0-9-._~} is percent-encoded), matching Go's {@code url.PathEscape},
     * Python's {@code quote(safe="")} and JS's {@code encodeURIComponent} for the
     * characters that appear in path params. Used by the generated operation layer.
     */
    static String escapePath(String s) {
        StringBuilder sb = new StringBuilder();
        byte[] bytes = s.getBytes(StandardCharsets.UTF_8);
        for (byte raw : bytes) {
            int b = raw & 0xFF;
            char c = (char) b;
            if (UNRESERVED.indexOf(c) >= 0) {
                sb.append(c);
            } else {
                sb.append('%').append(HEX[(b >> 4) & 0xF]).append(HEX[b & 0xF]);
            }
        }
        return sb.toString();
    }
}
