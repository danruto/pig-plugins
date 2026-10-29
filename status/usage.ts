interface SessionManager {
	getSessionId(): string;
	getEntries?(): SessionEntry[];
	getBranch(): SessionEntry[];
}

interface StatusContext {
	model?: { provider: string; id: string };
	sessionManager: SessionManager;
}

interface SessionEntry {
	type?: string;
	message?: {
		role?: string;
		usage?: {
			input?: number;
			output?: number;
			cacheRead?: number;
			cacheWrite?: number;
			cost?: { total?: number };
		};
	};
}

interface PiAPI {
	events: {
		on(name: string, handler: (payload: unknown) => void): void;
		emit(name: string, payload: unknown): void;
	};
	on(name: string, handler: (event: unknown, context: StatusContext) => void): void;
}

interface QuotaWindow {
	label: string;
	usedPercent: number;
	resetsAt?: number;
}

function record(value: unknown): Record<string, unknown> | undefined {
	if (value === null || typeof value !== "object" || Array.isArray(value)) return;
	return Object.fromEntries(Object.entries(value));
}

function finite(value: unknown): value is number {
	return typeof value === "number" && Number.isFinite(value);
}

function reportWindows(report: Record<string, unknown>): QuotaWindow[] {
	if (!Array.isArray(report.buckets)) return [];
	return report.buckets.flatMap((value: unknown) => {
		const bucket = record(value);
		if (!bucket || typeof bucket.label !== "string") return [];
		let used: number | undefined;
		if (bucket.unit === "percent") {
			if (finite(bucket.used)) used = bucket.used;
			else if (finite(bucket.remaining)) used = 100 - bucket.remaining;
		} else if (finite(bucket.limit) && bucket.limit > 0) {
			if (finite(bucket.used)) used = 100 * bucket.used / bucket.limit;
			else if (finite(bucket.remaining)) used = 100 * (1 - bucket.remaining / bucket.limit);
		}
		if (used === undefined || !Number.isFinite(used)) return [];
		return [{
			label: /^(672|696|720|744)h$/.test(bucket.label) ? "Month" : bucket.label,
			usedPercent: Math.max(0, Math.min(100, used)),
			...(finite(bucket.resetsAt) ? { resetsAt: bucket.resetsAt } : {}),
		}];
	});
}

export default function usageStatus(pi: PiAPI): void {
	let context: StatusContext | undefined;
	let windows: Array<QuotaWindow & { expiresAt: number }> = [];
	let generation = 0;
	let controller: AbortController | undefined;
	let expiry: ReturnType<typeof setTimeout> | undefined;
	let identity = "";

	const render = (): void => {
		if (!context) return;
		let input = 0, output = 0, cacheRead = 0, cacheWrite = 0, cost = 0;
		const entries = context.sessionManager.getEntries?.() ?? context.sessionManager.getBranch();
		for (const entry of entries) {
			if (entry.type !== "message" || entry.message?.role !== "assistant") continue;
			const usage = entry.message.usage;
			input += usage?.input ?? 0;
			output += usage?.output ?? 0;
			cacheRead += usage?.cacheRead ?? 0;
			cacheWrite += usage?.cacheWrite ?? 0;
			cost += usage?.cost?.total ?? 0;
		}
		pi.events.emit("pig-status:usage", {
			input, output, cacheRead, cacheWrite, cost,
			subscription: context.model?.provider === "claude-bridge",
			windows: windows.map(({ expiresAt: _expiresAt, ...window }) => window),
		});
	};

	const clear = (): void => {
		generation++;
		controller?.abort();
		controller = undefined;
		clearTimeout(expiry);
		expiry = undefined;
		windows = [];
	};

	const publish = (next: typeof windows): void => {
		clearTimeout(expiry);
		const now = Date.now();
		windows = next.filter((window) => window.expiresAt > now && (window.resetsAt === undefined || window.resetsAt > now));
		render();
		const deadline = Math.min(...windows.map((window) => Math.min(window.expiresAt, window.resetsAt ?? Number.POSITIVE_INFINITY)));
		if (!Number.isFinite(deadline)) return;
		expiry = setTimeout(() => publish(windows), Math.max(1, Math.min(2_147_483_647, deadline - now)));
		expiry.unref();
	};

	const query = (): void => {
		if (!context || context.model?.provider === "claude-bridge") return;
		const ctx = context;
		controller?.abort();
		controller = new AbortController();
		const signal = controller.signal;
		const owner = ++generation;
		let accepted = false;
		pi.events.emit("usage:query:v1", {
			session: ctx.sessionManager,
			sessionId: ctx.sessionManager.getSessionId(),
			providerId: ctx.model?.provider,
			refresh: false,
			signal,
			respond: (result: unknown): boolean => {
				if (accepted || signal.aborted) return false;
				accepted = true;
				void Promise.resolve(result).then((value: unknown) => {
					if (signal.aborted || owner !== generation || ctx !== context) return;
					const response = record(value);
					const report = record(response?.report);
					if (response?.status === "ready" && report && report.providerId === ctx.model?.provider && finite(response.expiresAt) && response.expiresAt > Date.now()) {
						const expiresAt = response.expiresAt;
						publish(reportWindows(report).map((window) => ({ ...window, expiresAt })));
					} else {
						publish([]);
					}
				}, () => {
					if (!signal.aborted && owner === generation) publish([]);
				});
				return true;
			},
		});
		if (!accepted) publish([]);
	};

	pi.events.on("usage:changed:v1", (value) => {
		const event = record(value);
		if (!context || event?.session !== context.sessionManager || event.sessionId !== context.sessionManager.getSessionId() || event.providerId !== context.model?.provider) return;
		query();
	});

	const update = (_event: unknown, ctx: StatusContext): void => {
		const next = JSON.stringify([ctx.sessionManager.getSessionId(), ctx.model?.provider, ctx.model?.id]);
		if (next !== identity) {
			clear();
			identity = next;
		}
		context = ctx;
		render();
		query();
	};
	for (const name of ["session_start", "model_select", "turn_end", "session_tree"]) pi.on(name, update);
	pi.on("session_shutdown", () => {
		clear();
		context = undefined;
		identity = "";
	});
}
