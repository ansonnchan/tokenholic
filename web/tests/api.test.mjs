import assert from "node:assert/strict";
import { test } from "node:test";

import { apiBase, NOT_FOUND, OK, RATE_LIMITED, readPublic, UNCONFIGURED, UNREACHABLE } from "../assets/js/api.js";

// A clock the test advances by hand, so nothing here really waits.
function fakeClock() {
  let now = 0;
  let nextID = 1;
  const timers = new Map();
  return {
    now: () => now,
    setTimeout(callback, ms) {
      timers.set(nextID, { at: now + ms, callback });
      return nextID++;
    },
    clearTimeout: (id) => timers.delete(id),
    // advance runs every timer due within ms, in order.
    async advance(ms) {
      const end = now + ms;
      for (;;) {
        const due = [...timers.entries()].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!due) break;
        timers.delete(due[0]);
        now = due[1].at;
        due[1].callback();
        await settle();
      }
      now = end;
      await settle();
    },
  };
}
const settle = async () => {
  for (let turn = 0; turn < 20; turn++) await Promise.resolve();
};

const reply = (status, body = {}, headers = {}) => ({
  status,
  headers: { get: (name) => headers[name] ?? null },
  json: async () => body,
});
const origin = "https://api.tokenholic.example";

test("only an HTTPS origin, or loopback HTTP, is used as the API", () => {
  assert.equal(apiBase("https://api.tokenholic.example"), "https://api.tokenholic.example");
  assert.equal(apiBase("https://api.tokenholic.example/ignored/path"), "https://api.tokenholic.example");
  assert.equal(apiBase("http://127.0.0.1:8080"), "http://127.0.0.1:8080");
  assert.equal(apiBase("http://localhost:8080"), "http://localhost:8080");
  for (const bad of ["", "http://api.tokenholic.example", "javascript:alert(1)", "api.tokenholic.example", null, undefined]) {
    assert.equal(apiBase(bad), "", String(bad));
  }
});

test("a site with no API configured says so and sends nothing", async () => {
  let called = false;
  const outcome = await readPublic("", "/v1/public/users/dana", { fetch: () => { called = true; } });
  assert.deepEqual(outcome, { status: UNCONFIGURED });
  assert.equal(called, false);
});

test("a read sends a plain credential-free GET and returns the data", async () => {
  const requests = [];
  const outcome = await readPublic(origin, "/v1/public/users/dana", {
    clock: fakeClock(),
    fetch: async (url, options) => {
      requests.push({ url, options });
      return reply(200, { login: "dana" });
    },
  });
  assert.deepEqual(outcome, { status: OK, data: { login: "dana" } });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, "https://api.tokenholic.example/v1/public/users/dana");
  const { method, credentials, headers, body } = requests[0].options;
  assert.deepEqual({ method, credentials, headers, body }, { method: "GET", credentials: "omit", headers: undefined, body: undefined });
});

test("not-found and rate-limited answers are final, not retried", async () => {
  let calls = 0;
  const missing = await readPublic(origin, "/v1/public/users/ghost", { clock: fakeClock(), fetch: async () => (calls++, reply(404)) });
  assert.deepEqual(missing, { status: NOT_FOUND });
  const limited = await readPublic(origin, "/v1/public/users/dana", {
    clock: fakeClock(), fetch: async () => (calls++, reply(429, {}, { "Retry-After": "12" })),
  });
  assert.deepEqual(limited, { status: RATE_LIMITED, retryAfterSeconds: 12 });
  const unknownWait = await readPublic(origin, "/v1/public/users/dana", { clock: fakeClock(), fetch: async () => (calls++, reply(429)) });
  assert.deepEqual(unknownWait, { status: RATE_LIMITED, retryAfterSeconds: null });
  assert.equal(calls, 3);
});

test("a slow answer reports the wake-up state once, then the data", async () => {
  const clock = fakeClock();
  let slow = 0;
  let respond;
  const pending = readPublic(origin, "/v1/public/users/dana", {
    clock, onSlow: () => slow++, fetch: () => new Promise((resolve) => { respond = resolve; }),
  });
  await clock.advance(2000);
  assert.equal(slow, 0, "not slow yet");
  await clock.advance(1000);
  assert.equal(slow, 1, "slow after the threshold");
  await clock.advance(20_000);
  respond(reply(200, { login: "dana" }));
  assert.deepEqual(await pending, { status: OK, data: { login: "dana" } });
  assert.equal(slow, 1);
});

test("a fast answer never shows the wake-up state", async () => {
  const clock = fakeClock();
  let slow = 0;
  const outcome = await readPublic(origin, "/v1/public/users/dana", { clock, onSlow: () => slow++, fetch: async () => reply(200, {}) });
  await clock.advance(10_000);
  assert.equal(outcome.status, OK);
  assert.equal(slow, 0);
});

test("a starting API is retried until it answers", async () => {
  const clock = fakeClock();
  let slow = 0;
  const answers = [() => Promise.reject(new TypeError("Failed to fetch")), () => Promise.resolve(reply(503)), () => Promise.resolve(reply(200, { ok: true }))];
  let calls = 0;
  const pending = readPublic(origin, "/v1/public/users/dana", { clock, onSlow: () => slow++, fetch: () => answers[calls++]() });
  await settle();
  assert.equal(slow, 1, "a failed attempt shows the wake-up state at once");
  await clock.advance(10_000);
  assert.deepEqual(await pending, { status: OK, data: { ok: true } });
  assert.equal(calls, 3);
});

test("an API that never answers ends as unreachable within the budget", async () => {
  const clock = fakeClock();
  let calls = 0;
  let outcome;
  readPublic(origin, "/v1/public/users/dana", {
    clock,
    fetch: (_url, { signal }) => {
      calls++;
      return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(new Error("aborted"))));
    },
  }).then((result) => { outcome = result; });
  await clock.advance(119_000);
  assert.equal(outcome, undefined, "still trying inside the budget");
  await clock.advance(30_000);
  assert.deepEqual(outcome, { status: UNREACHABLE });
  assert.ok(calls >= 2 && calls <= 4, `${calls} attempts`);
});

test("an unexpected status is not shown as data or as not-found", async () => {
  const outcome = await readPublic(origin, "/v1/public/users/dana", { clock: fakeClock(), fetch: async () => reply(400) });
  assert.deepEqual(outcome, { status: UNREACHABLE });
});
