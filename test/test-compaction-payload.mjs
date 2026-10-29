import assert from "node:assert/strict";
import { test } from "node:test";
import { applyRemoteHistoryPayloadPatch } from "../compaction/payload.ts";

function searchTool(overrides = {}) {
  return {
    type: "function", name: "fffind", description: "Find files",
    parameters: {
      type: "object", properties: { pattern: { type: "string" } }, required: ["pattern"],
    },
    strict: null,
    ...overrides,
  };
}

function shellTool() {
  return {
    type: "function", name: "shell", description: "Run a shell command",
    parameters: {
      type: "object", properties: { command: { type: "string" } }, required: ["command"],
      additionalProperties: false,
    },
    strict: true,
  };
}

function additionalTools(tools) {
  return { type: "additional_tools", role: "developer", tools };
}

function clientTools(tools) {
  return [
    {
      type: "tool_search_call", call_id: "fixture-load", execution: "client",
      status: "completed", arguments: { query: "fffind", limit: 1 },
    },
    {
      type: "tool_search_output", call_id: "fixture-load", execution: "client",
      status: "completed", tools,
    },
  ];
}

function instructions() {
  return [
    { role: "system", content: "Only use the disposable fixture workspace." },
    { type: "message", role: "developer", content: [
      { type: "input_text", text: "Current instruction: search before using shell." },
    ] },
  ];
}

function remoteHistory() {
  return [
    { role: "user", content: "Synthetic retained history." },
    { type: "compaction", encrypted_content: "synthetic-opaque" },
    { type: "function_call", call_id: "fixture-search", name: "fffind", arguments: '{"pattern":"*.ts"}' },
    { type: "function_call_output", call_id: "fixture-search", output: "fixture.ts" },
    { role: "user", content: "Continue with current tools." },
  ];
}

function request(context, tools = []) {
  return {
    model: "synthetic-model", stream: true,
    instructions: "Current top-level system instructions.", tools,
    input: [
      ...context,
      { role: "user", content: "Ordinary original history must be replaced." },
      { role: "assistant", content: "Ordinary original response must be replaced." },
    ],
  };
}

function isContext(item) {
  return item.role === "system" || item.role === "developer"
    || ["additional_tools", "tool_search_call", "tool_search_output"].includes(item.type);
}

function isLiveTurn(item) {
  return item.role === "user" && item.content === "Continue with current tools.";
}

const cases = [
  {
    name: "first post-compaction request retains additional_tools with exact schemas",
    context: [...instructions(), additionalTools([searchTool(), shellTool()])],
    tools: [],
    explicitHistory: remoteHistory(),
  },
  {
    name: "resumed history retains client tool-search pair and opaque namespace metadata",
    context: [...instructions(), ...clientTools([
      searchTool({ defer_loading: true, namespace: "fixture.files" }),
      { ...shellTool(), defer_loading: true, namespace: "fixture.shell" },
    ])],
    tools: [],
    explicitHistory: remoteHistory(),
  },
  {
    name: "top-level search and shell tools survive ordinary history replacement",
    context: instructions(),
    tools: [searchTool(), shellTool()],
    explicitHistory: remoteHistory(),
  },
  {
    name: "current instruction, removal, readdition and redefinition order overrides stale replay context",
    context: [
      ...instructions(),
      additionalTools([searchTool(), searchTool({ name: "old_tool" })]),
      { role: "developer", content: "The following tools are no longer available. Do not call them:\n- old_tool\n- fffind" },
      additionalTools([searchTool({ description: "Find current files", parameters: {
        type: "object", properties: { glob: { type: "string" } }, required: ["glob"],
      } }), shellTool()]),
      ...clientTools([searchTool({ description: "Latest search schema", defer_loading: true })]),
      { role: "system", content: "Updated system instruction: never leave the fixture root." },
      { type: "message", role: "developer", content: [
        { type: "input_text", text: "Updated developer instruction: report only fixture results." },
      ] },
    ],
    tools: [shellTool()],
    explicitHistory: [
      { role: "system", content: "Obsolete system instruction." },
      { role: "developer", content: "Obsolete developer instruction." },
      additionalTools([searchTool({ name: "old_tool" })]),
      ...remoteHistory(),
    ],
  },
];

cases.forEach(({ name, context, tools, explicitHistory }) => {
  test(name, () => {
    const payload = request(context, tools);
    const before = structuredClone({ payload, explicitHistory });

    const actual = applyRemoteHistoryPayloadPatch({ payload, explicitHistory });

    assert.deepEqual(actual.input.filter(isContext), context);
    assert.deepEqual(actual.input.filter((item) => !isContext(item)), explicitHistory.filter((item) => !isContext(item)));
    assert.deepEqual({ ...actual, input: undefined }, { ...payload, input: undefined });
    assert.ok(actual.input.findLastIndex(isContext) < actual.input.findIndex(isLiveTurn));
    assert.deepEqual({ payload, explicitHistory }, before);
  });
});

test("repeated replay and a subsequent compacted tool turn do not duplicate context or mutate inputs", () => {
  const context = [
    ...instructions(), additionalTools([searchTool(), shellTool()]),
    ...clientTools([searchTool({ defer_loading: true })]),
  ];
  const payload = request(context, [shellTool()]);
  const explicitHistory = remoteHistory();
  const nextHistory = [
    { type: "compaction", encrypted_content: "synthetic-second-opaque" },
    { type: "function_call", call_id: "fixture-shell", name: "shell", arguments: '{"command":"printf fixture"}' },
    { type: "function_call_output", call_id: "fixture-shell", output: "fixture" },
    { role: "user", content: "Continue with current tools." },
  ];
  const before = structuredClone({ payload, explicitHistory, nextHistory });

  const first = applyRemoteHistoryPayloadPatch({ payload, explicitHistory });
  const firstBefore = structuredClone(first);
  const again = applyRemoteHistoryPayloadPatch({ payload: first, explicitHistory });
  const second = applyRemoteHistoryPayloadPatch({ payload: again, explicitHistory: nextHistory });

  assert.deepEqual(again, first);
  assert.deepEqual(second.input.filter(isContext), context);
  assert.deepEqual(second.input.filter((item) => !isContext(item)), nextHistory);
  assert.deepEqual({ ...second, input: undefined }, { ...payload, input: undefined });
  assert.deepEqual({ payload, explicitHistory, nextHistory }, before);
  assert.deepEqual(first, firstBefore);
});

test("absent compatible remote history leaves a normal request unchanged", () => {
  const payload = request([...instructions(), additionalTools([searchTool(), shellTool()])], [shellTool()]);
  const before = structuredClone(payload);

  const actual = applyRemoteHistoryPayloadPatch({ payload, explicitHistory: undefined });

  assert.deepEqual(actual, before);
  assert.deepEqual(payload, before);
});
