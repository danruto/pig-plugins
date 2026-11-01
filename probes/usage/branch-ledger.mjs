/** Derive conservative source-arm and count-rule witnesses from V8 block coverage, classify public-API-seam outcomes out of scope and name the owner-decided baseline gaps; pending obligations, gaps included, are never exemptions. */
import { distSourceMap } from "./dist-attribution.mjs";
import { counterComposition, instrumentSources } from "./counter-replay.mjs";
import { readLedger, writeLedger } from "./ledger-artifact.mjs";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import * as ts from "typescript/unstable/ast";
import { API } from "typescript/unstable/sync";

const sha = (text) => createHash("sha256").update(text).digest("hex");
const json = async (path) => JSON.parse(await readFile(path, "utf8"));
const generatedBy = "GENERATED FROM CODE; DO NOT EDIT. Run node probes/usage-baseline.mjs --capture";

function interopNamespaces(parsed) {
  const names = new Set();
  function visit(node) {
    const call = ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer && ts.isAwaitExpression(node.initializer) ? node.initializer.expression : undefined;
    if (call && ts.isCallExpression(call) && ts.isIdentifier(call.expression) && call.expression.text === "jitiImport") names.add(node.name.text);
    node.forEachChild(visit);
  }
  visit(parsed);
  return names;
}

function importedBinding(node, namespaces) {
  let target = node;
  if (ts.isParenthesizedExpression(target) && ts.isBinaryExpression(target.expression) && target.expression.operatorToken.kind === ts.SyntaxKind.CommaToken
    && ts.isNumericLiteral(target.expression.left) && target.expression.left.text === "0") target = target.expression.right;
  return ts.isPropertyAccessExpression(target) && ts.isIdentifier(target.expression) && namespaces.has(target.expression.text) ? target.name.text : undefined;
}

function expressionIdentity(node, parsed, namespaces) {
  const binding = importedBinding(node, namespaces);
  if (binding) return [ts.SyntaxKind.Identifier, binding];
  if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isSatisfiesExpression(node) || ts.isNonNullExpression(node)) return expressionIdentity(node.expression, parsed, namespaces);
  const children = [];
  node.forEachChild((child) => { if (!ts.isTypeNode(child)) children.push(expressionIdentity(child, parsed, namespaces)); });
  return [node.kind, children.length ? children : node.getText(parsed)];
}

const logicalOperators = [ts.SyntaxKind.AmpersandAmpersandToken, ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken];
const logicalAssignments = [ts.SyntaxKind.AmpersandAmpersandEqualsToken, ts.SyntaxKind.BarBarEqualsToken, ts.SyntaxKind.QuestionQuestionEqualsToken];
const isLoop = (node) => ts.isForStatement(node) || ts.isForOfStatement(node) || ts.isForInStatement(node) || ts.isWhileStatement(node) || ts.isDoStatement(node);

function countRules(parsed) {
  const entry = (node, sign, role) => ({ sign, at: node.getStart(parsed), role });
  const abrupt = (statement) => ts.isReturnStatement(statement) || ts.isThrowStatement(statement) || ts.isBreakStatement(statement) || ts.isContinueStatement(statement)
    || (ts.isBlock(statement) && statement.statements.some(abrupt))
    || (ts.isIfStatement(statement) && Boolean(statement.elseStatement) && abrupt(statement.thenStatement) && abrupt(statement.elseStatement));
  const retained = (statement) => ts.isExpressionStatement(statement) || ts.isReturnStatement(statement) || ts.isThrowStatement(statement) || ts.isIfStatement(statement)
    || isLoop(statement) || ts.isTryStatement(statement) || ts.isSwitchStatement(statement) || ts.isBreakStatement(statement) || ts.isContinueStatement(statement)
    || (ts.isVariableStatement(statement) && statement.declarationList.declarations.every((declaration) => declaration.initializer));
  function continuation(statement) {
    if (!ts.isBlock(statement.parent)) return undefined;
    const next = statement.parent.statements[statement.parent.statements.indexOf(statement) + 1];
    if (!next || !retained(next) || next.getStart(parsed) <= statement.end) return undefined;
    return { sign: 1, at: next.getStart(parsed), role: "continuation", notAfter: statement.end };
  }
  function siblingReach(statement) {
    if (!ts.isBlock(statement.parent)) return undefined;
    const siblings = statement.parent.statements;
    for (const next of siblings.slice(siblings.indexOf(statement) + 1)) {
      const reached = ts.isIfStatement(next) ? outcomes(next.expression).any : undefined;
      if (reached) return reached.map((term) => ({ ...term, role: `later-if:${term.role}` }));
    }
    return undefined;
  }
  function after(statement) {
    const next = continuation(statement);
    return next ? [next] : siblingReach(statement);
  }
  function ifFalse(node) {
    let top = node;
    const arms = [node.thenStatement];
    while (ts.isIfStatement(top.parent) && top.parent.elseStatement === top) { top = top.parent; arms.push(top.thenStatement); }
    const reached = after(top);
    return reached && [...reached, ...arms.filter((arm) => !abrupt(arm)).map((arm) => entry(arm, -1, "then-normal-completion"))];
  }
  function breaks(node, found = []) {
    if (ts.isBreakStatement(node) && !node.label) found.push(entry(node, -1, "break"));
    else if (!isLoop(node) && !ts.isSwitchStatement(node) && !ts.isFunctionLikeDeclaration(node) && !ts.isClassLikeDeclaration(node) && !ts.isClassStaticBlockDeclaration(node)) node.forEachChild((child) => { breaks(child, found); });
    return found;
  }
  function normalExit(loop) {
    const reached = after(loop);
    return reached && [...reached, ...breaks(loop.statement)];
  }
  const split = (truthy, falsy) => ({ truthy, falsy, any: truthy || falsy ? [...truthy ?? [], ...falsy ?? []] : undefined });
  const assignmentPattern = (parent, node) => parent.operatorToken.kind === ts.SyntaxKind.EqualsToken && parent.left === node
    && (ts.isArrayLiteralExpression(node) || ts.isObjectLiteralExpression(node));
  function evaluatedWithParent(parent, node) {
    if (ts.isBinaryExpression(parent)) return !logicalOperators.includes(parent.operatorToken.kind) && !logicalAssignments.includes(parent.operatorToken.kind) && !assignmentPattern(parent, node);
    if (ts.isConditionalExpression(parent)) return parent.condition === node;
    if (ts.isPropertyAccessExpression(parent) || ts.isElementAccessExpression(parent) || ts.isCallExpression(parent) || ts.isNewExpression(parent) || ts.isTaggedTemplateExpression(parent)) {
      return parent.expression === node || !(parent.flags & ts.NodeFlags.OptionalChain);
    }
    if (ts.isPropertyAssignment(parent)) return parent.initializer === node;
    return ts.isAsExpression(parent) || ts.isSatisfiesExpression(parent) || ts.isNonNullExpression(parent) || ts.isAwaitExpression(parent) || ts.isTypeOfExpression(parent)
      || ts.isVoidExpression(parent) || ts.isPrefixUnaryExpression(parent) || ts.isPostfixUnaryExpression(parent) || ts.isSpreadElement(parent) || ts.isSpreadAssignment(parent)
      || ts.isTemplateSpan(parent) || ts.isTemplateExpression(parent) || ts.isArrayLiteralExpression(parent) || ts.isObjectLiteralExpression(parent);
  }
  function outcomes(node) {
    const parent = node.parent;
    if (ts.isIfStatement(parent) && parent.expression === node) return split([entry(parent.thenStatement, 1, "then-entry")], parent.elseStatement ? [entry(parent.elseStatement, 1, "else-entry")] : ifFalse(parent));
    if (ts.isConditionalExpression(parent) && parent.condition === node) return split([entry(parent.whenTrue, 1, "true-entry")], [entry(parent.whenFalse, 1, "false-entry")]);
    if ((ts.isWhileStatement(parent) && parent.expression === node) || (ts.isForStatement(parent) && parent.condition === node)) return split([entry(parent.statement, 1, "body-entry")], normalExit(parent));
    if (ts.isExpressionStatement(parent)) return { any: siblingReach(parent) };
    if (ts.isVariableDeclaration(parent) && parent.initializer === node && ts.isVariableStatement(parent.parent.parent)) return { any: siblingReach(parent.parent.parent) };
    if (ts.isParenthesizedExpression(parent)) return outcomes(parent);
    if (ts.isPrefixUnaryExpression(parent) && parent.operator === ts.SyntaxKind.ExclamationToken) {
      const negated = outcomes(parent);
      return { truthy: negated.falsy, falsy: negated.truthy, any: negated.any };
    }
    if (ts.isBinaryExpression(parent) && logicalOperators.includes(parent.operatorToken.kind)) {
      const combined = outcomes(parent);
      const operator = parent.operatorToken.kind;
      if (parent.left === node) {
        return { truthy: operator === ts.SyntaxKind.AmpersandAmpersandToken ? combined.truthy : undefined, falsy: operator === ts.SyntaxKind.BarBarToken ? combined.falsy : undefined, any: combined.any };
      }
      if (operator === ts.SyntaxKind.AmpersandAmpersandToken) return { truthy: combined.truthy, any: combined.truthy };
      if (operator === ts.SyntaxKind.BarBarToken) return { falsy: combined.falsy, any: combined.falsy };
      return {};
    }
    return evaluatedWithParent(parent, node) ? { any: outcomes(parent).any } : {};
  }
  function chainTop(node) {
    let top = node;
    while (((ts.isPropertyAccessExpression(top.parent) || ts.isElementAccessExpression(top.parent) || ts.isCallExpression(top.parent)) && top.parent.expression === top)
      || ts.isNonNullExpression(top.parent)) top = top.parent;
    return top;
  }
  function firstLink(node) {
    for (let base = node.expression; ts.isPropertyAccessExpression(base) || ts.isElementAccessExpression(base) || ts.isCallExpression(base) || ts.isNonNullExpression(base); base = base.expression) {
      if (base.questionDotToken) return false;
    }
    return true;
  }
  const rule = (name, terms) => terms && terms.some((term) => term.sign > 0) ? { rule: name, terms } : undefined;
  return (node, outcome) => {
    if (ts.isIfStatement(node) && outcome === "false" && !node.elseStatement) return rule("if-false-continuation", ifFalse(node));
    if (isLoop(node) && outcome === "normal-exit") return rule("loop-normal-exit", normalExit(node));
    if (isLoop(node) && outcome === "zero-iterations") { const exits = after(node); return rule("loop-zero-iterations", exits && [...exits, entry(node.statement, -1, "body-entry")]); }
    if (ts.isTryStatement(node) && outcome === "try-threw") return rule("try-threw-catch-entry", node.catchClause && [entry(node.catchClause.block, 1, "catch-entry")]);
    if (ts.isTryStatement(node) && outcome === "try-completed") {
      const reached = after(node);
      return rule("try-completed-continuation", reached && [...reached, ...(node.catchClause && !abrupt(node.catchClause.block) ? [entry(node.catchClause.block, -1, "catch-normal-completion")] : [])]);
    }
    if (ts.isBinaryExpression(node) && outcome === "rhs-skipped") { const total = outcomes(node).any; return rule("rhs-skipped-completions", total && [...total, entry(node.right, -1, "rhs-entry")]); }
    if (node.questionDotToken && outcome === "nullish" && firstLink(node)) {
      const total = outcomes(chainTop(node)).any;
      return rule("first-link-nullish-completions", total && [...total, { sign: -1, at: node.questionDotToken.getStart(parsed), role: "non-nullish-entry" }]);
    }
    return undefined;
  };
}

