import assert from "node:assert/strict";
import { test } from "node:test";

import {
  boardNotes,
  boardRows,
  boardSharePage,
  boardShareText,
  intentLink,
  profileSharePage,
  profileShareText,
  comparisonLabel,
  estimateText,
  estimateNote,
  movement,
  pricedBasisPoints,
  pricedText,
  safeAvatar,
  tokensNote,
  tokensText,
  unrankedText,
  untilText,
  validInviteCode,
  validLogin,
  validSlug,
} from "../assets/js/values.js";

const value = (amount, state = "complete") => ({ value: amount, state });

test("an unknown value is never shown as zero", () => {
  const table = [
    [value("4263000000"), "4.26B", "$4,263.00"],
    [value("4263000000", "partial"), "≥4.26B", "≥$4,263.00"],
    [value("0"), "0", "$0.00"],
    [value(null, "unavailable"), "—", "—"],
    [value(null, "empty"), "—", "—"],
    [value("12.5"), "—", "—"],
    [value("-3"), "—", "—"],
    [{}, "—", "—"],
    [null, "—", "—"],
    [undefined, "—", "—"],
  ];
  for (const [input, wantTokens, wantEstimate] of table) {
    assert.equal(tokensText(input), wantTokens, `tokensText(${JSON.stringify(input)})`);
    assert.equal(estimateText(input), wantEstimate, `estimateText(${JSON.stringify(input)})`);
  }
});

test("each state other than complete is explained in words", () => {
  assert.equal(tokensNote(value("1")), "");
  assert.equal(estimateNote(value("1")), "");
  assert.match(tokensNote(value("1", "partial")), /lower bound/);
  assert.match(estimateNote(value("1", "partial")), /lower bound/);
  assert.match(estimateNote(value(null, "unavailable")), /^No estimate/);
  assert.match(tokensNote(value(null, "unavailable")), /unknown/);
  assert.equal(tokensNote(value(null, "empty")), estimateNote(value(null, "empty")));
  assert.match(tokensNote(undefined), /unknown/);
  // Nothing describes a missing value as zero or as free.
  for (const state of ["partial", "unavailable", "empty", undefined]) {
    assert.doesNotMatch(tokensNote({ state }) + estimateNote({ state }), /zero|\$0|free/i);
  }
});

test("the priced share is cut, and absent when there are no tokens", () => {
  assert.equal(pricedBasisPoints("9499", "501"), 9499);
  assert.equal(pricedText(pricedBasisPoints("9499", "501")), "94.9%");
  assert.equal(pricedBasisPoints("0", "0"), null);
  assert.equal(pricedText(null), "—");
  assert.equal(pricedBasisPoints("0", "10"), 0);
  assert.equal(pricedText(0), "0%");
  assert.equal(pricedBasisPoints("x", "1"), null);
});

test("movement names its direction in text, not only by color", () => {
  const row = (change, isNew = false) => ({ trend: { rank_change: change, new: isNew } });
  assert.deepEqual(movement(row(2)), { text: "▲ 2", direction: "up", spoken: "up 2" });
  assert.deepEqual(movement(row(-1)), { text: "▼ 1", direction: "down", spoken: "down 1" });
  assert.deepEqual(movement(row(0)), { text: "–", direction: "same", spoken: "unchanged" });
  assert.deepEqual(movement(row(null, true)), { text: "NEW", direction: "new", spoken: "new this period" });
  assert.deepEqual(movement(row(null)), { text: "", direction: "none", spoken: "" });
  assert.deepEqual(movement({}), { text: "", direction: "none", spoken: "" });
});

test("an unranked row says why, and an unknown reason is shown as sent", () => {
  assert.equal(unrankedText({ unranked_reason: "no_usage" }), "no usage");
  assert.equal(unrankedText({ unranked_reason: "value_unavailable" }), "no estimate");
  assert.equal(
    unrankedText({ unranked_reason: "cost_coverage_below_threshold", cost_coverage: { priced_basis_points: 7100 } }),
    "71% priced",
  );
  assert.equal(unrankedText({ unranked_reason: "scope_too_small" }), "");
  assert.equal(unrankedText({ unranked_reason: "some_new_rule" }), "some new rule");
  assert.equal(unrankedText({}), "");
});

