/** Disposable response gates shared by registered lifecycle controls and request-observed fixture choreography. */
import assert from "node:assert/strict";

export function createResponseBarrier() {
  let enter;
  let release;
  let complete;
  let failure;
  let held = false;
  const entered = new Promise((resolve) => { enter = resolve; });
  const released = new Promise((resolve) => { release = resolve; });
  const completed = new Promise((resolve) => { complete = resolve; });
  function cancel(error) { failure ??= error; release(); }
  return {
    entered, completed, release, cancel,
    async hold(signal, timeoutMs = 5000) {
      assert.equal(held, false, "Response barrier entered twice");
      held = true;
      const abort = () => cancel(signal.reason);
      const timer = setTimeout(() => cancel(new Error("Unmet response barrier prerequisite")), timeoutMs);
      signal?.addEventListener("abort", abort, { once: true });
      enter();
      try {
        if (signal?.aborted) abort();
        await released;
        if (failure) throw failure;
      } finally {
        clearTimeout(timer);
        signal?.removeEventListener("abort", abort);
        complete();
      }
    },
  };
}

export function createRequestChoreography(requests) {
  const byUrl = new Map(requests.map((request) => [request.url, request]));
  assert.equal(byUrl.size, requests.length, "Duplicate choreography request");
  const prerequisites = new Map();
  for (const request of requests) {
    const after = request.afterResponses ?? [];
    for (const url of [...after, ...(request.releaseAfterRequest ? [request.releaseAfterRequest] : [])]) {
      assert.ok(byUrl.has(url), `Unknown choreography prerequisite: ${url}`);
    }
    prerequisites.set(`request:${request.url}`, after.map((url) => `response:${url}`));
    prerequisites.set(`response:${request.url}`, [request.url, ...(request.releaseAfterRequest ? [request.releaseAfterRequest] : [])].map((url) => `request:${url}`));
  }
  const reachable = new Set();
  let previous;
  do {
    previous = reachable.size;
    for (const [event, dependencies] of prerequisites) {
      if (dependencies.every((dependency) => reachable.has(dependency))) reachable.add(event);
    }
  } while (reachable.size !== previous);
  assert.equal(reachable.size, prerequisites.size, "Cyclic choreography prerequisites");
  const barriers = new Map(requests.filter((request) => request.releaseAfterRequest).map((request) => [request.url, createResponseBarrier()]));
  const observed = new Set();
  const outstanding = new Set();
  const released = new Set();
  const events = [];
  let failure;
  let finished = false;
  function cancel(error) {
    if (failure || finished) return;
    failure = error;
    events.push({ kind: "cancelled", message: error.message });
    for (const barrier of barriers.values()) barrier.cancel(error);
  }
  return {
    events,
    cancel,
    observeRequest(url) {
      try {
        if (failure) throw failure;
        assert.equal(url, requests[observed.size]?.url, "Fixture request choreography drift");
        const request = byUrl.get(url);
        for (const prerequisite of request.afterResponses ?? []) assert.ok(released.has(prerequisite), `Response prerequisite not released: ${prerequisite}`);
        observed.add(url);
        outstanding.add(url);
        events.push({ kind: "request-observed", url, outstanding: [...outstanding] });
        for (const request of requests) {
          if (request.releaseAfterRequest === url) barriers.get(request.url).release();
        }
      } catch (error) { cancel(error); throw error; }
    },
    async releaseResponse(url, signal) {
      try {
        if (failure) throw failure;
        assert.ok(outstanding.has(url), `Unobserved response: ${url}`);
        const request = byUrl.get(url);
        const barrier = barriers.get(url);
        if (barrier) {
          events.push({ kind: "response-held", url, releaseAfterRequest: request.releaseAfterRequest });
          await barrier.hold(signal);
          assert.ok(observed.has(request.releaseAfterRequest), `Unmet request prerequisite: ${request.releaseAfterRequest}`);
        }
        outstanding.delete(url);
        released.add(url);
        events.push({ kind: "response-released", url });
      } catch (error) { cancel(error); throw error; }
    },
    finish() {
      try {
        if (failure) throw failure;
        assert.equal(observed.size, requests.length, "Missing choreography requests");
        assert.equal(released.size, requests.length, "Unreleased choreography responses");
        for (const request of requests.filter((request) => request.releaseAfterRequest)) {
          const witness = events.find((event) => event.kind === "request-observed" && event.url === request.releaseAfterRequest);
          assert.ok(witness.outstanding.includes(request.url), `Missing concurrent request overlap: ${request.url}`);
        }
        finished = true;
      } catch (error) { cancel(error); throw error; }
    },
  };
}