function mappingAnchor(node, parsed) {
  let target = node;
  while (ts.isParenthesizedExpression(target)) target = target.expression;
  if (target.kind === ts.SyntaxKind.QuestionDotToken) target = target.parent.name ?? target.parent.argumentExpression ?? target.parent.arguments?.[0];
  if (!target) return null;
  const undefinedValue = (ts.isIdentifier(target) && target.text === "undefined") || (ts.isVoidExpression(target) && ts.isNumericLiteral(target.expression) && target.expression.text === "0");
  return { start: target.getStart(parsed), kind: undefinedValue ? "undefined" : ts.SyntaxKind[target.kind] };
}

function locatorStart(node, parsed) {
  if (ts.isCatchClause(node)) return node.block.getStart(parsed);
  let target = node;
  for (let child = target.forEachChild((item) => item); child; child = target.forEachChild((item) => item)) target = child;
  return target.getStart(parsed);
}

export function extractObligations(parsed) {
  const branches = [];
  const countRule = countRules(parsed);
  const namespaces = interopNamespaces(parsed);
  function visit(node, owner = parsed, symbol = "<module>") {
    if (ts.isFunctionLikeDeclaration(node) && node.body) {
      owner = node;
      symbol = node.name?.getText(parsed) ?? `<anonymous@${node.getStart(parsed)}>`;
    }
    const outcomes = [];
    let condition;
    const arm = (outcome, body) => outcomes.push({ outcome, arm: body ? { start: body.getStart(parsed), end: body.end, kind: ts.SyntaxKind[body.kind], line: parsed.getLineAndCharacterOfPosition(body.getStart(parsed)).line + 1, anchor: mappingAnchor(body, parsed) } : null });
    if (ts.isIfStatement(node)) {
      condition = node.expression;
      arm("true", node.thenStatement); arm("false", node.elseStatement);
    } else if (ts.isConditionalExpression(node)) {
      condition = node.condition;
      arm("true", node.whenTrue); arm("false", node.whenFalse);
    } else if (ts.isCatchClause(node)) arm("entered", node.block);
    else if (ts.isCaseClause(node) || ts.isDefaultClause(node)) {
      condition = node.expression;
      arm("selected");
    } else if (ts.isForStatement(node) || ts.isForOfStatement(node) || ts.isForInStatement(node) || ts.isWhileStatement(node) || ts.isDoStatement(node)) {
      condition = node.condition ?? node.expression;
      arm("body-entered", node.statement);
      if (!ts.isDoStatement(node)) arm("zero-iterations");
      arm("normal-exit");
    } else if (ts.isBinaryExpression(node) && [ts.SyntaxKind.AmpersandAmpersandToken, ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken].includes(node.operatorToken.kind)) {
      condition = node;
      arm("rhs-evaluated", node.right); arm("rhs-skipped");
    } else if (node.questionDotToken) {
      condition = node;
      arm("non-nullish", node.questionDotToken); arm("nullish");
    } else if (ts.isTryStatement(node)) {
      arm("try-completed"); arm("try-threw");
    }
    for (const outcome of outcomes) {
      const count = outcome.arm ? undefined : countRule(node, outcome.outcome);
      if (count) outcome.count = count;
    }
    if (outcomes.length) {
      const start = node.getStart(parsed);
      const line = parsed.getLineAndCharacterOfPosition(start).line + 1;
      const kind = ts.SyntaxKind[node.kind];
      const input = condition?.getText(parsed) ?? (ts.isCatchClause(node) ? "exception" : kind);
      branches.push({ start, end: node.end, line, kind, input, symbol, symbolStart: owner.getStart(parsed), symbolSha256: sha(owner.getText(parsed)), branchSha256: sha(node.getText(parsed)),
        structuralKey: JSON.stringify(expressionIdentity(node, parsed, namespaces)), matchKey: `${kind}:${line}:${JSON.stringify(condition ? expressionIdentity(condition, parsed, namespaces) : input)}`,
        locator: locatorStart(node, parsed),
        anchor: ts.isBinaryExpression(node) ? mappingAnchor(node.right, parsed) : node.questionDotToken ? mappingAnchor(node.questionDotToken, parsed) : null, outcomes });
    }
    node.forEachChild((child) => visit(child, owner, symbol));
  }
  visit(parsed);
  return branches;
}

