// Element construction and figure formatting shared by every view.

/** A new element with an optional class and text; text is only ever set as text. */
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls?: string,
  text?: string,
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (cls) el.className = cls;
  if (text !== undefined) el.textContent = text;
  return el;
}

/** A count with thousands separators, as every figure on the page shows it. */
export function count(n: number): string {
  return n.toLocaleString("en-US");
}
