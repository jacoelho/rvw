// One changed file's diff, unified or split, with context expansion, inline comments and the
// comment editor. The server derives hunks, line numbers, change pairing and word changes; this
// module only lays them out as virtual rows.

import type { Api, ChangedFile, Comment, FileDiff, Gap, Placement, TextDiff, Line as WireLine, Range, Side, Span, Target } from "./api";
import { renderCode } from "./code";
import { h } from "./dom";
import { CommentEditor } from "./editor";
import { VirtualList } from "./scroller";
import { lineTarget, type State, targetEnd, targetLabel } from "./state";

export const ROW_HEIGHT = 20;
const CONTEXT_STEP = 50;
/** Unchanged lines shown around a comment target that lies between hunks. */
const REVEAL_MARGIN = 3;
const MAX_CODE_COLUMNS = 4000;
/** Columns before split code text: line number (6ch) and change mark (2ch). */
const SPLIT_GUTTER_COLUMNS = 8;
/** Columns an arrow key moves split code text sideways. */
const SPLIT_KEY_STEP = 8;

export type Mode = "unified" | "split";

/** One shown line: a patch line or an expanded context line. */
interface Line {
  op: " " | "+" | "-";
  old: number | null;
  new: number | null;
  text: string;
  spans: Span[] | undefined;
  noEOL: boolean;
  words: Range[] | undefined;
}

function toLine(op: Line["op"], w: WireLine): Line {
  return { op, old: w.old ?? null, new: w.new ?? null, text: w.t, spans: w.s, noEOL: !!w.n, words: w.w };
}

/** A gap with which ends border a hunk; expansion grows from those ends. */
interface ShownGap extends Gap {
  before: boolean;
  after: boolean;
}

/** The gutter lines a new comment targets: start..end on one side. */
type Selection = { side: Side; start: number; end: number };

type Item = { hunk: string } | { gap: ShownGap } | { line: Line } | { left: Line | null; right: Line | null };

type DRow =
  | { k: "note"; text: string; cls: string }
  | { k: "info"; lines: string[] }
  | { k: "hunk"; text: string }
  | { k: "gap"; gap: ShownGap }
  | { k: "line"; line: Line }
  | { k: "split"; left: Line | null; right: Line | null }
  | { k: "comment"; c: Comment; at: string }
  | { k: "editor"; ed: CommentEditor; at: string };

/** The key a row's height is cached under: a comment's changes with its body and whether it is labelled. */
function rowKey(r: DRow): string {
  if (r.k === "comment") return `c:${r.c.id}:${r.c.body.length}:${r.at === "top"}`;
  if (r.k === "editor") return r.ed.key;
  return rowId(r);
}

/**
 * A row's identity within one file, which a rebuild keeps in place on screen. A comment and its
 * editor are one row, identified with where it sits: a comment or draft that moves is a new row, so
 * the rows around it, not the moved row, stay in place. Lines are identified by their numbers,
 * which stay with them when context is shown or comments move.
 */
function rowId(r: DRow): string {
  switch (r.k) {
    case "comment":
      return `c:${r.c.id}@${r.at}`;
    case "editor":
      return `c:${r.ed.id}@${r.at}`;
    case "line":
      return `l:${r.line.old ?? ""}:${r.line.new ?? ""}`;
    case "split":
      return `s:${r.left?.old ?? ""}:${r.right?.new ?? ""}`;
    case "gap":
      return `g:${r.gap.from}:${r.gap.to}`;
    case "hunk":
      return `h:${r.text}`;
    case "note":
      return `n:${r.text}`;
    case "info":
      return "i";
  }
}

/** The server calls FileView makes: it only reads diffs. */
export type DiffApi = Pick<Api, "file" | "context">;

export interface FileViewHooks {
  failed(err: unknown): void;
}

export class FileView {
  readonly list: VirtualList;
  private readonly header: HTMLElement;
  private index = -1;
  private diff: FileDiff | null = null;
  private loading: AbortController | null = null;
  private mode: Mode;
  private rows: DRow[] = [];
  /** Expanded context lines keyed by new-side line number. */
  private expanded = new Map<number, WireLine>();
  private selection: Selection | null = null;
  private editor: CommentEditor | null = null;
  /** A transition is leaving the open editor; every other transition meanwhile is refused. */
  private leaving = false;
  /**
   * Split view's horizontal scrollbar. Its scrollLeft is the one horizontal offset of both halves'
   * code text, published to CSS as --split-x; split rows themselves never scroll sideways.
   */
  private readonly strip: HTMLElement;
  private readonly stripSpacer: HTMLElement;
  /** Measures the mono character width the split range is computed in. */
  private readonly probe: HTMLElement;
  private codeCols = 0;
  /** Mono character width in px, measured when the split range is computed. */
  private ch = 7;