export function extractRuntimeEvidence(parsed) {
  assert.ok(parsed, "Missing runtime AST");
  const branches = extractObligations(parsed);
  const armEntries = branches.flatMap((branch) => branch.outcomes.flatMap((outcome) => outcome.arm ? [outcome.arm.start] : []));
  const boundaries = [];
  const implicitRegions = [];
  function visit(node) {
    const implicitBody = ts.isPropertyDeclaration(node) ? node.initializer : ts.isClassStaticBlockDeclaration(node) ? node.body : undefined;
    if (implicitBody) implicitRegions.push({ start: implicitBody.getStart(parsed), end: implicitBody.end,
      kind: ts.SyntaxKind[node.kind], sha256: sha(implicitBody.getText(parsed)) });
    if (ts.isFunctionLikeDeclaration(node) && node.body) {
      if (node.name && ts.isComputedPropertyName(node.name)) implicitRegions.push({ start: node.name.getStart(parsed), end: node.name.end,
        kind: ts.SyntaxKind[node.name.kind], sha256: sha(node.name.getText(parsed)) });
      const start = node.getStart(parsed), end = node.end;
      boundaries.push({ start, end, kind: ts.SyntaxKind[node.kind], sha256: sha(node.getText(parsed)),
        exclusionOnly: !branches.some((branch) => start <= branch.start && end >= branch.end)
          && !armEntries.some((entry) => start <= entry && entry < end) });
    }
    node.forEachChild(visit);
  }
  visit(parsed);
  boundaries.sort((a, b) => a.start - b.start || a.end - b.end);
  return { length: parsed.text.length, boundaries, implicitRegions, branches };
}

function validateFunctions(functions, length) {
  for (const fn of functions) {
    assert.equal(typeof fn.functionName, "string");
    assert.equal(typeof fn.isBlockCoverage, "boolean");
    assert.ok(fn.ranges.length > 0, "Missing V8 function range");
    for (const range of fn.ranges) assert.ok(Number.isSafeInteger(range.count) && range.count >= 0
      && Number.isSafeInteger(range.startOffset) && Number.isSafeInteger(range.endOffset)
      && range.startOffset >= 0 && range.startOffset < range.endOffset && range.endOffset <= length, "Invalid V8 range");
  }
}

function evidenceContract(observations, models) {
  const scripts = new Map(observations.scripts.map((script) => {
    assert.equal(sha(script.text), script.sha256, `Changed runtime identity: ${script.path}`);
    return [script.sha256, script];
  }));
  assert.equal(scripts.size, observations.scripts.length, "Duplicate runtime identity");
  assert.equal(new Set(observations.observations.map((item) => item.id)).size, observations.observations.length, "Duplicate observation window");
  return { ...observations,
    scripts: observations.scripts.map((script) => ({ ...script, ...models.get(script.sha256) })),
    observations: observations.observations.map((observation) => ({ ...observation,
      blocks: observation.blocks.map((block) => {
        assert.ok(scripts.has(block.script), `Dangling script: ${block.script}`);
        const model = models.get(block.script);
        validateFunctions(block.functions, model.length);
        return { ...block, functions: block.functions.filter((fn) => {
          const range = fn.ranges[0];
          const matches = model.boundaries.filter((boundary) => boundary.start === range.startOffset && boundary.end === range.endOffset);
          return !(fn.ranges.length === 1 && matches.length === 1 && matches[0].exclusionOnly);
        }) };
      }).filter((block) => block.functions.length > 0) })) };
}

export async function branchEvidenceContract(observations, root) {
  const directory = await mkdtemp(join(root, "branch-contract-"));
  const api = new API({ cwd: directory });
  let snapshot;
  try {
    const files = await Promise.all(observations.scripts.map(async (script, index) => {
      assert.equal(sha(script.text), script.sha256, `Changed runtime identity: ${script.path}`);
      const path = join(directory, `runtime-${index}.js`);
      await writeFile(path, script.text);
      return path;
    }));
    if (!files.length) return evidenceContract(observations, new Map());
    const config = join(directory, "tsconfig.json");
    await writeFile(config, JSON.stringify({ compilerOptions: { noResolve: true, allowJs: true }, files }));
    snapshot = api.updateSnapshot({ openProjects: [config] });
    const program = snapshot.getProject(config).program;
    for (const file of files) assert.deepEqual(program.getSyntacticDiagnostics(file), [], "Invalid runtime AST");
    const models = new Map(observations.scripts.map((script, index) => [script.sha256, extractRuntimeEvidence(program.getSourceFile(files[index]))]));
    return evidenceContract(observations, models);
  } finally { snapshot?.dispose(); api.close(); await rm(directory, { recursive: true, force: true }); }
}

function witnessIdentity(fn, range) {
  return { functionName: fn.functionName, functionStart: fn.ranges[0].startOffset, functionEnd: fn.ranges[0].endOffset,
    isBlockCoverage: fn.isBlockCoverage, range: { ...range } };
}

function rangeAt(functions, start, end, model) {
  assert.ok(model?.boundaries && model?.implicitRegions, "Static runtime execution regions required");
  const enclosing = functions.filter((fn) => fn.ranges[0].startOffset <= start && fn.ranges[0].endOffset >= end)
    .sort((a, b) => (a.ranges[0].endOffset - a.ranges[0].startOffset) - (b.ranges[0].endOffset - b.ranges[0].startOffset));
  const fn = enclosing[0];
  if (!fn?.isBlockCoverage || fn.ranges[0].count <= 0) return undefined;
  if (enclosing.filter((candidate) => candidate.ranges[0].startOffset === fn.ranges[0].startOffset
    && candidate.ranges[0].endOffset === fn.ranges[0].endOffset).length !== 1) return undefined;
  const owners = model.boundaries.filter((boundary) => boundary.start <= start && boundary.end >= end)
    .sort((a, b) => (a.end - a.start) - (b.end - b.start));
  const owner = owners[0] ?? { start: 0, end: model.length };
  if (model.implicitRegions.some((region) => region.start <= start && region.end >= end
    && !(region.start <= owner.start && region.end >= owner.end))) return undefined;
  if (owners.filter((candidate) => candidate.start === owner.start && candidate.end === owner.end).length > 1
    || fn.ranges[0].startOffset !== owner.start || fn.ranges[0].endOffset !== owner.end) return undefined;
  const ranges = fn.ranges.map((range, index) => ({ ...range, index })).filter((range) => range.startOffset <= start && range.endOffset >= end)
    .sort((a, b) => (a.endOffset - a.startOffset) - (b.endOffset - b.startOffset));
  const range = ranges[0];
  if (!range) return undefined;
  if (range.count > 0 && fn.ranges.some((other) => other.count === 0 && other.startOffset < end && other.endOffset > start)) return undefined;
  return { functionIndex: functions.indexOf(fn), rangeIndex: range.index, count: range.count, startOffset: range.startOffset, identity: witnessIdentity(fn, fn.ranges[range.index]) };
}

export function armWitness(functions, start, end, model) {
  const found = rangeAt(functions, start, end, model);
  if (!found || found.count <= 0) return undefined;
  const { startOffset: _startOffset, ...witness } = found;
  return witness;
}

function countWitness(functions, rule, model) {
  const terms = [];
  for (const term of rule.terms) {
    const found = rangeAt(functions, term.at, term.at + 1, model);
    if (!found || (terms.length && found.functionIndex !== terms[0].functionIndex)) return undefined;
    if (term.notAfter !== undefined && found.startOffset > term.notAfter) return undefined;
    terms.push({ role: term.role, sign: term.sign, at: term.at, functionIndex: found.functionIndex, rangeIndex: found.rangeIndex, count: found.count, identity: found.identity });
  }
  const lowerBound = terms.reduce((sum, term) => sum + term.sign * term.count, 0);
  return lowerBound > 0 ? { rule: rule.rule, lowerBound, terms } : undefined;
}

function counterWindows(observations, manifest, obligationById, sourceTexts) {
  if (!observations.counterManifest) {
    assert.ok(observations.observations.every((window) => window.counters === undefined), "Counter window without a counter manifest");
    return undefined;
  }
  assert.deepEqual(observations.counterManifest, manifest, "Counter manifest differs from the instrumentation of the pinned source");
  const sourceIds = [...obligationById.values()].filter((item) => item.source.startsWith("src/")).map((item) => item.id).sort();
  assert.deepEqual([...manifest.counters, ...manifest.uninstrumented.map((item) => item.id)].sort(), sourceIds, "Counter manifest does not cover each backend source outcome exactly once");
  assert.ok(Object.keys(manifest.sourceFiles).every((file) => file.startsWith("src/") && sourceTexts.has(file)));
  return new Map(observations.observations.map((window, windowIndex) => {
    if (window.counters === undefined) return [window.id, new Map()];
    assert.equal(window.counters.composition, counterComposition, `Invalid counter window: ${window.id}`);
    let previous = -1;
    const counts = new Map(window.counters.counts.map(([index, count], entry) => {
      assert.ok(Number.isSafeInteger(index) && index > previous && index < manifest.counters.length && Number.isSafeInteger(count) && count > 0, `Invalid counter entry: ${window.id}`);
      previous = index;
      return [manifest.counters[index], { index, count, pointer: `branch-observations.json#/observations/${windowIndex}/counters/counts/${entry}` }];
    }));
    return [window.id, counts];
  }));
}

