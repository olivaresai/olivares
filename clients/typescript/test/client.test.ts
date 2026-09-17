// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Smoke tests for the TypeScript client against a local fake control plane
// (node:http). Run from clients/typescript: pnpm test
// (`task sdk:test:ts` from the repo root.)

import { createServer, type IncomingMessage, type Server } from "node:http";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import {
  APIError,
  type AuthCapabilityQuestions,
  type AuthCapabilityResult,
  type AuthCapabilityResults,
  Client,
  type DeprecationNotice,
  type JsonInput,
  type SessionsCommunicationMessageSendBody,
} from "../src/index.js";

interface Seen {
  method: string;
  url: string;
  headers: IncomingMessage["headers"];
}

let server: Server;
let endpoint: string;
const seen: Seen[] = [];
let rateCalls = 0;

// A faithful answer from GET /v1/m/finops/statements/{id}/export: the handler's nine
// columns and one line. 9007199254740993 is 2**53+1 — the oracle, not filler. A
// consumer that routed this value through a JSON number and an IEEE double would hand
// back 9007199254740992.
const STATEMENT_EXPORT_CSV =
  "cost_center_code,cost_center_name,model,provider,agent," +
  "input_tokens,output_tokens,cost_micro_usd,sample_count\n" +
  "ENG-01,Engineering,claude-opus-5,anthropic,,100,50,9007199254740993,1\n";

