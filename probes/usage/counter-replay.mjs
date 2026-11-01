/** Probe-only exact-counter replay of the pinned usage backend. A disposable copy of its src/ tree gets one counter per branch outcome: if and conditional conditions, logical right operands, optional links, catch entries, try completions and throws, loop bodies, loop exits and case selections. The copy keeps every source character in order and only adds counting code; an independent erasure of the instrumented AST must reproduce each source file byte for byte and map every counter to exactly one obligation identity, or instrumentation fails. An outcome whose position cannot be counted without changing behaviour is listed with its reason and gets no counter. */
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import * as ts from "typescript/unstable/ast";
import { API } from "typescript/unstable/sync";

const sha = (text) => createHash("sha256").update(text).digest("hex");
export const counterStoreSymbol = "usage-fixture-counters";
export const counterComposition = "counter-replay";
const counterHelpers = new Set(["__ucB", "__ucL", "__ucR", "__ucO", "__ucM", "__ucCatch", "__ucThrew", "__ucDone", "__ucBody", "__ucExit", "__ucCase"]);
const logicalModes = new Map([[ts.SyntaxKind.AmpersandAmpersandToken, 0], [ts.SyntaxKind.BarBarToken, 1], [ts.SyntaxKind.QuestionQuestionToken, 2]]);
const isLoop = (node) => ts.isForStatement(node) || ts.isForOfStatement(node) || ts.isForInStatement(node) || ts.isWhileStatement(node) || ts.isDoStatement(node);
const isChainLink = (node) => ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node) || ts.isCallExpression(node) || ts.isNonNullExpression(node);
const inChain = (node) => isChainLink(node) && Boolean(node.flags & ts.NodeFlags.OptionalChain);
const isFunctionBoundary = (node) => ts.isFunctionLikeDeclaration(node) || ts.isClassLikeDeclaration(node) || ts.isClassStaticBlockDeclaration(node);

function trailer(manifestSha256, total) {
  return `
function __ucStore() { const store = (globalThis[Symbol.for(${JSON.stringify(counterStoreSymbol)})] ??= { manifestSha256: ${JSON.stringify(manifestSha256)}, counts: new Float64Array(${total}) }); if (store.manifestSha256 !== ${JSON.stringify(manifestSha256)}) throw new Error("Counter replay manifest mismatch"); return store.counts; }
function __ucHit(index) { __ucStore()[index] += 1; return 1; }
function __ucB(value, whenTrue, whenFalse) { __ucHit(value ? whenTrue : whenFalse); return value; }
function __ucL(value, mode, skipped) { if (mode === 0 ? !value : mode === 1 ? Boolean(value) : value !== null && value !== undefined) __ucHit(skipped); return value; }
function __ucR(index) { return __ucHit(index); }
function __ucO(value, present, absent, rest) { if (value === null || value === undefined) { __ucHit(absent); return undefined; } __ucHit(present); return rest(value); }
function __ucM(target, key, present, absent, rest) { const method = target[key]; if (method === null || method === undefined) { __ucHit(absent); return undefined; } __ucHit(present); return rest(method, target); }
function __ucCatch(index) { __ucHit(index); }
function __ucThrew(index) { __ucHit(index); }
function __ucDone(index) { __ucHit(index); }
function __ucBody(index) { return __ucHit(index); }
function __ucExit(entered, broke, normal, zero) { if (broke) return; __ucHit(normal); if (!entered && zero >= 0) __ucHit(zero); }
function __ucCase(value, discriminant, index) { if (value === discriminant) __ucHit(index); return value; }
`;
}

function outcomesOf(node) {
  if (ts.isIfStatement(node) || ts.isConditionalExpression(node)) return ["true", "false"];
  if (ts.isCatchClause(node)) return ["entered"];
  if (ts.isCaseClause(node) || ts.isDefaultClause(node)) return ["selected"];
  if (isLoop(node)) return ts.isDoStatement(node) ? ["body-entered", "normal-exit"] : ["body-entered", "zero-iterations", "normal-exit"];
  if (ts.isBinaryExpression(node) && logicalModes.has(node.operatorToken.kind)) return ["rhs-evaluated", "rhs-skipped"];
  if (node.questionDotToken) return ["non-nullish", "nullish"];
  if (ts.isTryStatement(node)) return ["try-completed", "try-threw"];
  return [];
}

export function obligationIdentities(parsed, file) {
  const ids = [];
  const visit = (node) => {
    for (const outcome of outcomesOf(node)) ids.push(`${file}:${node.getStart(parsed)}:${ts.SyntaxKind[node.kind]}:${sha(node.getText(parsed))}:${outcome}`);
    node.forEachChild(visit);
  };
  visit(parsed);
  return ids;
}

function anchorKey(kind, outcome, start, end, mode = "") {
  return `${kind}|${outcome}|${start}|${end}|${mode}`;
}

