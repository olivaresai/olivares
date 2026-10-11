// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { createServer, type IncomingMessage, type RequestListener, type Server } from "node:http";
import { once } from "node:events";
import { describe, expect, it, vi } from "vitest";
import { Client } from "./index.js";

async function peer(handler: RequestListener): Promise<{ server: Server; url: string }> {
  const server = createServer(handler).listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("missing peer port");
  return { server, url: `http://127.0.0.1:${address.port}` };
}

async function stop(server: Server): Promise<void> {
  const closed = new Promise<void>((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));
  server.closeAllConnections();
  await closed;
}

async function requestBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(Buffer.from(chunk));
  return Buffer.concat(chunks).toString("utf8");
}

describe("client redirect and response-read compatibility", () => {
  const cases = [301, 302, 303, 307, 308].flatMap((status) =>
    ["same_origin", "other_port", "localhost_vs_127", "http_to_https"].map((destination) => ({ status, destination })),
  );

  it.each(cases)("$status/$destination", async ({ status, destination }) => {
    const seen: Array<{ method: string | undefined; auth: string | undefined; tenant: string | string[] | undefined; body: string }> = [];
    const record = async (req: IncomingMessage) => {
      seen.push({ method: req.method, auth: req.headers.authorization, tenant: req.headers["x-olivares-tenant"], body: await requestBody(req) });
    };
    const target = await peer(async (req, res) => {
      await record(req);
      res.end('{"redirected":true}');
    });
    const newOrigin = destination === "localhost_vs_127" ? target.url.replace("127.0.0.1", "localhost")
      : destination === "http_to_https" ? target.url.replace("http:", "https:") : target.url;
    const location = destination === "same_origin" ? "/destination" : newOrigin + "/destination";
    const origin = await peer(async (req, res) => {
      await record(req);
      if (req.url === "/v1/auth/login") {
        res.writeHead(status, { Location: location, "Content-Length": "0" });
        res.end();
      } else {
        res.end('{"redirected":true}');
      }
    });
    try {
      const client = new Client({ endpoint: origin.url, token: "synthetic-client-token", tenant: "synthetic-tenant" });
      if (destination !== "same_origin") {
        await expect(client.postV1AuthLogin({ sentinel: "synthetic-body" })).rejects.toThrow(
          `olivares: the server redirected to ${newOrigin}; set the client's base URL to it`,
        );
        expect(seen).toHaveLength(1);
        expect(seen[0].auth).toBe("Bearer synthetic-client-token");
        expect(seen[0].tenant).toBe("synthetic-tenant");
        expect(JSON.parse(seen[0].body)).toEqual({ sentinel: "synthetic-body" });
        return;
      }
      await expect(client.postV1AuthLogin({ sentinel: "synthetic-body" })).resolves.toEqual({ redirected: true });
      expect(seen).toHaveLength(2);
      expect(seen[1].auth).toBe("Bearer synthetic-client-token");
      expect(seen[1].tenant).toBe("synthetic-tenant");
      if (status < 307) {
        expect(seen[1].method).toBe("GET");
        expect(seen[1].body).toBe("");
      } else {
        expect(seen[1].method).toBe("POST");
        expect(JSON.parse(seen[1].body)).toEqual({ sentinel: "synthetic-body" });
      }
    } finally {
      await stop(origin.server);
      await stop(target.server);
    }
  });

  it.each(["username", "password", "both"])("same-origin credentialed %s Location fails safely before a second Fetch", async (userinfo) => {
    let calls = 0;
    let location = "";
    const origin = await peer(async (req, res) => {
      calls++;
      await requestBody(req);
      res.writeHead(307, { Location: location, "Content-Length": "0" });
      res.end();
    });
    try {
      const next = new URL(origin.url + "/destination?synthetic-query#synthetic-fragment");
      if (userinfo !== "password") next.username = "synthetic-user";
      if (userinfo !== "username") next.password = "synthetic-password";
      location = next.href;
      const doFetch = vi.fn<typeof fetch>((input, init) => fetch(input, init));
      const client = new Client({ endpoint: origin.url, token: "synthetic-client-token", tenant: "synthetic-tenant", fetch: doFetch });
      const error = await client.postV1AuthLogin({ sentinel: "synthetic-body" }).catch((err: unknown) => err);
      expect(error).toBeInstanceOf(Error);
      const message = (error as Error).message;
      expect(message).toBe(`olivares: the server redirected to ${origin.url}; set the client's base URL to it`);
      for (const secret of ["synthetic-user", "synthetic-password", "synthetic-query", "synthetic-fragment"]) {
        expect(message).not.toContain(secret);
      }
      expect(doFetch).toHaveBeenCalledTimes(1);
      expect(calls).toBe(1);
    } finally {
      await stop(origin.server);
    }
  });

  it.each([307, 308])("same-origin %i replays the original raw Buffer after caller mutation", async (status) => {
    const body = Buffer.from("synthetic-original-body");
    const seen: Array<{ method: string | undefined; contentType: string | undefined; auth: string | undefined; tenant: string | string[] | undefined; body: string }> = [];
    const origin = await peer(async (req, res) => {
      seen.push({ method: req.method, contentType: req.headers["content-type"], auth: req.headers.authorization, tenant: req.headers["x-olivares-tenant"], body: await requestBody(req) });
      if (seen.length === 1) {
        body.fill("x");
        res.writeHead(status, { Location: "/destination", "Content-Length": "0" });
        res.end();
      } else {
        res.end('{"redirected":true}');
      }
    });
    try {
      const client = new Client({ endpoint: origin.url, token: "synthetic-client-token", tenant: "synthetic-tenant" });
      await expect(client.putV1MSessionsWorkspacesByRefFilesRaw("synthetic-ref", body)).resolves.toEqual({ redirected: true });
      expect(body.toString()).toBe("x".repeat("synthetic-original-body".length));
      expect(seen).toHaveLength(2);
      for (const request of seen) {
        expect(request).toEqual({ method: "PUT", contentType: "application/octet-stream", auth: "Bearer synthetic-client-token", tenant: "synthetic-tenant", body: "synthetic-original-body" });
      }
    } finally {
      await stop(origin.server);
    }
  });

  it.each([200, 429, 503])("short %i body fails without retry", async (status) => {
    let calls = 0;
    const origin = await peer((_req, res) => {
      calls++;
      const body = '{"error":{"code":"busy","message":"try later"}}';
      res.writeHead(status, { "Content-Length": String(Buffer.byteLength(body) + 1), Connection: "close" });
      res.end(body);
    });
    try {
      const client = new Client({ endpoint: origin.url, token: "synthetic-client-token" });
      await expect(client.getMetrics()).rejects.toBeInstanceOf(TypeError);
      expect(calls).toBe(1);
    } finally {
      await stop(origin.server);
    }
  });

  it("same-origin redirect loops retain the platform's twenty-hop bound", async () => {
    let calls = 0;
    const origin = await peer((_req, res) => {
      calls++;
      res.writeHead(307, { Location: "/loop", "Content-Length": "0" });
      res.end();
    });
    try {
      await expect(new Client({ endpoint: origin.url }).getMetrics()).rejects.toBeInstanceOf(TypeError);
      expect(calls).toBe(21);
    } finally {
      await stop(origin.server);
    }
  });
});

