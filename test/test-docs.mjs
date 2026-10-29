import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { once } from "node:events";
import { mkdtemp, mkdir, rm, symlink, writeFile } from "node:fs/promises";
import { request } from "node:http";
import { networkInterfaces, tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { createDocsServer, verifyAsset } from "../scripts/docs-dev.mjs";

test("renderer bytes must match their pinned integrity", () => {
  const bytes = Buffer.from("renderer");
  const hash = `sha384-${createHash("sha384").update(bytes).digest("base64")}`;
  assert.equal(verifyAsset(bytes, hash), bytes);
  assert.throws(() => verifyAsset(Buffer.from("changed"), hash), /integrity check failed/);
});

test("docs server renders Markdown and confines requests to disposable docs", async (t) => {
  const temp = await mkdtemp(join(tmpdir(), "pig-docs-test-"));
  t.after(() => rm(temp, { recursive: true, force: true }));
  const docsRoot = join(temp, "docs");
  await mkdir(join(docsRoot, "designs"), { recursive: true });
  const source = '# Architecture\n\n## Links and headings\n\n[Other](other.md#detail)\n\n## Links and headings\n\n<script>alert("unsafe")</script>\n\n| Owner | Work |\n| --- | --- |\n| Status | Refresh |\n\n```mermaid\nflowchart LR\n A --> B\n```\n\n```mermaid\nC4Context\n Person(user, "User")\n```\n';
  await writeFile(join(docsRoot, "designs/architecture.md"), source);
  await writeFile(join(docsRoot, "designs/other.md"), "# Other\n\n## Detail\n");
  await writeFile(join(docsRoot, "capture.json"), '{"captured":true}');
  await writeFile(join(temp, "secret.md"), "not docs");
  await symlink(join(temp, "secret.md"), join(docsRoot, "escape.md"));
  const server = await createDocsServer({ docsRoot, assets: { "renderer.js": Buffer.from("fixture-js"), "renderer.css": Buffer.from("fixture-css") } });
  t.after(() => new Promise((resolve) => { server.close(resolve); server.closeAllConnections(); }));
  server.listen(0, "0.0.0.0");
  await once(server, "listening");
  const base = `http://127.0.0.1:${server.address().port}`;

  await t.test("accepts connections addressed to a network interface", async (t) => {
    const address = Object.values(networkInterfaces()).flat().find((entry) => entry.family === "IPv4" && !entry.internal)?.address;
    if (!address) return t.skip("No external IPv4 interface available");
    const response = await fetch(`http://${address}:${server.address().port}/designs/architecture.md`);
    assert.equal(response.status, 200);
    assert.match(await response.text(), /<h1 id="architecture">/);
  });
  await t.test("root redirects so relative document links resolve correctly", async () => {
    const response = await fetch(base, { redirect: "manual" });
    assert.equal(response.status, 302);
    assert.equal(response.headers.get("location"), "/designs/architecture.md");
  });
  await t.test("renders navigation, stable anchors, tables and escaped source", async () => {
    const response = await fetch(`${base}/designs/architecture.md`);
    const html = await response.text();
    assert.equal(response.status, 200);
    assert.match(html, /<h1 id="architecture">Architecture<\/h1>/);
    assert.match(html, /id="links-and-headings"/);
    assert.match(html, /id="links-and-headings-1"/);
    assert.match(html, /href="other.md#detail"/);
    assert.match(html, /href="\/designs\/other.md"/);
    assert.match(html, /aria-current="page"/);
    assert.match(html, /<table>/);
    assert.match(html, /&lt;script&gt;/);
    assert.doesNotMatch(html, /<script>alert/);
    assert.equal((html.match(/class="language-mermaid"/g) ?? []).length, 2);
    assert.match(html, /C4Context/);
    assert.match(html, /integrity="sha384-/);
    assert.match(html, /connect-src 'none'/);
    const nonce = /nonce="([^"]+)"/.exec(html)?.[1];
    assert.ok(nonce);
    assert.ok(html.includes(`style-src 'self' 'nonce-${nonce}'`));
    const next = await (await fetch(`${base}/designs/architecture.md`)).text();
    assert.notEqual(/nonce="([^"]+)"/.exec(next)?.[1], nonce);
  });
  await t.test("raw source and captured JSON stay readable", async () => {
    assert.equal(await (await fetch(`${base}/designs/architecture.md?raw=1`)).text(), source);
    assert.equal(await (await fetch(`${base}/capture.json`)).text(), '{"captured":true}');
  });
  await t.test("refresh discovers added documents", async () => {
    await writeFile(join(docsRoot, "new.md"), "# New\n");
    assert.match(await (await fetch(`${base}/designs/architecture.md`)).text(), /href="\/new.md"/);
  });
  await t.test("serves only explicit local assets", async () => {
    assert.equal(await (await fetch(`${base}/_docs/renderer.js`)).text(), "fixture-js");
    const client = await fetch(`${base}/_docs/client.js`);
    assert.match(client.headers.get("content-type"), /text\/javascript/);
    assert.match(await client.text(), /mountDiagram/);
    assert.equal((await fetch(`${base}/_docs/missing.js`)).status, 404);
  });
  await t.test("rejects traversal, symlink escapes, missing files and non-doc files", async () => {
    for (const path of ["/%2e%2e%2fsecret.md", "/escape.md", "/missing.md", "/package.json", "/.env", "/designs%5csecret.md"]) {
      assert.equal((await fetch(`${base}${path}`)).status, 404, path);
    }
    assert.equal((await fetch(`${base}/%ZZ.md`)).status, 400);
  });
  await t.test("rejects writes and foreign Host headers", async () => {
    assert.equal((await fetch(`${base}/designs/other.md`, { method: "POST" })).status, 405);
    const status = await new Promise((resolve, reject) => {
      const req = request(base, { headers: { Host: "foreign.example" } }, (res) => { res.resume(); resolve(res.statusCode); });
      req.on("error", reject);
      req.end();
    });
    assert.equal(status, 403);
  });
});
