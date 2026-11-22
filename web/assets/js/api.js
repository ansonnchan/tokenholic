// Reads the API's public, read-only routes. This is the site's only network
// code: it sends GET requests with no credential, cookie, or custom header,
// and never writes.
//
// The demo API sleeps when idle and can take about a minute to wake. A page
// must say so instead of showing empty data, so a read reports "slow" once it
// has waited a moment, keeps retrying while the API is unreachable or
// starting, and gives up only after the whole budget is spent.

const DEFAULTS = {
  slowAfterMs: 2500, // when to tell the reader the API is waking
  attemptMs: 45_000, // one request may hang this long behind a cold start
  budgetMs: 120_000, // total time before giving up
  pauseMs: 3000, // wait between attempts
};

// The outcomes a page has to draw. "not_found" covers a private, deleted, or
// nonexistent record alike: the API answers them identically, and so must
// the page.
export const OK = "ok";
export const NOT_FOUND = "not_found";
export const RATE_LIMITED = "rate_limited";
export const UNREACHABLE = "unreachable";
export const UNCONFIGURED = "unconfigured";

// apiBase validates the configured API origin. Only HTTPS is accepted, plus
// plain HTTP on loopback for local development.
export function apiBase(origin) {
  try {
    const parsed = new URL(origin);
    const loopback = parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost";
    if (parsed.protocol === "https:" || (parsed.protocol === "http:" && loopback)) return parsed.origin;
  } catch {
    // fall through
  }
  return "";
}

// readPublic fetches one public route. path starts with "/v1/public/".
// onSlow is called at most once, when the read is taking long enough that
// the page should show the wake-up state.
export async function readPublic(origin, path, { onSlow = () => {}, fetch = globalThis.fetch, clock = realClock, ...overrides } = {}) {
  const base = apiBase(origin);
  if (!base) return { status: UNCONFIGURED };
  const limits = { ...DEFAULTS, ...overrides };
  const started = clock.now();
  let toldSlow = false;
  const slow = () => {
    if (!toldSlow) {
      toldSlow = true;
      onSlow();
    }
  };
  const slowTimer = clock.setTimeout(slow, limits.slowAfterMs);
  try {
    for (;;) {
      const outcome = await attempt(base + path, limits.attemptMs, fetch, clock);
      if (outcome) return outcome;
      // No usable answer: the API is asleep, starting, or unreachable.
      slow();
      if (clock.now() - started + limits.pauseMs >= limits.budgetMs) return { status: UNREACHABLE };
      await new Promise((resolve) => clock.setTimeout(resolve, limits.pauseMs));
    }
  } finally {
    clock.clearTimeout(slowTimer);
  }
}

// attempt makes one request. It returns an outcome, or null when the request
// should be tried again.
async function attempt(url, timeoutMs, fetch, clock) {
  const abort = new AbortController();
  const timer = clock.setTimeout(() => abort.abort(), timeoutMs);
  try {
    const response = await fetch(url, {
      method: "GET",
      mode: "cors",
      credentials: "omit",
      cache: "no-store",
      referrerPolicy: "no-referrer",
      signal: abort.signal,
    });
    if (response.status === 200) {
      return { status: OK, data: await response.json() };
    }
    if (response.status === 404) return { status: NOT_FOUND };
    if (response.status === 429) {
      const seconds = Number.parseInt(response.headers.get("Retry-After") ?? "", 10);
      return { status: RATE_LIMITED, retryAfterSeconds: Number.isFinite(seconds) && seconds > 0 ? seconds : null };
    }
    // A gateway error is what a host returns while the API starts.
    if (response.status >= 500) return null;
    return { status: UNREACHABLE };
  } catch {
    // Refused, timed out, blocked, or an unreadable body.
    return null;
  } finally {
    clock.clearTimeout(timer);
  }
}

const realClock = {
  now: () => Date.now(),
  setTimeout: (callback, ms) => globalThis.setTimeout(callback, ms),
  clearTimeout: (id) => globalThis.clearTimeout(id),
};