function sourceAnchors(parsed, file) {
  const anchors = new Map();
  const add = (node, outcome, start, end, mode) => {
    const key = anchorKey(ts.SyntaxKind[node.kind], outcome, start, end, mode);
    anchors.set(key, [...anchors.get(key) ?? [], `${file}:${node.getStart(parsed)}:${ts.SyntaxKind[node.kind]}:${sha(node.getText(parsed))}:${outcome}`]);
  };
  const span = (node) => [node.getStart(parsed), node.end];
  const visit = (node) => {
    if (ts.isIfStatement(node) || ts.isConditionalExpression(node)) {
      const condition = ts.isIfStatement(node) ? node.expression : node.condition;
      for (const outcome of ["true", "false"]) add(node, outcome, ...span(condition));
    } else if (ts.isCatchClause(node)) add(node, "entered", node.block.getStart(parsed) + 1, node.block.getStart(parsed) + 1);
    else if (ts.isCaseClause(node)) add(node, "selected", ...span(node.expression));
    else if (isLoop(node)) {
      const body = node.statement;
      const bodyAt = ts.isBlock(body) ? body.getStart(parsed) + 1 : body.getStart(parsed);
      add(node, "body-entered", bodyAt, bodyAt);
      add(node, "normal-exit", node.getStart(parsed), node.getStart(parsed));
      if (!ts.isDoStatement(node)) add(node, "zero-iterations", node.getStart(parsed), node.getStart(parsed));
    } else if (ts.isBinaryExpression(node) && logicalModes.has(node.operatorToken.kind)) {
      add(node, "rhs-evaluated", ...span(node.right));
      add(node, "rhs-skipped", ...span(node.left), logicalModes.get(node.operatorToken.kind));
    } else if (node.questionDotToken) {
      const at = node.questionDotToken.getStart(parsed);
      for (const outcome of ["non-nullish", "nullish"]) add(node, outcome, at, at);
    } else if (ts.isTryStatement(node)) {
      add(node, "try-completed", node.tryBlock.end - 1, node.tryBlock.end - 1);
      const threwAt = node.catchClause ? node.catchClause.block.getStart(parsed) + 1 : node.tryBlock.getStart(parsed);
      add(node, "try-threw", threwAt, threwAt);
    }
    node.forEachChild(visit);
  };
  visit(parsed);
  return anchors;
}

function children(node) {
  const found = [];
  node.forEachChild((child) => { found.push(child); });
  for (let index = 1; index < found.length; index += 1) assert.ok(found[index - 1].end <= found[index].pos, "Unordered AST children");
  return found;
}

function nearestFunction(node) {
  for (let current = node.parent; current; current = current.parent) if (isFunctionBoundary(current)) return current;
  return undefined;
}

function suspends(node, owner) {
  let found = false;
  const visit = (child) => {
    if (found || isFunctionBoundary(child)) return;
    if ((ts.isAwaitExpression(child) || ts.isYieldExpression(child)) && nearestFunction(child) === owner) { found = true; return; }
    child.forEachChild(visit);
  };
  visit(node);
  return found;
}

function breakTarget(statement) {
  for (let current = statement.parent; current && !isFunctionBoundary(current); current = current.parent) {
    if (statement.label) {
      if (ts.isLabeledStatement(current) && current.label.text === statement.label.text) {
        let target = current.statement;
        while (ts.isLabeledStatement(target)) target = target.statement;
        return target;
      }
    } else if (isLoop(current) || ts.isSwitchStatement(current)) return current;
  }
  return undefined;
}

function chainLinks(top) {
  const links = [];
  let node = top;
  while (inChain(node)) { links.unshift(node); node = node.expression; }
  return { links, base: node };
}