function group(source) {
  if (source.startsWith("src/providers/")) return "provider-normalization-and-adapters";
  if (["src/query.ts", "src/auth-resolution-guard.ts", "src/oauth-credential-source.ts", "src/openai-companion-auth.ts"].includes(source)) return "dispatch-host-auth-and-transport";
  if (source.includes("settings")) return "settings-and-persistence";
  if (source.startsWith("src/codex-")) return "fast-and-resets";
  if (source.startsWith("src/usage-api")) return "public-api";
  if (["src/usage.ts", "src/core.ts", "src/usage-targets.ts"].includes(source)) return "commands-cache-and-lifecycle";
  return "normalization-consumers-and-display";
}

const publicApiDecision = "docs/adr/0004-no-public-usage-event-api.md";
const footerConsumer = "status/usage.ts";
const namedGapDecision = "docs/compatibility/usage.md#named-gaps";
const listenerErrorGaps = new Map([
  ["OAUTH_CREDENTIAL_SOURCE_CHANNEL", {
    reason: "Named gap: PiG runs every event listener inside a handler that catches and logs its error, so emitting oauth:credential-source:v1 cannot throw for any registered listener and credential-candidate discovery failure is unreachable in the baseline; the native port must prove fail-closed discovery at its Go seam",
    nativeObligation: "Prove fail-closed OAuth credential-candidate discovery at the native Go seam" }],
  ["OAUTH_CREDENTIAL_READINESS_CHANNEL", {
    reason: "Named gap: PiG runs every event listener inside a handler that catches and logs its error, so emitting oauth:credential-readiness:v1 cannot throw for any registered listener and the readiness emit's catch is unreachable in the baseline; readiness failure itself is witnessed through a rejected waitUntil; the native port must prove fail-closed readiness at its Go seam",
    nativeObligation: "Prove fail-closed OAuth credential readiness, including a failing readiness emit, at the native Go seam" }],
]);

function emitFailureGaps(parsed) {
  const gaps = [];
  function visit(node) {
    const only = ts.isTryStatement(node) && node.catchClause && node.tryBlock.statements.length === 1 ? node.tryBlock.statements[0] : undefined;
    const call = only && ts.isExpressionStatement(only) && ts.isCallExpression(only.expression) ? only.expression : undefined;
    const callee = call?.expression;
    const gap = call?.arguments[0] && ts.isIdentifier(call.arguments[0]) ? listenerErrorGaps.get(call.arguments[0].text) : undefined;
    if (gap && ts.isPropertyAccessExpression(callee) && callee.name.text === "emit" && ts.isPropertyAccessExpression(callee.expression) && callee.expression.name.text === "events") {
      gaps.push({ start: node.getStart(parsed), kind: "TryStatement", outcome: "try-threw", ...gap }, { start: node.catchClause.getStart(parsed), kind: "CatchClause", outcome: "entered", ...gap });
    }
    node.forEachChild(visit);
  }
  visit(parsed);
  return gaps;
}

const footerReport = "the footer sends only refresh:false usage:query:v1 requests, and the pinned backend settles every refresh:false query with an unavailable result (not-ready, no-model, unsupported, cache-miss at usage-api.ts:278, or a cancellation reason) before it reads any cache, so the footer never receives a status \"ready\" report";
const footerObligation = "Define and prove the native footer's report rendering (report-to-window conversion, expiry filter, deadline timer and failure handling) at the Go collector-to-footer seam from the design";
const kimiObligation = "Keep an equivalent guard in the native Kimi Coding normalizer";
const pinnedGapSources = { "status/usage.ts": "893e2e41223e01ae4c82ae0260b046cd7ba6482e2f6adffe8af27f4fe88aef8b", "src/providers/kimi-coding.ts": "62a675d812755261cdbd6871dad49f197e0fabf15dbede64e9cc3cb93900cdb2", "src/codex-resets.ts": "24d491223027ea217a2fe97830e80ae0d88fabcf43f44f2afd4bae64e9654656" };
const ownerGaps = [
  { source: "status/usage.ts", lines: [46, 66], outcomes: 30, nativeObligation: footerObligation,
    reason: `Named gap: finite and reportWindows run only for a ready report: reportWindows is called only inside the line 140 ready branch, and finite elsewhere only in that branch's condition after status === "ready"; ${footerReport}` },
  { source: "status/usage.ts", line: 111, kind: "BinaryExpression", outcomes: 4, nativeObligation: footerObligation,
    reason: `Named gap: publish receives only [] (lines 144, 147 and 152) or its own kept windows through the expiry timer, which line 114 arms only for a non-empty list, so the filter callback never runs; ${footerReport}` },
  { source: "status/usage.ts", line: 113, kind: "BinaryExpression", outcomes: 2, nativeObligation: footerObligation,
    reason: `Named gap: publish only ever keeps an empty window list, so the deadline map callback never runs; ${footerReport}` },
  { source: "status/usage.ts", line: 114, kind: "IfStatement", outcome: "false", outcomes: 1, nativeObligation: footerObligation,
    reason: `Named gap: deadline is Math.min over an always-empty window list, which is +Infinity, so !Number.isFinite(deadline) is always true; ${footerReport}` },
  { source: "status/usage.ts", line: 140, kind: "IfStatement", outcome: "true", outcomes: 1, nativeObligation: footerObligation,
    reason: `Named gap: the ready branch needs response.status === "ready"; ${footerReport}` },
  { source: "status/usage.ts", line: 139, kind: "PropertyAccessExpression", outcome: "nullish", input: "response?.report", outcomes: 1, nativeObligation: footerObligation,
    reason: "Named gap: response is record() of the value the backend's offered promise resolves to, which is always a plain result object (failure() at usage-api.ts:119-121), so response is never undefined and response?. never short-circuits" },
  { source: "status/usage.ts", line: 140, kind: "PropertyAccessExpression", outcome: "nullish", input: "response?.status", outcomes: 1, nativeObligation: footerObligation,
    reason: "Named gap: response is record() of the value the backend's offered promise resolves to, which is always a plain result object (failure() at usage-api.ts:119-121), so response is never undefined and response?. never short-circuits" },
  { source: "status/usage.ts", line: 147, outcomes: 4, nativeObligation: footerObligation,
    reason: "Named gap: the backend offers a promise that only its own resolve function settles (usage-api.ts:185-188) and nothing rejects it, so the footer's rejection handler never runs" },
  { source: "status/usage.ts", line: 134, kind: "IfStatement", outcome: "true", outcomes: 1, nativeObligation: footerObligation,
    reason: "Named gap: the pinned backend calls respond exactly once per query, synchronously inside the footer's emit and before the footer can abort or replace that query's controller (usage-api.ts:190-191), so accepted and signal.aborted are both false at the call" },
  { source: "status/usage.ts", line: 134, kind: "BinaryExpression", outcome: "rhs-skipped", outcomes: 1, nativeObligation: footerObligation,
    reason: "Named gap: skipping signal.aborted needs accepted to be true at a respond call, which only a second respond call for the same query can see; the pinned backend calls respond exactly once per query (usage-api.ts:190-191)" },
  { source: "status/usage.ts", line: 140, kind: "BinaryExpression", outcome: "rhs-evaluated", outcomes: 4, nativeObligation: footerObligation,
    reason: `Named gap: every right operand of the line 140 && chain runs only after its leftmost operand, response?.status === "ready", is true; ${footerReport}` },
  { source: "status/usage.ts", line: 140, kind: "PropertyAccessExpression", input: "ctx.model?.provider", outcomes: 2, nativeObligation: footerObligation,
    reason: `Named gap: ctx.model?.provider on line 140 is the right operand of report.providerId === ..., which runs only after response?.status === "ready" and report are both truthy; ${footerReport}` },
  { source: "src/providers/kimi-coding.ts", line: 134, kind: "PropertyAccessExpression", outcome: "nullish", input: "row?.reset_time", outcomes: 1, nativeObligation: kimiObligation,
    reason: "Named gap: line 133 returns unless ratio, which is row?.used_ratio, is a number, so row is an object whenever line 134 runs and row?.reset_time never short-circuits" },
  { source: "src/providers/kimi-coding.ts", line: 180, kind: "IfStatement", outcome: "true", outcomes: 1, nativeObligation: kimiObligation,
    reason: "Named gap: currencies holds only parseMoney currencies, which asCurrency limits to /^[A-Z]{3}$/ and parseMoney drops when empty; with exactly one member the currency is a non-empty string, so !currency is never true" },
  ...[[183, "IfStatement", "true"], [183, "BinaryExpression", "rhs-skipped"], [232, "ConditionalExpression", "false"], [232, "BinaryExpression", "rhs-skipped"]].map(([line, kind, outcome]) => ({
    source: "src/providers/kimi-coding.ts", line, kind, outcome, outcomes: 1, nativeObligation: kimiObligation,
    reason: "Named gap: fixedPointToMajor receives only asPositiveInteger or asNonnegativeInteger values, which are safe non-negative integers; dividing by 1,000,000 and rounding keeps a safe integer and dividing by 100 keeps it finite, so fixedPointToMajor never returns undefined" })),
  { source: "src/codex-resets.ts", line: 103, kind: "IfStatement", outcome: "true", outcomes: 1, nativeObligation: "Keep an equivalent missing-adapter guard in the native Codex reset authentication",
    reason: "Named gap: adapter is adapterForProvider(\"openai-codex\"), a lookup in the module constant SUPPORTED_ADAPTERS, which always contains the openai-codex adapter (query.ts:117) and which no backend code mutates, so !adapter is never true" },
];
const ownerGapMatches = (gap, obligation) => obligation.source === gap.source && obligation.sourceSha256 === pinnedGapSources[gap.source]
  && (gap.lines ? gap.lines[0] <= obligation.line && obligation.line <= gap.lines[1] : obligation.line === gap.line)
  && (gap.kind === undefined || obligation.kind === gap.kind) && (gap.outcome === undefined || obligation.outcome === gap.outcome)
  && (gap.input === undefined || obligation.input === gap.input);

