import { readFile, writeFile } from "node:fs/promises";
import { gunzipSync, gzipSync } from "node:zlib";

export async function readLedger(path) {
  return gunzipSync(await readFile(path));
}

export async function writeLedger(path, bytes) {
  await writeFile(path, gzipSync(bytes, { level: 9 }));
}
