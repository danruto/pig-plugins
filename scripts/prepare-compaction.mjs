/** Explicitly prepares pinned compaction hooks for native PiG providers, without the upstream custom transport. */
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";

const source = new URL("../node_modules/pi-openai-server-compaction/src/", import.meta.url);
const destination = new URL("../compaction/generated/", import.meta.url);
const hashes = {
	"config.ts": "e4566e1b8b75a581219a5d691f04d7674088a640516a368b7f1f96e761544d42",
	"custom-stream.ts": "6afd5c980cb26b71f4b6b796dc80464801326676646240b53cc3c86430d84126",
	"index.ts": "477adf73e0bd37047f3f597531a49528e122312c9c2590874a7caf283ed19607",
	"openai-ws-connection.ts": "3dfbe432c5c709fc2b3345264ff39f95940bad00839a5670fb92ddbab781659e",
	"openai-ws-stream.ts": "362c45476683b2525803343d4ebb227cea1cb1357107b32594709ea22f891a61",
	"openai.ts": "412973e91038fcd426cbd3598750af6c9c702260bb948ebf54fe44dc8d149b42",
	"remote-compaction.ts": "bb81c130a96693f1cf8a30bc62d5700f7cbaed10b56ed13b92023d9447b57c53",
	"state.ts": "bb4f41ec5961f714b82a2f526ab22c3bbc380587f6ba373631fbe314c1b6d0d2",
	"stream-message-shared.ts": "849dc1d524789386bd3430ce0b7a64331413187af3b056e3a00fd6b47d1fd4f5",
};

function replaceOnce(text, before, after) {
	assert.equal(text.split(before).length, 2, "Upstream patch must match exactly once");
	return text.replace(before, after);
}

function replaceSection(text, start, end, replacement = "") {
	assert.equal(text.split(start).length, 2, "Upstream section start must match exactly once");
	assert.equal(text.split(end).length, 2, "Upstream section end must match exactly once");
	const from = text.indexOf(start);
	const to = text.indexOf(end);
	assert.ok(to > from, "Upstream section must be ordered");
	return text.slice(0, from) + replacement + text.slice(to);
}

assert.deepEqual(readdirSync(source).sort(), Object.keys(hashes).sort());
const files = new Map(Object.entries(hashes).map(([name, hash]) => {
	const text = readFileSync(new URL(name, source), "utf8");
	assert.equal(createHash("sha256").update(text).digest("hex"), hash, `Upstream drift: ${name}`);
	return [name, text];
}));

let openai = files.get("openai.ts");
openai = replaceOnce(openai, "OpenAI/Azure/Codex model and payload helpers.", "OpenAI/Codex model and payload helpers.");
openai = replaceOnce(openai, "ExtensionConfig, JsonRecord", "JsonRecord");
openai = replaceOnce(openai, "isRecord, toPositiveInteger", "isRecord");
for (const [start, end] of [
	["type AssistantMessageLike =", "export function hostnameFromBaseUrl("],
	["export function supportsStore(", "export function isOpenAIResponsesModel("],
	["export function isAzureOpenAIResponsesModel(", "export function isOpenAICodexResponsesModel("],
	["export function supportsPreviousResponseId(", "export function supportsRemoteCompactionModel("],
	["export function resolveCompactThreshold(", "export function looksLikeResponsesPayload("],
	["export function applyPayloadPatch(", "export function thinkingLevelToResponsesReasoning("],
	["export function extractAssistantResponseId(", "export function messageMatchesModel("],
]) openai = replaceSection(openai, start, end);
openai = replaceSection(openai,
	"export function applyRemoteHistoryPayloadPatch(params:",
	"export function extractResponsesReasoningConfig(",
	'export { applyRemoteHistoryPayloadPatch } from "../payload.ts";\n\n');
files.set("openai.ts", openai);

