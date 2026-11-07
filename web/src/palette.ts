// The search palette (⌘K, REQ-NAV-006/007): files by fuzzy path, path:line, :line in the shown
// file, and #text over the changed files' contents. Its query and results are transient UI state.

import { type Api, BadReply, HttpError, isGone, type SearchMatch, type Side } from "./api";
import { h } from "./dom";
import { fileIcon } from "./fileicon";
import { type Ranked, rankFiles } from "./fuzzy";
import { VirtualList } from "./scroller";
import type { State } from "./state";

/** What the palette's input asks for. */
export type Query =
  | { kind: "files"; text: string }
  | { kind: "fileLine"; path: string; line: number }
  | { kind: "line"; line: number }
  | { kind: "content"; text: string };

/**
 * Reads the palette's input. `#` starts a content search. `<path>:<n>` with n a positive decimal
 * without leading zeros is a line, so a path that itself ends in `:<digits>` is read as a line;
 * `:<n>` is a line of the shown file. Anything else is a files query.
 */
export function parseQuery(input: string): Query {
  let m = /^#(.*)$/s.exec(input);
  if (m) return { kind: "content", text: m[1]! };
  m = /^:([1-9]\d*)$/.exec(input);
  if (m && Number.isSafeInteger(Number(m[1]))) return { kind: "line", line: Number(m[1]) };
  m = /^(.+):([1-9]\d*)$/s.exec(input);
  if (m && Number.isSafeInteger(Number(m[2]))) return { kind: "fileLine", path: m[1]!, line: Number(m[2]) };
  return { kind: "files", text: input };
}

/** Where a chosen result goes: a file, and optionally a line on a side (new by default). */
export interface GoTarget {
  file: number;
  side?: Side;
  line?: number;
}

export interface PaletteHooks {
  go(t: GoTarget): void;
  /** The shown file's index, or -1. */
  current(): number;
  /** Every file index in tree order. */
  order(): readonly number[];
  failed(err: unknown): void;
}

export type SearchApi = Pick<Api, "search">;

const DEBOUNCE_MS = 200;
const ROW_HEIGHT = 26;

type Row = { k: "file"; r: Ranked } | { k: "match"; m: SearchMatch };

export class Palette {
  readonly el: HTMLDialogElement;
  private readonly input: HTMLInputElement;
  private readonly mode: HTMLElement;
  private readonly status: HTMLElement;
  private readonly list: VirtualList;
  private query: Query = { kind: "files", text: "" };
  private rows: Row[] = [];
  private sel = 0;
  /** The content search in flight or debouncing; a reply is applied only while it is this one. */
  private search: AbortController | null = null;
  private timer = 0;
  private returnTo: Element | null = null;

  constructor(
    private readonly state: State,
    private readonly api: SearchApi,
    private readonly hooks: PaletteHooks,
  ) {
    this.el = h("dialog", "palette");
    this.el.setAttribute("aria-label", "Search");
    this.input = h("input", "pal-input");
    this.input.type = "text";
    this.input.placeholder = "Files, path:line, :line or #text";
    this.input.spellcheck = false;
    this.input.autocomplete = "off";
    this.input.addEventListener("input", () => this.update());
    this.mode = h("div", "pal-mode");
    this.status = h("div", "pal-status");
    this.status.setAttribute("role", "status");
    const listEl = h("div", "pal-list");
    this.list = new VirtualList(listEl, ROW_HEIGHT);
    listEl.addEventListener("click", (e) => {
      const row = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-row]") : null;
      if (!row) return;
      this.sel = Number(row.dataset["row"]);
      this.choose();
    });
    this.el.append(this.input, this.mode, this.status, listEl);

