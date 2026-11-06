// Renders one line of code as text nodes: highlight spans and word-diff ranges become classed
// <span>s. Code is only ever set as text.

import type { Range, Span } from "./api";

export function renderCode(el: HTMLElement, text: string, spans: Span[] | undefined, changed: Range[] | undefined): void {
  const segs = segments(text, spans);
  if (!changed?.length) {
    for (const [cls, t] of segs) el.append(piece(cls, t, false));
    return;
  }
  let pos = 0;
  let r = 0;
  for (const [cls, t] of segs) {
    let off = 0;
    while (off < t.length) {
      while (r < changed.length && changed[r]![1] <= pos + off) r++;
      const range = changed[r];
      let end = t.length;
      let inside = false;
      if (range) {
        if (range[0] <= pos + off) {
          inside = true;
          end = Math.min(t.length, range[1] - pos);
        } else {
          end = Math.min(t.length, range[0] - pos);
        }
      }
      el.append(piece(cls, t.slice(off, end), inside));
      off = end;
    }
    pos += t.length;
  }
}

/** Highlight spans when they reproduce text exactly; otherwise the plain text. */
function segments(text: string, spans: Span[] | undefined): [string, string][] {
  if (spans?.length) {
    let joined = "";
    for (const s of spans) joined += s.t;
    if (joined === text) return spans.map((s) => [s.c ?? "", s.t]);
  }
  return [["", text]];
}

function piece(cls: string, text: string, changed: boolean): Node {
  if (!cls && !changed) return document.createTextNode(text);
  const span = document.createElement("span");
  if (cls) span.className = `hl-${cls}`;
  if (changed) span.classList.add("wd");
  span.textContent = text;
  return span;
}