  constructor(
    headerEl: HTMLElement,
    listEl: HTMLElement,
    private readonly state: State,
    private readonly api: DiffApi,
    private readonly hooks: FileViewHooks,
  ) {
    this.header = headerEl;
    this.list = new VirtualList(listEl, ROW_HEIGHT, 90);
    this.mode = localStorage.getItem("rvw.mode") === "split" ? "split" : "unified";
    listEl.addEventListener("click", (e) => this.onClick(e));
    this.strip = h("div", "d-hscroll");
    this.stripSpacer = h("div", "d-hscroll-spacer");
    this.strip.append(this.stripSpacer);
    this.strip.addEventListener("scroll", () => this.publishSplitX(), { passive: true });
    listEl.after(this.strip);
    // The probe sits in a zero-size clipped box so it adds nothing to the list's scroll width.
    this.probe = h("span", "", "0".repeat(100));
    this.probe.style.cssText = "visibility:hidden;white-space:pre";
    const probeBox = h("div", "");
    probeBox.style.cssText = "position:absolute;top:0;left:0;width:0;height:0;overflow:clip";
    probeBox.append(this.probe);
    listEl.append(probeBox);
    // Split rows never scroll sideways; find-in-page or focus can still scroll the list, so undo it.
    listEl.addEventListener("scroll", () => {
      if (this.mode === "split" && listEl.scrollLeft !== 0) listEl.scrollLeft = 0;
    }, { passive: true });
    new ResizeObserver(() => this.updateSplitRange()).observe(listEl);
    this.applyMode();
  }

  /** Wheel input in split view: horizontal motion moves the code text, vertical scrolls rows. */
  private readonly onWheel = (e: WheelEvent): void => {
    let dx = e.deltaX;
    let dy = e.deltaY;
    if (e.shiftKey && dx === 0) [dx, dy] = [dy, 0];
    if (Math.abs(dx) <= Math.abs(dy)) return;
    e.preventDefault();
    const unit = e.deltaMode === 1 ? ROW_HEIGHT : 1;
    this.setSplitX(this.strip.scrollLeft + dx * unit);
    if (dy !== 0) this.list.el.scrollTop += dy * unit;
  };

  /** Moves split code text sideways by dir steps; false in unified view, where rows scroll natively. */
  scrollSideways(dir: 1 | -1): boolean {
    if (this.mode !== "split") return false;
    this.setSplitX(this.strip.scrollLeft + dir * SPLIT_KEY_STEP * this.ch);
    return true;
  }

  private setSplitX(x: number): void {
    this.strip.scrollLeft = x;
    this.publishSplitX();
  }

  /**
   * Publishes the strip's (clamped) offset synchronously, without waiting for its scroll event,
   * rounded up to whole characters so no glyph is cut in half at the code box's edge.
   */
  private publishSplitX(): void {
    const x = this.mode === "split" ? Math.ceil(this.strip.scrollLeft / this.ch) * this.ch : 0;
    this.list.el.style.setProperty("--split-x", `${x}px`);
  }


  /**
   * Sizes the strip so its scroll range is exactly how far the longest line overflows a half's code
   * box. The strip keeps its height whenever split is on, so showing it never changes the list's size.
   */
  private updateSplitRange(): void {
    if (this.mode !== "split") return;
    const ch = (this.ch = this.probe.getBoundingClientRect().width / 100 || 7);
    // The old half is the narrower code box: it also holds the 1px divider.
    const box = this.list.el.clientWidth / 2 - SPLIT_GUTTER_COLUMNS * ch - 1;
    const range = Math.max(0, Math.ceil(this.codeCols * ch - box));
    this.stripSpacer.style.width = `${this.strip.clientWidth + range}px`;
    this.strip.style.visibility = range > 0 ? "" : "hidden";
    this.publishSplitX();
  }

  /** Applies the mode's layout: split owns the horizontal offset, unified scrolls natively. */
  private applyMode(): void {
    const split = this.mode === "split";
    this.list.el.classList.toggle("split", split);
    this.strip.hidden = !split;
    if (split) this.list.el.addEventListener("wheel", this.onWheel, { passive: false });
    else this.list.el.removeEventListener("wheel", this.onWheel);
    this.resetHorizontal();
  }