    // The page's keys never see the palette's: Esc must not close an editor behind it.
    this.el.addEventListener("keydown", (e) => {
      e.stopPropagation();
      this.onKey(e);
    });
    this.el.addEventListener("click", (e) => {
      // A click outside the dialog's box lands on the dialog itself: the backdrop.
      if (e.target !== this.el) return;
      const r = this.el.getBoundingClientRect();
      if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) this.close();
    });
    // The close event comes a task after the dialog closes, so the palette may be open again by
    // then; closes made here have already cleaned up. It still covers a close made elsewhere.
    this.el.addEventListener("close", () => {
      if (!this.el.open) this.closed();
    });
  }

  /** Closes the palette and gives the focus back now, so the page's next key is the page's. */
  private close(): void {
    this.el.close();
    this.closed();
  }

  /** Cleans up after a close. Idempotent: it runs again on the close event. */
  private closed(): void {
    this.stopSearch();
    this.rows = [];
    this.list.setSource(this.source());
    const back = this.returnTo;
    this.returnTo = null;
    if (back instanceof HTMLElement && back.isConnected) back.focus({ preventScroll: true });
    // With nothing to go back to, focus can stay on the closed input, whose keydown listener would
    // swallow the page's keys, ⌘K included.
    if (this.el.contains(document.activeElement)) this.input.blur();
  }

  get isOpen(): boolean {
    return this.el.open;
  }

  open(): void {
    if (this.el.open) return;
    this.returnTo = document.activeElement;
    this.input.value = "";
    this.el.showModal();
    this.update();
    this.input.focus();
  }

  private onKey(e: KeyboardEvent): void {
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        this.close();
        return;
      case "ArrowDown":
      case "ArrowUp": {
        e.preventDefault();
        if (this.rows.length === 0) return;
        const d = e.key === "ArrowDown" ? 1 : -1;
        this.sel = Math.max(0, Math.min(this.rows.length - 1, this.sel + d));
        this.list.scrollToIndex(this.sel, "nearest");
        this.list.refresh();
        return;
      }
      case "Enter":
        e.preventDefault();
        if (!e.isComposing) this.choose();
        return;
      case "k":
      case "K":
        if (e.metaKey && !e.ctrlKey && !e.altKey) {
          e.preventDefault();
          this.input.select();
        }
        return;
    }
  }

  /** Re-reads the input: any search in flight is abandoned, and the new query's rows are shown. */
  private update(): void {
    this.stopSearch();
    const q = parseQuery(this.input.value);
    this.query = q;
    this.rows = [];
    this.sel = 0;
    let status = "";
    switch (q.kind) {
      case "files":
        this.mode.textContent = "Files";
        this.rows = this.rank(q.text);
        if (this.rows.length === 0) status = "No matching files";
        break;
      case "fileLine":
        this.mode.textContent = `Go to line ${q.line} of a file`;
        this.rows = this.rank(q.path);
        if (this.rows.length === 0) status = "No matching file";
        break;
      case "line": {
        const f = this.state.files[this.hooks.current()];
        this.mode.textContent = `Go to line ${q.line} of the shown file`;
        status = f ? f.path : "No file is shown";
        break;
      }
      case "content": {
        this.mode.textContent = "Search file contents";
        // The server's rules, checked here so nothing it would refuse is sent.
        const { minChars, maxBytes } = this.state.searchLimits;
        if ([...q.text].length < minChars) status = `Type at least ${minChars} characters`;
        else if (new TextEncoder().encode(q.text).length > maxBytes) status = `Search text is longer than ${maxBytes} bytes`;
        else {
          status = "Searching…";
          this.startSearch(q.text);
        }
        break;
      }
    }
    this.status.textContent = status;
    this.list.setSource(this.source());
    this.list.scrollToIndex(0, "start");
  }

  private rank(text: string): Row[] {
    const files = this.state.files;
    const candidates = this.hooks.order().map((i) => ({ i, path: files[i]!.path, oldPath: files[i]!.renamedFrom }));
    return rankFiles(candidates, text).map((r) => ({ k: "file", r }));
  }

  /** Sends a search for text after the debounce, unless a later keystroke or closing stops it. */
  private startSearch(text: string): void {
    const ctl = new AbortController();
    this.search = ctl;
    this.timer = window.setTimeout(() => {
      this.timer = 0;
      void this.runSearch(text, ctl);
    }, DEBOUNCE_MS);
  }

  private async runSearch(text: string, ctl: AbortController): Promise<void> {
    try {
      const reply = await this.api.search(text, this.state.files, ctl.signal);
      if (this.search !== ctl) return;
      this.search = null;
      this.rows = reply.matches.map((m) => ({ k: "match", m }));
      const n = reply.matches.length;
      const count = `${n} ${n === 1 ? "match" : "matches"}`;
      this.status.textContent = reply.truncated ? `${count}. Search stopped early; some files may not be listed` : count;
      this.sel = 0;
      this.list.setSource(this.source());
      this.list.scrollToIndex(0, "start");
    } catch (err) {
      // A superseded search, aborted or not, shows nothing.
      if (this.search !== ctl) return;
      this.search = null;
      this.status.textContent = searchError(err);
      if (isGone(err)) this.hooks.failed(err);
    }
  }

  /** Abandons the search in flight or debouncing; its reply, if any, is never applied. */
  private stopSearch(): void {
    window.clearTimeout(this.timer);
    this.timer = 0;
    this.search?.abort();
    this.search = null;
  }

  /** Goes to the selected row. Ignored while a newer search is pending, so a stale row is never chosen. */
  private choose(): void {
    if (this.search) return;
    const q = this.query;
    let t: GoTarget | null = null;
    if (q.kind === "line") {
      const cur = this.hooks.current();
      if (cur >= 0) t = { file: cur, line: q.line };
    } else {
      const row = this.rows[this.sel];
      if (row?.k === "file") t = q.kind === "fileLine" ? { file: row.r.i, line: q.line } : { file: row.r.i };
      else if (row?.k === "match") t = { file: row.m.file, side: row.m.side, line: row.m.line };
    }
    if (!t) return;
    this.close();
    this.hooks.go(t);
  }

  private source() {
    return {
      count: () => this.rows.length,
      variable: () => false,
      key: (n: number) => String(n),
      // Each new result list is shown from its first row, so rows anchor by position.
      id: (n: number) => String(n),
      render: (n: number) => this.renderRow(n),
    };
  }

  /** A row built from text nodes only: paths and file text never become markup. */
  private renderRow(n: number): HTMLElement {
    const row = this.rows[n]!;
    const el = h("div", n === this.sel ? "pal-row sel" : "pal-row");
    el.dataset["row"] = String(n);
    if (row.k === "file") {
      const f = this.state.files[row.r.i]!;
      el.append(fileIcon(f.path), marked("pal-path", f.path, row.r.on === "path" ? row.r.positions : []));
      if (f.renamedFrom !== undefined) {
        el.append(h("span", "pal-from", " ← "), marked("pal-path old", f.renamedFrom, row.r.on === "oldPath" ? row.r.positions : []));
      }
      return el;
    }
    const m = row.m;
    const f = this.state.files[m.file]!;
    el.append(fileIcon(f.path), h("span", "pal-loc", `${f.path}:${m.line}`));
    const code = h("span", "pal-code");
    code.append(`${m.cutStart ? "…" : ""}${m.before}`, h("mark", "", m.match), `${m.after}${m.cutEnd ? "…" : ""}`);
    el.append(code);
    return el;
  }
}

/** text in a span, with the characters at positions in <mark>. */
function marked(cls: string, text: string, positions: readonly number[]): HTMLElement {
  const el = h("span", cls);
  let at = 0;
  for (let k = 0; k < positions.length; ) {
    const start = positions[k]!;
    let end = start + 1;
    while (positions[++k] === end) end++;
    if (start > at) el.append(text.slice(at, start));
    el.append(h("mark", "", text.slice(start, end)));
    at = end;
  }
  if (at < text.length) el.append(text.slice(at));
  return el;
}

/** The inline text of a failed search; the palette stays usable. */
function searchError(err: unknown): string {
  if (err instanceof HttpError) {
    if (err.status === 400) return `Search text not accepted: ${err.message}`;
    if (err.status === 503) return `Search unavailable: ${err.message}`;
    return `Search failed: ${err.message}`;
  }
  if (err instanceof BadReply) return "Search failed: bad reply from rvw";
  return `Search failed: ${err instanceof Error ? err.message : String(err)}`;
}
