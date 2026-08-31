// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Transport core for the Olivares AI control plane client: endpoint/auth/
// tenancy wiring for the opaque bearer tokens (olvs_/olvk_), the single error
// envelope mapped to APIError, cursor pagination, Retry-After-aware retries
// for rate-limited calls, and surfacing of the stability policy's deprecation
// signal (RFC 9745 Deprecation / RFC 8594 Sunset response headers), once per
// endpoint. Runtime-dependency-free (global fetch), by design.

import { API_VERSION } from "./version.gen.js";

/** Generic representation used by the thin SDK for published JSON schemas. */
export type Json = Record<string, unknown>;

/** Any JSON request value. Objects retain the SDK's historically permissive
 * value shape; top-level null, scalars and arrays are represented explicitly. */
export type JsonInput = Json | null | boolean | number | string | JsonInput[];

/** The SDK's own semantic version. Pre-1.0 while the product is pre-1.0; from
 * GA on, the MAJOR tracks the API contract major (API_VERSION). */
export const VERSION = "0.1.0";

/** One deprecated-endpoint signal, parsed from the policy's headers. */
export interface DeprecationNotice {
  method: string;
  /** The request path (concrete, not the route template). */
  path: string;
  /** Raw Deprecation value, e.g. "@1780272000". */
  deprecation: string;
  /** Raw Sunset value (HTTP-date), "" if not yet scheduled. */
  sunset: string;
  /** Migration-guide URL from Link rel="deprecation", if any. */
  link: string;
}

/** The API's single error envelope ({"error":{code,message}}) plus transport
 * context. `code` values are part of the stable contract. */
export class APIError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId: string,
    readonly retryAfterSeconds: number,
  ) {
    super(`${message} (${code} ${status}, request ${requestId})`);
    this.name = "APIError";
  }
}

export interface ClientOptions {
  /** Absolute base URL, e.g. "https://olivares.example:8443". */
  endpoint: string;
  /** Opaque bearer token (olvs_ session or olvk_ API key). */
  token?: string;
  /** Default X-Olivares-Tenant; override per call via RequestOptions. */
  tenant?: string;
  /** Retries for retryable statuses (429 always, 503 for GET). Default 2. */
  maxRetries?: number;
  /** Replace the transport (custom TLS dispatch, proxies, test fakes). */
  fetch?: typeof fetch;
  /** Replaces the default deprecation handler (a console.warn); called at
   * most once per METHOD+path per client. */
  onDeprecation?: (n: DeprecationNotice) => void;
  /** Retry wait seam (tests inject a recorder). */
  retrySleep?: (ms: number) => Promise<void>;
  /** Prefixed to the SDK User-Agent. */
  userAgent?: string;
}

export interface RequestOptions {
  /** Query parameters. An array value is a REPEATED parameter — one occurrence per
   *  entry — which is how the API expresses a repeatable filter such as
   *  /v1/audit's exclude_action. */
  query?: Record<string, string | string[]>;
  /** Per-call tenant override. */
  tenant?: string;
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** Extract the rel="deprecation" target from a Link header value. */
function deprecationLink(link: string | null): string {
  if (!link) return "";
  for (const part of link.split(",")) {
    if (!part.includes('rel="deprecation"')) continue;
    const m = part.match(/<([^>]*)>/);
    if (m) return m[1];
  }
  return "";
}

export class ClientCore {
  private readonly endpoint: string;
  private readonly opts: ClientOptions;
  private readonly userAgent: string;
  private readonly depSeen = new Set<string>();

  constructor(opts: ClientOptions) {
    const u = new URL(opts.endpoint); // throws on a non-absolute endpoint
    this.endpoint = u.toString().replace(/\/$/, "");
    this.opts = opts;
    const ua = `olivares-client-ts/${VERSION} (api ${API_VERSION})`;
    this.userAgent = opts.userAgent ? `${opts.userAgent} ${ua}` : ua;
  }

  /** One JSON operation. `route` is the spec path template (e.g.
   * /v1/agents/{id}) — the deprecation-dedup key; `path` is the concrete
   * escaped request path. */
  protected do(
    method: string,
    route: string,
    path: string,
    body?: JsonInput,
    opts?: RequestOptions,
  ): Promise<Json> {
    return this.executeJson(method, route, path, body, undefined, opts);
  }

  /** Execute a required JSON request. `null` is a present JSON value; only
   * `undefined` means absent on the optional/legacy `do` seam. */
  protected doJsonRequired(
    method: string,
    route: string,
    path: string,
    body: JsonInput,
    opts?: RequestOptions,
  ): Promise<Json> {
    return this.executeJson(method, route, path, body, undefined, opts);
  }

  /** Compatibility seam for operation layers generated before request media
   * types were preserved. Those operations were octet-stream only. */
  protected doReqRaw(
    method: string,
    route: string,
    path: string,
    body: Uint8Array | undefined,
    opts?: RequestOptions,
  ): Promise<Json> {
    return this.doReqRawWithType(method, route, path, body, "application/octet-stream", opts);
  }

  /** Send raw request bytes under the operation's exact declared media type. */
  protected doReqRawWithType(
    method: string,
    route: string,
    path: string,
    body: Uint8Array | undefined,
    contentType: string,
    opts?: RequestOptions,
  ): Promise<Json> {
    return this.executeJson(method, route, path, body, contentType, opts);
  }