  /** Returns both horizontal offsets to the start, synchronously. */
  private resetHorizontal(): void {
    this.list.el.scrollLeft = 0;
    this.list.el.style.setProperty("--scroll-left", "0px");
    this.strip.scrollLeft = 0;
    this.publishSplitX();
  }

  get file(): number {
    return this.index;
  }

  get viewMode(): Mode {
    return this.mode;
  }

  /**
   * Loads and shows file i. A pending editor is discarded only after it was saved or is empty; the
   * call is refused while the editor cannot be left.
   */
  async show(i: number): Promise<void> {
    if (!(await this.leaveEditor())) return;
    this.loading?.abort();
    const ctl = new AbortController();
    this.loading = ctl;
    this.index = i;
    this.diff = null;
    this.expanded = new Map();
    this.selection = null;
    this.resetHorizontal();
    this.renderHeader();
    this.rows = [{ k: "note", text: "Loading…", cls: "muted" }];
    this.setRows();
    try {
      const d = await this.api.file(i, ctl.signal);
      if (ctl.signal.aborted) return;
      this.diff = d;
      this.rebuild();
      this.list.scrollToIndex(0, "start");
      await this.revealTargets(i, d, ctl.signal);
    } catch (err) {
      if (ctl.signal.aborted) return;
      this.rows = [{ k: "note", text: `Could not load this file: ${err instanceof Error ? err.message : err}`, cls: "error" }];
      this.setRows();
      this.hooks.failed(err);
    }
  }

  setMode(mode: Mode): void {
    if (mode === this.mode) return;
    const anchor = this.anchorAt(this.list.firstVisible());
    this.mode = mode;
    localStorage.setItem("rvw.mode", mode);
    this.applyMode();
    this.rebuild();
    if (anchor) {
      const at = this.rowOf(anchor.side, anchor.line);
      if (at >= 0) this.list.scrollToIndex(at, "start");
    }
  }

  /** Rebuilds rows after comments or viewed marks changed; a stale editor closes (Q-41). */
  refresh(): void {
    if (this.editor?.stale) {
      this.list.forget(this.editor.key);
      this.editor = null;
      this.selection = null;
    }
    this.renderHeader();
    if (this.diff) this.rebuild();
  }

  /** Scrolls to the next (dir 1) or previous (dir -1) hunk header. */
  moveHunk(dir: 1 | -1): void {
    this.moveTo(dir, (r) => r.k === "hunk");
  }

  /** Scrolls to the next or previous comment of this file; false when there is none that way. */
  moveComment(dir: 1 | -1): boolean {
    return this.moveTo(dir, (r) => r.k === "comment");
  }

  /** Scrolls to the first or last comment row. */
  scrollToEdgeComment(last: boolean): void {
    const idx = this.rows.map((r, n) => (r.k === "comment" ? n : -1)).filter((n) => n >= 0);
    const at = last ? idx[idx.length - 1] : idx[0];
    if (at !== undefined) this.list.scrollToIndex(at, "center");
  }

  /** Opens the editor on the selection, else on the first line in view. */
  commentOnCurrent(): void {
    const f = this.commentableFile();
    if (!f) return;
    if (this.selection) {
      this.run(this.openLineEditor(this.selection));
      return;
    }
    for (let n = this.list.firstVisible(); n < this.rows.length; n++) {
      const a = this.anchorAt(n);
      if (a && this.rows[n]?.k !== "comment") {
        this.run(this.openLineEditor({ side: a.side, start: a.line, end: a.line }));
        return;
      }
    }
  }

  commentOnFile(): void {
    const f = this.commentableFile();
    if (f) this.run(this.openEditor({ target: "file", file: this.index }, null));
  }

  hasEditor(): boolean {
    return this.editor !== null;
  }

  /** Saves the open editor. Returns false when saving failed and the editor stays open. */
  async saveEditor(): Promise<boolean> {
    const ed = this.editor;
    if (!ed) return true;
    return this.settleEditor(ed, (await ed.save()) !== "kept");
  }

  /** Closes ed after a save that succeeded; otherwise shows why it failed, keeping it open. */
  private settleEditor(ed: CommentEditor, ok: boolean): boolean {
    if (!ok) {
      this.list.forget(ed.key);
      this.list.refresh();
      return false;
    }
    if (this.editor === ed) this.closeEditor();
    return true;
  }

  /**
   * Closes the open editor without saving its text (Close, Esc). Returns false when a request whose
   * reply was lost could not be resent, and the editor stays open showing why.
   */
  async discardEditor(): Promise<boolean> {
    const ed = this.editor;
    if (!ed) return true;
    return this.settleEditor(ed, await ed.discard());
  }