function planFile(parsed, file, allocate) {
  const text = parsed.text;
  const plan = { cond: new Map(), left: new Map(), right: new Map(), blockStart: new Map(), blockEnd: new Map(), tryWrap: new Map(),
    loops: new Map(), bodies: new Map(), breaks: new Map(), switches: new Map(), cases: new Map(), discriminants: new Map(), chains: new Map() };
  const counters = [];
  const skipped = [];
  let names = 0;
  const name = (prefix) => `__uc${prefix}${++names}`;
  const id = (node, outcome) => `${file}:${node.getStart(parsed)}:${ts.SyntaxKind[node.kind]}:${sha(node.getText(parsed))}:${outcome}`;
  const counter = (node, outcome) => {
    const index = allocate();
    counters.push({ index, id: id(node, outcome) });
    return index;
  };
  const skip = (node, reason) => { for (const outcome of outcomesOf(node)) skipped.push({ id: id(node, outcome), reason }); };
  const pushStart = (block, statement) => plan.blockStart.set(block, [...plan.blockStart.get(block) ?? [], statement]);
  const loopFlags = new Map();
  const chainTops = [];
  const visit = (node) => {
    if (ts.isIfStatement(node)) plan.cond.set(node.expression, [counter(node, "true"), counter(node, "false")]);
    else if (ts.isConditionalExpression(node)) plan.cond.set(node.condition, [counter(node, "true"), counter(node, "false")]);
    else if (ts.isCatchClause(node)) pushStart(node.block, `__ucCatch(${counter(node, "entered")});`);
    else if (ts.isCaseClause(node)) {
      const owner = node.parent.parent;
      if (!plan.switches.has(owner)) plan.switches.set(owner, name("D"));
      plan.cases.set(node.expression, { discriminant: plan.switches.get(owner), index: counter(node, "selected") });
      plan.discriminants.set(owner.expression, plan.switches.get(owner));
    } else if (ts.isDefaultClause(node)) skip(node, "A default clause has no expression to compare; its selection is not counted");
    else if (isLoop(node)) {
      const flags = { entered: name("S"), broke: name("K") };
      const body = counter(node, "body-entered");
      const zero = ts.isDoStatement(node) ? -1 : counter(node, "zero-iterations");
      plan.loops.set(node, { ...flags, normal: counter(node, "normal-exit"), zero });
      if (ts.isBlock(node.statement)) pushStart(node.statement, `${flags.entered} = __ucBody(${body});`);
      else plan.bodies.set(node.statement, `${flags.entered} = __ucBody(${body});`);
      loopFlags.set(node, flags.broke);
    } else if (ts.isBinaryExpression(node) && logicalModes.has(node.operatorToken.kind)) {
      plan.right.set(node.right, counter(node, "rhs-evaluated"));
      plan.left.set(node.left, { mode: logicalModes.get(node.operatorToken.kind), index: counter(node, "rhs-skipped") });
    } else if (ts.isTryStatement(node)) {
      plan.blockEnd.set(node.tryBlock, `;__ucDone(${counter(node, "try-completed")});`);
      const threw = counter(node, "try-threw");
      if (node.catchClause) pushStart(node.catchClause.block, `__ucThrew(${threw});`);
      else plan.tryWrap.set(node.tryBlock, { error: name("E"), index: threw });
    }
    if (inChain(node) && !(isChainLink(node.parent) && node.parent.expression === node && inChain(node.parent))) chainTops.push(node);
    node.forEachChild(visit);
  };
  visit(parsed);
  const memberCallee = (member, call) => ts.isPropertyAccessExpression(member) && !ts.isPrivateIdentifier(member.name)
    && member.name.getStart(parsed) === (member.questionDotToken ? member.questionDotToken.end : text.indexOf(".", member.expression.end) + 1)
    && member.name.end === call.questionDotToken.getStart(parsed);
  for (const top of chainTops) {
    const { links, base } = chainLinks(top);
    const optional = links.filter((link) => link.questionDotToken);
    const owner = nearestFunction(top);
    const remainder = links.flatMap((link) => children(link).filter((child) => child !== link.expression));
    const callee = (index) => index > 0 ? links[index - 1] : base;
    const unsupportedCall = links.some((link, index) => link.questionDotToken && ts.isCallExpression(link) && (link.typeArguments
      || ts.isParenthesizedExpression(callee(index)) || ts.isElementAccessExpression(callee(index)) || ts.isNonNullExpression(callee(index)) || callee(index).kind === ts.SyntaxKind.SuperKeyword
      || (ts.isPropertyAccessExpression(callee(index)) && !memberCallee(callee(index), link))));
    const reason = ts.isDeleteExpression(top.parent) ? "Optional chain operand of delete"
      : remainder.some((child) => suspends(child, owner)) ? "Await or yield inside the chain remainder"
      : links.some((link) => ts.isPropertyAccessExpression(link) && ts.isPrivateIdentifier(link.name)) ? "Private name in an optional chain"
      : unsupportedCall ? "Optional call whose callee is not a plain identifier, call result or directly written property name" : undefined;
    if (reason) {
      for (const link of optional) skip(link, reason);
      continue;
    }
    plan.chains.set(top, { links, base, indices: new Map(optional.map((link) => [link, [counter(link, "non-nullish"), counter(link, "nullish")]])), param: () => name("P") });
  }
  for (const statement of collect(parsed, ts.isBreakStatement)) {
    const target = breakTarget(statement);
    if (target && loopFlags.has(target)) plan.breaks.set(statement, loopFlags.get(target));
  }
  return { plan, counters, skipped };
}

function collect(root, predicate) {
  const found = [];
  const visit = (node) => { if (predicate(node)) found.push(node); node.forEachChild(visit); };
  visit(root);
  return found;
}

