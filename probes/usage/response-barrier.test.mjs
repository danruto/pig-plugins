/** Runnable owner checks for disposable response release, dependency rejection, overlap and cancellation. */
import assert from "node:assert/strict";
import test from "node:test";
import { createRequestChoreography, createResponseBarrier } from "./response-barrier.mjs";

test("response barrier holds until explicit release and supports release before entry", async () => {
  const barrier = createResponseBarrier();
  let returned = false;
  const response = (async () => { await barrier.hold(); returned = true; })();
  await barrier.entered;
  assert.equal(returned, false);
  barrier.release();
  await response;
  await barrier.completed;
  assert.equal(returned, true);
  const early = createResponseBarrier();
  early.release();
  await early.hold();
  await early.completed;
});

test("request choreography releases responses only after observed siblings with real outstanding overlap", async () => {
  const choreography = createRequestChoreography([
    { url: "identity" },
    { url: "left", afterResponses: ["identity"], releaseAfterRequest: "right" },
    { url: "right", afterResponses: ["identity"], releaseAfterRequest: "summary" },
    { url: "summary", afterResponses: ["left"] },
  ]);
  choreography.observeRequest("identity");
  await choreography.releaseResponse("identity");
  choreography.observeRequest("left");
  const left = choreography.releaseResponse("left");
  assert.deepEqual(choreography.events.at(-1), { kind: "response-held", url: "left", releaseAfterRequest: "right" });
  choreography.observeRequest("right");
  const right = choreography.releaseResponse("right");
  await left;
  choreography.observeRequest("summary");
  await Promise.all([right, choreography.releaseResponse("summary")]);
  choreography.finish();
  assert.deepEqual(choreography.events.filter((event) => event.kind === "request-observed").map((event) => event.url), ["identity", "left", "right", "summary"]);
  const summaryObserved = choreography.events.findIndex((event) => event.kind === "request-observed" && event.url === "summary");
  assert.ok(choreography.events.findIndex((event) => event.kind === "response-released" && event.url === "left") < summaryObserved);
  assert.ok(choreography.events.findIndex((event) => event.kind === "response-released" && event.url === "right") > summaryObserved);
  assert.deepEqual(choreography.events.find((event) => event.kind === "request-observed" && event.url === "summary").outstanding, ["right", "summary"]);
});

test("request choreography rejects missing, cyclic, duplicate and causally impossible prerequisites", async () => {
  assert.throws(() => createRequestChoreography([{ url: "left", releaseAfterRequest: "missing" }]), /Unknown choreography prerequisite/u);
  assert.throws(() => createRequestChoreography([{ url: "left", releaseAfterRequest: "right" }, { url: "right", afterResponses: ["left"] }]), /Cyclic choreography prerequisites/u);
  assert.throws(() => createRequestChoreography([{ url: "left" }, { url: "left" }]), /Duplicate choreography request/u);
  const order = createRequestChoreography([{ url: "left" }, { url: "right" }]);
  assert.throws(() => order.observeRequest("right"), /Fixture request choreography drift/u);
  const causal = createRequestChoreography([{ url: "left" }, { url: "right", afterResponses: ["left"] }]);
  causal.observeRequest("left");
  assert.throws(() => causal.observeRequest("right"), /Response prerequisite not released/u);
  await assert.rejects(causal.releaseResponse("left"), /Response prerequisite not released/u);
});

test("unmet requests fail finish and release outstanding waiters instead of hanging", async () => {
  const choreography = createRequestChoreography([{ url: "left", releaseAfterRequest: "right" }, { url: "right" }]);
  choreography.observeRequest("left");
  const waiting = choreography.releaseResponse("left");
  assert.throws(() => choreography.finish(), /Missing choreography requests/u);
  await assert.rejects(waiting, /Missing choreography requests/u);
  assert.equal(choreography.events.at(-1).kind, "cancelled");
  const barrier = createResponseBarrier();
  await assert.rejects(barrier.hold(undefined, 1), /Unmet response barrier prerequisite/u);
  await barrier.completed;
});

test("cancellation rejects current and pre-aborted holds and joins every sibling waiter", async () => {
  const choreography = createRequestChoreography([
    { url: "left", releaseAfterRequest: "future" },
    { url: "right", releaseAfterRequest: "future" },
    { url: "future" },
  ]);
  const controller = new AbortController();
  const error = new Error("fixture interrupted");
  choreography.observeRequest("left");
  const left = choreography.releaseResponse("left", controller.signal);
  choreography.observeRequest("right");
  const right = choreography.releaseResponse("right");
  controller.abort(error);
  await Promise.all([assert.rejects(left, (cause) => cause === error), assert.rejects(right, (cause) => cause === error)]);
  assert.throws(() => choreography.finish(), (cause) => cause === error);
  const early = createResponseBarrier();
  await assert.rejects(early.hold(controller.signal), (cause) => cause === error);
  await early.completed;
});