  private closeEditor(): void {
    if (!this.editor) return;
    this.list.forget(this.editor.key);
    this.editor = null;
    this.selection = null;
    if (this.diff) this.rebuild();
  }

  /**
   * Leaves the open editor for a transition (another editor, another file, Finish): a blank new
   * comment closes, any other is saved. Refused while another transition is leaving it: that one
   * decides what is shown next. False also when the save failed and the editor stays open showing why.
   */
  async leaveEditor(): Promise<boolean> {
    if (this.leaving) return false;
    const ed = this.editor;
    if (!ed) return true;
    this.leaving = true;
    try {
      return this.settleEditor(ed, await ed.leave());
    } finally {
      this.leaving = false;
    }
  }

  /** Selects sel and opens a new comment's editor on it, unless an editor cannot be opened now. */
  private openLineEditor(sel: Selection): Promise<void> {
    return this.openEditor(lineTarget(this.index, sel.side, sel.start, sel.end), sel);
  }

  /**
   * Opens an editor on a saved comment, or on a new comment at a placement selecting sel. A new
   * comment's open editor is retargeted, keeping its text and ID, unless its target is fixed; any
   * other open editor is left first, as moving to another file leaves it, and a failed save keeps
   * it open showing why.
   */
  private async openEditor(on: Comment | Placement, sel: Selection | null): Promise<void> {
    const prev = this.editor;
    // A pending save would land after the editor is replaced, so the editor stays until it ends.
    if (prev?.isSaving || this.leaving) return;
    if ("id" in on && prev?.saved && prev.id === on.id) return;
    const retarget = !("id" in on) && prev !== null && !prev.saved;
    if (retarget && this.refusesRetarget(prev)) return;
    if (prev && !retarget && !(await this.leaveEditor())) return;
    if (prev && retarget) this.list.forget(prev.key);
    this.selection = sel;
    const ed = "id" in on ? CommentEditor.edit(this.state, this.hooks.failed, on) : CommentEditor.create(this.state, this.hooks.failed, on, retarget ? prev : null);
    this.editor = ed;
    this.rebuild();
    const at = this.rows.findIndex((r) => r.k === "editor");
    if (at >= 0) this.list.scrollToIndex(at, "nearest");
    requestAnimationFrame(() => ed.textarea.focus());
  }

  /**
   * Whether ed is a new comment's editor whose target is fixed (see CommentEditor.targetFixed), so
   * opening a new comment would retarget it; it then shows why.
   */
  private refusesRetarget(ed: CommentEditor | null): boolean {
    if (!ed || ed.saved || !ed.targetFixed) return false;
    ed.refuseRetarget();
    this.list.forget(ed.key);
    this.list.refresh();
    return true;
  }

  private moveTo(dir: 1 | -1, match: (r: DRow) => boolean): boolean {
    const first = this.list.firstVisible();
    const start = dir === 1 ? first + 1 : first - 1;
    for (let n = start; n >= 0 && n < this.rows.length; n += dir) {
      if (match(this.rows[n]!)) {
        this.list.scrollToIndex(n, "start");
        return true;
      }
    }
    return false;
  }

  /** The shown file when it accepts line and file comments, else null. */
  private commentableFile(): ChangedFile | null {
    const f = this.state.files[this.index];
    return f?.commentable ? f : null;
  }

  private renderHeader(): void {
    const f = this.state.files[this.index];
    this.header.replaceChildren();
    if (!f) return;
    const title = h("div", "fh-title");
    title.append(h("span", `tree-status st-${f.status}`, f.status));
    if (f.renamedFrom !== undefined) {
      title.append(h("span", "fh-path old", f.renamedFrom), h("span", "fh-arrow", " → "));
    }
    title.append(h("span", "fh-path", f.path));
    const actions = h("div", "fh-actions");
    if (f.commentable) {
      const fc = h("button", "btn", "Comment on file");
      fc.addEventListener("click", () => this.commentOnFile());
      actions.append(fc);
    }
    const viewed = h("label", "fh-viewed");
    const box = h("input");
    box.type = "checkbox";
    box.checked = this.state.viewed.has(this.index);
    box.addEventListener("change", () => this.run(this.setViewed(box.checked)));
    viewed.append(box, document.createTextNode(" Viewed"));
    actions.append(viewed);
    this.header.append(title, actions);
  }

  /** Marks the shown file viewed or not. A failed save also redraws the header, so its checkbox reverts. */
  async setViewed(viewed: boolean): Promise<void> {
    try {
      await this.state.setViewed(this.index, viewed);
    } finally {
      this.renderHeader();
    }
  }

