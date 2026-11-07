// One changed file's diff, unified or split, with context expansion, inline comments and the
// comment editor. The server derives hunks, line numbers, change pairing and word changes; this
// module only lays them out as virtual rows.

import type { Api, ChangedFile, Comment, CommentSite, EarlierComment, EarlierPlacement, FileDiff, FileReply, Gap, Placement, TextDiff, Line as WireLine, Range, Side, Span } from "./api";
import { renderCode } from "./code";
import { h } from "./dom";
import { fileIcon } from "./fileicon";
import { CommentEditor } from "./editor";
import { LineDrag, type Selection } from "./linedrag";
import { type Align, VirtualList } from "./scroller";
import { earlierCard } from "./earlier";
import { clock, earlierStatus, lineTarget, type State, targetEnd, targetLabel } from "./state";

export const ROW_HEIGHT = 20;
const CONTEXT_STEP = 50;
/** Unchanged lines shown around a comment target that lies between hunks. */
const REVEAL_MARGIN = 3;
const MAX_CODE_COLUMNS = 4000;
/** Columns before split code text: line number (6ch) and change mark (2ch). */
const SPLIT_GUTTER_COLUMNS = 8;
/** Columns before unified code text: two line numbers and the change mark. */
const UNIFIED_GUTTER_COLUMNS = 14;
/** Columns an arrow key moves split code text sideways. */
const SPLIT_KEY_STEP = 8;

export type Layout = "unified" | "split";

/** A step through rows or files: 1 forward, -1 back. */
export type Step = 1 | -1;

/** How revealLine ended; see revealLine. */
export type Reveal = "shown" | "absent" | "hidden" | "superseded";
/** How a show ended: refused leaves the earlier file and its scope untouched; superseded and failed do not. */
export type Load = "shown" | "refused" | "superseded" | "failed";

/** What an editor opens on: a saved comment, or the placement of a new one. */
type EditorSubject = Comment | Placement;

/** A shown line's change: context, added or deleted. */
type LineOp = " " | "+" | "-";

/** One shown line: a patch line or an expanded context line. */
interface Line {
  op: LineOp;
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

/** A whitespace-only hunk shown as one row (REQ-DIFF-011): its index and its changed line count. */
interface HiddenHunk {
  hunk: number;
  lines: number;
}

type Item = { hunk: string } | { gap: ShownGap } | { hidden: HiddenHunk } | { line: Line } | { left: Line | null; right: Line | null };

/** The lines a hunk spans on each side, for matching targets to it; [0, -1] when it has none. */
interface HunkSpan {
  old: readonly [number, number];
  new: readonly [number, number];
}

type DRow =
  | { k: "note"; text: string; cls: string }
  | { k: "info"; lines: string[] }
  | { k: "hunk"; text: string }
  | { k: "gap"; gap: ShownGap }
  // cols: the character columns of the line, or of the split row's wider half.
  | { k: "line"; line: Line; cols: number }
  | { k: "split"; left: Line | null; right: Line | null; cols: number }
  | { k: "hidden"; h: HiddenHunk }
  | { k: "comment"; c: Comment; at: string }
  | { k: "earlier"; e: EarlierComment; p: EarlierPlacement | undefined; at: string }
  | { k: "editor"; ed: CommentEditor; at: string };

/** The key a row's height is cached under: a comment's changes with its body and whether it is labelled. */
function rowKey(r: DRow): string {
  if (r.k === "comment") return `c:${r.c.id}:${r.c.body.length}:${r.at === "top"}`;
  if (r.k === "earlier") return `e:${r.e.id}:${r.at === "top"}`;
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
    case "earlier":
      return `e:${r.e.id}@${r.at}`;
    case "hidden":
      return `w:${r.h.hunk}`;
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
  /** Whether file i is to be shown as its interdiff: Since last review is chosen and i changed since review. */
  since(i: number): boolean;
  /** Chooses All changes and shows the shown file again (REQ-RND-014); true once it is shown. */
  showAll(): Promise<boolean>;
}

export class FileView {
  readonly list: VirtualList;
  private readonly header: HTMLElement;
  private index = -1;
  private diff: FileDiff | null = null;
  /** The loaded file's reply: its scope, whitespace-only hunks and earlier comment placements. */
  private reply: FileReply | null = null;
  private spans: HunkSpan[] = [];
  /** Whitespace-only hunks the human or a navigation showed. */
  private revealed = new Set<number>();
  /** Earlier comment placements of the loaded file, by earlier comment ID. */
  private placements = new Map<string, EarlierPlacement>();
  private loading: AbortController | null = null;
  private chosenLayout: Layout;
  private rows: DRow[] = [];
  /** Expanded context lines keyed by new-side line number. */
  private expanded = new Map<number, WireLine>();
  private selection: Selection | null = null;
  /** Id of the comment ] or [ last showed in this load of the file; steps start from it while it is in view. */
  private commentAt: string | null = null;
  /** The range a gutter drag in progress covers, shown instead of the selection. */
  private dragged: Selection | null = null;
  private readonly drag: LineDrag;
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
  /** Mono character width in px, measured whenever the list's width changes. */
  private ch = 7;
  /** Wrap lines (REQ-DIFF-013): code rows wrap within their column and are measured rows. */
  private wrapped: boolean;
  /**
   * Characters a wrapped line's code column holds. It is part of a wrapped row's height key, so a
   * width change drops every height measured at the old width.
   */
  private wrapCols = 80;