function emitFile(parsed, plan) {
  const text = parsed.text;
  const start = (node) => node.getStart(parsed);
  const statementWrapper = (node) => {
    if (ts.isLabeledStatement(node.parent)) return undefined;
    let inner = node;
    while (ts.isLabeledStatement(inner)) inner = inner.statement;
    const loop = plan.loops.get(inner);
    if (loop) return [`{ let ${loop.entered} = 0, ${loop.broke} = 0; `, ` __ucExit(${loop.entered}, ${loop.broke}, ${loop.normal}, ${loop.zero}); }`];
    const discriminant = plan.switches.get(inner);
    return discriminant ? [`{ let ${discriminant}; `, " }"] : undefined;
  };
  function core(node) {
    const wrapper = ts.isLabeledStatement(node) || isLoop(node) || ts.isSwitchStatement(node) ? statementWrapper(node) : undefined;
    const body = plan.chains.has(node) ? lowerChain(node) : plan.breaks.has(node) ? `{ ${plan.breaks.get(node)} = 1; ${plain(node)} }` : plain(node);
    return wrapper ? wrapper[0] + body + wrapper[1] : body;
  }
  function plain(node, from = start(node), skip) {
    const inserts = [];
    if (ts.isBlock(node)) {
      if (plan.blockStart.has(node)) inserts.push({ at: start(node) + 1, text: plan.blockStart.get(node).join("") });
      if (plan.blockEnd.has(node)) inserts.push({ at: node.end - 1, text: plan.blockEnd.get(node) });
    }
    let out = "";
    let cursor = from;
    const gap = (to) => {
      for (const insert of inserts.filter((item) => item.at >= cursor && item.at <= to && !item.done)) {
        out += text.slice(cursor, insert.at) + insert.text;
        cursor = insert.at;
        insert.done = true;
      }
      out += text.slice(cursor, to);
      cursor = to;
    };
    for (const child of children(node)) {
      if (child === skip || child.end <= from) continue;
      gap(Math.max(cursor, child.pos));
      out += child === skip ? "" : emitChild(child, cursor);
      cursor = child.end;
    }
    gap(node.end);
    assert.ok(inserts.every((item) => item.done), "Unplaced counter statement");
    return out;
  }
  function emitChild(child, from) {
    const lead = text.slice(from, start(child));
    const inner = core(child);
    if (plan.cond.has(child)) return `${lead}__ucB(${inner}, ${plan.cond.get(child).join(", ")})`;
    if (plan.left.has(child)) return `${lead}__ucL(${inner}, ${plan.left.get(child).mode}, ${plan.left.get(child).index})`;
    if (plan.right.has(child)) return `${lead}(__ucR(${plan.right.get(child)}), ${inner})`;
    if (plan.bodies.has(child)) return `${lead}{ ${plan.bodies.get(child)} ${inner} }`;
    if (plan.tryWrap.has(child)) {
      const { error, index } = plan.tryWrap.get(child);
      return `${lead}{ try ${inner} catch (${error}) { __ucThrew(${index}); throw ${error}; } }`;
    }
    if (plan.discriminants.has(child)) return `${lead}${plan.discriminants.get(child)} = ${inner}`;
    if (plan.cases.has(child)) return `${lead}__ucCase(${inner}, ${plan.cases.get(child).discriminant}, ${plan.cases.get(child).index})`;
    return lead + inner;
  }
  function tail(link) {
    return plain(link, link.expression.end, link.expression);
  }
  function lowerChain(top) {
    const { links, base, indices, param } = plan.chains.get(top);
    const methodCall = (target, member, call, next) => {
      const [present, absent] = indices.get(call);
      const callTail = tail(call);
      const question = call.questionDotToken.getStart(parsed) - call.expression.end;
      assert.equal(callTail.slice(question, question + 2), "?.");
      const callee = param();
      const receiver = param();
      return `__ucM(${target}, ${JSON.stringify(member.name.text)}, ${present}, ${absent}, (${callee}, ${receiver}) => ${lower(next, `Reflect.apply(${callee}, ${receiver}, Array.of${callTail.slice(question + 2)})`)})`;
    };
    const leadingDot = (member) => text.slice(member.expression.end, text.indexOf(".", member.expression.end));
    function lower(from, current) {
      for (let index = from; index < links.length; index += 1) {
        const link = links[index];
        const call = links[index + 1];
        if (ts.isPropertyAccessExpression(link) && call?.questionDotToken && ts.isCallExpression(call)) {
          if (!link.questionDotToken) return methodCall(current + leadingDot(link), link, call, index + 2);
          const parameter = param();
          const [present, absent] = indices.get(link);
          const leading = text.slice(link.expression.end, link.questionDotToken.getStart(parsed));
          return `__ucO(${current}${leading}, ${present}, ${absent}, (${parameter}) => ${methodCall(parameter, link, call, index + 2)})`;
        }
        if (!link.questionDotToken) { current += tail(link); continue; }
        const linkTail = tail(link);
        const question = link.questionDotToken.getStart(parsed) - link.expression.end;
        assert.equal(linkTail.slice(question, question + 2), "?.");
        const parameter = param();
        const [present, absent] = indices.get(link);
        const rest = `${parameter}${ts.isPropertyAccessExpression(link) ? "." : ""}${linkTail.slice(question + 2)}`;
        return `__ucO(${current}${linkTail.slice(0, question)}, ${present}, ${absent}, (${parameter}) => ${lower(index + 1, rest)})`;
      }
      return current;
    }
    if (ts.isCallExpression(links[0]) && ts.isPropertyAccessExpression(base)) return methodCall(core(base.expression) + leadingDot(base), base, links[0], 1);
    return lower(0, core(base));
  }
  return plain(parsed, 0);
}

