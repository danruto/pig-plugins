/** Replays opaque remote history with the current request's ordered context, never stale declarations. */

function isCurrentContext(item: unknown): boolean {
	if (typeof item !== "object" || item === null) return false;
	if ("role" in item && (item.role === "system" || item.role === "developer")) return true;
	if (!("type" in item)) return false;
	return item.type === "additional_tools" || item.type === "configuration_update"
		|| ((item.type === "tool_search_call" || item.type === "tool_search_output")
			&& "execution" in item && item.execution === "client");
}

export function applyRemoteHistoryPayloadPatch({ payload, explicitHistory }: {
	payload: Record<string, unknown>;
	explicitHistory: readonly unknown[] | undefined;
}): Record<string, unknown> {
	if (explicitHistory === undefined) return payload;
	const currentInput: unknown[] = Array.isArray(payload.input) ? payload.input : [];
	return {
		...payload,
		input: [
			...currentInput.filter(isCurrentContext),
			...explicitHistory.filter((item) => !isCurrentContext(item)),
		],
	};
}
