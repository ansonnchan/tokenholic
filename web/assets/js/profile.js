// Public profile page: /u/?login=<github login>. Read-only. It shows what
// the account chose to publish: the GitHub login, avatar, and aggregate
// totals. There is no account ID, device, group, or sync time to show.

import { apiBase, OK, readPublic } from "./api.js";
import { apiOrigin } from "./config.js";
import { avatar, command, el, INSTALL, instantText, panel, shareButton, show, showLoading, showProblem, showWaking } from "./ui.js";
import {
  estimateNote, estimateText, intentLink, pricedBasisPoints, pricedText, profileSharePage, profileShareText, tokensNote, tokensText, validLogin,
} from "./values.js";

const NOT_FOUND = {
  title: "No public profile",
  lede: "There is no public profile at this address.",
  detail: "Profiles are private unless their owner publishes them, and a private profile looks exactly like one that does not exist. If this is yours, run tokenholic profile public.",
};

function tile(label, value, note) {
  return el("div", { class: "tile" },
    el("dt", { class: "label", text: label }),
    el("dd", {}, el("span", { class: "tile-value", text: value }), note ? el("span", { class: "tile-note", text: note }) : null),
  );
}

function totals(title, window) {
  const share = pricedBasisPoints(window?.priced_tokens, window?.unpriced_tokens);
  const notes = [...new Set([tokensNote(window?.total_tokens), estimateNote(window?.api_equivalent_estimate_microusd)])].filter(Boolean);
  return panel(title,
    el("dl", { class: "tiles" },
      tile("Tokens", tokensText(window?.total_tokens)),
      tile("$ Est", estimateText(window?.api_equivalent_estimate_microusd)),
      tile("Priced", pricedText(share)),
    ),
    notes.length ? el("p", { class: "panel-foot", text: notes.join(" ") }) : null,
  );
}

function render(view, profile) {
  const login = typeof profile.login === "string" ? profile.login : "";
  const draft = profileShareText(profile);
  const share = shareButton(draft && intentLink(draft, profileSharePage(apiBase(apiOrigin), login)));
  const asOf = instantText(profile.generated_at, "UTC", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
  show(view, `@${login}`,
    el("header", { class: "profile-head" },
      avatar(login, profile.avatar_url, true),
      el("div", {},
        el("p", { class: "label", text: "Public profile" }),
        el("h1", { class: "profile-name", text: `@${login}` }),
        el("a", { class: "profile-link", href: `https://github.com/${encodeURIComponent(login)}`, rel: "noopener noreferrer", text: `github.com/${login}` }),
      ),
      share ? el("p", { class: "profile-share" }, share) : null,
    ),
    el("div", { class: "pair" }, totals("Last 30 days", profile.last_30_days), totals("All-time", profile.all_time)),
    el("p", { class: "fineprint", text: `Periods use UTC days${asOf ? `, as of ${asOf} UTC` : ""}. $ EST is an estimate at API list prices, never spending. Usage is reported by this member's own machines.` }),
    el("section", { class: "section", "aria-labelledby": "own" },
      el("h2", { class: "rule", id: "own", text: "Get your own" }),
      command(INSTALL, "Reads your local Claude Code and Codex records. Nothing uploads until you claim."),
    ),
  );
}

async function main() {
  const view = document.getElementById("view");
  const login = new URLSearchParams(window.location.search).get("login") ?? "";
  // A name that could not be a GitHub login is not sent anywhere.
  if (!validLogin(login)) {
    showProblem(view, { status: "not_found" }, NOT_FOUND);
    return;
  }
  showLoading(view);
  const outcome = await readPublic(apiOrigin, `/v1/public/users/${encodeURIComponent(login)}`, { onSlow: () => showWaking(view) });
  if (outcome.status === OK && outcome.data && typeof outcome.data === "object") render(view, outcome.data);
  else showProblem(view, outcome, NOT_FOUND);
}

main();