const board = {
  metric: "api_estimate",
  period: "week",
  comparison: { kind: "previous_period" },
  cost_rank_min_priced_basis_points: 9500,
  plausible_daily_tokens: "5000000000",
  min_ranked_members: 3,
  ranks_hidden: false,
  total_members: 3,
  rows: [
    {
      rank: 1, display_name: "dana", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4",
      value: value("1180000000"), cost_coverage: { priced_basis_points: 9980 },
      trend: { previous_value: value("1012400000"), previous_rank: 2, rank_change: 1, new: false },
      plausibility: { daily_tokens_above_limit: true },
    },
    {
      rank: null, unranked_reason: "cost_coverage_below_threshold", display_name: "sam",
      value: value("560750000", "partial"), cost_coverage: { priced_basis_points: 7100 },
      trend: { previous_value: value(null, "empty"), previous_rank: null, rank_change: null, new: false },
      plausibility: { daily_tokens_above_limit: false },
    },
    {
      rank: null, unranked_reason: "no_usage", display_name: "lee",
      value: value(null, "empty"), cost_coverage: { priced_basis_points: null },
      trend: { previous_value: value(null, "empty"), previous_rank: null, rank_change: null, new: false },
      plausibility: { daily_tokens_above_limit: false },
    },
  ],
};

test("board rows keep the server's order, ranks, and reasons", () => {
  const rows = boardRows(board);
  assert.deepEqual(rows.map((row) => row.login), ["dana", "sam", "lee"]);
  assert.deepEqual(rows.map((row) => row.rank), ["1", "—", "—"]);
  assert.deepEqual(rows.map((row) => row.value), ["$1,180.00", "≥$560.75", "—"]);
  assert.deepEqual(rows.map((row) => row.move.text), ["▲ 1", "71% priced", "no usage"]);
  assert.equal(rows[0].flagged, true);
  assert.equal(rows[0].previous, "$1,012.40");
  assert.equal(rows[1].previous, "—");
  assert.equal(boardRows({ ...board, metric: "total_tokens" })[0].value, "1.18B");
  assert.deepEqual(boardRows({}), []);
  assert.deepEqual(boardRows(null), []);
});