  private setRows(): void {
    this.list.setSource({
      count: () => this.rows.length,
      variable: (n) => {
        const k = this.rows[n]?.k;
        return k === "comment" || k === "editor" || k === "info";
      },
      key: (n) => rowKey(this.rows[n]!),
      id: (n) => rowId(this.rows[n]!),
      render: (n) => this.renderRow(this.rows[n]!),
    });
  }

  private rebuild(): void {
    const d = this.diff;
    const f = this.state.files[this.index];
    if (!d || !f) return;
    const rows: DRow[] = [];
    if (d.kind !== "text") {
      rows.push({ k: "info", lines: describe(d, f) });
    } else if (d.large) {
      rows.push({ k: "note", text: "Large file: shown without syntax highlighting or context expansion.", cls: "warn" });
    }
    if (!f.commentable) rows.push({ k: "note", text: "This path cannot take file or line comments; use a review comment.", cls: "muted" });

    const lines = d.kind === "text" ? this.lines(d) : [];
    const fileComments = this.state.commentsFor(this.index);
    const placed = new Set<string>();
    const anchors = new Map<string, Comment[]>();
    for (const c of fileComments) {
      if (c.target.target === "file" || c.target.target === "review") continue;
      const key = `${c.target.side}:${targetEnd(c.target)}`;
      anchors.set(key, [...(anchors.get(key) ?? []), c]);
    }
    const ed = this.editor;
    // A comment State holds is shown once: its editor takes the comment's row. A saved editor that
    // is not stale is at its comment's target, in this file.
    const inCommentRow = ed?.saved ?? false;
    const editorAnchor = ed && ed.target.target !== "file" && ed.target.target !== "review" ? `${ed.target.side}:${targetEnd(ed.target)}` : null;

    // File comments, then line comments whose line is not shown, sit above the diff.
    const top: DRow[] = [];
    for (const c of fileComments) {
      if (c.target.target === "file") top.push(this.commentRow(c, "file"));
    }
    if (ed && ed.target.target === "file" && !inCommentRow) top.push({ k: "editor", ed, at: "file" });

    const body: DRow[] = [];
    const emitAnchor = (side: Side, n: number | null): void => {
      if (n === null) return;
      const key = `${side}:${n}`;
      for (const c of anchors.get(key) ?? []) {
        body.push(this.commentRow(c, key));
        placed.add(c.id);
      }
      if (editorAnchor === key && ed && !inCommentRow) {
        body.push({ k: "editor", ed, at: key });
        placed.add("editor");
      }
    };
    const emitLineAnchors = (l: Line | null, side: Side | null): void => {
      if (!l) return;
      if (side !== "new" && l.op !== "+") emitAnchor("old", l.old);
      if (side !== "old" && l.op !== "-") emitAnchor("new", l.new);
    };

    for (const item of lines) {
      if ("hunk" in item) {
        body.push({ k: "hunk", text: item.hunk });
        continue;
      }
      if ("gap" in item) {
        body.push({ k: "gap", gap: item.gap });
        continue;
      }
      if ("line" in item) {
        body.push({ k: "line", line: item.line });
        emitLineAnchors(item.line, null);
      } else {
        body.push({ k: "split", left: item.left, right: item.right });
        emitLineAnchors(item.left, "old");
        emitLineAnchors(item.right, "new");
      }
    }
    for (const c of fileComments) {
      if (c.target.target !== "file" && !placed.has(c.id)) top.push(this.commentRow(c, "top"));
    }
    if (ed && !inCommentRow && editorAnchor && !placed.has("editor")) top.push({ k: "editor", ed, at: "top" });
    rows.push(...top);
    // A text file without hunks (a pure rename) still has its unchanged lines as one gap.
    if (d.kind === "text" && d.hunks.length === 0) rows.push({ k: "note", text: "No line changes.", cls: "muted" });
    rows.push(...body);
    this.rows = rows;

    let cols = 0;
    for (const item of lines) {
      if ("line" in item) cols = Math.max(cols, width(item.line));
      else if ("left" in item) cols = Math.max(cols, width(item.left), width(item.right));
    }
    // Unified sizes its scroll width from --code-cols, capped; split's range uses the full width,
    // since its clipped code boxes add nothing to any scroll width.
    this.codeCols = cols;
    this.list.el.style.setProperty("--code-cols", String(Math.min(cols, MAX_CODE_COLUMNS)));
    this.setRows();
    this.updateSplitRange();
  }

