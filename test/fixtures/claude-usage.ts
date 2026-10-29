export default function (pi) {
	pi.registerProvider("claude-bridge", {
		api: "offline-fixture", apiKey: "fixture", baseUrl: "http://127.0.0.1:1",
		models: [{ id: "fixture", name: "Offline fixture", reasoning: false, input: ["text"], contextWindow: 200_000, maxTokens: 4096, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } }],
		streamSimple: () => { throw new Error("This fixture must never receive a model prompt"); },
	});
	pi.on("session_start", async (_event, ctx) => {
		await ctx.ui.setStatus("claude-usage", "Claude 5h 6% ↻3h · 7d 76% ↻8h · Fable 99% ↻8h");
		pi.events.emit("pig-status:usage", { input: 100, output: 25, cacheRead: 900, cacheWrite: 300, cost: 0, subscription: true, windows: [] });
	});
}