beforeAll(async () => {
  server = createServer((req, res) => {
    seen.push({
      method: req.method ?? "",
      url: req.url ?? "",
      headers: req.headers,
    });
    const url = new URL(req.url ?? "/", "http://x");
    const json = (
      status: number,
      payload: unknown,
      headers: Record<string, string | string[]> = {},
    ) => {
      res.writeHead(status, { "Content-Type": "application/json", ...headers });
      res.end(JSON.stringify(payload));
    };
    if (url.pathname === "/v1/agents" && req.method === "GET") {
      if (url.searchParams.get("cursor") === "c1") {
        json(200, { items: [{ id: "c" }], has_more: false });
      } else {
        json(200, {
          items: [{ id: "a" }, { id: "b" }],
          cursor: "c1",
          has_more: true,
        });
      }
    } else if (url.pathname.startsWith("/v1/agents/")) {
      json(
        404,
        { error: { code: "not_found", message: "no such agent" } },
        { "X-Request-ID": "req-42" },
      );
    } else if (url.pathname === "/v1/server-info") {
      json(
        200,
        { version: "test" },
        {
          Deprecation: "@1780272000",
          Sunset: "Thu, 01 Jun 2028 00:00:00 GMT",
          Link: '<https://docs.olivares.invalid/how-to/migrate-example/>; rel="deprecation"',
        },
      );
    } else if (url.pathname === "/v1/tokens" && req.method === "POST") {
      rateCalls++;
      if (rateCalls === 1) {
        json(
          429,
          { error: { code: "rate_limited", message: "slow down" } },
          { "Retry-After": "3" },
        );
      } else {
        json(201, { ok: true });
      }
    } else if (
      url.pathname.startsWith("/v1/tokens/") &&
      req.method === "DELETE"
    ) {
      // Deprecated parameterized route: one notice per ENDPOINT expected.
      json(200, {}, { Deprecation: "@1780272000" });
    } else if (url.pathname === "/metrics") {
      res.writeHead(200, {
        "Content-Type": "text/plain; version=0.0.4; charset=utf-8",
      });
      res.end(
        "# HELP olivares_requests_total Requests.\nolivares_requests_total 42\n",
      );
    } else if (
      url.pathname.startsWith("/v1/m/finops/statements/") &&
      url.pathname.endsWith("/export")
    ) {
      // The chargeback statement export: always CSV on 200, never negotiated.
      res.writeHead(200, {
        "Content-Type": "text/csv; charset=utf-8",
        "Content-Disposition":
          'attachment; filename="chargeback_ENG-01_2026-06-01.csv"',
      });
      res.end(STATEMENT_EXPORT_CSV);
    } else if (url.pathname === "/raw-request") {
      json(200, {});
    } else if (url.pathname === "/v1/users" && req.method === "GET") {
      // Hostile shape: a 200 whose body is not a JSON object.
      json(200, [1, 2]);
    } else {
      json(400, { error: { code: "bad_request", message: "nope" } });
    }
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const addr = server.address();
  if (addr === null || typeof addr === "string") throw new Error("no port");
  endpoint = `http://127.0.0.1:${addr.port}`;
});

afterAll(() => new Promise<void>((r) => server.close(() => r())));

beforeEach(() => {
  seen.length = 0;
  rateCalls = 0;
});

function newClient(
  extra: Partial<ConstructorParameters<typeof Client>[0]> = {},
) {
  return new Client({
    endpoint,
    token: "olvk_test_secret",
    tenant: "t-default",
    retrySleep: async (ms) => {
      slept.push(ms);
    },
    ...extra,
  });
}

class RawRequestClient extends Client {
  sendRaw(body: Uint8Array | undefined, contentType: string) {
    return this.doReqRawWithType(
      "POST",
      "/raw-request",
      "/raw-request",
      body,
      contentType,
    );
  }

  sendLegacyOctetStream(body: Uint8Array) {
    return this.doReqRaw("PUT", "/raw-request", "/raw-request", body);
  }
}

class JsonRequestClient extends Client {
  sendRequired(body: JsonInput) {
    return this.doJsonRequired("POST", "/required", "/required", body);
  }

  sendOptional(body?: JsonInput) {
    return this.do("POST", "/optional", "/optional", body);
  }
}
const slept: number[] = [];
beforeEach(() => {
  slept.length = 0;
});

describe("request shape", () => {
  it("sends auth, tenant override, UA and query", async () => {
    const c = newClient();
    const out = await c.getV1Agents({
      query: { limit: "5" },
      tenant: "t-override",
    });
    expect((out.items as { id: string }[]).map((i) => i.id)).toEqual([
      "a",
      "b",
    ]);
    const r = seen[0];
    expect(r.url).toBe("/v1/agents?limit=5");
    expect(r.headers.authorization).toBe("Bearer olvk_test_secret");
    expect(r.headers["x-olivares-tenant"]).toBe("t-override");
    expect(r.headers["user-agent"]).toContain("olivares-client-ts/");
    expect(r.headers["user-agent"]).toContain("(api v1)");
  });

  // The API has repeatable query parameters (GET /v1/audit's exclude_action, the
  // first of them). An array must become N occurrences: handed straight to
  // URLSearchParams it becomes ONE comma-joined value, which a server reading a
  // repeatable filter takes as a single value and matches nothing.
  it("repeats an array query param instead of comma-joining it", async () => {
    const c = newClient();
    await c.getV1Agents({ query: { limit: "5", tag: ["a", "b"] } });
    expect(seen[0].url).toBe("/v1/agents?limit=5&tag=a&tag=b");
  });

  it("escapes path params in generated operations", async () => {
    const c = newClient();
    await expect(c.getV1AgentsById("a/b c")).rejects.toThrow(APIError);
    expect(seen[0].url).toBe("/v1/agents/a%2Fb%20c");
  });

  it("preserves the declared raw request Content-Type", async () => {
    const c = new RawRequestClient({ endpoint, token: "olvk_test_secret" });
    await c.sendRaw(new TextEncoder().encode("{}\n"), "application/x-ndjson");
    expect(seen.at(-1)?.headers["content-type"]).toBe("application/x-ndjson");

    await c.sendLegacyOctetStream(new TextEncoder().encode("raw"));
    expect(seen.at(-1)?.headers["content-type"]).toBe(
      "application/octet-stream",
    );

    await c.sendRaw(undefined, "application/x-ndjson");
    expect(seen.at(-1)?.headers["content-type"]).toBeUndefined();
  });

  it("sends required JSON null but omits optional undefined", async () => {
    const requests: RequestInit[] = [];
    const c = new JsonRequestClient({
      endpoint: "https://olivares.invalid",
      fetch: async (_input, init) => {
        requests.push(init ?? {});
        return new Response("{}", {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      },
    });

    const validInputs: JsonInput[] = [null, true, 1, "scalar", [null]];
    expect(validInputs).toHaveLength(5);
    await c.sendRequired(null);
    await c.sendOptional(undefined);

    expect(requests[0].body).toBe("null");
    expect(
      (requests[0].headers as Record<string, string>)["Content-Type"],
    ).toBe("application/json");
    expect(requests[1].body).toBeUndefined();
    expect(
      (requests[1].headers as Record<string, string>)["Content-Type"],
    ).toBeUndefined();
  });
});

describe("error envelope", () => {
  it("maps the single envelope to APIError", async () => {
    const c = newClient();
    const err = await c.getV1AgentsById("missing").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(APIError);
    const ae = err as APIError;
    expect([ae.status, ae.code, ae.requestId]).toEqual([
      404,
      "not_found",
      "req-42",
    ]);
    expect(ae.message).toContain("no such agent");
  });
});

describe("retry policy", () => {
  it("retries 429 honouring Retry-After as the lower bound", async () => {
    const c = newClient();
    const out = await c.postV1Tokens({ name: "ci" });
    expect(out.ok).toBe(true);
    expect(rateCalls).toBe(2);
    expect(slept).toEqual([3000]);
  });

  it("never retries a 400", async () => {
    const c = newClient();
    await expect(c.postV1Memberships({})).rejects.toThrow(APIError);
    expect(seen.length).toBe(1);
  });
});

describe("stability policy signal", () => {
  it("surfaces deprecation headers once per endpoint", async () => {
    const notices: DeprecationNotice[] = [];
    const c = newClient({ onDeprecation: (n) => notices.push(n) });
    await c.getV1ServerInfo();
    await c.getV1ServerInfo();
    expect(notices).toEqual([
      {
        method: "GET",
        path: "/v1/server-info",
        deprecation: "@1780272000",
        sunset: "Thu, 01 Jun 2028 00:00:00 GMT",
        link: "https://docs.olivares.invalid/how-to/migrate-example/",
      },
    ]);
  });

  it("dedups per route template, not per resource", async () => {
    const notices: DeprecationNotice[] = [];
    const c = newClient({ onDeprecation: (n) => notices.push(n) });
    await c.deleteV1TokensById("tok_001");
    await c.deleteV1TokensById("tok_002");
    expect(notices).toHaveLength(1);
    expect(notices[0].path).toBe("/v1/tokens/tok_001");
  });
});

describe("response shape", () => {
  it("returns the raw body for non-JSON operations", async () => {
    const c = newClient();
    const text = await c.getMetrics();
    expect(text).toContain("olivares_requests_total 42");
    expect(seen[0].headers.accept).not.toBe("application/json");
  });

  it("consumes a CSV 200 through the generated statement export operation", async () => {
    // ⛔ While the document declared this 200 an application/json object the emitter
    // produced `Promise<Json>` + this.do, and core.ts ran JSON.parse over the CSV: a
    // valid 200 raised APIError(bad_response) in every generated TypeScript client.
    // The assertion is on the OPERATION — doRaw itself was always right.
    const c = newClient();
    const text = await c.getV1MFinopsStatementsByIdExport(
      "01a084d5-988c-7e46-b396-5cff04bf2793",
    );
    expect(text).toBe(STATEMENT_EXPORT_CSV);
    expect(text).toContain("9007199254740993");
    expect(seen[0].url).toBe(
      "/v1/m/finops/statements/01a084d5-988c-7e46-b396-5cff04bf2793/export",
    );
    // A CSV-only route must not demand JSON: the server does not negotiate.
    expect(seen[0].headers.accept).not.toBe("application/json");
  });

  it("rejects a 200 whose body is not a JSON object", async () => {
    const c = newClient();
    const err = await c.getV1Users().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(APIError);
    expect((err as APIError).code).toBe("bad_response");
  });
});

describe("pagination", () => {
  it("follows the items/cursor/has_more envelope", async () => {
    const c = newClient();
    const ids: string[] = [];
    for await (const item of c.paginate("/v1/agents")) {
      ids.push(item.id as string);
    }
    expect(ids).toEqual(["a", "b", "c"]);
  });
});

describe("construction", () => {
  it("publishes the typed communication request surface from the package root", () => {
    const body: SessionsCommunicationMessageSendBody = {
      channel_id: "00000000-0000-4000-8000-000000000001",
      recipient: { kind: "user", ref: "00000000-0000-4000-8000-000000000002" },
      content: {
        subject: "scheduled",
        blocks: [{ type: "text", text: "hello" }],
      },
      available_at: "2026-09-06T03:00:00Z",
    };
    expect(body.available_at).toBe("2026-09-06T03:00:00Z");
  });

  it("rejects a non-absolute endpoint", () => {
    expect(() => new Client({ endpoint: "not-a-url" })).toThrow();
  });
});

describe("capability projection, schema 2", () => {
  // docs/contracts/CAPABILITY-PROJECTION.md: one positive with its budget, one
  // concealed non-verdict without one, one surface admission.
  const published: AuthCapabilityResults = {
    schema_version: 2,
    results: [
      {
        id: "sheet",
        kind: "operation",
        state: "allowed",
        code: "authorized",
        observed_at: "2026-09-07T10:00:00.250Z",
        refresh_after_ms: 30000,
      },
      {
        id: "held",
        kind: "operation",
        state: "undisclosed",
        code: "not_disclosed",
        observed_at: "2026-09-07T10:00:00Z",
      },
      {
        id: "list",
        kind: "surface",
        state: "reachable",
        code: "admitted",
        observed_at: "2026-09-07T10:00:00.100Z",
        refresh_after_ms: 12000,
      },
    ],
  };

  it("round-trips the generated DTO family through the package root", async () => {
    const requests: { url: string; init: RequestInit }[] = [];
    const c = new Client({
      endpoint: "https://olivares.invalid",
      token: "olvk_test_secret",
      fetch: async (input, init) => {
        requests.push({ url: String(input), init: init ?? {} });
        return new Response(JSON.stringify(published), {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "Cache-Control": "no-store",
          },
        });
      },
    });
    const workspace = "00000000-0000-4000-8000-000000000010";
    const body: AuthCapabilityQuestions = {
      schema_version: 2,
      questions: [
        {
          id: "sheet",
          kind: "operation",
          operation: "GET /v1/m/sessions/channels/{id}/grants",
          workspace_id: workspace,
          selectors: { path: { id: "00000000-0000-4000-8000-000000000001" } },
        },
        {
          id: "held",
          kind: "operation",
          operation: "PATCH /v1/m/sessions/channels",
          workspace_id: workspace,
          selectors: {
            body: { channel_id: "00000000-0000-4000-8000-000000000002" },
          },
        },
        {
          id: "list",
          kind: "surface",
          operation: "GET /v1/m/sessions/channels",
          workspace_id: workspace,
        },
      ],
    };
    const out: AuthCapabilityResults = await c.postV1AuthCapabilities({ body });

    expect(requests).toHaveLength(1);
    expect(requests[0].url).toBe(
      "https://olivares.invalid/v1/auth/capabilities",
    );
    expect(requests[0].init.method).toBe("POST");
    expect(
      (requests[0].init.headers as Record<string, string>)["Content-Type"],
    ).toBe("application/json");
    const wire = JSON.parse(String(requests[0].init.body)) as Record<
      string,
      unknown
    >;
    expect(wire.schema_version).toBe(2);
    expect(wire.questions).toEqual(body.questions);

    expect(out.schema_version).toBe(2);
    expect(out.results.map((r) => [r.id, r.state, r.code])).toEqual([
      ["sheet", "allowed", "authorized"],
      ["held", "undisclosed", "not_disclosed"],
      ["list", "reachable", "admitted"],
    ]);
    const positive: AuthCapabilityResult = out.results[0];
    const concealed: AuthCapabilityResult = out.results[1];
    expect(positive.refresh_after_ms).toBe(30000);
    // The non-verdict carries no budget at all: absent, not zero.
    expect("refresh_after_ms" in concealed).toBe(false);
    expect(concealed.observed_at).toBe("2026-09-07T10:00:00Z");
    // Re-serializing the typed result reproduces the wire exactly.
    expect(JSON.parse(JSON.stringify(out))).toEqual(published);
  });
});
