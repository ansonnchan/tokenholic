// Turns API values into display text. The rule this file exists to keep, the
// same one internal/tui/values.go keeps for the terminal: a value that is not
// known renders as the unavailable glyph, a partial one as a lower bound, and
// only a known number renders as a number. Nothing here invents a zero.
//
// Everything is plain data in and plain text out, so it runs under Node's
// test runner without a browser.

import { parseDecimal, percent, tokens, usd } from "./format.js";

export const UNAVAILABLE = "—";
export const AT_LEAST = "≥";

// An API value is {value: decimal string or null, state}. state is one of
// "complete", "partial", "unavailable", or "empty".
function known(value) {
  return value && typeof value === "object" ? parseDecimal(value.value) : null;
}

function bound(value) {
  return value.state === "partial" ? AT_LEAST : "";
}

// tokensText is a compact count, a lower bound, or the unavailable glyph.
export function tokensText(value) {
  const count = known(value);
  return count === null ? UNAVAILABLE : bound(value) + tokens(count);
}

// estimateText is an API-equivalent estimate in dollars. It carries no EST
// label; the tile or column it sits in does.
export function estimateText(value) {
  const micros = known(value);
  return micros === null ? UNAVAILABLE : bound(value) + usd(micros);
}

// pricedBasisPoints is the share of tokens that have a price, in hundredths
// of a percent, rounded down. It is null when there are no tokens to take a
// share of, which is not the same as none being priced.
export function pricedBasisPoints(priced, unpriced) {
  const [a, b] = [parseDecimal(priced), parseDecimal(unpriced)];
  if (a === null || b === null || a + b === 0n) return null;
  return Number((a * 10_000n) / (a + b));
}

export function pricedText(basisPoints) {
  return Number.isInteger(basisPoints) ? percent(basisPoints) : UNAVAILABLE;
}

// tokensNote and estimateNote say in words why a value is not a plain
// number, or "" when it is.
export function tokensNote(value) {
  switch (value?.state) {
    case "complete":
      return "";
    case "partial":
      return "Tokens are a lower bound: an agent did not record every category.";
    case "empty":
      return "No usage in this period.";
    default:
      return "The token total is unknown.";
  }
}

export function estimateNote(value) {
  switch (value?.state) {
    case "complete":
      return "";
    case "partial":
      return "The estimate is a lower bound: some usage could not be fully priced.";
    case "empty":
      return "No usage in this period.";
    default:
      return "No estimate: this usage has no known price.";
  }
}

const PERIODS = {
  today: { chip: "Today", versus: "vs yesterday", phrase: "today" },
  week: { chip: "This week", versus: "vs last week", phrase: "this week" },
  last_30_days: { chip: "30 days", versus: "vs prior 30 days", phrase: "in the last 30 days" },
  all_time: { chip: "All-time", versus: "vs 24h ago", phrase: "all-time" },
};
const METRICS = {
  api_estimate: { chip: "$ Est", column: "API-equivalent estimate" },
  total_tokens: { chip: "Tokens", column: "Total tokens" },
};

export const PERIOD_ORDER = ["today", "week", "last_30_days", "all_time"];
export const METRIC_ORDER = ["api_estimate", "total_tokens"];
// A public board opens as the terminal's does: this week, by estimate.
export const DEFAULT_PERIOD = "week";
export const DEFAULT_METRIC = "api_estimate";

export function periodLabel(period) {
  return PERIODS[period]?.chip ?? period;
}
export function metricLabel(metric) {
  return METRICS[metric]?.chip ?? metric;
}
export function metricColumn(metric) {
  return METRICS[metric]?.column ?? metric;
}

// comparisonLabel names what trends are measured against. The server says
// which comparison it used; the period only picks the words.
export function comparisonLabel(board) {
  if (board?.comparison?.kind === "24h_earlier") return PERIODS.all_time.versus;
  return PERIODS[board?.period]?.versus ?? "vs previous period";
}

// movement describes a row's trend as text plus a direction for styling.
// Direction is never the only signal: the glyph and number carry it too.
export function movement(row) {
  const change = row?.trend?.rank_change;
  if (row?.trend?.new) return { text: "NEW", direction: "new", spoken: "new this period" };
  if (!Number.isInteger(change)) return { text: "", direction: "none", spoken: "" };
  if (change > 0) return { text: `▲ ${change}`, direction: "up", spoken: `up ${change}` };
  if (change < 0) return { text: `▼ ${-change}`, direction: "down", spoken: `down ${-change}` };
  return { text: "–", direction: "same", spoken: "unchanged" };
}

// unrankedText explains a row without a rank. A reason this build does not
// know is shown as the server sent it rather than hidden.
export function unrankedText(row) {
  switch (row?.unranked_reason) {
    case "no_usage":
      return "no usage";
    case "value_unavailable":
      return "no estimate";
    case "cost_coverage_below_threshold":
      return `${pricedText(row?.cost_coverage?.priced_basis_points)} priced`;
    case "scope_too_small":
      return "";
    case undefined:
    case "":
      return "";
    default:
      return String(row.unranked_reason).replaceAll("_", " ");
  }
}