  constructor(
    headerEl: HTMLElement,
    listEl: HTMLElement,
    private readonly state: State,
    private readonly api: DiffApi,
    private readonly hooks: FileViewHooks,
  ) {
    this.header = headerEl;
    this.list = new VirtualList(listEl, ROW_HEIGHT, 90);
    this.chosenLayout = localStorage.getItem("rvw.mode") === "split" ? "split" : "unified";
    this.wrapped = localStorage.getItem("rvw.wrap") === "1";
    listEl.addEventListener("click", (e) => this.onClick(e));
    this.drag = new LineDrag(listEl, {
      start: (e) => this.gutterAt(e.target),
      lineAt: (side, x, y) => this.lineAt(side, x, y),
      preview: (sel) => {
        this.dragged = sel;
        this.list.refresh();
      },
      done: (sel) => {
        this.dragged = sel;
        this.run(
          this.openLineEditor(sel).finally(() => {
            if (this.dragged !== sel) return;
            this.dragged = null;
            this.list.refresh();
          }),
        );
      },
    });
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
      if (this.chosenLayout === "split" && listEl.scrollLeft !== 0) listEl.scrollLeft = 0;
    }, { passive: true });
    new ResizeObserver(() => {
      this.measure();
      this.updateSplitRange();
      // Re-keying wrapped rows replaces the rows, which waits for the frame, outside the observer.
      if (this.wrapped && this.updateWrapCols()) requestAnimationFrame(() => this.setRows());
    }).observe(listEl);
    this.applyLayout();
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

  /** Split view scrolls its code text through the strip; unified and wrapped lines do not. */
  private get stripped(): boolean {
    return this.chosenLayout === "split" && !this.wrapped;
  }

  /** Moves split code text sideways by dir steps; false otherwise, where rows scroll natively or not at all. */
  scrollSideways(dir: Step): boolean {
    if (!this.stripped) return false;
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
    const x = this.stripped ? Math.ceil(this.strip.scrollLeft / this.ch) * this.ch : 0;
    this.list.el.style.setProperty("--split-x", `${x}px`);
  }


  /**
   * Sizes the strip so its scroll range is exactly how far the longest line overflows a half's code
   * box. The strip keeps its height whenever split is on, so showing it never changes the list's size.
   */
  private updateSplitRange(): void {
    if (!this.stripped) return;
    const range = Math.max(0, Math.ceil(this.codeCols * this.ch - this.codeBox()));
    this.stripSpacer.style.width = `${this.strip.clientWidth + range}px`;
    this.strip.style.visibility = range > 0 ? "" : "hidden";
    this.publishSplitX();
  }

  private measure(): void {
    this.ch = this.probe.getBoundingClientRect().width / 100 || 7;
  }

  /** The px width of one code column: unified's, or split's old half, which also holds the 1px divider. */
  private codeBox(): number {
    const w = this.list.el.clientWidth;
    return this.chosenLayout === "split" ? w / 2 - SPLIT_GUTTER_COLUMNS * this.ch - 1 : w - UNIFIED_GUTTER_COLUMNS * this.ch;
  }

