// Invite landing page: /invite/#<code>. Read-only. The code sits in the
// fragment, which a browser never sends to the server hosting this page; it
// goes only to the API, to ask which group it joins. Joining itself happens
// in the terminal: this page cannot sign anyone in or accept the invite.

import { OK, readPublic } from "./api.js";
import { apiOrigin } from "./config.js";
import { command, el, panel, show, showLoading, showProblem, showWaking } from "./ui.js";
import { untilText, validInviteCode } from "./values.js";

const NOT_FOUND = {
  title: "Invite not valid",
  lede: "This invite cannot be used.",
  detail: "It may have expired, been used up, or lost part of its address on the way. Ask whoever sent it for a new one.",
};

function render(view, preview, code) {
  const group = typeof preview.group_name === "string" && preview.group_name ? preview.group_name : "a group";
  const expires = untilText(preview.expires_at, new Date());
  show(view, `Join ${group}`,
    panel("You're invited",
      el("p", { class: "label", text: "Private group" }),
      el("h1", { class: "invite-group", text: group }),
      expires ? el("p", { class: "muted", text: expires === "now" ? "This invite expires any moment." : `This invite expires ${expires}.` }) : null,
      el("ol", { class: "steps is-stacked" },
        el("li", {},
          el("h3", { text: "Claim your spot" }),
          el("p", { text: "Scans your local agent records, signs you in with GitHub, and uploads your totals." }),
          command("npx @tokenholic/cli claim"),
        ),
        el("li", {},
          el("h3", { text: "Join the group" }),
          el("p", { text: "Or open the app, go to BOARDS, press i, and paste the code." }),
          command(`npx @tokenholic/cli group join ${code}`),
        ),
      ),
      el("p", { class: "panel-foot", text: "Joining shows your GitHub login and usage totals to this group's members. Nothing becomes public unless you opt in. Prompts, code, and file paths never leave your machine." }),
    ),
  );
}

async function main() {
  const view = document.getElementById("view");
  const code = window.location.hash.slice(1);
  // Anything that could not be a code is refused here and never sent.
  if (!validInviteCode(code)) {
    showProblem(view, { status: "not_found" }, NOT_FOUND);
    return;
  }
  showLoading(view);
  const outcome = await readPublic(apiOrigin, `/v1/public/invites/${encodeURIComponent(code)}`, { onSlow: () => showWaking(view) });
  if (outcome.status === OK && outcome.data && typeof outcome.data === "object") render(view, outcome.data, code);
  else showProblem(view, outcome, NOT_FOUND);
}

main();
// A different code pasted into the address bar only changes the fragment.
window.addEventListener("hashchange", main);