let index = files.get("index.ts");
index = replaceOnce(index, "reconstruction, session lifecycle cleanup, and provider override registration.", "reconstruction, and session lifecycle cleanup for native PiG providers.");
for (const removedImport of [
	'import { streamOpenAIResponsesWithPhase2B } from "./custom-stream.ts";\n',
	'import { releaseAllWsSessions, releaseWsSession } from "./openai-ws-stream.ts";\n',
	"  applyPayloadPatch,\n",
	"  extractAssistantResponseId,\n",
	"  isOpenAICodexResponsesModel,\n",
	"  supportsPreviousResponseId,\n",
	"  clearContinuationState,\n",
	"  getContinuationState,\n",
	"  setContinuationState,\n",
]) index = replaceOnce(index, removedImport, "");
index = replaceSection(index, "function getBranchMessageCount(", "function getBranchThinkingLevel(");
index = replaceSection(index, "function clearLiveContinuation(", "function clearSessionRuntimeState(");
index = replaceOnce(index, `  pi.registerProvider("openai", {
    api: "openai-responses",
    streamSimple: streamOpenAIResponsesWithPhase2B,
  });

`, "");
index = replaceOnce(index, `  pi.on("model_select", (_event, ctx) => {
    clearLiveContinuation(getSessionId(ctx));
  });

`, "");
index = replaceOnce(index, "\n  clearLiveContinuation(sessionId);\n", "\n");
index = replaceOnce(index, "\n    clearLiveContinuation(sessionId);\n", "\n");
index = replaceOnce(index, "    clearLiveContinuation(getSessionId(ctx));\n", "");
index = replaceOnce(index, "    releaseAllWsSessions();\n", "");
index = replaceSection(index,
	"    const cfg = loadConfig(ctx.cwd);\n    if (!cfg.enabled || !supportsPreviousResponseId",
	'  });\n\n  pi.on("before_provider_request"');
index = replaceOnce(index, "      message: event.message,\n    });\n\n  });", "      message: event.message,\n    });\n  });");
index = replaceOnce(index,
	"    if (!model || !isRecord(event.payload) || !looksLikeResponsesPayload(event.payload)) return undefined;",
	"    if (!supportsRemoteCompactionModel(model) || !isRecord(event.payload) || !looksLikeResponsesPayload(event.payload)) return undefined;");
index = replaceSection(index,
	"    if (isOpenAICodexResponsesModel(model)) {",
	"    return payload;\n  });\n}",
	`    if (!remoteState) return undefined;
    const payload = applyRemoteHistoryPayloadPatch({
      payload: event.payload,
      explicitHistory: normalizeResponseItemsForPrompt(remoteState.explicitHistory, model),
    });
    delete payload.messages;
    delete payload.previous_response_id;
    maybeNotifyRequestFeatures({
      notifiedModels,
      hasUI: ctx.hasUI,
      notify: cfg.notify,
      ui: ctx.ui,
      model,
      features: ["remote_compaction_history"],
    });

`);
index = index.replaceAll("clearAllContinuationState", "clearAllCompactionState");
files.set("index.ts", index);

let state = files.get("state.ts");
state = replaceOnce(state, "continuation and reconstructed replay state", "reconstructed replay and request-shape state");
state = replaceSection(state, "export type ContinuationState =", "export type ResponsesRequestShapeState =");
state = replaceOnce(state, "const continuationBySessionId = new Map<string, ContinuationState>();\n", "");
state = replaceSection(state, "export function getContinuationState(", "export function getRemoteCompactionState(");
state = replaceOnce(state, "export function clearAllContinuationState(): void {\n  continuationBySessionId.clear();", "export function clearAllCompactionState(): void {");
files.set("state.ts", state);

const license = readFileSync(new URL("../LICENSE.md", source));
rmSync(destination, { recursive: true, force: true });
mkdirSync(destination, { recursive: true });
for (const name of ["config.ts", "index.ts", "openai.ts", "remote-compaction.ts", "state.ts"]) {
	writeFileSync(new URL(name, destination), `// @ts-nocheck -- generated upstream code uses PiG-provided runtime types.\n${files.get(name)}`);
}
writeFileSync(new URL("LICENSE.md", destination), license);
console.log("Prepared pinned compaction hooks: native OpenAI and Codex replay use compaction/payload.ts; no custom provider");
