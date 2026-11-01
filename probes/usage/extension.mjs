/** Real PiG probe of the installed dist and shipped source; not a package member. Maglev and TurboFan are disabled in the observed process because optimized code does not increment V8 invocation counts, so a hot function would drop out of a coverage window. */
import { writeFile } from "node:fs/promises";
import { setFlagsFromString } from "node:v8";
import * as backend from "../../node_modules/@narumitw/pi-usage/dist/index.ts";
import * as source from "../../node_modules/@narumitw/pi-usage/src/index.ts";
import { copyUsageReportForApi } from "../../node_modules/@narumitw/pi-usage/src/usage-api.ts";
import { createOAuthCredentialCandidateReader as sourceReader } from "../../node_modules/@narumitw/pi-usage/src/oauth-credential-source.ts";
import { createOAuthCredentialCandidateReader as distReader } from "../../node_modules/@narumitw/pi-usage/dist/chunks/chunk-OSLPZGGG.ts";
import { registerLifecycle } from "./lifecycle.mjs";
import { writeHostAuthSync } from "./settings-auth.mjs";
import { cases } from "./cases.mjs";
import { captureConsumers, protocolFixtures } from "./consumers.mjs";
import { observeBranches, beginBranchObservation, endBranchObservation } from "./branch-runtime.mjs";
import { registerBridge } from "./bridge.mjs";

setFlagsFromString("--no-maglev");
setFlagsFromString("--no-turbofan");

export default function usageFixture(pi) {
  let compositionSession;
  let compositionSessionId;
  pi.events.on("usage:query:v1", (request) => {
    if (compositionSession === undefined) {
      compositionSession = request.session;
      compositionSessionId = request.sessionId;
    }
  });
  registerLifecycle(pi, () => ({ session: compositionSession, sessionId: compositionSessionId }));
  const captureBridge = registerBridge(pi, () => ({ session: compositionSession, sessionId: compositionSessionId }));
  pi.registerCommand("fixture-observe", {
    description: "Observe pinned usage behavior with synthetic loopback fixtures",
    handler: async (args, ctx) => {
      if (args === "lifecycle-only") {
        await writeFile(process.env.USAGE_FIXTURE_OUTPUT, JSON.stringify({ results: [], consumers: [], protocol: [] }));
        return;
      }
      const results = [];
      for (const implementation of ["dist", "source"]) {
        const library = implementation === "dist" ? backend : source;
        for (const item of cases) {
          process.env.USAGE_FIXTURE_CASE = `${implementation}/${item.id}`;
          const trace = [];
          globalThis[Symbol.for("usage-fixture-trace")] = trace;
          let guardCalls = 0;
          const guard = async () => {
            guardCalls += 1;
            trace.push({ kind: "guard" });
            if (item.rejectGuard === guardCalls) throw new Error("Synthetic auth ownership changed.");
          };
          const access = item.apiKey ?? "fixture-access";
          const auth = {
            apiKey: access, headers: { Authorization: `Bearer ${access}` }, fingerprint: "synthetic",
            secrets: [access, `Bearer ${access}`, "fixture-refresh"],
            model: { provider: item.provider, id: "synthetic", baseUrl: item.baseUrl },
            ...(item.companionEnabled ? { openaiUseCodexUsage: true } : {}),
            ...(item.companion ? { openaiClientId: "fixture-client", openaiCompanion: { headers: { Authorization: "Bearer fixture-companion", "ChatGPT-Account-Id": "fixture-account" } } } : {}),
          };
          const controller = new AbortController();
          if (item.aborted) controller.abort();
          try {
            const collect = async () => {
              const report = await library.queryProviderUsage(library.adapterForProvider(item.provider), auth, controller.signal, 3000, guard,
                item.provider === "fireworks" ? { fireworksAccountId: item.target } : undefined);
              return { report, apiReport: copyUsageReportForApi(report) };
            };
            const { report, apiReport } = implementation === "source" ? await observeBranches(item.id, collect) : await collect();
            const present = () => ({
              current: library.formatUsageReport(report, "current"),
              configured: library.formatUsageReport(report, "configured"),
              status: library.formatUsageStatusline(report, auth.model),
              used: library.formatUsageStatusline(report, auth.model, Date.now(), true, "used"),
              noCountdown: library.formatUsageStatusline(report, auth.model, Date.now(), false),
              expiredReset: library.formatUsageStatusline(report, auth.model, Date.now() + 604800000),
            });
            const presentation = implementation === "source" ? await observeBranches(`presentation:${item.id}`, present) : present();
            results.push({ implementation, id: item.id, outcome: "report", report, apiReport, presentation, trace });
          } catch (error) {
            if (!(error instanceof Error)) throw error;
            results.push({ implementation, id: item.id, outcome: "error", error: { name: error.name, message: error.message }, trace });
          }
        }
      }
      const consumers = [];
      for (const implementation of ["dist", "source"]) {
        consumers.push(...await captureConsumers(implementation === "dist" ? backend : source, implementation, process.env.TMPDIR,
          implementation === "source" ? observeBranches : (_id, operation) => operation(),
          { pi, ctx, writeAuthSync: writeHostAuthSync, createReader: implementation === "source" ? sourceReader : distReader }));
      }
      process.env.USAGE_FIXTURE_CASE = "host";
      globalThis[Symbol.for("usage-fixture-trace")] = undefined;
      const protocol = [];
      for (const { id, event, options } of protocolFixtures) {
        process.env.USAGE_FIXTURE_CASE = `protocol/${id}`;
        const trace = [];
        globalThis[Symbol.for("usage-fixture-trace")] = trace;
        const controller = new AbortController();
        if (options.preaborted) controller.abort();
        let offered = false;
        let finish;
        const result = new Promise((resolve) => { finish = resolve; });
        const request = {
          session: options.foreignSession ? {} : options.ownSession ? ctx.sessionManager : compositionSession,
          sessionId: compositionSessionId ?? ctx.sessionManager.getSessionId(), signal: options.invalidSignal ? {} : controller.signal,
          ...(options.providerId === undefined ? {} : { providerId: options.providerId }),
          ...(options.refresh === undefined ? {} : { refresh: options.refresh }),
          respond(value) {
            offered = true;
            finish(value);
            if (options.offerThrows) throw new Error("Synthetic offer failure");
            return options.offerReturn !== false;
          },
        };
        await beginBranchObservation(id);
        pi.events.emit(event, request);
        if (options.cancel) controller.abort();
        let timeout;
        const response = await Promise.race([result, new Promise((resolve) => {
          timeout = setTimeout(() => resolve({ fixtureTimeout: true }), options.expectNoOffer ? 100 : 3000);
        })]);
        clearTimeout(timeout);
        await endBranchObservation();
        protocol.push({ id, event, options, offered, response, trace });
        globalThis[Symbol.for("usage-fixture-trace")] = undefined;
      }
      process.env.USAGE_FIXTURE_CASE = "host";
      protocol.push(...await captureBridge(observeBranches));
      const identity = { observedCompositionSession: compositionSession !== undefined, ownManagerMatches: ctx.sessionManager === compositionSession, sessionIdMatches: ctx.sessionManager.getSessionId() === compositionSessionId };
      await writeFile(process.env.USAGE_FIXTURE_OUTPUT, JSON.stringify({ results, consumers, protocol, identity, locale: Intl.DateTimeFormat().resolvedOptions() }, null, 2) + "\n", { mode: 0o600 });
    },
  });
}