  private async executeJson(
    method: string,
    route: string,
    path: string,
    body: JsonInput | Uint8Array | undefined,
    rawRequestContentType: string | undefined,
    opts?: RequestOptions,
  ): Promise<Json> {
    const raw = await this.execute(method, route, path, true, body, rawRequestContentType, opts);
    if (raw.trim() === "") return {};
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      parsed = undefined;
    }
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw new APIError(0, "bad_response", `response is not a JSON object (${method} ${path})`, "", 0);
    }
    return parsed as Json;
  }

  /** An operation whose success body is NOT JSON (e.g. /metrics); errors still
   * arrive in the JSON envelope and throw APIError. */
  protected doRaw(
    method: string,
    route: string,
    path: string,
    opts?: RequestOptions,
  ): Promise<string> {
    return this.execute(method, route, path, false, undefined, undefined, opts);
  }

  /** The policy-aware retry loop: 429 is always retryable (the limiter rejects
   * before execution and Retry-After is a safe lower bound), 503 only for GET.
   * Everything else surfaces. */
  private async execute(
    method: string,
    route: string,
    path: string,
    wantJson: boolean,
    body: JsonInput | Uint8Array | undefined,
    rawRequestContentType: string | undefined,
    opts?: RequestOptions,
  ): Promise<string> {
    const maxRetries = this.opts.maxRetries ?? 2;
    const wait = this.opts.retrySleep ?? sleep;
    for (let attempt = 0; ; attempt++) {
      try {
        return await this.once(method, route, path, wantJson, body, rawRequestContentType, opts);
      } catch (err) {
        if (!(err instanceof APIError) || attempt >= maxRetries) throw err;
        const retryable =
          err.status === 429 || (err.status === 503 && method === "GET");
        if (!retryable) throw err;
        const ms =
          err.retryAfterSeconds > 0
            ? err.retryAfterSeconds * 1000
            : (attempt + 1) * 500;
        await wait(ms);
      }
    }
  }

  private async once(
    method: string,
    route: string,
    path: string,
    wantJson: boolean,
    body: JsonInput | Uint8Array | undefined,
    rawRequestContentType: string | undefined,
    opts?: RequestOptions,
  ): Promise<string> {
    let url = this.endpoint + path;
    if (opts?.query && Object.keys(opts.query).length > 0) {
      // Built entry by entry rather than handed to the URLSearchParams constructor:
      // given an array that constructor stringifies it into ONE comma-joined value,
      // which a server reading a repeatable parameter takes as a single value and
      // matches nothing.
      const params = new URLSearchParams();
      for (const [key, value] of Object.entries(opts.query)) {
        if (Array.isArray(value)) {
          for (const entry of value) params.append(key, entry);
        } else {
          params.set(key, value);
        }
      }
      const qs = params.toString();
      if (qs) url += "?" + qs;
    }
    const headers: Record<string, string> = {
      "User-Agent": this.userAgent,
    };
    if (wantJson) headers["Accept"] = "application/json";
    if (body !== undefined)
      headers["Content-Type"] = rawRequestContentType ?? "application/json";
    if (this.opts.token) headers["Authorization"] = `Bearer ${this.opts.token}`;
    const tenant = opts?.tenant ?? this.opts.tenant;
    if (tenant) headers["X-Olivares-Tenant"] = tenant;

    const doFetch = this.opts.fetch ?? fetch;
    const resp = await doFetch(url, {
      method,
      headers,
      body:
        body === undefined
          ? undefined
          : rawRequestContentType !== undefined
            ? (body as Uint8Array as unknown as BodyInit)
            : JSON.stringify(body),
    });
    this.noticeDeprecation(method, route, path, resp.headers);

    const raw = await resp.text();
    if (resp.ok) return raw;
    let code = `http_${resp.status}`;
    let message = raw.trim();
    try {
      const envelope = JSON.parse(raw) as { error?: { code?: string; message?: string } };
      if (envelope.error?.code) {
        code = envelope.error.code;
        message = envelope.error.message ?? "";
      }
    } catch {
      // non-envelope body: keep the raw text as the message
    }
    const retryAfter = parseInt(resp.headers.get("Retry-After") ?? "", 10);
    throw new APIError(
      resp.status,
      code,
      message,
      resp.headers.get("X-Request-ID") ?? "",
      Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : 0,
    );
  }

  /** Dedup per ENDPOINT (the route template, matching the server-side
   * declaration): a deprecated /v1/agents/{id} warns once, not once per agent,
   * and the set stays bounded by the published surface. */
  private noticeDeprecation(method: string, route: string, path: string, headers: Headers) {
    const dep = headers.get("Deprecation");
    if (!dep) return;
    const key = `${method} ${route}`;
    if (this.depSeen.has(key)) return;
    this.depSeen.add(key);
    const notice: DeprecationNotice = {
      method,
      path,
      deprecation: dep,
      sunset: headers.get("Sunset") ?? "",
      link: deprecationLink(headers.get("Link")),
    };
    if (this.opts.onDeprecation) {
      this.opts.onDeprecation(notice);
      return;
    }
    console.warn(
      `olivares: ${method} ${path} is deprecated (${notice.deprecation}` +
        (notice.sunset ? `, sunset ${notice.sunset}` : "") +
        (notice.link ? `); see ${notice.link}` : ")"),
    );
  }

  /** Iterate a cursor-paginated collection endpoint (the items/cursor/has_more
   * envelope), yielding each item until exhaustion. */
  async *paginate(path: string, opts?: RequestOptions): AsyncGenerator<Json> {
    let cursor = "";
    for (;;) {
      const query = { ...(opts?.query ?? {}) };
      if (cursor) query["cursor"] = cursor;
      const page = await this.do("GET", path, path, undefined, { ...opts, query });
      for (const item of (page["items"] as Json[] | undefined) ?? []) yield item;
      cursor = typeof page["cursor"] === "string" ? page["cursor"] : "";
      if (page["has_more"] !== true || cursor === "") return;
    }
  }
}
