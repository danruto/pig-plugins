/** Real pinned TypeScript lease peer; protocol-only harness, no coordinator algorithm. */
import assert from "node:assert/strict";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";
import { join } from "node:path";

const source =
  process.env.HERMES_PACKAGE || process.env.HERMES_PUBLICATION_SOURCE;
assert(
  source,
  "HERMES_PACKAGE or HERMES_PUBLICATION_SOURCE must name the pinned public executable source",
);
const { AtomicLockCoordinator } = await import(
  pathToFileURL(join(source, "src/store/atomic-lock-coordinator.ts")).href
);
let lease;
console.log(JSON.stringify({ ready: true, pid: process.pid }));
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  const start = performance.now();
  let renewed = false;
  if (request.action === "acquire") {
    lease = AtomicLockCoordinator.shared(request.database).tryAcquire(
      request.key,
      { staleMs: request.staleMS || 300_000 },
    );
  } else if (request.action === "renew") {
    renewed = lease?.renew() ?? false;
  } else if (request.action === "release") {
    lease?.release();
  } else if (request.action === "exit") {
    lease?.release();
    break;
  } else {
    throw new Error(`unrecognized peer action: ${request.action}`);
  }
  console.log(
    JSON.stringify({
      acquired: lease !== null && lease !== undefined,
      renewed,
      elapsedMS: Math.floor(performance.now() - start),
      error: "",
    }),
  );
}