function publicApiSeams(parsed, file) {
  if (/^src\/usage-api(-types)?\.ts$/u.test(file)) return [{ start: 0, end: parsed.text.length, reason: "Public usage:*:v1 event API module" }];
  const seams = [];
  const handles = new Set();
  const chains = [];
  function visit(node) {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === "registerUsageApi") {
      const target = ts.isBinaryExpression(node.parent) && node.parent.operatorToken.kind === ts.SyntaxKind.EqualsToken ? node.parent.left
        : ts.isVariableDeclaration(node.parent) ? node.parent.name : undefined;
      if (target && ts.isIdentifier(target)) handles.add(target.text);
    }
    const listener = ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) && node.expression.name.text === "on" && node.arguments.length === 2 ? node.arguments : undefined;
    if (file !== footerConsumer && listener && ts.isStringLiteral(listener[0]) && /^usage:[a-z]+:v1$/u.test(listener[0].text) && (ts.isArrowFunction(listener[1]) || ts.isFunctionExpression(listener[1]))) {
      seams.push({ start: listener[1].getStart(parsed), end: listener[1].end, reason: `Listener for the public ${listener[0].text} event` });
    }
    if (node.questionDotToken && ts.isIdentifier(node.expression)) chains.push(node);
    node.forEachChild(visit);
  }
  visit(parsed);
  for (const node of chains) {
    if (handles.has(node.expression.text)) seams.push({ start: node.getStart(parsed), end: node.end, reason: "Optional call through the public usage event API registration handle" });
  }
  return seams.sort((a, b) => a.start - b.start);
}

export function evidenceAt(artifacts, pointer) {
  const [file, fragment] = pointer.split("#");
  assert.ok(Object.hasOwn(artifacts, file), `Unknown evidence artifact: ${pointer}`);
  assert.ok(fragment?.startsWith("/"), `Invalid evidence pointer: ${pointer}`);
  let value = artifacts[file];
  for (const part of fragment.slice(1).split("/")) {
    const key = part.replaceAll("~1", "/").replaceAll("~0", "~");
    assert.ok(value !== null && typeof value === "object" && Object.hasOwn(value, key), `Dangling evidence: ${pointer}`);
    value = value[key];
  }
  return value;
}

function sameOrigin(origin, source, parsed, start) {
  const position = parsed.getLineAndCharacterOfPosition(start);
  return Boolean(origin) && origin.source === source && origin.line === position.line + 1 && origin.character === position.character;
}

