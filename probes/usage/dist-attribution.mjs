/** Strict pinned source-map coordinate attribution; no enclosing-symbol or nearby-line inference. */
import assert from "node:assert/strict";
import { SourceMap } from "node:module";
import { posix } from "node:path";

export function distSourceMap(path, payload, sources) {
  assert.equal(payload.version, 3);
  assert.equal(payload.sources.length, payload.sourcesContent.length);
  const names = new Map();
  for (const [index, name] of payload.sources.entries()) {
    const source = posix.normalize(posix.join(posix.dirname(path), name));
    assert.ok(source.startsWith("src/"), `Unexpected mapped source: ${source}`);
    assert.equal(payload.sourcesContent[index], sources.get(source), `Changed source-map content: ${source}`);
    names.set(name, source);
  }
  const map = new SourceMap(payload);
  return (parsed, offset) => {
    const { line, character } = parsed.getLineAndCharacterOfPosition(offset);
    const entry = map.findEntry(line, character);
    if (entry.generatedLine !== line || entry.generatedColumn > character) return undefined;
    const prefix = parsed.text.slice(offset - character + entry.generatedColumn, offset);
    if (prefix.trim() !== "") return undefined;
    const source = names.get(entry.originalSource);
    if (!source) return undefined;
    return { source, line: entry.originalLine + 1, character: entry.originalColumn };
  };
}
