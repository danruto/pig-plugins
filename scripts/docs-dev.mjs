import { createHash, randomBytes } from "node:crypto";
import { createServer } from "node:http";
import { mkdir, readFile, readdir, realpath, writeFile } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import MarkdownIt from "markdown-it";

const root = fileURLToPath(new URL("../", import.meta.url));
const hostFiles = fileURLToPath(new URL("./docs/", import.meta.url));
export const release = {
  version: "2.0.0",
  base: "https://raw.githubusercontent.com/danruto/aqua-cutter-releases/9be4e3d9cab518de4cae13471388ca516d47978f/releases/2.0.0/",
  assets: {
    "renderer.js": "sha384-ZPQAC+h+zWM5FQDJnZdPDWswSsPZXy+rJV1f5I7YXkzt232BwIqJ62hf4JVeWx4k",
    "renderer.css": "sha384-I3oN7FW+be8wlu5s6b4vNvTnrS7wWvffYxkPnTYTLn7rd41CgiM8e5ezZI6wHasH",
  },
};
const markdown = new MarkdownIt({ html: false });
const escape = markdown.utils.escapeHtml;

markdown.renderer.rules.heading_open = (tokens, index, options, env, renderer) => {
  const text = tokens[index + 1].children.map((token) => token.content).join("");
  const slug = text.toLowerCase().replace(/[^\p{L}\p{N}_\s-]/gu, "").trim().replace(/\s+/g, "-");
  const count = env.headings.get(slug) ?? 0;
  env.headings.set(slug, count + 1);
  tokens[index].attrSet("id", count ? `${slug}-${count}` : slug);
  return renderer.renderToken(tokens, index, options);
};
const renderFence = markdown.renderer.rules.fence;
markdown.renderer.rules.fence = (tokens, index, options, env, renderer) => {
  const token = tokens[index];
  if (token.info.trim() !== "mermaid") return renderFence(tokens, index, options, env, renderer);
  return `<section class="diagram"><p class="diagram-status" role="status">Diagram source — requires JavaScript and Aqua Cutter.</p><div class="diagram-reader"></div><pre><code class="language-mermaid">${escape(token.content)}</code></pre></section>`;
};

export function verifyAsset(bytes, integrity) {
  if (`sha384-${createHash("sha384").update(bytes).digest("base64")}` !== integrity) {
    throw new Error("Aqua Cutter asset integrity check failed");
  }
  return bytes;
}

export async function loadAssets() {
  const cache = join(root, "node_modules/.cache/pig-docs", release.version);
  await mkdir(cache, { recursive: true });
  return Object.fromEntries(await Promise.all(Object.entries(release.assets).map(async ([name, integrity]) => {
    const path = join(cache, name);
    let bytes;
    try {
      bytes = await readFile(path);
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      console.log(`Downloading Aqua Cutter ${release.version} ${name}`);
      const response = await fetch(`${release.base}${name}`, { signal: AbortSignal.timeout(30_000), redirect: "error" });
      if (!response.ok) throw new Error(`Renderer download failed: HTTP ${response.status}`);
      bytes = verifyAsset(Buffer.from(await response.arrayBuffer()), integrity);
      await writeFile(path, bytes);
    }
    return [name, verifyAsset(bytes, integrity)];
  })));
}

async function listDocs(directory, prefix = "") {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map(async (entry) => {
    const path = `${prefix}${entry.name}`;
    if (entry.isDirectory()) return listDocs(join(directory, entry.name), `${path}/`);
    return entry.isFile() && entry.name.endsWith(".md") ? [path] : [];
  }));
  return files.flat().sort();
}

export async function createDocsServer({ docsRoot = join(root, "docs"), assets } = {}) {
  const directory = await realpath(docsRoot);
  const template = await readFile(join(hostFiles, "index.html"), "utf8");
  const staticFiles = {
    "/_docs/client.js": [await readFile(join(hostFiles, "client.js")), "text/javascript"],
    "/_docs/style.css": [await readFile(join(hostFiles, "style.css")), "text/css"],
    "/_docs/renderer.js": [assets["renderer.js"], "text/javascript"],
    "/_docs/renderer.css": [assets["renderer.css"], "text/css"],
  };
  const server = createServer(async (request, response) => {
    const send = (status, type, body) => {
      response.writeHead(status, { "Content-Type": `${type}; charset=utf-8`, "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" });
      response.end(request.method === "HEAD" ? undefined : body);
    };
    try {
      const port = server.address().port;
      if (![ `127.0.0.1:${port}`, `localhost:${port}`, `${request.socket.localAddress}:${port}` ].includes(request.headers.host)) {
        return send(403, "text/plain", "Forbidden host");
      }
      if (!["GET", "HEAD"].includes(request.method)) return send(405, "text/plain", "Method not allowed");
      const url = new URL(request.url, `http://127.0.0.1:${port}`);
      if (staticFiles[url.pathname]) return send(200, staticFiles[url.pathname][1], staticFiles[url.pathname][0]);
      if (url.pathname === "/") {
        response.writeHead(302, { Location: "/designs/architecture.md" });
        return response.end();
      }
      const path = decodeURIComponent(url.pathname.slice(1));
      if (path.includes("\\") || path.includes("\0") || path.split("/").some((part) => part === "..") || !/\.(md|json)$/.test(path)) {
        return send(404, "text/plain", "Document not found");
      }
      const fullPath = await realpath(resolve(directory, path));
      if (!fullPath.startsWith(`${directory}${sep}`)) return send(404, "text/plain", "Document not found");
      const source = await readFile(fullPath, "utf8");
      if (url.searchParams.has("raw") || path.endsWith(".json")) return send(200, "text/plain", source);
      const docs = await listDocs(directory);
      const navigation = docs.map((doc) => `<a href="/${doc.split("/").map(encodeURIComponent).join("/")}"${doc === path ? ' aria-current="page"' : ""}>${escape(doc)}</a>`).join("\n");
      const title = /^#\s+(.+)$/m.exec(source)?.[1] ?? path;
      const values = {
        TITLE: escape(title), NAVIGATION: navigation,
        CONTENT: markdown.render(source, { headings: new Map() }),
        RAW_URL: `/${path.split("/").map(encodeURIComponent).join("/")}?raw=1`,
        JS_INTEGRITY: release.assets["renderer.js"], CSS_INTEGRITY: release.assets["renderer.css"],
        STYLE_NONCE: randomBytes(18).toString("base64"),
      };
      send(200, "text/html", template.replace(/__([A-Z_]+)__/g, (_, key) => values[key]));
    } catch (error) {
      if (["ENOENT", "ENOTDIR", "EISDIR"].includes(error.code)) return send(404, "text/plain", "Document not found");
      if (error instanceof URIError) return send(400, "text/plain", "Invalid path");
      console.error(error);
      send(500, "text/plain", "Docs preview failed");
    }
  });
  return server;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const server = await createDocsServer({ assets: await loadAssets() });
  const host = process.env.HOST ?? "127.0.0.1";
  server.listen(Number(process.env.PORT ?? 4173), host, () => {
    console.log(`Design docs: http://${host}:${server.address().port}/`);
    console.log("Refresh the browser after edits. Press Ctrl+C to stop.");
  });
}