// Runtime globals are hidden only during the synchronous public call; restoring
// them before awaiting leaves the test runner and Node's I/O environment intact.
function inRuntime<T>(runtime: "browser" | "Deno" | "Bun", run: () => T): T {
  vi.stubGlobal("process", undefined);
  vi.stubGlobal("Deno", runtime === "Deno" ? {} : undefined);
  vi.stubGlobal("Bun", runtime === "Bun" ? {} : undefined);
  try {
    return run();
  } finally {
    vi.unstubAllGlobals();
  }
}

describe("browser platform redirect path", () => {
  it.each(["same_origin", "other_port", "localhost_vs_127", "http_to_https", "origin_only_diagnostic"])("%s final answer", async (destination) => {
    const endpoint = "http://127.0.0.1:8443";
    const newOrigin = destination === "other_port" ? "http://127.0.0.1:8444"
      : destination === "localhost_vs_127" ? "http://localhost:8443"
        : destination === "http_to_https" ? "https://127.0.0.1:8443" : endpoint;
    const response = new Response('{"redirected":true}');
    const finalUrl = destination === "origin_only_diagnostic"
      ? "https://synthetic-user:synthetic-password@other.example/destination?synthetic-query#synthetic-fragment"
      : newOrigin + "/destination";
    Object.defineProperties(response, { redirected: { value: true }, url: { value: finalUrl } });
    const doFetch = vi.fn<typeof fetch>(async (_input, init) => {
      expect(init?.redirect ?? "follow").toBe("follow");
      return response;
    });
    const client = new Client({ endpoint, fetch: doFetch, token: "synthetic-client-token", tenant: "synthetic-tenant" });
    const pending = inRuntime("browser", () => client.postV1AuthLogin({ sentinel: "synthetic-body" }));
    if (destination === "same_origin") {
      await expect(pending).resolves.toEqual({ redirected: true });
    } else {
      await expect(pending).rejects.toThrow(`olivares: the server redirected to ${new URL(finalUrl).origin}; set the client's base URL to it`);
    }
    expect(doFetch).toHaveBeenCalledTimes(1);
  });
});