  /** A comment's row at `at`: "file", "top" (labelled, its line not shown), or its anchor key. */
  private commentRow(c: Comment, at: string): DRow {
    const ed = this.editor;
    if (ed?.id === c.id) return { k: "editor", ed, at };
    return { k: "comment", c, at };
  }

  /** The file's shown lines in display order, with hunk headers and gaps. */
  private lines(d: TextDiff): Item[] {
    const out: Item[] = [];
    const pushLine = (l: Line): void => {
      out.push(this.mode === "unified" ? { line: l } : { left: l.op === "+" ? null : l, right: l.op === "-" ? null : l });
    };
    const emitGap = (g: Gap | undefined, before: boolean, after: boolean): void => {
      if (!g) return;
      let runStart = -1;
      for (let n = g.from; n <= g.to + 1; n++) {
        const w = n <= g.to ? this.expanded.get(n) : undefined;
        if (!w && n <= g.to) {
          if (runStart < 0) runStart = n;
          continue;
        }
        if (runStart >= 0) {
          out.push({
            gap: {
              from: runStart,
              to: n - 1,
              old_from: g.old_from + (runStart - g.from),
              before: before || runStart > g.from,
              after: after || n - 1 < g.to,
            },
          });
          runStart = -1;
        }
        if (w) pushLine(toLine(" ", w));
      }
    };
    for (const hk of d.hunks) {
      emitGap(hk.gap_before, false, true);
      out.push({ hunk: hk.header });
      for (const b of hk.blocks) {
        for (const w of b.ctx ?? []) pushLine(toLine(" ", w));
        const del = (b.del ?? []).map((w) => toLine("-", w));
        const add = (b.add ?? []).map((w) => toLine("+", w));
        if (this.mode === "unified") {
          for (const l of del) out.push({ line: l });
          for (const l of add) out.push({ line: l });
        } else {
          for (let k = 0; k < Math.max(del.length, add.length); k++) out.push({ left: del[k] ?? null, right: add[k] ?? null });
        }
      }
    }
    emitGap(d.gap_after, d.hunks.length > 0, false);
    return out;
  }

  private renderRow(r: DRow): HTMLElement {
    switch (r.k) {
      case "note":
        return h("div", `d-note ${r.cls}`, r.text);
      case "info": {
        const el = h("div", "d-info");
        for (const l of r.lines) el.append(h("div", "", l));
        return el;
      }
      case "hunk":
        return h("div", "d-hunk", r.text);
      case "gap":
        return this.renderGap(r.gap);
      case "line":
        return this.renderUnified(r.line);
      case "split": {
        const row = h("div", "d-row d-split");
        row.append(this.renderHalf(r.left, "old"), this.renderHalf(r.right, "new"));
        return row;
      }
      case "comment":
        return this.renderComment(r.c, r.at === "top");
      case "editor":
        return this.renderEditor(r.ed);
    }
  }

  private renderUnified(l: Line): HTMLElement {
    const row = h("div", `d-row d-line op${opClass(l.op)}`);
    const side: Side = l.op === "-" ? "old" : "new";
    const n = side === "old" ? l.old : l.new;
    if (this.isSelected(side, n)) row.classList.add("sel");
    row.append(
      this.lineNumber(l.op === "+" ? null : l.old, "old"),
      this.lineNumber(l.op === "-" ? null : l.new, "new"),
      h("span", "d-mark", l.op === " " ? "" : l.op),
      this.code(l),
    );
    return row;
  }

  private renderHalf(l: Line | null, side: Side): HTMLElement {
    const half = h("div", `d-half ${side}`);
    if (!l) {
      half.classList.add("empty");
      return half;
    }
    half.classList.add(`op${opClass(l.op)}`);
    const n = side === "old" ? l.old : l.new;
    if (this.isSelected(side, n)) half.classList.add("sel");
    half.append(this.lineNumber(n, side), h("span", "d-mark", l.op === " " ? "" : l.op), this.code(l));
    return half;
  }

  private code(l: Line): HTMLElement {
    // In split view the text, with its no-EOL marker, moves inside the clipped code box.
    const text = h("span", "d-text");
    renderCode(text, l.text, l.spans, l.words);
    if (l.noEOL) text.append(h("span", "d-noeol", "⊘"));
    const el = h("span", "d-code");
    el.append(text);
    return el;
  }

  private lineNumber(n: number | null, side: Side): HTMLElement {
    const el = h("span", "d-ln", n === null ? "" : String(n));
    if (n !== null && this.commentableFile()) {
      el.dataset["side"] = side;
      el.dataset["line"] = String(n);
      el.title = "Click to comment, shift-click to extend";
    }
    return el;
  }