function eraseFile(parsed, bodyLength) {
  const text = parsed.text;
  const start = (node) => node.getStart(parsed);
  const anchors = [];
  const values = new Set();
  const receivers = new Set();
  const consumed = new Set();
  let out = "";
  const literal = (node) => {
    assert.ok(node && (ts.isNumericLiteral(node) || (ts.isPrefixUnaryExpression(node) && node.operator === ts.SyntaxKind.MinusToken && ts.isNumericLiteral(node.operand))), "Counter index is not a literal");
    return ts.isNumericLiteral(node) ? Number(node.text) : -Number(node.operand.text);
  };
  const helper = (node) => node && ts.isCallExpression(node) && ts.isIdentifier(node.expression) && counterHelpers.has(node.expression.text) ? node.expression.text : undefined;
  const statementHelper = (statement) => ts.isExpressionStatement(statement) ? helper(statement.expression) : undefined;
  const flagAssignment = (statement, value) => ts.isExpressionStatement(statement) && ts.isBinaryExpression(statement.expression) && statement.expression.operatorToken.kind === ts.SyntaxKind.EqualsToken
    && ts.isIdentifier(statement.expression.left) && statement.expression.left.text.startsWith("__uc")
    && (value === undefined ? helper(statement.expression.right) === "__ucBody" : ts.isNumericLiteral(statement.expression.right) && statement.expression.right.text === value) ? statement.expression : undefined;
  const exact = (from, to, expected) => assert.equal(text.slice(from, to), expected, "Counter scaffolding changed");
  const record = (index, kind, outcome, from, to, mode = "") => { if (index >= 0) anchors.push({ index, key: anchorKey(kind, outcome, from, to, mode) }); };
  const isParameter = (node) => ts.isIdentifier(node) && values.has(node.text);
  const isReceiverCall = (node) => ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) && ts.isIdentifier(node.expression.expression)
    && node.expression.expression.text === "Reflect" && node.expression.name.text === "apply" && node.arguments.length === 3 && ts.isIdentifier(node.arguments[0]) && receivers.has(node.arguments[0].text);
  function loopWrapper(block) {
    if (!ts.isBlock(block) || block.statements.length !== 3) return undefined;
    const [flags, middle, exit] = block.statements;
    if (!ts.isVariableStatement(flags) || statementHelper(exit) !== "__ucExit") return undefined;
    const names = flags.declarationList.declarations.map((item) => item.name.text);
    assert.equal(names.length, 2);
    let loop = middle;
    while (ts.isLabeledStatement(loop)) loop = loop.statement;
    assert.ok(isLoop(loop), "Loop counter wrapper without a loop");
    const [entered, broke, normal, zero] = exit.expression.arguments;
    assert.deepEqual([entered.text, broke.text], names);
    exact(start(block), start(middle), `{ let ${names[0]} = 0, ${names[1]} = 0; `);
    exact(middle.end, block.end, ` __ucExit(${names[0]}, ${names[1]}, ${literal(normal)}, ${literal(zero)}); }`);
    return { middle, loop, names, normal: literal(normal), zero: literal(zero) };
  }
  const loops = new Map();
  for (const block of collect(parsed, ts.isBlock)) {
    const wrapper = loopWrapper(block);
    if (wrapper) loops.set(wrapper.loop, wrapper);
  }
  for (const statement of collect(parsed, ts.isBreakStatement)) {
    const wrapper = loops.get(breakTarget(statement));
    if (!wrapper) continue;
    const block = statement.parent;
    assert.ok(ts.isBlock(block) && block.statements.length === 2 && block.statements[1] === statement && flagAssignment(block.statements[0], "1")?.left.text === wrapper.names[1],
      "A break leaving a counted loop does not set its flag");
  }
  function emit(node, from) {
    out += text.slice(from, start(node));
    core(node);
  }
  function plain(node, from = start(node), intercept) {
    let cursor = from;
    const list = children(node).filter((child) => child.end > from && (!ts.isSourceFile(node) || start(child) < bodyLength));
    for (const [index, child] of list.entries()) {
      if (intercept?.(child, cursor)) { cursor = child.end; continue; }
      if (ts.isBlock(node) && inserted(node, child, list[index + 1], out.length + start(child) - cursor)) {
        out += text.slice(cursor, start(child));
        cursor = child.end;
        continue;
      }
      emit(child, cursor);
      cursor = child.end;
    }
    out += text.slice(cursor, ts.isSourceFile(node) ? bodyLength : node.end);
  }
  function bodyHit(statement, loop, at = out.length) {
    const wrapper = loops.get(loop);
    assert.ok(wrapper && statement.expression.left.text === wrapper.names[0], "Loop body counter outside its loop");
    const index = literal(statement.expression.right.arguments[0]);
    exact(start(statement), statement.end, `${wrapper.names[0]} = __ucBody(${index});`);
    record(index, ts.SyntaxKind[loop.kind], "body-entered", at, at);
  }
  function inserted(block, statement, next, at) {
    if (ts.isEmptyStatement(statement) && next && statementHelper(next) === "__ucDone" && next.pos === statement.end) {
      assert.equal(next, block.statements.at(-1), "Completion counter before the end of its block");
      return true;
    }
    const name = ["__ucCatch", "__ucThrew", "__ucDone"].includes(statementHelper(statement)) ? statementHelper(statement) : undefined;
    if (!name && !flagAssignment(statement)) return false;
    assert.equal(statement.pos, start(statement), "Counter statement carries trivia");
    if (!name) {
      assert.ok(isLoop(block.parent) && block.parent.statement === block && block.statements[0] === statement, "Loop body counter outside its body start");
      bodyHit(statement, block.parent, at);
      return true;
    }
    const index = literal(statement.expression.arguments[0]);
    exact(start(statement), statement.end, `${name}(${index});`);
    const catchBlock = ts.isCatchClause(block.parent) && block.parent.block === block;
    const leading = block.statements.slice(0, block.statements.indexOf(statement)).every((item) => ["__ucCatch", "__ucThrew"].includes(statementHelper(item)));
    if (name === "__ucCatch" && catchBlock && leading) record(index, "CatchClause", "entered", at, at);
    else if (name === "__ucThrew" && catchBlock && leading) record(index, "TryStatement", "try-threw", at, at);
    else if (name === "__ucDone" && ts.isTryStatement(block.parent) && block.parent.tryBlock === block && block.statements.at(-1) === statement) {
      assert.ok(ts.isEmptyStatement(block.statements.at(-2)) && block.statements.at(-2).end === statement.pos, "Completion counter without its separator");
      record(index, "TryStatement", "try-completed", at, at);
    } else assert.fail(`Counter statement in an unexpected position: ${name}`);
    return true;
  }
  function wrapped(node, kind, outcome, indices, suffix, mode) {
    const [value] = node.arguments;
    assert.equal(value.pos, start(value), "Counted operand carries inserted trivia");
    const from = out.length;
    core(value);
    exact(value.end, node.end, suffix);
    for (const [index, item] of indices) record(index, kind, item, from, out.length, mode);
  }
  function chainKind(body, parameter) {
    for (let node = body; ;) {
      if (helper(node) === "__ucO" || helper(node) === "__ucM") {
        if (ts.isIdentifier(node.arguments[0]) && node.arguments[0].text === parameter) { assert.equal(helper(node), "__ucM"); return "PropertyAccessExpression"; }
        node = node.arguments[0];
      } else if (isChainLink(node)) {
        if (ts.isIdentifier(node.expression) && node.expression.text === parameter) return ts.SyntaxKind[node.kind];
        node = node.expression;
      } else assert.fail("Chain counter remainder does not start at its value");
    }
  }
  function chain(node, method) {
    const [value, ...rest] = node.arguments;
    const callback = rest.at(-1);
    const [present, absent] = rest.slice(method ? 1 : 0, -1).map(literal);
    assert.equal(rest.length, method ? 4 : 3);
    assert.ok(ts.isArrowFunction(callback) && callback.parameters.length === (method ? 2 : 1) && !ts.isBlock(callback.body), "Chain counter without its remainder");
    const parameters = callback.parameters.map((item) => item.name.text);
    assert.ok(parameters.every((item) => item.startsWith("__ucP") && !values.has(item) && !receivers.has(item)));
    assert.equal(value.pos, start(value), "Chain base carries inserted trivia");
    const parameterTarget = method && isParameter(value);
    if (parameterTarget) consumed.add(value.text);
    else core(value);
    const comma = text.indexOf(",", value.end);
    out += text.slice(value.end, comma);
    if (method) {
      assert.ok(ts.isStringLiteral(rest[0]));
      exact(comma, start(callback), `, ${JSON.stringify(rest[0].text)}, ${present}, ${absent}, `);
      out += `${parameterTarget ? "" : "."}${rest[0].text}`;
    } else exact(comma, start(callback), `, ${present}, ${absent}, `);
    exact(start(callback), start(callback.body), `(${parameters.join(", ")}) => `);
    exact(callback.end, node.end, ")");
    const kind = method ? "CallExpression" : chainKind(callback.body, parameters[0]);
    record(present, kind, "non-nullish", out.length, out.length);
    record(absent, kind, "nullish", out.length, out.length);
    out += "?.";
    (method ? receivers : values).add(parameters[0]);
    core(callback.body);
    assert.ok(consumed.has(parameters[0]), "Chain counter remainder never used its value");
  }
  function core(node) {
    const name = helper(node);
    if (name === "__ucB") {
      assert.ok((ts.isIfStatement(node.parent) && node.parent.expression === node) || (ts.isConditionalExpression(node.parent) && node.parent.condition === node), "Condition counter outside a condition");
      const [, whenTrue, whenFalse] = node.arguments.map((item, index) => index ? literal(item) : item);
      return wrapped(node, ts.SyntaxKind[node.parent.kind], undefined, [[whenTrue, "true"], [whenFalse, "false"]], `, ${whenTrue}, ${whenFalse})`);
    }
    if (name === "__ucL") {
      const mode = literal(node.arguments[1]);
      const skipped = literal(node.arguments[2]);
      assert.ok(ts.isBinaryExpression(node.parent) && node.parent.left === node && logicalModes.get(node.parent.operatorToken.kind) === mode, "Logical counter outside its operand");
      return wrapped(node, "BinaryExpression", undefined, [[skipped, "rhs-skipped"]], `, ${mode}, ${skipped})`, mode);
    }
    if (name === "__ucCase") {
      const discriminant = node.arguments[1];
      const index = literal(node.arguments[2]);
      assert.ok(ts.isCaseClause(node.parent) && node.parent.expression === node, "Case counter outside a case");
      const owner = node.parent.parent.parent;
      assert.ok(ts.isBinaryExpression(owner.expression) && owner.expression.left.text === discriminant.text, "Case counter without its discriminant");
      return wrapped(node, "CaseClause", undefined, [[index, "selected"]], `, ${discriminant.text}, ${index})`);
    }
    if (name === "__ucO" || name === "__ucM") return chain(node, name === "__ucM");
    assert.equal(name, undefined, `Counter helper in an unexpected position: ${name}`);
    if (ts.isParenthesizedExpression(node) && ts.isBinaryExpression(node.expression) && node.expression.operatorToken.kind === ts.SyntaxKind.CommaToken && helper(node.expression.left) === "__ucR") {
      const index = literal(node.expression.left.arguments[0]);
      assert.ok(ts.isBinaryExpression(node.parent) && node.parent.right === node && logicalModes.has(node.parent.operatorToken.kind), "Right-operand counter outside a logical operand");
      const value = node.expression.right;
      exact(start(node), start(value), `(__ucR(${index}), `);
      exact(value.end, node.end, ")");
      const from = out.length;
      core(value);
      return record(index, "BinaryExpression", "rhs-evaluated", from, out.length);
    }
    if ((ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node) || ts.isCallExpression(node)) && isParameter(node.expression)) {
      assert.ok(!consumed.has(node.expression.text), "Chain value used twice");
      consumed.add(node.expression.text);
      if (ts.isPropertyAccessExpression(node)) {
        exact(node.expression.end, node.expression.end + 1, ".");
        return plain(node, node.expression.end + 1);
      }
      return plain(node, node.expression.end);
    }
    if (isReceiverCall(node)) {
      const [callee, receiver, list] = node.arguments;
      assert.ok(ts.isIdentifier(receiver) && receiver.text.startsWith("__ucP") && ts.isCallExpression(list) && text.slice(start(list), list.expression.end) === "Array.of");
      exact(start(node), start(list), `Reflect.apply(${callee.text}, ${receiver.text}, `);
      exact(list.end, node.end, ")");
      assert.ok(!consumed.has(callee.text), "Method receiver used twice");
      consumed.add(callee.text);
      return plain(list, list.expression.end);
    }
    if (ts.isBlock(node)) {
      const wrapper = loopWrapper(node);
      if (wrapper) return core(wrapper.middle);
      const [first, second] = node.statements;
      if (node.statements.length === 2 && flagAssignment(first) && isLoop(node.parent) && node.parent.statement === node && start(first) === start(node) + 2) {
        exact(start(node), start(first), "{ ");
        exact(first.end, start(second), " ");
        exact(second.end, node.end, " }");
        bodyHit(first, node.parent);
        return core(second);
      }
      if (node.statements.length === 2 && flagAssignment(first, "1") && ts.isBreakStatement(second)) {
        exact(start(node), start(second), `{ ${first.expression.left.text} = 1; `);
        exact(second.end, node.end, " }");
        return core(second);
      }
      if (ts.isTryStatement(node.parent) && node.parent.tryBlock === node && node.statements.length === 1 && ts.isTryStatement(first) && first.catchClause && !first.finallyBlock
        && first.catchClause.variableDeclaration?.name.text?.startsWith("__ucE")) {
        const error = first.catchClause.variableDeclaration.name.text;
        const threw = first.catchClause.block.statements[0];
        assert.equal(statementHelper(threw), "__ucThrew");
        const index = literal(threw.expression.arguments[0]);
        exact(start(node), start(first.tryBlock), "{ try ");
        exact(first.tryBlock.end, node.end, ` catch (${error}) { __ucThrew(${index}); throw ${error}; } }`);
        record(index, "TryStatement", "try-threw", out.length, out.length);
        return core(first.tryBlock);
      }
      if (node.statements.length === 2 && ts.isVariableStatement(first) && ts.isSwitchStatement(second) && first.declarationList.declarations[0]?.name.text?.startsWith("__ucD")) {
        const discriminant = first.declarationList.declarations[0].name.text;
        exact(start(node), start(second), `{ let ${discriminant}; `);
        exact(second.end, node.end, " }");
        assert.ok(ts.isBinaryExpression(second.expression) && second.expression.left.text === discriminant, "Switch counter without its discriminant");
        return core(second);
      }
    }
    if (ts.isSwitchStatement(node) && ts.isBinaryExpression(node.expression) && ts.isIdentifier(node.expression.left) && node.expression.left.text.startsWith("__ucD")) {
      assert.ok(ts.isBlock(node.parent) && node.parent.statements[0].declarationList?.declarations[0]?.name.text === node.expression.left.text, "Switch discriminant outside its counter block");
      return plain(node, start(node), (child, from) => {
        if (child !== node.expression) return false;
        out += text.slice(from, start(child));
        exact(start(child), start(child.right), `${child.left.text} = `);
        core(child.right);
        return true;
      });
    }
    if (isLoop(node) && loops.has(node)) {
      const wrapper = loops.get(node);
      record(wrapper.normal, ts.SyntaxKind[node.kind], "normal-exit", out.length, out.length);
      record(wrapper.zero, ts.SyntaxKind[node.kind], "zero-iterations", out.length, out.length);
    }
    return plain(node);
  }
  plain(parsed, 0);
  assert.equal([...consumed].sort().join(), [...values, ...receivers].sort().join(), "Chain value not consumed exactly once");
  return { text: out, anchors };
}