export async function buildBranchLedger(project, root, inventory, observations, expected, provenance) {
  const backend = join(project, "node_modules/@narumitw/pi-usage");
  const pinned = { ...provenance.dependencies["node_modules/@narumitw/pi-usage"].installed.files,
    ...(provenance.packageInputs ? { "status/usage.ts": { sha256: provenance.packageInputs["status/usage.ts"] } } : {}) };
  const files = Object.keys(pinned).filter((name) => (name.startsWith("src/") || name === "status/usage.ts") && name.endsWith(".ts")).sort();
  const sourceFiles = files.map((file) => join(file.startsWith("src/") ? backend : project, file));
  for (const [index, file] of files.entries()) assert.equal(sha(await readFile(sourceFiles[index])), pinned[file].sha256, `Changed source identity: ${file}`);
  const distFiles = [...new Set(observations.scripts.filter((script) => script.path.startsWith("dist/")).map((script) => script.path))].sort();
  const distPaths = distFiles.map((file) => join(backend, file));
  const sourceTexts = new Map(await Promise.all(files.filter((file) => file.startsWith("src/")).map(async (file) => [file, await readFile(join(backend, file), "utf8")])));
  const distMaps = new Map();
  for (const file of distFiles) {
    assert.equal(sha(await readFile(join(backend, file))), pinned[file]?.sha256, `Changed installed dist: ${file}`);
    const mapPath = `${file}.map`;
    const raw = await readFile(join(backend, mapPath), "utf8");
    assert.equal(sha(raw), pinned[mapPath]?.sha256, `Changed installed source map: ${mapPath}`);
    distMaps.set(file, distSourceMap(file, JSON.parse(raw), sourceTexts));
  }
  const runtimeFiles = [];
  const scripts = new Map();
  for (const script of observations.scripts) {
    assert.ok(files.includes(script.path) || distFiles.includes(script.path), `Invented runtime source: ${script.path}`);
    assert.equal(sha(script.text), script.sha256, `Changed runtime identity: ${script.path}`);
    assert.ok(!scripts.has(script.sha256), "Duplicate runtime identity");
    scripts.set(script.sha256, script);
    const path = join(root, `runtime-${runtimeFiles.length}.js`);
    await writeFile(path, script.text);
    runtimeFiles.push(path);
  }
  const config = join(root, "branch-ledger-tsconfig.json");
  await writeFile(config, JSON.stringify({ compilerOptions: { noResolve: true, allowJs: true }, files: [...sourceFiles, ...distPaths, ...runtimeFiles] }));
  const api = new API({ cwd: root });
  let snapshot;
  try {
    snapshot = api.updateSnapshot({ openProjects: [config] });
    const program = snapshot.getProject(config).program;
    const sourceBranches = new Map(files.map((file, index) => [file, extractObligations(program.getSourceFile(sourceFiles[index]))]));
    const seams = new Map(files.map((file, index) => [file, publicApiSeams(program.getSourceFile(sourceFiles[index]), file)]));
    const gaps = new Map(files.map((file, index) => [file, emitFailureGaps(program.getSourceFile(sourceFiles[index]))]));
    const distParsed = new Map(distFiles.map((file, index) => [file, program.getSourceFile(distPaths[index])]));
    const installedBranches = new Map(distFiles.map((file) => [file, extractObligations(distParsed.get(file))]));
    for (const file of runtimeFiles) assert.deepEqual(program.getSyntacticDiagnostics(file), [], "Invalid runtime AST");
    const runtimeModels = new Map(observations.scripts.map((script, index) => [script.sha256, extractRuntimeEvidence(program.getSourceFile(runtimeFiles[index]))]));
    const contract = evidenceContract(observations, runtimeModels);
    const obligations = [];
    for (const [file, branches] of sourceBranches) {
      for (const branch of branches) {
        for (const outcome of branch.outcomes) {
          obligations.push({ id: `${file}:${branch.start}:${branch.kind}:${branch.branchSha256}:${outcome.outcome}`,
            group: group(file), source: file, sourceSha256: pinned[file].sha256,
            symbol: branch.symbol, symbolStart: branch.symbolStart, symbolSha256: branch.symbolSha256,
            line: branch.line, start: branch.start, end: branch.end, kind: branch.kind, branchSha256: branch.branchSha256, input: branch.input,
            outcome: outcome.outcome, sourceArm: outcome.arm, expected: `Observe ${outcome.outcome} at this exact source branch; preserve the fixture result and side effects.`,
            owningCheckpoint: inventory.symbols.find((item) => item.source === file && item.symbol === branch.symbol)?.checkpoint ?? (group(file) === "settings-and-persistence" || group(file) === "fast-and-resets" ? 4 : file.startsWith("src/providers/") ? 2 : file === "status/usage.ts" ? 5 : 3),
            evidence: [] });
        }
      }
    }
    for (const branch of inventory.branches ?? []) {
      assert.ok(obligations.some((item) => item.source === branch.source && item.line === branch.line && item.kind === branch.kind && item.branchSha256 === branch.branchSha256), `Dropped syntactic obligation: ${branch.source}:${branch.line}`);
    }
    const obligationById = new Map(obligations.map((item) => [item.id, item]));
    assert.equal(obligationById.size, obligations.length, "Duplicate source outcome identity");
    const counterManifest = observations.counterManifest
      ? (await instrumentSources(Object.fromEntries([...sourceTexts].filter(([file]) => files.includes(file))), root)).manifest : undefined;
    const counters = counterWindows(observations, counterManifest, obligationById, sourceTexts);
    const counted = new Set(counterManifest?.counters);
    const fixtureById = new Map(inventory.semanticFixtures.map((fixture) => [fixture.id, fixture]));
    assert.equal(fixtureById.size, inventory.semanticFixtures.length);
    assert.equal(new Set(observations.observations.map((item) => item.id)).size, observations.observations.length, "Duplicate observation window");
    assert.deepEqual([...fixtureById.keys()].sort(), observations.observations.map((item) => item.id).sort(), "Missing or invented fixture window");
    const artifacts = { "expected.json": expected, "branch-observations.json": observations };
    const fixtureMappings = [];
    const armlessMatches = new Map();
    for (const [fixtureIndex, fixture] of inventory.semanticFixtures.entries()) {
      assert.equal(fixture.sourceBranchEvidence, `branch-ledger.json#/fixtureMappings/${fixtureIndex}`, `Dangling semantic fixture/branch link: ${fixture.id}`);
      const observationIndex = observations.observations.findIndex((item) => item.id === fixture.id);
      const observation = observations.observations[observationIndex];
      const output = fixture.evidence;
      for (const pointer of output) evidenceAt(artifacts, pointer);
      const baseId = fixture.id.replace(/^presentation:/u, "");
      const table = fixture.id.startsWith("lifecycle:") ? "lifecycle" : fixture.id.startsWith("protocol:") ? "protocol" : expected.results?.some((item) => item.id === baseId) ? "results" : "consumers";
      const implementations = ["protocol", "lifecycle"].includes(table) ? [undefined] : ["source", "dist"];
      const pointers = implementations.map((implementation) => {
        const matches = expected[table].flatMap((item, index) => item.id === baseId && item.implementation === implementation ? [index] : []);
        assert.equal(matches.length, 1, `Missing or ambiguous fixture output: ${fixture.id}/${implementation}`);
        return `expected.json#/${table}/${matches[0]}${fixture.id.startsWith("presentation:") ? "/presentation" : ""}`;
      });
      assert.deepEqual(output, pointers, `Invented fixture/output association: ${fixture.id}`);
      const requestId = observation.id.startsWith("lifecycle:") ? observation.id : observation.id.startsWith("protocol:") ? `protocol/${observation.id}` : `source/${observation.id}`;
      const requests = expected.requests.flatMap((item, index) => item.id === requestId ? [`expected.json#/requests/${index}`] : []);
      const mapping = { fixtureId: fixture.id, capturedAtCheckpoint: 1, inputs: fixture.inputs, output, requests,
        sourceBranchOutcomes: [], productionDistBranchOutcomes: [], scope: "Observed source-arm lower bound, not a complete execution trace or semantic-combination proof" };
      for (const [blockIndex, block] of observation.blocks.entries()) {
        const script = scripts.get(block.script);
        assert.ok(script, `Dangling script: ${block.script}`);
        validateFunctions(block.functions, script.text.length);
        const dist = script.path.startsWith("dist/");
        assert.ok(block.composition === undefined || (block.composition === "source-replay" && script.path.startsWith("src/") && fixture.id.startsWith("lifecycle:")), `Invalid composition block: ${fixture.id}`);
        for (const installedBranch of (dist ? installedBranches : sourceBranches).get(script.path)) {
          let candidates = runtimeModels.get(block.script).branches.filter((item) => item.matchKey === installedBranch.matchKey);
          if (dist && candidates.length !== 1) {
            const uniqueInstalled = installedBranches.get(script.path).filter((item) => item.structuralKey === installedBranch.structuralKey);
            candidates = uniqueInstalled.length === 1 ? runtimeModels.get(block.script).branches.filter((item) => item.structuralKey === installedBranch.structuralKey) : [];
          }
          if (dist && candidates.length === 0) continue;
          if (dist && candidates.length === 1 && candidates[0].matchKey !== installedBranch.matchKey && candidates[0].structuralKey !== installedBranch.structuralKey) continue;
          if (candidates.length !== 1) continue;
          const runtime = candidates[0];
          let sourcePath = script.path;
          let branch = installedBranch;
          if (dist) {
            const startOrigin = distMaps.get(script.path)(distParsed.get(script.path), installedBranch.start);
            const origin = startOrigin ?? distMaps.get(script.path)(distParsed.get(script.path), installedBranch.locator);
            if (!origin) continue;
            const parsed = program.getSourceFile(join(backend, origin.source));
            let matches = sourceBranches.get(origin.source)?.filter((item) => item.kind === installedBranch.kind && sameOrigin(origin, origin.source, parsed, startOrigin ? item.start : item.locator)) ?? [];
            if (installedBranch.anchor) {
              const anchorOrigin = distMaps.get(script.path)(distParsed.get(script.path), installedBranch.anchor.start);
              matches = matches.filter((item) => item.anchor?.kind === installedBranch.anchor.kind && sameOrigin(anchorOrigin, origin.source, parsed, item.anchor.start));
            }
            if (matches.length !== 1) continue;
            sourcePath = origin.source;
            branch = matches[0];
          }
          for (const outcome of branch.outcomes) {
            const runtimeOutcome = runtime.outcomes.find((item) => item.outcome === outcome.outcome);
            const installedOutcome = installedBranch.outcomes.find((item) => item.outcome === outcome.outcome);
            const attribution = dist ? { implementation: "production-dist", installedScript: script.path, installedBranchStart: installedBranch.start, ...(installedOutcome?.arm ? { installedArmStart: installedOutcome.arm.start } : {}), runtimeMatch: installedBranch.matchKey === runtime.matchKey ? "unique-retained-line-condition-AST" : "unique-complete-branch-AST", sourceMapSha256: pinned[`${script.path}.map`].sha256 } : { implementation: "source", ...(block.composition ? { composition: block.composition } : {}) };
            const obligation = obligationById.get(`${sourcePath}:${branch.start}:${branch.kind}:${branch.branchSha256}:${outcome.outcome}`);
            if (!outcome.arm) {
              if (!runtimeOutcome || runtimeOutcome.arm || !installedOutcome || installedOutcome.arm) continue;
              armlessMatches.set(obligation.id, armlessMatches.get(obligation.id) || Boolean(runtimeOutcome.count));
              const witness = runtimeOutcome.count && countWitness(block.functions, runtimeOutcome.count, runtimeModels.get(block.script));
              if (!witness) continue;
              const terms = witness.terms.map((term) => {
                const pointer = `branch-observations.json#/observations/${observationIndex}/blocks/${blockIndex}/functions/${term.functionIndex}/ranges/${term.rangeIndex}`;
                assert.equal(evidenceAt(artifacts, pointer).count, term.count);
                return { role: term.role, sign: term.sign, runtimeStart: term.at, v8Witness: term.identity, v8Range: pointer };
              });
              obligation.evidence.push({ fixtureId: fixture.id, ...attribution, runtimeScript: block.script, runtimeBranchStart: runtime.start, countRule: witness.rule, lowerBound: witness.lowerBound, terms, output, requests });
              mapping.sourceBranchOutcomes.push(obligation.id);
              if (dist) mapping.productionDistBranchOutcomes.push(obligation.id);
              continue;
            }
            if ( !runtimeOutcome?.arm || !installedOutcome?.arm || installedOutcome.arm.kind !== runtimeOutcome.arm.kind || (installedOutcome.arm.line !== runtimeOutcome.arm.line && (!dist || installedBranch.structuralKey !== runtime.structuralKey))) continue;
            if (dist) {
              if (!outcome.arm.anchor || !installedOutcome.arm.anchor || outcome.arm.anchor.kind !== installedOutcome.arm.anchor.kind) continue;
              const origin = distMaps.get(script.path)(distParsed.get(script.path), installedOutcome.arm.anchor.start);
              if (!sameOrigin(origin, sourcePath, program.getSourceFile(join(backend, sourcePath)), outcome.arm.anchor.start)) continue;
            } else if (outcome.arm.kind !== runtimeOutcome.arm.kind || outcome.arm.line !== runtimeOutcome.arm.line) continue;
            const witness = armWitness(block.functions, runtimeOutcome.arm.start, runtimeOutcome.arm.start + 1, runtimeModels.get(block.script));
            if (!witness) continue;
            const pointer = `branch-observations.json#/observations/${observationIndex}/blocks/${blockIndex}/functions/${witness.functionIndex}/ranges/${witness.rangeIndex}`;
            assert.ok(evidenceAt(artifacts, pointer).count > 0);
            obligation.evidence.push({ fixtureId: fixture.id, ...attribution, runtimeScript: block.script, runtimeBranchStart: runtime.start, runtimeArmStart: runtimeOutcome.arm.start, v8Witness: witness.identity, v8Range: pointer, output, requests });
            mapping.sourceBranchOutcomes.push(obligation.id);
            if (dist) mapping.productionDistBranchOutcomes.push(obligation.id);
          }
        }
      }
      mapping.sourceBranchOutcomes = [...new Set(mapping.sourceBranchOutcomes)].sort();
      mapping.productionDistBranchOutcomes = [...new Set(mapping.productionDistBranchOutcomes)].sort();
      fixtureMappings.push(mapping);
    }
    if (counters) {
      for (const obligation of obligations) {
        for (const evidence of obligation.evidence) {
          assert.ok(!counted.has(obligation.id) || counters.get(evidence.fixtureId).has(obligation.id), `Counter replay misses a V8-witnessed outcome: ${obligation.id} in ${evidence.fixtureId}`);
        }
      }
      for (const [fixtureIndex, fixture] of inventory.semanticFixtures.entries()) {
        const mapping = fixtureMappings[fixtureIndex];
        for (const [id, counter] of counters.get(fixture.id)) {
          const obligation = obligationById.get(id);
          assert.ok(obligation.source.startsWith("src/") && counted.has(id), `Counter witness outside the backend source: ${id}`);
          if (obligation.sourceArm || obligation.evidence.some((item) => item.composition !== counterComposition)) continue;
          assert.equal(evidenceAt(artifacts, counter.pointer)[1], counter.count);
          obligation.evidence.push({ fixtureId: fixture.id, implementation: "source", composition: counterComposition, counterManifestSha256: counterManifest.sha256,
            counterIndex: counter.index, count: counter.count, counterRange: counter.pointer, output: mapping.output, requests: mapping.requests });
          mapping.sourceBranchOutcomes = [...new Set([...mapping.sourceBranchOutcomes, id])].sort();
        }
      }
    }
    const remainingGroups = [];
    for (const gap of ownerGaps.filter((item) => pinned[item.source]?.sha256 === pinnedGapSources[item.source])) assert.equal(obligations.filter((obligation) => ownerGapMatches(gap, obligation)).length, gap.outcomes, `Owner named gap no longer matches its outcomes: ${gap.source}:${gap.line ?? gap.lines.join("-")}`);
    for (const obligation of obligations) {
      const seam = seams.get(obligation.source).find((item) => item.start <= obligation.start && obligation.end <= item.end);
      if (seam) {
        obligation.status = "out-of-scope";
        obligation.scopeDecision = { decision: publicApiDecision, reason: `${seam.reason}; the native collector feeds the footer in process and does not port this seam` };
        continue;
      }
      const gap = gaps.get(obligation.source).find((item) => item.start === obligation.start && item.kind === obligation.kind && item.outcome === obligation.outcome)
        ?? ownerGaps.find((item) => ownerGapMatches(item, obligation));
      assert.ok(!gap || !obligation.evidence.length, `Named gap outcome was observed: ${obligation.id}`);
      obligation.status = obligation.evidence.length ? "observed" : "pending";
      if (obligation.status === "observed") continue;
      if (gap) obligation.namedGap = { decision: namedGapDecision, nativeObligation: gap.nativeObligation };
      obligation.pendingReason = gap ? gap.reason : obligation.sourceArm
        ? "No positive uniquely source-matched arm-entry witness in the retained fixture windows"
        : counted.has(obligation.id) ? "No arm entry; the exact-counter replay counted this outcome zero times in every retained fixture window; requires an observing fixture, not an exemption"
        : !armlessMatches.has(obligation.id) ? "No arm entry; no uniquely matched runtime branch in the retained fixture windows, so no count rule could be evaluated; requires an observing fixture, not an exemption"
        : armlessMatches.get(obligation.id) ? "No arm entry; the sound count rule applies but no retained fixture window gives a positive lower bound; requires an observing fixture, not an exemption"
        : "No arm entry and no sound count rule at the matched runtime position; requires a stronger observation rule, not an exemption";
      const key = `${obligation.group}:${obligation.source}:${obligation.symbolStart}`;
      let remaining = remainingGroups.find((item) => item.key === key);
      if (!remaining) { remaining = { key, group: obligation.group, source: obligation.source, symbol: obligation.symbol, symbolStart: obligation.symbolStart, obligations: [] }; remainingGroups.push(remaining); }
      remaining.obligations.push(obligation.id);
    }
    return { generatedBy, formatVersion: 1, qualification: "INCOMPLETE; pending obligations block baseline qualification", baselineCommit: "c81190b09830f3be88c3489b447a30fad0444423",
      sourceFiles: Object.fromEntries(files.map((file) => [file, pinned[file].sha256])),
      observationRepresentation: observations.representation ?? "Raw detailed V8 ranges", observationSha256: sha(JSON.stringify(observations)), observationContractSha256: sha(JSON.stringify(contract)), expectedSha256: sha(JSON.stringify(expected)),
      method: "V8 detailed coverage in the unchanged Jiti execution; unique same-file/kind/retained-line/condition-AST match, where the condition AST ignores parentheses and the TypeScript-only syntax Jiti erases, and same-line/kind arm. Dist also permits a unique complete branch AST when Jiti collapses lines, followed by exact pinned source-map source branch/arm coordinates; only whitespace can separate a generated segment from its AST token. A branch whose start has no segment is located at its leftmost leaf token (a catch clause at its block); same-kind source branches sharing that coordinate must be separated by the exact mapped start of the logical right operand or optional-chain key, else the branch stays unmatched. Arms compare at their mapping anchor: parentheses are unwrapped, an optional link maps through its key token, and esbuild's void 0 equals the identifier undefined; any other kind change fails closed. Jiti interop references (0, _ns.name) and _ns.name, where _ns is declared from jitiImport, match the installed imported binding name. Positive innermost V8 range at the arm entry only, with exact static function correspondence; static boundaries exclude inheritance and never supply execution evidence. Implicit class initializer bodies cannot inherit enclosing execution; only independently matched explicit functions within them can qualify. Optional-chain non-nullish outcomes use the V8 counter that starts at each ?. link. Armless outcomes use count rules only: a lower bound summing exact arm or continuation counters and subtracting upper-bound counters must be positive within one exactly matched V8 function. A continuation counter is used only after an if, loop or try statement that is followed in the same braced block by a statement V8 retains, read at that statement's start from a range that begins no later than the statement end. Without one, reaching the statement's end is bounded below by the condition completions (then entry plus else or if-false bound) of the first later if statement in the same braced block with a positive bound; later loops never bound it, because their condition runs once per iteration. Rules: if-false = reached end minus normally completing then arms of its else-if chain; loop normal exit = reached end minus unlabeled breaks; zero iterations = reached end minus body entries; try completed = reached end minus a normally completing catch; try threw = catch entry; skipped RHS and first-link nullish = completions of the expression minus RHS or non-nullish entries. Expression completions come from an if, conditional or loop condition consumer, or from the later-if bound when the expression is a whole expression statement or a variable statement initializer, carried through parentheses, negation (swapping truthy and falsy), operands a completed parent always evaluated (non-short-circuit operators, call and new arguments outside optional chains, member objects, awaits, unary, template, array and object literal values outside a destructuring pattern, a conditional's condition) and logical operands: a left operand completes whenever its parent does; the right operand of && only on a truthy parent, of || only on a falsy parent, and of ?? never. Conditional arms, optional-chain arguments, logical assignments, nested functions and the target pattern of a destructuring assignment stop the walk, as do binding-pattern elements, parameters and for-in/of heads: a default inside a pattern runs only when its slot is undefined, so it never takes the completions of the statement that holds the pattern. No inference from scenario labels, function membership, fallthrough, or a missing else. Exact-counter replay (owner decision, 2026-10-31): every registered, provider, consumer and API fixture is replayed in one more disposable PiG process against a probe-only copy of the pinned backend src/ tree in which each branch outcome increments one counter; erasure of the instrumented AST must reproduce each pinned source byte for byte and map each counter to exactly one obligation, the replay's requests, RPC UI, receipts and results must equal the uninstrumented run, and every V8-witnessed outcome must have a positive counter in the same window. A positive per-window counter is a witness, tagged composition counter-replay, only for an armless src/ outcome with no V8 witness.",
      outOfScope: { decision: publicApiDecision, rule: "Obligations inside the public usage:*:v1 API module, inside a usage:*:v1 event listener outside the footer consumer (status/usage.ts), or on an optional call through the registerUsageApi handle are out of scope; their evidence is retained and they are excluded from the pending count. The footer's own listener holds its session/model fence, which stays in scope at the collector-to-footer seam." },
      namedGaps: { decision: namedGapDecision, rule: "The throw outcome and catch entry of a try whose only statement emits oauth:credential-source:v1 or oauth:credential-readiness:v1 stay pending with a named-gap reason: PiG contains listener errors, so no registered fixture can reach them. The owner-listed footer report-path outcomes in status/usage.ts and the defensive Kimi Coding and Codex reset guards stay pending, each with the invariant that makes it unreachable; every listed selector must match its exact outcome count, and a named-gap outcome with a witness fails the build." },
      limitations: ["Implicit instance/static field initializers and static blocks remain pending without independently matched explicit-function ownership; class field computed keys and heritage are separate eager regions; computed method, accessor and object-literal method keys stay pending without independently matched explicit-function ownership", "Installed-dist attribution requires exact pinned source-map branch and arm coordinates plus unchanged Jiti retained-line/condition AST or unique complete branch AST, and positive V8 arm entry; unmatched transformations stay pending: optional calls with no argument, rewritten negations, renamed imports, await-wrapped conditions and same-line duplicate conditions", "A witnessed outcome is not MC/DC, complete behavior, side-effect attribution, or a semantic-combination proof", "Armless outcomes without a positive count-rule lower bound or exact counter remain pending, not exempt; an armless src/ outcome the counter replay counted zero times in every window needs an observing fixture", "Exact counters exist only for the backend src/ tree; status/usage.ts and installed dist keep the V8 rules alone, and an armed outcome without a positive V8 arm witness stays pending whatever its counter", "Syntax is a floor: lifecycle interleavings, safety obligations and semantic combinations in the design still require consumer-derived fixtures"],
      obligations, fixtureMappings, remainingGroups };
  } finally { snapshot?.dispose(); api.close(); }
}