  private isSelected(side: Side, n: number | null): boolean {
    const s = this.selection;
    return !!s && n !== null && s.side === side && n >= s.start && n <= s.end;
  }

  private renderGap(g: ShownGap): HTMLElement {
    const row = h("div", "d-row d-gap");
    const count = g.to - g.from + 1;
    row.append(h("span", "d-gap-label", `⋯ ${count} unchanged ${count === 1 ? "line" : "lines"}`));
    const add = (label: string, from: number, to: number): void => {
      const b = h("button", "link", label);
      b.addEventListener("click", () => this.run(this.expand(from, to)));
      row.append(b);
    };
    if (count <= CONTEXT_STEP * 2) {
      add("Show", g.from, g.to);
    } else {
      if (g.before) add(`Show ${CONTEXT_STEP} ↓`, g.from, g.from + CONTEXT_STEP - 1);
      if (g.after) add(`Show ${CONTEXT_STEP} ↑`, g.to - CONTEXT_STEP + 1, g.to);
      add("Show all", g.from, g.to);
    }
    return row;
  }

  /**
   * Expands context around comment targets that fall between hunks, so a seeded comment on an
   * unchanged line is shown at its line. Large files have no expansion.
   */
  private async revealTargets(i: number, d: FileDiff, signal: AbortSignal): Promise<void> {
    if (d.kind !== "text" || d.large) return;
    const gaps: Gap[] = [];
    for (const hk of d.hunks) if (hk.gap_before) gaps.push(hk.gap_before);
    if (d.gap_after) gaps.push(d.gap_after);
    // Ranges are merged per gap: the server serves a range only inside one gap, and two gaps
    // can be adjacent in new-side numbers around a hunk that only deletes.
    const merged: [number, number][] = [];
    for (const g of gaps) {
      const want: [number, number][] = [];
      for (const c of this.state.commentsFor(i)) {
        const t = c.target;
        if (t.target !== "line" && t.target !== "range") continue;
        const start = t.target === "line" ? t.line : t.start;
        // Old-side numbers map into the gap's new-side numbering by a fixed offset.
        const off = t.side === "new" ? 0 : g.from - g.old_from;
        const a = Math.max(g.from, start + off - REVEAL_MARGIN);
        const b = Math.min(g.to, targetEnd(t) + off + REVEAL_MARGIN);
        if (a <= b) want.push([a, b]);
      }
      want.sort((x, y) => x[0] - y[0]);
      let last: [number, number] | undefined;
      for (const [a, b] of want) {
        if (last && a <= last[1] + 1) last[1] = Math.max(last[1], b);
        else merged.push((last = [a, b]));
      }
    }
    if (merged.length === 0) return;
    try {
      for (const [a, b] of merged) {
        const res = await this.api.context(i, a, b);
        if (signal.aborted || i !== this.index) return;
        for (const l of res.lines) if (l.new !== undefined) this.expanded.set(l.new, l);
      }
    } catch (err) {
      if (!signal.aborted) this.hooks.failed(err);
    }
    if (!signal.aborted && i === this.index) this.rebuild();
  }

  private async expand(from: number, to: number): Promise<void> {
    const i = this.index;
    const res = await this.api.context(i, from, to);
    if (i !== this.index) return;
    for (const l of res.lines) if (l.new !== undefined) this.expanded.set(l.new, l);
    this.rebuild();
  }

  private renderComment(c: Comment, label: boolean): HTMLElement {
    const el = h("div", `d-comment author-${c.author}${sideClass(c.target)}`);
    const head = h("div", "c-head");
    head.append(h("span", `badge ${c.author}`, c.author === "agent" ? "Agent" : "You"));
    if (label || c.target.target === "range") head.append(h("span", "c-target", targetLabel(c.target)));
    const edit = h("button", "link quiet", "Edit");
    edit.addEventListener("click", () => this.run(this.openEditor(c, null)));
    const del = h("button", "link quiet danger", "Delete");
    del.addEventListener("click", () => this.run(this.state.deleteComment(c)));
    head.append(edit, del);
    el.append(head, h("div", "c-body", c.body));
    return el;
  }

  private renderEditor(ed: CommentEditor): HTMLElement {
    const el = h("div", `d-editor${sideClass(ed.target)}`);
    el.append(h("div", "c-target", ed.target.target === "file" ? "Comment on this file" : targetLabel(ed.target)), ed.textarea, ed.error);
    const bar = h("div", "editor-bar");
    const save = h("button", "btn primary", "Save");
    save.addEventListener("click", () => this.run(this.saveEditor()));
    const cancel = h("button", "btn", "Close");
    cancel.addEventListener("click", () => this.run(this.discardEditor()));
    bar.append(save, cancel);
    el.append(bar);
    return el;
  }