  /** Recomputes wrapCols; true when it changed. */
  private updateWrapCols(): boolean {
    const cols = Math.max(1, Math.floor(this.codeBox() / this.ch));
    if (cols === this.wrapCols) return false;
    this.wrapCols = cols;
    return true;
  }

  /**
   * Applies the layout and wrapping: unwrapped split owns the horizontal offset through the strip,
   * unwrapped unified scrolls natively, and wrapped lines never scroll sideways.
   */
  private applyLayout(): void {
    const el = this.list.el;
    el.classList.toggle("split", this.chosenLayout === "split");
    el.classList.toggle("wrap", this.wrapped);
    this.strip.hidden = !this.stripped;
    if (this.stripped) el.addEventListener("wheel", this.onWheel, { passive: false });
    else el.removeEventListener("wheel", this.onWheel);
    this.measure();
    this.updateWrapCols();
    this.resetHorizontal();
    this.updateSplitRange();
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

  get layout(): Layout {
    return this.chosenLayout;
  }

  get wrap(): boolean {
    return this.wrapped;
  }

  /**
   * Turns Wrap lines on or off. The rows stay the same rows, so replacing them keeps the row in
   * view in place while its height changes.
   */
  setWrap(on: boolean): void {
    if (on === this.wrapped) return;
    this.drag.cancel();
    this.wrapped = on;
    localStorage.setItem("rvw.wrap", on ? "1" : "0");
    this.applyLayout();
    this.setRows();
  }

  /** The shown file's diff has loaded. */
  get loaded(): boolean {
    return this.diff !== null;
  }

  /** The shown file is shown as its interdiff, whose old side is the reviewed version. */
  get interdiff(): boolean {
    return this.reply?.scope === "since";
  }

  /**
   * Shows the shown file again with the current scope and whitespace setting, keeping the
   * line in view where it can.
   */
  async reload(): Promise<Load> {
    if (this.index < 0) return "refused";
    const anchor = this.anchorAt(this.list.firstVisible());
    const load = await this.show(this.index);
    if (load !== "shown") return load;
    if (anchor) {
      const at = this.rowOf(anchor.side, anchor.line);
      if (at >= 0) this.list.scrollToIndex(at, "start");
    }
    return "shown";
  }

  /**
   * Loads and shows file i. A pending editor is discarded only after it was saved or is empty; the
   * call is refused, changing nothing, while the editor cannot be left; it is superseded by a later
   * show, and failed when its diff did not load.
   */
  async show(i: number): Promise<Load> {
    if (!(await this.leaveEditor())) return "refused";
    this.drag.cancel();
    this.loading?.abort();
    const ctl = new AbortController();
    this.loading = ctl;
    this.index = i;
    this.diff = null;
    this.reply = null;
    this.spans = [];
    this.revealed = new Set();
    this.placements = new Map();
    this.expanded = new Map();
    this.selection = null;
    this.commentAt = null;
    this.resetHorizontal();
    this.renderHeader();
    this.rows = [{ k: "note", text: "Loading…", cls: "muted" }];
    this.setRows();
    try {
      const reply = await this.api.file(i, { since: this.hooks.since(i), ws: this.state.hideWhitespace }, ctl.signal);
      if (ctl.signal.aborted) return "superseded";
      const d = reply.diff;
      this.reply = reply;
      this.diff = d;
      this.spans = d.kind === "text" ? d.hunks.map(hunkSpan) : [];
      for (const p of reply.earlier) this.placements.set(p.id, p);
      this.renderHeader();
      this.rebuild();
      this.list.scrollToIndex(0, "start");
      await this.revealTargets(i, d, ctl.signal);
      return ctl.signal.aborted ? "superseded" : "shown";
    } catch (err) {
      if (ctl.signal.aborted) return "superseded";
      this.rows = [{ k: "note", text: `Could not load this file: ${err instanceof Error ? err.message : err}`, cls: "error" }];
      this.setRows();
      this.hooks.failed(err);
      return "failed";
    }
  }

  /**
   * Shows line on side of the loaded file, centred and selected, expanding the context that holds
   * it. A deleted file has only its old side, so a new-side request means the old side. "absent":
   * the side does not exist or has fewer lines; "hidden": the line is in no hunk and no expandable
   * gap (a large or non-text file, or a line past the end); "superseded": another file or load
   * replaced this one meanwhile, and nothing was changed.
   */
  async revealLine(side: Side, line: number): Promise<Reveal> {
    const d = this.diff;
    const i = this.index;
    const ctl = this.loading;
    const f = this.state.files[i];
    if (!d || !f) return "superseded";
    if (side === "new" && f.newPath === undefined) side = "old";
    // The interdiff's old side is the reviewed version, not the snapshot's old side.
    if (side === "old" && this.interdiff) {
      if (!(await this.hooks.showAll()) || this.index !== i) return "superseded";
      return this.revealLine(side, line);
    }
    const s = d[side];
    // A side's line count is known unless it is too large to be counted, which leaves lines 0.
    const counted = s.lines > 0 || s.size === 0;
    if (!s.present || (counted && line > s.lines)) return "absent";
    let row = this.rowOf(side, line);
    if (row < 0 && this.unhide(side, line, line)) {
      this.rebuild();
      row = this.rowOf(side, line);
    }
    if (row < 0) {
      if (d.kind !== "text" || d.large) return "hidden";
      const ranges = gapRanges(d, [{ side, start: line, end: line }]);
      if (ranges.length === 0) return "hidden";
      const fetched: WireLine[] = [];
      for (const [a, b] of ranges) {
        const res = await this.api.context(i, a, b, this.interdiff);
        if (this.loading !== ctl || this.index !== i) return "superseded";
        fetched.push(...res.lines);
      }
      for (const l of fetched) if (l.new !== undefined) this.expanded.set(l.new, l);
      this.rebuild();
      row = this.rowOf(side, line);
      if (row < 0) return "hidden";
    }
    this.selection = { side, start: line, end: line };
    this.list.refresh();
    this.list.scrollToIndex(row, "center");
    return "shown";
  }

  setLayout(layout: Layout): void {
    if (layout === this.chosenLayout) return;
    this.drag.cancel();
    const anchor = this.anchorAt(this.list.firstVisible());
    this.chosenLayout = layout;
    // Saved in earlier versions under this key; renaming it would drop the human's choice.
    localStorage.setItem("rvw.mode", layout);
    this.applyLayout();
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
  moveHunk(dir: Step): void {
    this.moveTo(dir, (r) => r.k === "hunk");
  }

  /** Scrolls to the next or previous comment of this file; false when there is none that way. */
  async moveComment(dir: Step): Promise<boolean> {
    for (let n = this.commentCursor(dir) + dir; n >= 0 && n < this.rows.length; n += dir) {
      const r = this.rows[n]!;
      if (r.k !== "comment") continue;
      await this.showComment(r.c, n, "start");
      return true;
    }
    return false;
  }

  /**
   * The row a comment step starts after: the comment last shown while it is still in view, else
   * the edge of the view, so a comment in the first visible row is the next one, not skipped.
   */
  private commentCursor(dir: Step): number {
    const first = this.list.firstVisible();
    const at = this.rows.findIndex((r) => r.k === "comment" && r.c.id === this.commentAt);
    if (at >= first && at <= this.list.lastVisible()) return at;
    return dir === 1 ? first - 1 : first;
  }

  /** Scrolls to the first or last comment row. */
  async scrollToEdgeComment(last: boolean): Promise<void> {
    const idx = this.rows.map((r, n) => (r.k === "comment" ? n : -1)).filter((n) => n >= 0);
    const at = last ? idx[idx.length - 1] : idx[0];
    const r = at === undefined ? undefined : this.rows[at];
    if (r?.k === "comment") await this.showComment(r.c, at!, "center");
  }

  /**
   * Scrolls to comment c's row n. An old-side comment of an interdiff is shown at the top, away
   * from its line, so All changes is chosen first and the comment shown at its line (REQ-RND-014).
   */
  private async showComment(c: Comment, n: number, align: Align): Promise<void> {
    const t = c.target;
    if ((t.target === "line" || t.target === "range") && t.side === "old" && this.interdiff) {
      const i = this.index;
      if (!(await this.hooks.showAll()) || this.index !== i) return;
      n = this.rows.findIndex((r) => r.k === "comment" && r.c.id === c.id);
      if (n < 0) return;
    }
    this.commentAt = c.id;
    this.list.scrollToIndex(n, align);
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
      if (a && this.rows[n]?.k !== "comment" && this.commentableSide(a.side)) {
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
  private async openEditor(on: EditorSubject, sel: Selection | null): Promise<void> {
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

  private moveTo(dir: Step, match: (r: DRow) => boolean): boolean {
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

  /** Lines on side take comments: an interdiff's old side is the reviewed version, outside the snapshot. */
  private commentableSide(side: Side): boolean {
    return side === "new" || !this.interdiff;
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
    title.append(h("span", `tree-status st-${f.status}`, f.status), fileIcon(f.path));
    if (f.renamedFrom !== undefined) {
      title.append(h("span", "fh-path old", f.renamedFrom), h("span", "fh-arrow", " → "));
    }
    title.append(h("span", "fh-path", f.path));
    const round = this.roundLabel(f, this.index);
    if (round) title.append(h("span", "fh-round", round));
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

  /** The header's round label (REQ-RND-011), or "" for a file with nothing to say. */
  private roundLabel(f: ChangedFile, i: number): string {
    const r = f.round;
    if (!r) return "";
    const base = this.state.baseChanged(i);
    if (this.state.changedSince(i)) {
      let text = "Changed since review";
      const n = this.sinceCounts();
      if (n) text += ` · ${n} since ${clock(r.reviewedAt)}`;
      return base ? `${text} · Base changed` : text;
    }
    if (base) return "Base changed";
    return this.state.reviewedWithWhitespaceHidden(i) ? "Reviewed with whitespace hidden" : "";
  }

  /** "+a −d" of the changes since review, as far as the loaded reply tells them, or "". */
  private sinceCounts(): string {
    const reply = this.reply;
    if (!reply) return "";
    if (reply.scope === "since" && reply.diff.kind === "text") {
      let add = 0;
      let del = 0;
      for (const hk of reply.diff.hunks) {
        for (const b of hk.blocks) {
          add += b.add?.length ?? 0;
          del += b.del?.length ?? 0;
        }
      }
      return `+${add} −${del}`;
    }
    if (reply.sinceAdded.length === 0) return "";
    let add = 0;
    for (const [a, b] of reply.sinceAdded) add += b - a + 1;
    return `+${add}`;
  }

  /** Marks the shown file viewed or not. A failed save also redraws the header, so its checkbox reverts. */
  async setViewed(viewed: boolean): Promise<void> {
    try {
      await this.state.setViewed([this.index], viewed);
    } finally {
      this.renderHeader();
    }
  }

  private setRows(): void {
    // The list asks for every row's key on each layout; each is built once per source.
    const keys: (string | undefined)[] = new Array(this.rows.length);
    const wrapped = this.wrapped;
    const wrapCols = this.wrapCols;
    this.list.setSource({
      count: () => this.rows.length,
      variable: (n) => {
        const k = this.rows[n]?.k;
        return k === "comment" || k === "editor" || k === "info" || k === "earlier" || (wrapped && (k === "line" || k === "split"));
      },
      key: (n) => {
        const r = this.rows[n]!;
        return (keys[n] ??= wrapped && (r.k === "line" || r.k === "split") ? `${rowId(r)}|${wrapCols}` : rowKey(r));
      },
      id: (n) => rowId(this.rows[n]!),
      // A wrapped row's estimate is its longest half in visual lines; measurement corrects it.
      estimate: (n) => {
        const r = this.rows[n]!;
        return r.k === "line" || r.k === "split" ? Math.max(1, Math.ceil(r.cols / wrapCols)) * ROW_HEIGHT : 90;
      },
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
    const unavailable = this.reply?.sinceUnavailable;
    if (unavailable === "missing") rows.push({ k: "note", text: "The reviewed version is unavailable; showing all changes.", cls: "warn" });
    else if (unavailable !== undefined) rows.push({ k: "note", text: "Changes since review cannot be shown as lines for this file; showing all changes.", cls: "muted" });

    const fileComments = this.state.commentsFor(this.index);
    const earlier = this.state.earlierFor(this.index);
    const lines = d.kind === "text" ? this.lines(d, fileComments) : [];
    const placed = new Set<string>();
    const anchors = new Map<string, Comment[]>();
    for (const c of fileComments) {
      if (c.target.target === "file" || c.target.target === "review" || !this.commentableSide(c.target.side)) continue;
      const key = `${c.target.side}:${targetEnd(c.target)}`;
      anchors.set(key, [...(anchors.get(key) ?? []), c]);
    }
    const earlierAnchors = new Map<string, EarlierComment[]>();
    for (const e of earlier) {
      const p = this.placements.get(e.id);
      if (!p || p.status !== "current" || !this.commentableSide(p.side)) continue;
      const key = `${p.side}:${p.end}`;
      earlierAnchors.set(key, [...(earlierAnchors.get(key) ?? []), e]);
    }
    const ed = this.editor;
    // A comment State holds is shown once: its editor takes the comment's row. A saved editor that
    // is not stale is at its comment's target, in this file.
    const inCommentRow = ed?.saved ?? false;
    const editorAnchor = ed && ed.target.target !== "file" && ed.target.target !== "review" ? `${ed.target.side}:${targetEnd(ed.target)}` : null;

    // File comments, then line comments whose line is not shown, sit above the diff.
    const top: DRow[] = [];
    for (const e of earlier) {
      if (e.target.target === "file") top.push({ k: "earlier", e, p: this.placements.get(e.id), at: "file" });
    }
    for (const c of fileComments) {
      if (c.target.target === "file") top.push(this.commentRow(c, "file"));
    }
    if (ed && ed.target.target === "file" && !inCommentRow) top.push({ k: "editor", ed, at: "file" });

    const body: DRow[] = [];
    const emitAnchor = (side: Side, n: number | null): void => {
      if (n === null) return;
      const key = `${side}:${n}`;
      for (const e of earlierAnchors.get(key) ?? []) {
        body.push({ k: "earlier", e, p: this.placements.get(e.id), at: key });
        placed.add(e.id);
      }
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
      if ("hidden" in item) {
        body.push({ k: "hidden", h: item.hidden });
        continue;
      }
      if ("line" in item) {
        body.push({ k: "line", line: item.line, cols: width(item.line) });
        emitLineAnchors(item.line, null);
      } else {
        body.push({ k: "split", left: item.left, right: item.right, cols: Math.max(width(item.left), width(item.right)) });
        emitLineAnchors(item.left, "old");
        emitLineAnchors(item.right, "new");
      }
    }
    // Earlier comments not at a shown line (outdated, unplaced, or an interdiff's old side) come
    // first, then draft comments not at a shown line.
    for (const e of earlier) {
      if (e.target.target !== "file" && !placed.has(e.id)) top.push({ k: "earlier", e, p: this.placements.get(e.id), at: "top" });
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
    for (const r of body) if (r.k === "line" || r.k === "split") cols = Math.max(cols, r.cols);
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

  /**
   * Whether hunk n is shown as one hidden-whitespace row: it changes only whitespace, whitespace is
   * hidden, nothing showed it, and no comment, earlier comment or open editor is on its lines.
   */
  private hidesHunk(n: number, comments: readonly Comment[]): boolean {
    if (!this.reply?.wsOnly?.[n] || !this.state.hideWhitespace || this.revealed.has(n)) return false;
    const span = this.spans[n];
    if (!span) return false;
    const on = (side: Side, a: number, b: number): boolean => a <= span[side][1] && b >= span[side][0];
    for (const c of comments) {
      const t = c.target;
      if (t.target === "line" && on(t.side, t.line, t.line)) return false;
      if (t.target === "range" && on(t.side, t.start, t.end)) return false;
    }
    for (const p of this.placements.values()) if (p.status === "current" && on(p.side, p.start, p.end)) return false;
    const ed = this.editor?.target;
    if (ed && (ed.target === "line" || ed.target === "range") && on(ed.side, ed.target === "line" ? ed.line : ed.start, targetEnd(ed))) return false;
    return true;
  }

  /** Shows the hidden whitespace-only hunks holding lines start..end on side; true if any was hidden. */
  private unhide(side: Side, start: number, end: number): boolean {
    const comments = this.state.commentsFor(this.index);
    let any = false;
    this.spans.forEach((span, n) => {
      if (start <= span[side][1] && end >= span[side][0] && this.hidesHunk(n, comments)) {
        this.revealed.add(n);
        any = true;
      }
    });
    return any;
  }

  private showHunk(n: number): void {
    this.revealed.add(n);
    this.rebuild();
  }

  /** The file's shown lines in display order, with hunk headers, gaps and hidden hunks. */
  private lines(d: TextDiff, comments: readonly Comment[]): Item[] {
    const out: Item[] = [];
    const pushLine = (l: Line): void => {
      out.push(this.chosenLayout === "unified" ? { line: l } : { left: l.op === "+" ? null : l, right: l.op === "-" ? null : l });
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
    for (const [n, hk] of d.hunks.entries()) {
      emitGap(hk.gap_before, false, true);
      if (this.hidesHunk(n, comments)) {
        let changed = 0;
        for (const b of hk.blocks) changed += Math.max(b.del?.length ?? 0, b.add?.length ?? 0);
        out.push({ hidden: { hunk: n, lines: changed } });
        continue;
      }
      out.push({ hunk: hk.header });
      for (const b of hk.blocks) {
        for (const w of b.ctx ?? []) pushLine(toLine(" ", w));
        const del = (b.del ?? []).map((w) => toLine("-", w));
        const add = (b.add ?? []).map((w) => toLine("+", w));
        if (this.chosenLayout === "unified") {
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
      case "hidden": {
        const row = h("div", "d-row d-hidden-ws");
        row.append(h("span", "", `⋯ ${r.h.lines} ${r.h.lines === 1 ? "line" : "lines"} changed only in whitespace`));
        const show = h("button", "link", "Show");
        show.addEventListener("click", () => this.showHunk(r.h.hunk));
        row.append(show);
        return row;
      }
      case "earlier":
        return this.renderEarlier(r.e, r.p);
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
    if (l.op !== "-" && this.addedSince(l.new)) row.classList.add("since");
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
    if (side === "new" && this.addedSince(n)) half.classList.add("since");
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

  /** New-side line n was added since the last review (REQ-RND-013). */
  private addedSince(n: number | null): boolean {
    if (n === null || !this.reply) return false;
    for (const [a, b] of this.reply.sinceAdded) if (n >= a && n <= b) return true;
    return false;
  }

  private lineNumber(n: number | null, side: Side): HTMLElement {
    const el = h("span", "d-ln", n === null ? "" : String(n));
    if (n !== null && this.commentableFile() && this.commentableSide(side)) {
      el.dataset["side"] = side;
      el.dataset["line"] = String(n);
      el.title = "Click to comment; shift-click or drag to select lines";
    }
    return el;
  }

  private isSelected(side: Side, n: number | null): boolean {
    const s = this.dragged ?? this.selection;
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
    const wants: Selection[] = [];
    for (const c of this.state.commentsFor(i)) {
      const t = c.target;
      if ((t.target === "line" || t.target === "range") && this.commentableSide(t.side)) {
        wants.push({ side: t.side, start: t.target === "line" ? t.line : t.start, end: targetEnd(t) });
      }
    }
    for (const p of this.placements.values()) {
      if (p.status === "current" && this.commentableSide(p.side)) wants.push({ side: p.side, start: p.start, end: p.end });
    }
    const merged = gapRanges(d, wants);
    if (merged.length === 0) return;
    try {
      for (const [a, b] of merged) {
        const res = await this.api.context(i, a, b, this.interdiff);
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
    const reply = this.reply;
    const res = await this.api.context(i, from, to, this.interdiff);
    if (i !== this.index || reply !== this.reply) return;
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
    head.append(h("span", "spacer"), edit, del);
    // The row is measured by its own box, so the card's inset is the row's padding, not a margin.
    const card = h("div", "c-card");
    card.append(head, h("div", "c-body", c.body));
    el.append(card);
    return el;
  }

  private renderEarlier(e: EarlierComment, p: EarlierPlacement | undefined): HTMLElement {
    const status = earlierStatus(e, p);
    const t = e.target;
    const newSide = status === "current" && p?.side === "new" && (t.target === "line" || t.target === "range");
    const el = h("div", `d-comment earlier${status === "outdated" ? " outdated" : ""}${newSide ? " side-new" : ""}`);
    el.append(earlierCard(this.state, e, p, (task) => this.run(task)));
    return el;
  }

  private renderEditor(ed: CommentEditor): HTMLElement {
    const el = h("div", `d-editor${sideClass(ed.target)}`);
    const card = h("div", "c-card");
    card.append(h("div", "c-target", ed.target.target === "file" ? "Comment on this file" : targetLabel(ed.target)), ed.textarea, ed.error);
    const bar = h("div", "editor-bar");
    const save = h("button", "btn primary", "Save");
    save.addEventListener("click", () => this.run(this.saveEditor()));
    const cancel = h("button", "btn", "Close");
    cancel.addEventListener("click", () => this.run(this.discardEditor()));
    bar.append(save, cancel);
    card.append(bar);
    el.append(card);
    return el;
  }

  private onClick(e: MouseEvent): void {
    const at = this.gutterAt(e.target);
    if (!at) return;
    const { side, line: n } = at;
    const s = this.selection;
    this.run(this.openLineEditor(e.shiftKey && s && s.side === side ? { side, start: Math.min(s.start, n), end: Math.max(s.end, n) } : { side, start: n, end: n }));
  }

  /** The side and line of the commentable gutter cell target is in, if any. */
  private gutterAt(target: EventTarget | null): { side: Side; line: number } | null {
    const ln = target instanceof Element ? target.closest<HTMLElement>(".d-ln[data-line]") : null;
    if (!ln || !this.commentableFile()) return null;
    const side = parseSide(ln.dataset["side"]);
    const line = Number(ln.dataset["line"]);
    return side && Number.isInteger(line) ? { side, line } : null;
  }

  /** The line on side of the diff row at client point (x, y), wherever in the row the point is. */
  private lineAt(side: Side, x: number, y: number): number | null {
    const row = document.elementFromPoint(x, y)?.closest(".d-row");
    if (!row || !this.list.el.contains(row)) return null;
    return this.gutterAt(row.querySelector(`.d-ln[data-side="${side}"][data-line]`))?.line ?? null;
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

/** The lines hunk hk spans on each side. */
function hunkSpan(hk: TextDiff["hunks"][number]): HunkSpan {
  const lo = { old: Infinity, new: Infinity };
  const hi = { old: -Infinity, new: -Infinity };
  const see = (w: WireLine): void => {
    if (w.old !== undefined) {
      lo.old = Math.min(lo.old, w.old);
      hi.old = Math.max(hi.old, w.old);
    }
    if (w.new !== undefined) {
      lo.new = Math.min(lo.new, w.new);
      hi.new = Math.max(hi.new, w.new);
    }
  };
  for (const b of hk.blocks) for (const w of [...(b.ctx ?? []), ...(b.del ?? []), ...(b.add ?? [])]) see(w);
  const span = (side: Side): readonly [number, number] => (hi[side] < lo[side] ? [0, -1] : [lo[side], hi[side]]);
  return { old: span("old"), new: span("new") };
}

/**
 * The new-side line ranges to fetch so every wanted line that lies between hunks is shown with
 * REVEAL_MARGIN lines around it: the one rule for which gap holds a line. Ranges are merged per
 * gap, since the server serves a range only inside one gap, and two gaps can be adjacent in
 * new-side numbers around a hunk that only deletes.
 */
export function gapRanges(d: TextDiff, wants: readonly Selection[]): [number, number][] {
  const gaps: Gap[] = [];
  for (const hk of d.hunks) if (hk.gap_before) gaps.push(hk.gap_before);
  if (d.gap_after) gaps.push(d.gap_after);
  const merged: [number, number][] = [];
  for (const g of gaps) {
    const want: [number, number][] = [];
    for (const t of wants) {
      // Old-side numbers map into the gap's new-side numbering by a fixed offset.
      const off = t.side === "new" ? 0 : g.from - g.old_from;
      const a = Math.max(g.from, t.start + off - REVEAL_MARGIN);
      const b = Math.min(g.to, t.end + off + REVEAL_MARGIN);
      if (a <= b) want.push([a, b]);
    }
    want.sort((x, y) => x[0] - y[0]);
    let last: [number, number] | undefined;
    for (const [a, b] of want) {
      if (last && a <= last[1] + 1) last[1] = Math.max(last[1], b);
      else merged.push((last = [a, b]));
    }
  }
  return merged;
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
function sideClass(t: CommentSite): string {
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
