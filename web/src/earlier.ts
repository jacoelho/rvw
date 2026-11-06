// An earlier comment's card, as the diff and the Finish dialog show it: read-only, with its
// status, and Still applies until a draft comment copies it (REQ-CMT-009 to REQ-CMT-011).

import type { EarlierComment, EarlierPlacement } from "./api";
import { h } from "./dom";
import { clock, earlierStatus, earlierTargetLabel, type State } from "./state";

/**
 * The card of earlier comment e, placed by p when its file was requested. where, when given,
 * replaces the target label, for a list that also names the file.
 */
export function earlierCard(state: State, e: EarlierComment, p: EarlierPlacement | undefined, run: (task: Promise<unknown>) => void, where?: string): HTMLElement {
  const status = earlierStatus(e, p);
  const card = h("div", "c-card");
  const head = h("div", "c-head");
  head.append(h("span", "badge", e.author === "agent" ? "Agent" : "You"), h("span", "e-when", `${clock(e.at)} review`));
  const chip = status === "outdated" ? h("span", "chip chip-out", "Outdated") : h("span", "chip chip-cur", status === "current" ? "Current" : "Unplaced");
  head.append(chip, h("span", "c-target", where ?? earlierTargetLabel(e, p)), h("span", "spacer"));
  if (state.copiedEarlier().has(e.id)) {
    head.append(h("span", "e-when", "Copied"));
  } else {
    const copy = h("button", "link quiet", "Still applies");
    copy.title = "Copy into this review as a new comment";
    copy.addEventListener("click", () => {
      copy.disabled = true;
      run(state.copyEarlier(e.id).finally(() => (copy.disabled = false)));
    });
    head.append(copy);
  }
  card.append(head);
  if (status === "outdated" && p && p.quote.length > 0) {
    const quote = h("div", "fin-quote e-quote");
    for (const l of p.quote) quote.append(h("div", "fin-quote-line", l === "" ? " " : l));
    card.append(quote);
    if (p.quoteNote) card.append(h("div", "fin-quote-note", p.quoteNote));
  }
  card.append(h("div", "c-body", e.body));
  return card;
}