async function parseAll(root, files) {
  const config = join(root, "counter-tsconfig.json");
  await writeFile(config, JSON.stringify({ compilerOptions: { noResolve: true, allowJs: true }, files }));
  const api = new API({ cwd: root });
  const snapshot = api.updateSnapshot({ openProjects: [config] });
  const program = snapshot.getProject(config).program;
  for (const file of files) assert.deepEqual(program.getSyntacticDiagnostics(file), [], `Invalid counter AST: ${file}`);
  return { program, close: () => { snapshot.dispose(); api.close(); } };
}

export async function instrumentSources(sources, scratch) {
  const root = await mkdtemp(join(scratch, "counter-instrument-"));
  try {
    const paths = Object.keys(sources).sort();
    const originals = paths.map((path, index) => join(root, "original", `${index}.ts`));
    await mkdir(join(root, "original"), { recursive: true });
    await Promise.all(paths.map((path, index) => writeFile(originals[index], sources[path])));
    const parsed = await parseAll(root, originals);
    const plans = [];
    const identities = [];
    let total = 0;
    try {
      for (const [index, path] of paths.entries()) {
        const file = parsed.program.getSourceFile(originals[index]);
        assert.equal(file.text, sources[path]);
        plans.push({ path, file, ...planFile(file, path, () => total++) });
        identities.push(...obligationIdentities(file, path));
      }
      const counters = plans.flatMap((plan) => plan.counters).sort((a, b) => a.index - b.index).map((item) => item.id);
      const uninstrumented = plans.flatMap((plan) => plan.skipped);
      assert.deepEqual([...counters, ...uninstrumented.map((item) => item.id)].sort(), [...identities].sort(), "Counter plan does not cover each outcome exactly once");
      const manifest = { formatVersion: 1, method: "Source-to-source counters on a disposable copy; erasure of the instrumented AST reproduces each source byte for byte", sourceFiles: Object.fromEntries(paths.map((path) => [path, sha(sources[path])])), counters, uninstrumented };
      const manifestSha256 = sha(JSON.stringify(manifest));
      const tail = trailer(manifestSha256, counters.length);
      const instrumented = Object.fromEntries(plans.map(({ path, file, plan }) => [path, emitFile(file, plan) + tail]));
      const result = { ...manifest, sha256: manifestSha256, instrumentedFiles: Object.fromEntries(paths.map((path) => [path, sha(instrumented[path])])) };
      await verifyCounterCopy(sources, instrumented, result, scratch);
      return { manifest: result, instrumented };
    } finally { parsed.close(); }
  } finally { await rm(root, { recursive: true, force: true }); }
}

