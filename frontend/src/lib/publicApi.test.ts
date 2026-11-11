import { test } from "node:test";
import assert from "node:assert/strict";
import { fetchPublicModelDetail } from "./publicApi.ts";

function stubFetch(status: number, body: unknown) {
  const calls: string[] = [];
  globalThis.fetch = (async (input: string) => {
    calls.push(input);
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  return calls;
}

test("model detail 404 resolves to null, not an error", async () => {
  const calls = stubFetch(404, { error: { message: "no market detail" } });
  assert.equal(await fetchPublicModelDetail("a/b c"), null);
  assert.deepEqual(calls, ["/v1/public/model-detail?model=a%2Fb%20c"]);
});

test("model detail other failures still throw", async () => {
  stubFetch(500, { error: { message: "detail unavailable" } });
  await assert.rejects(fetchPublicModelDetail("m"), /500/);
  stubFetch(429, {});
  await assert.rejects(fetchPublicModelDetail("m"), /429/);
});

test("model detail 200 returns the payload", async () => {
  stubFetch(200, { model: "m", requests_24h: 3 });
  const d = await fetchPublicModelDetail("m");
  assert.equal(d?.model, "m");
  assert.equal(d?.requests_24h, 3);
});