function rawWitness(observations, evidence, witness, v8Range) {
  const match = /^branch-observations\.json#\/observations\/(\d+)\/blocks\/(\d+)\/functions\/(\d+)\/ranges\/(\d+)$/u.exec(v8Range);
  assert.ok(match, `Invalid raw witness pointer: ${v8Range}`);
  const [, observationIndex, blockIndex, functionIndex] = match;
  const observation = observations.observations[observationIndex];
  const block = observation?.blocks[blockIndex];
  const fn = block?.functions[functionIndex];
  const range = evidenceAt({ "branch-observations.json": observations }, v8Range);
  assert.equal(observation.id, evidence.fixtureId, "Changed witness fixture");
  assert.equal(block.script, evidence.runtimeScript, "Changed witness script");
  assert.deepEqual(witness, witnessIdentity(fn, range), "Changed raw witness identity");
  assert.equal(fn.isBlockCoverage, true, "Non-block witness");
  return { functionStart: fn.ranges[0].startOffset, count: range.count };
}

export function branchLedgerIdentity(ledger, observations) {
  assert.equal(ledger.observationSha256, sha(JSON.stringify(observations)), "Changed raw observation receipt");
  const { observationSha256, ...identity } = ledger;
  return { ...identity, obligations: ledger.obligations.map((obligation) => ({ ...obligation,
    evidence: obligation.evidence.map((evidence) => {
      if (evidence.terms) {
        const raw = evidence.terms.map((term) => rawWitness(observations, evidence, term.v8Witness, term.v8Range));
        assert.equal(new Set(raw.map((item) => item.functionStart)).size, 1, "Count witness spans functions");
        assert.ok(evidence.terms.every((term) => term.sign === 1 || term.sign === -1), "Invalid count term sign");
        const lowerBound = raw.reduce((sum, item, index) => sum + evidence.terms[index].sign * item.count, 0);
        assert.equal(lowerBound, evidence.lowerBound, "Changed count lower bound");
        assert.ok(lowerBound > 0, "Non-positive count witness");
        return { ...evidence, terms: evidence.terms.map(({ v8Range: _v8Range, ...term }) => term) };
      }
      if (evidence.composition === counterComposition) {
        const { counterRange, ...association } = evidence;
        const [index, count] = evidenceAt({ "branch-observations.json": observations }, counterRange);
        assert.equal(observations.observations[Number(/^branch-observations\.json#\/observations\/(\d+)\//u.exec(counterRange)[1])].id, evidence.fixtureId, "Changed counter witness fixture");
        assert.deepEqual([index, count], [evidence.counterIndex, evidence.count], "Changed counter witness");
        assert.ok(count > 0, "Non-positive counter witness");
        return association;
      }
      const { v8Range, ...association } = evidence;
      assert.ok(rawWitness(observations, evidence, evidence.v8Witness, v8Range).count > 0, "Non-positive witness");
      return association;
    }) })) };
}

export async function branchLedger(project, root, inventory, provenance, capture, replay) {
  const retained = join(project, "docs/compatibility/usage");
  const observations = await json(join(retained, "branch-observations.json"));
  const expected = await json(join(retained, "expected.json"));
  const ledger = await buildBranchLedger(project, root, inventory, observations, expected, provenance);
  const path = join(retained, "branch-ledger.json.gz");
  if (capture) await writeLedger(path, JSON.stringify(ledger, null, 2) + "\n");
  else assert.deepEqual(ledger, JSON.parse(await readLedger(path)), "Source-to-fixture ledger drift");
  if (replay) {
    const actual = await buildBranchLedger(project, root, inventory, replay, expected, provenance);
    assert.deepEqual(branchLedgerIdentity(actual, replay), branchLedgerIdentity(ledger, observations), "Source-to-fixture replay drift");
  }
  console.log(`usage source ledger: ${ledger.obligations.filter((item) => item.status === "observed").length} observed outcomes; ${ledger.obligations.filter((item) => item.status === "pending").length} pending outcomes in ${ledger.remainingGroups.length} source-symbol groups (incomplete), ${ledger.obligations.filter((item) => item.namedGap).length} of them named gaps; ${ledger.obligations.filter((item) => item.status === "out-of-scope").length} out of scope (${publicApiDecision})`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  assert.equal(process.argv[2], "--check", "Use --check; capture only through usage-baseline.mjs");
  const project = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
  const scratch = join(project, ".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "ledger-"));
  try { await branchLedger(project, root, await json(join(project, "docs/compatibility/usage/inventory.json")), await json(join(project, "docs/compatibility/usage/provenance.json")), false); }
  finally { await rm(root, { recursive: true, force: true }); }
}
