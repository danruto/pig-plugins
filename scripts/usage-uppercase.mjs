/** Generates Unicode full-uppercase expansions from the installed ECMAScript runtime, not provider fixtures. */
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";

assert.equal(process.versions.unicode, "17.0");
const expansions = [];
for (let point = 0; point <= 0x10ffff; point++) {
  const upper = String.fromCodePoint(point).toUpperCase();
  if ([...upper].length > 1) {
    const escaped = [...upper].map(c => `\\U${c.codePointAt(0).toString(16).padStart(8, "0")}`).join("");
    expansions.push(`\t0x${point.toString(16)}: "${escaped}",`);
  }
}
const output = `//go:generate node ../../../../scripts/usage-uppercase.mjs

package usage

import (
\t"strings"
\t"unicode"
)

var jsUppercaseExpansions = map[rune]string{
${expansions.join("\n")}
}

func jsUppercase(text string) string {
\tvar upper strings.Builder
\tfor _, r := range text {
\t\tif expansion, ok := jsUppercaseExpansions[r]; ok {
\t\t\tupper.WriteString(expansion)
\t\t} else {
\t\t\tupper.WriteRune(unicode.ToUpper(r))
\t\t}
\t}
\treturn upper.String()
}
`;
const formatted = execFileSync("gofmt", { input: output, encoding: "utf8" });
const destination = new URL("../extensions/pig-status/internal/usage/consumer_uppercase.go", import.meta.url);
if (process.argv[2] === "--check") assert.equal(await readFile(destination, "utf8"), formatted);
else { assert.equal(process.argv.length, 2); await writeFile(destination, formatted); }
console.log(`Unicode ${process.versions.unicode} full uppercase: ${expansions.length} generated expansions`);