describe("non-browser manual redirect paths", () => {
  it.each(["Deno", "Bun"] as const)("%s refuses before requesting another origin", async (runtime) => {
    const doFetch = vi.fn<typeof fetch>(async (_input, init) => {
      expect(init?.redirect).toBe("manual");
      return new Response(null, { status: 307, headers: { Location: "https://synthetic-user:synthetic-password@other.example/destination?synthetic-query#synthetic-fragment" } });
    });
    const client = new Client({ endpoint: "https://engine.example", fetch: doFetch, token: "synthetic-client-token", tenant: "synthetic-tenant" });
    await expect(inRuntime(runtime, () => client.postV1AuthLogin({ sentinel: "synthetic-body" }))).rejects.toThrow(
      "olivares: the server redirected to https://other.example; set the client's base URL to it",
    );
    expect(doFetch).toHaveBeenCalledTimes(1);
  });

  it.each(["Deno", "Bun"] as const)("%s keeps a same-origin 307 body and tenant", async (runtime) => {
    const doFetch = vi.fn<typeof fetch>();
    doFetch.mockImplementation(async (_input, init) => {
      expect(init?.redirect).toBe("manual");
      return doFetch.mock.calls.length === 1
        ? new Response(null, { status: 307, headers: { Location: "/destination" } })
        : new Response('{"redirected":true}');
    });
    const client = new Client({ endpoint: "https://engine.example", fetch: doFetch, token: "synthetic-client-token", tenant: "synthetic-tenant" });
    await expect(inRuntime(runtime, () => client.postV1AuthLogin({ sentinel: "synthetic-body" }))).resolves.toEqual({ redirected: true });
    expect(doFetch).toHaveBeenCalledTimes(2);
    expect(doFetch.mock.calls[1][0]).toBe("https://engine.example/destination");
    const init = doFetch.mock.calls[1][1];
    expect(init?.method).toBe("POST");
    expect(JSON.parse(init?.body as string)).toEqual({ sentinel: "synthetic-body" });
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer synthetic-client-token");
    expect(new Headers(init?.headers).get("X-Olivares-Tenant")).toBe("synthetic-tenant");
  });

  it("an unexpectedly opaque transport never repeats a POST to discover its runtime", async () => {
    const response = new Response(null, { status: 302 });
    Object.defineProperties(response, { type: { value: "opaqueredirect" }, status: { value: 0 } });
    const doFetch = vi.fn<typeof fetch>(async () => response);
    const client = new Client({ endpoint: "https://engine.example", fetch: doFetch });
    await expect(client.postV1AuthLogin({ sentinel: "synthetic-body" })).rejects.toThrow("this runtime hides the address");
    expect(doFetch).toHaveBeenCalledTimes(1);
  });
});
