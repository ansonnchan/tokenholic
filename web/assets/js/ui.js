// DOM helpers shared by the data pages. Text from the API is only ever set
// as text, never parsed as HTML, so a group name or login cannot inject
// markup.

import { NOT_FOUND, RATE_LIMITED, UNCONFIGURED } from "./api.js";
import { wireCopyButtons } from "./copy.js";
import { safeAvatar } from "./values.js";

// el builds an element. Props set attributes, except "class" and "text".
// Children may be nodes, strings, or null.
export function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(props)) {
    if (value === null || value === undefined || value === false) continue;
    if (name === "class") node.className = value;
    else if (name === "text") node.textContent = value;
    else node.setAttribute(name, value === true ? "" : String(value));
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child);
  }
  return node;
}

export function panel(title, ...children) {
  return el("section", { class: "panel" }, el("h2", { class: "panel-title", text: title }), ...children);
}

// show replaces the page's view and moves the document title along with it.
export function show(view, title, ...children) {
  view.replaceChildren(...children.flat().filter(Boolean));
  view.setAttribute("aria-busy", "false");
  if (title) document.title = `${title} · tokenholic`;
  wireCopyButtons(view);
}

// avatar is a member's GitHub avatar, or a letter tile when there is none or
// it fails to load. It is decoration: the login beside it carries the name.
export function avatar(login, url, large = false) {
  const tile = el("span", { class: large ? "avatar is-large" : "avatar", "aria-hidden": "true" });
  const letter = (login || "?").slice(0, 1);
  const source = safeAvatar(url);
  if (!source) {
    tile.textContent = letter;
    return tile;
  }
  const size = large ? 72 : 24;
  const image = el("img", { src: source, alt: "", width: size, height: size, loading: "lazy", decoding: "async", referrerpolicy: "no-referrer" });
  image.addEventListener("error", () => { tile.textContent = letter; });
  tile.append(image);
  return tile;
}

let commandCount = 0;

// command is a copy-able shell command with a status line under it.
export function command(text, note = "") {
  const id = `command-${++commandCount}`;
  return el("div", {},
    el("div", { class: "command" },
      el("code", { id, text }),
      el("button", { class: "button", type: "button", "data-copy": id, "data-copy-status": `${id}-status`, text: "Copy" }),
    ),
    el("p", { class: "command-note", id: `${id}-status`, role: "status", text: note }),
  );
}

export const INSTALL = "npx @tokenholic/cli";

// shareButton is a link to X's compose page with a draft the visitor reviews
// and posts themselves. It is null when there is nothing to draft.
export function shareButton(link) {
  if (!link) return null;
  return el("a", { class: "button is-quiet", href: link, target: "_blank", rel: "noopener noreferrer", text: "Share on X" });
}

// showLoading is the first thing a data page draws. It never shows a number.
export function showLoading(view) {
  view.setAttribute("aria-busy", "true");
  view.replaceChildren(el("p", { class: "state-line label" }, "Loading", el("span", { class: "caret-blink", "aria-hidden": "true", text: "_" })));
}

// showWaking is the explicit wake-up state: the API has not answered yet, and
// the page says why instead of looking empty or broken.
export function showWaking(view) {
  view.setAttribute("aria-busy", "true");
  view.replaceChildren(
    el("section", { class: "panel state", role: "status" },
      el("h1", { class: "panel-title" }, "Waking the API", el("span", { class: "caret-blink", "aria-hidden": "true", text: "_" })),
      el("p", { class: "state-lede", text: "The API was asleep. It runs on free hosting that stops when idle and takes up to a minute to start." }),
      el("p", { class: "muted", text: "Nothing is wrong and nothing is missing. This page fills in by itself as soon as the API answers." }),
    ),
  );
}

// showProblem draws every outcome that is not data. notFound names what was
// looked for, in words that are the same for a private record and a missing
// one, because the page cannot tell them apart and must not pretend to.
export function showProblem(view, outcome, notFound) {
  const home = el("a", { class: "button is-quiet", href: "../", text: "Back to tokenholic" });
  const retry = el("button", { class: "button", type: "button", text: "Try again" });
  retry.addEventListener("click", () => window.location.reload());

  if (outcome.status === NOT_FOUND) {
    show(view, notFound.title,
      el("section", { class: "panel state" },
        el("h1", { class: "panel-title", text: notFound.title }),
        el("p", { class: "state-lede", text: notFound.lede }),
        notFound.detail ? el("p", { class: "muted", text: notFound.detail }) : null,
        el("p", { class: "state-actions" }, home),
      ));
    return;
  }
  if (outcome.status === RATE_LIMITED) {
    const wait = outcome.retryAfterSeconds ? `Try again in ${outcome.retryAfterSeconds} seconds.` : "Try again in a moment.";
    show(view, "Slow down",
      el("section", { class: "panel state" },
        el("h1", { class: "panel-title", text: "Too many requests" }),
        el("p", { class: "state-lede", text: `The API is limiting requests from this network. ${wait}` }),
        el("p", { class: "state-actions" }, retry, home),
      ));
    return;
  }
  if (outcome.status === UNCONFIGURED) {
    show(view, "Not connected",
      el("section", { class: "panel state" },
        el("h1", { class: "panel-title", text: "Not connected" }),
        el("p", { class: "state-lede", text: "This copy of the site has no API address, so it has no data to show." }),
        el("p", { class: "muted", text: "The published site is given its API address when it is deployed." }),
        el("p", { class: "state-actions" }, home),
      ));
    return;
  }
  show(view, "API unreachable",
    el("section", { class: "panel state" },
      el("h1", { class: "panel-title" }, el("span", { class: "notice-level", text: "Error" }), " API unreachable"),
      el("p", { class: "state-lede", text: "The API did not answer after two minutes of trying." }),
      el("p", { class: "muted", text: "It may still be starting, or your connection may be down. Nothing was loaded, so nothing is shown." }),
      el("p", { class: "state-actions" }, retry, home),
    ));
}

// instantText formats an instant in a named timezone, falling back to UTC
// when the browser does not know the zone.
export function instantText(instant, timeZone, options) {
  const date = new Date(instant);
  if (Number.isNaN(date.getTime())) return "";
  for (const zone of [timeZone, "UTC"]) {
    try {
      return new Intl.DateTimeFormat("en-US", { ...options, timeZone: zone }).format(date);
    } catch {
      // try the next zone
    }
  }
  return "";
}
