// Public board page: /b/?slug=<public slug>[&period=][&metric=][#login].
// Read-only. The board is drawn exactly as the server ranked it; this page
// never orders rows, assigns a rank, or judges coverage. A public board lists
// only members whose own profile is public.
//
// The site has no sign-in, so it cannot know who is looking. A fragment
// naming a member highlights that row and puts it in the rail; it is a
// pointer someone chose to share, not an identity.

import { apiBase, OK, readPublic } from "./api.js";
import { apiOrigin } from "./config.js";
import { avatar, command, el, INSTALL, instantText, panel, shareButton, show, showLoading, showProblem, showWaking } from "./ui.js";
import {
  boardNotes, boardRows, boardSharePage, boardShareText, comparisonLabel, DEFAULT_METRIC, DEFAULT_PERIOD, intentLink,
  METRIC_ORDER, metricColumn, metricLabel, PERIOD_ORDER, periodLabel, validLogin, validSlug,
} from "./values.js";

const NOT_FOUND = {
  title: "No public board",
  lede: "There is no public board at this address.",
  detail: "A group's board is private unless its owner publishes it, and a private board looks exactly like one that does not exist.",
};

function pageLink(slug, period, metric, fragment) {
  const query = new URLSearchParams({ slug });
  if (period !== DEFAULT_PERIOD) query.set("period", period);
  if (metric !== DEFAULT_METRIC) query.set("metric", metric);
  return `?${query}${fragment ? `#${encodeURIComponent(fragment)}` : ""}`;
}

function chips(label, order, current, text, href) {
  return el("nav", { class: "chips", "aria-label": label },
    order.map((key) => el("a", { class: "chip", href: href(key), "aria-current": key === current ? "true" : null, text: text(key) })));
}

function standings(board, rows, selected) {
  const body = rows.map((row) => {
    const classes = [row.rankNumber && row.rankNumber <= 3 ? `rank-${row.rankNumber}` : "", row.login === selected ? "is-current" : ""].filter(Boolean).join(" ");
    const name = validLogin(row.login)
      ? el("a", { class: "who-name", href: `../u/?login=${encodeURIComponent(row.login)}`, text: `@${row.login}` })
      : el("span", { class: "who-name", text: `@${row.login}` });
    const flag = row.flagged ? el("abbr", { class: "tag move-new", title: "A day above the plausibility limit", text: "!" }) : null;
    const move = el("span", { class: `move-${row.move.direction}`, text: row.move.text });
    if (row.move.spoken && row.move.spoken !== row.move.text) move.setAttribute("aria-label", row.move.spoken);
    return el("tr", { class: classes || null },
      el("td", { class: "col-rank", text: row.rank }),
      el("td", { class: "col-name" }, el("span", { class: "who" }, avatar(row.login, row.avatar), name, flag)),
      el("td", { class: "col-value", text: row.value }),
      el("td", { class: "col-move" }, move),
    );
  });
  return el("table", { class: "standings" },
    el("thead", { class: "visually-hidden" }, el("tr", {},
      el("th", { scope: "col", text: "Rank" }), el("th", { scope: "col", text: "Member" }),
      el("th", { scope: "col", text: metricColumn(board.metric) }), el("th", { scope: "col", text: "Movement" }))),
    el("tbody", {}, body),
  );
}

function fact(label, value) {
  return value ? [el("dt", { class: "label", text: label }), el("dd", { text: value })] : [];
}

function standingPanel(board, rows, selected) {
  const row = rows.find((candidate) => candidate.login === selected) ?? rows.find((candidate) => candidate.rankNumber === 1);
  if (!row) return null;
  const estimate = board.metric === "api_estimate";
  const previousLabel = comparisonLabel(board).replace(/^vs /, "");
  return panel(row.login === selected ? "Standing" : "Leader",
    el("p", { class: "standing-who" }, avatar(row.login, row.avatar), el("span", { class: "who-name", text: `@${row.login}` })),
    el("dl", { class: "facts" },
      fact("Rank", row.rankNumber ? `#${row.rankNumber}` : row.move.text || "unranked"),
      fact(estimate ? "$ Est" : "Tokens", row.value),
      row.rankNumber ? fact("Movement", row.move.text) : [],
      fact(previousLabel, row.previous),
    ),
  );
}

function render(view, board, slug, selected) {
  const rows = boardRows(board);
  const name = typeof board.group_name === "string" && board.group_name ? board.group_name : "Board";
  const zone = typeof board.timezone === "string" ? board.timezone : "UTC";
  const day = { weekday: "short", month: "short", day: "numeric" };
  const since = board.window?.start ? instantText(board.window.start, zone, day) : "the beginning";
  const asOf = instantText(board.generated_at, zone, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
  const notes = boardNotes(board);
  const draft = boardShareText(board);
  const share = shareButton(draft && intentLink(draft, boardSharePage(apiBase(apiOrigin), slug, board.period, board.metric)));

  const main = panel(name,
    el("div", { class: "board-controls" },
      chips("Period", PERIOD_ORDER, board.period, periodLabel, (period) => pageLink(slug, period, board.metric, selected)),
      chips("Metric", METRIC_ORDER, board.metric, metricLabel, (metric) => pageLink(slug, board.period, metric, selected)),
    ),
    el("p", { class: "rule" }, "Standings ", el("span", { class: "rule-end", text: `▲▼ ${comparisonLabel(board)}` })),
    rows.length ? standings(board, rows, selected) : el("p", { class: "board-empty", text: "No member of this group has a public profile, so there is nobody to list." }),
    notes.map((note) => el("p", { class: "board-foot", text: note })),
  );
  const rail = el("div", { class: "rail" },
    standingPanel(board, rows, selected),
    panel("Board",
      el("dl", { class: "facts" },
        fact("Public members", Number.isInteger(board.total_members) ? String(board.total_members) : ""),
        fact("Counting since", since),
        fact("Timezone", zone),
        fact("As of", asOf),
      ),
      el("p", { class: "panel-foot", text: "Lists only members whose own profile is public." }),
      share ? el("p", { class: "panel-actions" }, share) : null,
    ),
    panel("Get on a board", command(INSTALL, "Private by default. You choose what goes public.")),
  );
  show(view, name,
    el("h1", { class: "visually-hidden", text: `${name}: public board` }),
    el("div", { class: "board-layout" }, main, rail),
  );
}

async function main() {
  const view = document.getElementById("view");
  const query = new URLSearchParams(window.location.search);
  const slug = query.get("slug") ?? "";
  const period = PERIOD_ORDER.includes(query.get("period")) ? query.get("period") : DEFAULT_PERIOD;
  const metric = METRIC_ORDER.includes(query.get("metric")) ? query.get("metric") : DEFAULT_METRIC;
  let selected = "";
  try {
    selected = decodeURIComponent(window.location.hash.slice(1));
  } catch {
    // a malformed fragment selects nobody
  }
  if (!validLogin(selected)) selected = "";
  if (!validSlug(slug)) {
    showProblem(view, { status: "not_found" }, NOT_FOUND);
    return;
  }
  showLoading(view);
  const path = `/v1/public/groups/${encodeURIComponent(slug)}/leaderboard?period=${period}&metric=${metric}`;
  const outcome = await readPublic(apiOrigin, path, { onSlow: () => showWaking(view) });
  if (outcome.status === OK && outcome.data && typeof outcome.data === "object") render(view, outcome.data, slug, selected);
  else showProblem(view, outcome, NOT_FOUND);
}

main();