test("board notes state the rules behind what is shown", () => {
  const notes = boardNotes(board).join(" ");
  assert.match(notes, /at least 95% of a member's tokens have a price/);
  assert.match(notes, /never spending/);
  assert.match(notes, /above 5B tokens/);
  assert.match(boardNotes({ ...board, ranks_hidden: true }).join(" "), /Ranks appear once 3 members can be ranked/);
  assert.match(boardNotes({ ...board, total_members: 250 }).join(" "), /first 3 of 250 public members/);
  assert.doesNotMatch(boardNotes({ ...board, metric: "total_tokens" }).join(" "), /have a price/);
});

test("the comparison legend follows what the server compared against", () => {
  assert.equal(comparisonLabel({ period: "today", comparison: { kind: "previous_period" } }), "vs yesterday");
  assert.equal(comparisonLabel({ period: "week", comparison: { kind: "previous_period" } }), "vs last week");
  assert.equal(comparisonLabel({ period: "last_30_days", comparison: { kind: "previous_period" } }), "vs prior 30 days");
  assert.equal(comparisonLabel({ period: "all_time", comparison: { kind: "24h_earlier" } }), "vs 24h ago");
  assert.equal(comparisonLabel({ period: "soon" }), "vs previous period");
});

test("only well-formed names reach the API", () => {
  for (const login of ["dana", "a", "a-b", "A1-b2", "a".repeat(39)]) assert.ok(validLogin(login), login);
  for (const login of ["", "-a", "a-", "a--b", "a b", "a/b", "../x", "a".repeat(40), null, 7]) assert.ok(!validLogin(login), String(login));
  for (const slug of ["g-nightshift", "abc", "a1b"]) assert.ok(validSlug(slug), slug);
  for (const slug of ["", "ab", "-ab", "ab-", "G-upper", "a/b", "a".repeat(65), null]) assert.ok(!validSlug(slug), String(slug));
  assert.ok(validInviteCode("ovt_" + "A".repeat(32)));
  for (const code of ["", "short", "has space in it", "a/b/c/d/e/f", "x".repeat(65), null]) assert.ok(!validInviteCode(code), String(code));
});

test("only GitHub's avatar host is ever loaded", () => {
  assert.equal(safeAvatar("https://avatars.githubusercontent.com/u/1?v=4"), "https://avatars.githubusercontent.com/u/1?v=4");
  for (const url of [
    "", "http://avatars.githubusercontent.com/u/1", "https://avatars.githubusercontent.com.evil.example/u/1",
    "https://example.com/a.png", "javascript:alert(1)", "data:image/png;base64,AAAA", "//avatars.githubusercontent.com/u/1", null,
  ]) {
    assert.equal(safeAvatar(url), "", String(url));
  }
});

test("untilText says how far away an expiry is", () => {
  const now = new Date("2026-11-21T18:00:00Z");
  assert.equal(untilText("2026-11-21T18:00:30Z", now), "in 1m");
  assert.equal(untilText("2026-11-21T18:45:00Z", now), "in 45m");
  assert.equal(untilText("2026-11-22T17:30:00Z", now), "in 23h");
  assert.equal(untilText("2026-11-25T18:00:00Z", now), "in 4d");
  assert.equal(untilText("2026-11-21T17:00:00Z", now), "now");
  assert.equal(untilText("not a date", now), "");
});

test("a share link is X's compose page with an encoded draft", () => {
  const link = new URL(intentLink("  @dana burned 4.26B tokens & more.\n\n>_ npx @tokenholic/cli  ", "https://api.tokenholic.example/s/u/dana"));
  assert.equal(link.origin + link.pathname, "https://x.com/intent/post");
  assert.equal(link.searchParams.get("text"), "@dana burned 4.26B tokens & more.\n\n>_ npx @tokenholic/cli");
  assert.equal(link.searchParams.get("url"), "https://api.tokenholic.example/s/u/dana");
  assert.equal(intentLink("text only"), "https://x.com/intent/post?text=text+only");
});

test("a profile draft states lower bounds and calls the estimate an estimate", () => {
  const profile = (tokens, estimate) => ({ login: "dana", last_30_days: { total_tokens: tokens, api_equivalent_estimate_microusd: estimate } });
  assert.equal(
    profileShareText(profile(value("4263000000", "partial"), value("1180420000", "partial"))),
    "@dana burned at least 4.26B tokens with coding agents in the last 30 days. That is at least $1,180.42 at API list prices (an estimate).\n\n>_ npx @tokenholic/cli",
  );
  assert.equal(
    profileShareText(profile(value("987"), value(null, "unavailable"))),
    "@dana burned 987 tokens with coding agents in the last 30 days.\n\n>_ npx @tokenholic/cli",
  );
  // Nothing to state means no draft, never a draft about zero.
  for (const tokens of [value(null, "empty"), value(null, "unavailable"), value("0"), value("0", "partial"), undefined]) {
    assert.equal(profileShareText(profile(tokens, value("5"))), "", JSON.stringify(tokens));
  }
  assert.equal(profileShareText({ login: "not a login", last_30_days: { total_tokens: value("5") } }), "");
  assert.doesNotMatch(profileShareText(profile(value("5"), value("5"))), /spent|spend/);
});

test("a board draft names the group and its period, and no member", () => {
  assert.equal(boardShareText({ group_name: "night-shift", period: "week" }), "night-shift standings this week, on tokenholic.\n\n>_ npx @tokenholic/cli");
  assert.equal(boardShareText({ group_name: "night-shift", period: "all_time" }), "night-shift standings all-time, on tokenholic.\n\n>_ npx @tokenholic/cli");
  assert.equal(boardShareText({ group_name: "night-shift", period: "soon" }), "night-shift standings, on tokenholic.\n\n>_ npx @tokenholic/cli");
  assert.equal(boardShareText({ group_name: "  " }), "");
  assert.equal(boardShareText({}), "");
});

test("share pages live on the API", () => {
  assert.equal(profileSharePage("https://api.tokenholic.example", "dana"), "https://api.tokenholic.example/s/u/dana");
  assert.equal(
    boardSharePage("https://api.tokenholic.example", "g-nightshift", "all_time", "total_tokens"),
    "https://api.tokenholic.example/s/b/g-nightshift?period=all_time&metric=total_tokens",
  );
});
