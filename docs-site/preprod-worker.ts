// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// PREPROD ONLY. Serves the same `dist/` as production and adds one header:
// `X-Robots-Tag: noindex, nofollow`.
//
// ⛔ WHY A WORKER AND NOT A CONFIG LINE — I SAID "one line in the config" AND THAT WAS WRONG.
// A pure-assets Worker has no place to set a response header: `wrangler.jsonc` configures the
// assets runtime, it does not sit in the response path. The other obvious hook,
// `docs-site/public/_headers`, IS THE WRONG PLACE because it is part of the build and ships to
// PRODUCTION too — putting noindex there would deindex docs.olivares.ai. So the header needs
// code, and the code has to exist only on the preprod side. This file is referenced by
// `wrangler.preprod.jsonc` and by nothing else; production keeps serving assets with no code in
// its response path, which was a deliberate property of that config and stays one.
//
// ⛔ AND WHY IT IS NOT OPTIONAL. Preprod would serve a COMPLETE, CRAWLABLE COPY of the
// documentation on a public hostname. Production sets its own canonical, so the duplicate is
// what carries the risk: search engines index the preprod URLs, and the damage is slow to
// notice and slower to undo. The reversible direction is to ship noindex and remove it if
// somebody ever wants preprod indexed. The irreversible one is the other way round.

interface Env {
  ASSETS: { fetch: (request: Request) => Promise<Response> };
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const response = await env.ASSETS.fetch(request);

    // The body is a stream and must not be read here — re-wrap so the headers are mutable while
    // the body passes through untouched. Reading it would buffer every page in memory.
    const headers = new Headers(response.headers);
    headers.set('X-Robots-Tag', 'noindex, nofollow');

    return new Response(response.body, {
      status: response.status,
      statusText: response.statusText,
      headers,
    });
  },
};
