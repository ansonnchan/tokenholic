// Copy buttons. A button names the element whose text it copies in
// data-copy and, optionally, the element that announces the result in
// data-copy-status. The text stays selectable, so a browser that refuses
// clipboard access still leaves a way to copy it by hand.

async function copyFrom(button) {
  const source = document.getElementById(button.dataset.copy);
  if (!source) return;
  const status = document.getElementById(button.dataset.copyStatus || "");
  let message = "Copied.";
  try {
    await navigator.clipboard.writeText(source.textContent.trim());
  } catch {
    const range = document.createRange();
    range.selectNodeContents(source);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    message = "Selected. Press Ctrl+C or ⌘C to copy.";
  }
  if (status) status.textContent = message;
}

export function wireCopyButtons(root = document) {
  for (const button of root.querySelectorAll("button[data-copy]")) {
    button.addEventListener("click", () => copyFrom(button));
  }
}

wireCopyButtons();