// boardRows reduces a board response to what a table row shows. The board is
// rendered exactly as the server ranked it: nothing here orders rows, assigns
// a rank, or judges coverage.
export function boardRows(board) {
  const estimate = board?.metric === "api_estimate";
  return (Array.isArray(board?.rows) ? board.rows : []).map((row) => {
    const ranked = Number.isInteger(row.rank);
    const move = ranked ? movement(row) : { text: unrankedText(row), direction: "none", spoken: unrankedText(row) };
    return {
      login: typeof row.display_name === "string" ? row.display_name : "",
      avatar: typeof row.avatar_url === "string" ? row.avatar_url : "",
      rank: ranked ? String(row.rank) : UNAVAILABLE,
      rankNumber: ranked ? row.rank : null,
      value: estimate ? estimateText(row.value) : tokensText(row.value),
      move,
      flagged: row?.plausibility?.daily_tokens_above_limit === true,
      previous: estimate ? estimateText(row?.trend?.previous_value) : tokensText(row?.trend?.previous_value),
      previousRank: Number.isInteger(row?.trend?.previous_rank) ? row.trend.previous_rank : null,
    };
  });
}

// boardNotes are the sentences under a board that explain what it shows.
export function boardNotes(board) {
  const notes = [];
  const shown = Array.isArray(board?.rows) ? board.rows.length : 0;
  if (Number.isInteger(board?.total_members) && board.total_members > shown) {
    notes.push(`Showing the first ${shown} of ${board.total_members} public members.`);
  }
  if (board?.ranks_hidden) {
    notes.push(`Ranks appear once ${board.min_ranked_members} members can be ranked.`);
  }
  if (board?.metric === "api_estimate" && Number.isInteger(board?.cost_rank_min_priced_basis_points)) {
    notes.push(`An estimate is ranked when at least ${percent(board.cost_rank_min_priced_basis_points)} of a member's tokens have a price. Estimates are at API list prices, never spending.`);
  }
  if ((board?.rows ?? []).some((row) => row?.plausibility?.daily_tokens_above_limit)) {
    const limit = parseDecimal(board?.plausible_daily_tokens);
    notes.push(`! marks a day above ${limit === null ? "the plausibility limit" : tokens(limit) + " tokens"}. Usage is self-reported.`);
  }
  return notes;
}

// GitHub logins are letters, digits, and single hyphens, at most 39 long.
export function validLogin(login) {
  return typeof login === "string" && /^[A-Za-z0-9](?:[A-Za-z0-9]|-(?=[A-Za-z0-9])){0,38}$/.test(login);
}

// Public group slugs are lowercase letters, digits, and inner hyphens.
export function validSlug(slug) {
  return typeof slug === "string" && /^[a-z0-9](?:[a-z0-9-]{1,62})[a-z0-9]$/.test(slug);
}

// Invite codes are a fixed prefix plus URL-safe base64.
export function validInviteCode(code) {
  return typeof code === "string" && /^[A-Za-z0-9_-]{8,64}$/.test(code);
}

// safeAvatar accepts only GitHub's avatar host over HTTPS. The server
// applies the same rule; this keeps a bad response from loading anything
// else.
export function safeAvatar(url) {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" && parsed.hostname === "avatars.githubusercontent.com" ? parsed.href : "";
  } catch {
    return "";
  }
}

// untilText says how far away an instant is: "in 23h", "in 3d".
export function untilText(instant, now) {
  const remaining = new Date(instant).getTime() - now.getTime();
  if (!Number.isFinite(remaining)) return "";
  if (remaining <= 0) return "now";
  const minutes = Math.floor(remaining / 60_000);
  if (minutes < 60) return `in ${Math.max(minutes, 1)}m`;
  if (minutes < 48 * 60) return `in ${Math.floor(minutes / 60)}h`;
  return `in ${Math.floor(minutes / (60 * 24))}d`;
}

// ---- Sharing -------------------------------------------------------------
//
// A share button is a plain link to X's compose page. The site posts nothing
// and calls no API: the visitor reviews the draft there and sends it
// themselves. The link attached to the draft is the API's share page, which
// unfurls with a card and sends browsers back here.

const INSTALL_LINE = ">_ npx @tokenholic/cli";

// intentLink builds the compose link the way internal/share does.
export function intentLink(text, url) {
  const query = new URLSearchParams({ text: text.trim() });
  if (url) query.set("url", url);
  return `https://x.com/intent/post?${query}`;
}

// spoken turns a formatted value into prose: "≥4.26B" reads "at least 4.26B".
function spoken(text) {
  return text.startsWith(AT_LEAST) ? `at least ${text.slice(AT_LEAST.length)}` : text;
}

// profileShareText drafts a post about a public profile's last 30 days, in
// the third person because the visitor may not be its owner. It is "" when
// there is no token total to state: an unknown total is never drafted.
export function profileShareText(profile) {
  const recent = profile?.last_30_days;
  const total = tokensText(recent?.total_tokens);
  if (!validLogin(profile?.login) || total === UNAVAILABLE || total === "0" || total === `${AT_LEAST}0`) return "";
  let text = `@${profile.login} burned ${spoken(total)} tokens with coding agents in the last 30 days.`;
  const estimate = estimateText(recent?.api_equivalent_estimate_microusd);
  if (estimate !== UNAVAILABLE) text += ` That is ${spoken(estimate)} at API list prices (an estimate).`;
  return `${text}\n\n${INSTALL_LINE}`;
}

// boardShareText drafts a post about a public board. It names the group,
// which its owner made public, and no member.
export function boardShareText(board) {
  const name = typeof board?.group_name === "string" ? board.group_name.trim() : "";
  if (!name) return "";
  const period = PERIODS[board?.period]?.phrase ?? "";
  return `${name} standings ${period}, on tokenholic.`.replace(" ,", ",") + `\n\n${INSTALL_LINE}`;
}

// profileSharePage and boardSharePage are the API's share pages.
export function profileSharePage(api, login) {
  return `${api}/s/u/${encodeURIComponent(login)}`;
}
export function boardSharePage(api, slug, period, metric) {
  return `${api}/s/b/${encodeURIComponent(slug)}?${new URLSearchParams({ period, metric })}`;
}
