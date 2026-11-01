/** Disposable transport substitution for pinned usage observations; never installed as an extension. */
const endpoint = process.env.USAGE_FIXTURE_ENDPOINT;
if (!endpoint) throw new Error("Usage fixture endpoint is required");
const origin = new URL(endpoint);
if (origin.origin !== endpoint || origin.hostname !== "127.0.0.1" || origin.protocol !== "http:") {
  throw new Error("Usage fixtures require an explicit loopback HTTP origin");
}
const fetchLocal = globalThis.fetch;
const clock = Number(process.env.USAGE_FIXTURE_NOW);
if (!Number.isSafeInteger(clock) || clock <= 0) throw new Error("Usage fixture clock is required");
Date.now = () => clock;
globalThis.fetch = async function fixtureFetch(input, options = {}) {
  if (!(typeof input === "string" || input instanceof URL)) throw new Error("Unsupported fixture fetch input");
  const url = new URL(input);
  if (url.protocol !== "https:" || url.username || url.password) throw new Error("Non-provider fixture request refused");
  const headers = new Headers(options.headers);
  headers.set("x-usage-fixture-url", url.href);
  headers.set("x-usage-fixture-case", process.env.USAGE_FIXTURE_CASE ?? "host");
  headers.set("x-usage-fixture-redirect", options.redirect ?? "follow");
  globalThis[Symbol.for("usage-fixture-trace")]?.push({ kind: "request", url: url.href });
  const response = await fetchLocal(`${endpoint}/provider`, { ...options, headers });
  await globalThis[Symbol.for("usage-fixture-barrier")]?.(url.href, response);
  return response;
};