  private onClick(e: MouseEvent): void {
    const ln = e.target instanceof Element ? e.target.closest<HTMLElement>(".d-ln[data-line]") : null;
    const f = this.commentableFile();
    if (!ln || !f) return;
    const side = parseSide(ln.dataset["side"]);
    const n = Number(ln.dataset["line"]);
    if (!side || !Number.isInteger(n)) return;
    const s = this.selection;
    this.run(this.openLineEditor(e.shiftKey && s && s.side === side ? { side, start: Math.min(s.start, n), end: Math.max(s.end, n) } : { side, start: n, end: n }));
  }

  /** The (side, line) a row shows, used to keep position across view changes. */
  private anchorAt(n: number): { side: Side; line: number } | null {
    for (let k = n; k < this.rows.length; k++) {
      const r = this.rows[k]!;
      if (r.k === "line") {
        const l = r.line;
        return l.op === "-" ? { side: "old", line: l.old! } : { side: "new", line: l.new! };
      }
      if (r.k === "split") {
        if (r.right) return { side: "new", line: r.right.new! };
        if (r.left) return { side: "old", line: r.left.old! };
      }
    }
    return null;
  }

  /** Runs a task started by an event, reporting its failure. */
  private run(task: Promise<unknown>): void {
    task.catch((err: unknown) => this.hooks.failed(err));
  }

  private rowOf(side: Side, line: number): number {
    return this.rows.findIndex((r) => {
      if (r.k === "line") return side === "old" ? r.line.old === line && r.line.op !== "+" : r.line.new === line && r.line.op !== "-";
      if (r.k === "split") return side === "old" ? r.left?.old === line : r.right?.new === line;
      return false;
    });
  }
}

/**
 * A line's width in mono columns, plus 2 for the no-EOL marker: a tab counts 4, an East Asian wide
 * character 2 (a surrogate pair is already 2 UTF-16 units). Split view's scroll range depends on it
 * reaching the end of every line, so it over-estimates rather than under-estimates.
 */
function width(l: Line | null): number {
  if (!l) return 0;
  let extra = 0;
  for (let k = 0; k < l.text.length; k++) {
    const c = l.text.charCodeAt(k);
    if (c === 9) extra += 3;
    else if (c >= 0x1100 && isWide(c)) extra++;
  }
  return l.text.length + extra + 2;
}

function isWide(c: number): boolean {
  return (
    (c >= 0x1100 && c <= 0x115f) ||
    (c >= 0x2e80 && c <= 0xa4cf) ||
    (c >= 0xac00 && c <= 0xd7a3) ||
    (c >= 0xf900 && c <= 0xfaff) ||
    (c >= 0xfe30 && c <= 0xfe4f) ||
    (c >= 0xff00 && c <= 0xff60) ||
    (c >= 0xffe0 && c <= 0xffe6)
  );
}

/** Places a new-side line comment under the new half in split view. */
function sideClass(t: Target | Placement): string {
  return (t.target === "line" || t.target === "range") && t.side === "new" ? " side-new" : "";
}

function opClass(op: Line["op"]): string {
  return op === "+" ? "add" : op === "-" ? "del" : "ctx";
}

function parseSide(v: string | undefined): Side | null {
  return v === "old" || v === "new" ? v : null;
}

function describe(d: FileDiff, f: ChangedFile): string[] {
  const out: string[] = [];
  const kind = d.kind;
  switch (kind) {
    case "binary":
      out.push("Binary file: no line diff.");
      break;
    case "submodule":
      out.push("Submodule change: no line diff.");
      break;
    case "mode":
      out.push("Mode change only.");
      break;
    case "unavailable":
      out.push("Unavailable: a blob of this file is missing from the local object database.");
      break;
    case "text":
      break;
    default:
      return unknownKind(kind);
  }
  if (f.oldPath !== undefined) out.push(`old: ${f.oldPath}${f.oldMode ? ` (mode ${f.oldMode})` : ""}${d.old.present && d.kind === "binary" ? `, ${d.old.size} bytes` : ""}`);
  if (f.newPath !== undefined) out.push(`new: ${f.newPath}${f.newMode ? ` (mode ${f.newMode})` : ""}${d.new.present && d.kind === "binary" ? `, ${d.new.size} bytes` : ""}`);
  return out;
}

function unknownKind(k: never): never {
  throw new Error(`unknown diff kind ${String(k)}`);
}