export async function verifyCounterCopy(sources, instrumented, manifest, scratch) {
  const { sha256, instrumentedFiles, ...identity } = manifest;
  assert.equal(sha(JSON.stringify(identity)), sha256, "Changed counter manifest");
  const paths = Object.keys(sources).sort();
  assert.deepEqual(identity.sourceFiles, Object.fromEntries(paths.map((path) => [path, sha(sources[path])])), "Counter manifest names other sources");
  assert.deepEqual(Object.keys(instrumented).sort(), paths, "Counter copy names other files");
  assert.deepEqual(instrumentedFiles, Object.fromEntries(paths.map((path) => [path, sha(instrumented[path])])), "Changed counter copy");
  const tail = trailer(sha256, identity.counters.length);
  const root = await mkdtemp(join(scratch, "counter-erase-"));
  try {
    const files = paths.map((path, index) => join(root, `${index}.ts`));
    await Promise.all(paths.map((path, index) => writeFile(files[index], instrumented[path])));
    const originals = paths.map((path, index) => join(root, `original-${index}.ts`));
    await Promise.all(paths.map((path, index) => writeFile(originals[index], sources[path])));
    const parsed = await parseAll(root, [...files, ...originals]);
    try {
      const seen = new Map();
      for (const [index, path] of paths.entries()) {
        assert.ok(instrumented[path].endsWith(tail), `Counter helpers missing: ${path}`);
        const erased = eraseFile(parsed.program.getSourceFile(files[index]), instrumented[path].length - tail.length);
        assert.equal(erased.text, sources[path], `Erasure does not reproduce the pinned source: ${path}`);
        const expected = sourceAnchors(parsed.program.getSourceFile(originals[index]), path);
        for (const anchor of erased.anchors) {
          const ids = expected.get(anchor.key) ?? [];
          assert.equal(ids.length, 1, `Counter ${anchor.index} does not map to exactly one obligation: ${path} ${anchor.key}`);
          assert.ok(!seen.has(anchor.index), `Counter ${anchor.index} appears twice`);
          seen.set(anchor.index, ids[0]);
        }
      }
      assert.deepEqual([...seen].sort((a, b) => a[0] - b[0]), identity.counters.map((id, index) => [index, id]), "Counter map differs from the erased instrumented AST");
      return true;
    } finally { parsed.close(); }
  } finally { await rm(root, { recursive: true, force: true }); }
}

export async function pinnedBackendSources(project, provenance) {
  const backend = join(project, "node_modules/@narumitw/pi-usage");
  const pinned = provenance.dependencies["node_modules/@narumitw/pi-usage"].installed.files;
  const paths = Object.keys(pinned).filter((path) => path.startsWith("src/") && path.endsWith(".ts")).sort();
  const sources = {};
  for (const path of paths) {
    const text = await readFile(join(backend, path), "utf8");
    assert.equal(sha(text), pinned[path].sha256, `Changed source identity: ${path}`);
    sources[path] = text;
  }
  return sources;
}

export async function writeCounterCopy(directory, instrumented) {
  for (const [path, text] of Object.entries(instrumented)) {
    await mkdir(dirname(join(directory, path)), { recursive: true });
    await writeFile(join(directory, path), text);
  }
}
