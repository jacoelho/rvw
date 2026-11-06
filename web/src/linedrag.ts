// Dragging across the line-number gutter to select a range of lines on one side.

import type { Side } from "./api";

/** Lines start to end, inclusive, on one side of a file. */
export type Selection = { side: Side; start: number; end: number };

export type LineDragHooks = {
  /** The side and line of the gutter cell a press is on, or null when a drag cannot start there. */
  start(e: PointerEvent): { side: Side; line: number } | null;
  /** The line on side of the row at client point (x, y), or null when that row has none. */
  lineAt(side: Side, x: number, y: number): number | null;
  /** The range dragged so far, to show; null when the drag is abandoned. */
  preview(sel: Selection | null): void;
  /** The range a drag across lines ended on. */
  done(sel: Selection): void;
};

/** How far from the list's top or bottom edge, in px, a dragging pointer scrolls it. */
const EDGE = 24;
/** The most the list scrolls in one frame while dragging, in px. */
const MAX_STEP = 24;

type Drag = { pointer: number; side: Side; from: number; to: number; moved: boolean; x: number; y: number };

/**
 * A drag across one list's gutter. A press on a numbered gutter cell starts it; a release after
 * the pointer crossed to another line ends it with that range, and the click that follows is not
 * a click on a line. A release that never left the first line is left to the click. The window
 * listeners and the scrolling frame exist only while a drag does.
 */
export class LineDrag {
  private drag: Drag | null = null;
  private frame = 0;
  /** A drag across lines ended; the click its release produces is part of the drag. */
  private swallowClick = false;
  private readonly onMove = (e: PointerEvent): void => this.move(e);
  private readonly onUp = (e: PointerEvent): void => this.up(e);
  private readonly onCancel = (): void => this.cancel();

  constructor(
    private readonly el: HTMLElement,
    private readonly hooks: LineDragHooks,
  ) {
    el.addEventListener("pointerdown", (e) => this.down(e));
    el.addEventListener("scroll", () => this.track());
    el.addEventListener(
      "click",
      (e) => {
        if (!this.swallowClick) return;
        this.swallowClick = false;
        e.stopImmediatePropagation();
      },
      true,
    );
  }

  /** Abandons a drag in progress, as when the list shows another file. */
  cancel(): void {
    if (!this.end()) return;
    this.hooks.preview(null);
  }

  private down(e: PointerEvent): void {
    // A release that produced no click must not leave the next click swallowed.
    this.swallowClick = false;
    if (e.button !== 0 || e.shiftKey || e.metaKey || e.ctrlKey || e.altKey || this.drag) return;
    const at = this.hooks.start(e);
    if (!at) return;
    // Keeps the press from starting a text selection across the code.
    e.preventDefault();
    this.drag = { pointer: e.pointerId, side: at.side, from: at.line, to: at.line, moved: false, x: e.clientX, y: e.clientY };
    window.addEventListener("pointermove", this.onMove);
    window.addEventListener("pointerup", this.onUp);
    window.addEventListener("pointercancel", this.onCancel);
    window.addEventListener("blur", this.onCancel);
  }

  private move(e: PointerEvent): void {
    const d = this.drag;
    if (!d || e.pointerId !== d.pointer) return;
    d.x = e.clientX;
    d.y = e.clientY;
    this.track();
    if (this.scrollStep() !== 0 && this.frame === 0) this.frame = requestAnimationFrame(() => this.scroll());
  }

  private up(e: PointerEvent): void {
    const d = this.drag;
    if (!d || e.pointerId !== d.pointer) return;
    this.end();
    if (!d.moved) return;
    // The click a release produces is dispatched with it, before any task runs. A release outside
    // the list clicks an ancestor the list never sees, so the flag must not outlive this input.
    this.swallowClick = true;
    setTimeout(() => {
      this.swallowClick = false;
    });
    this.hooks.done(range(d));
  }

  /** Ends the drag, reporting whether there was one. */
  private end(): boolean {
    if (!this.drag) return false;
    this.drag = null;
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    window.removeEventListener("pointermove", this.onMove);
    window.removeEventListener("pointerup", this.onUp);
    window.removeEventListener("pointercancel", this.onCancel);
    window.removeEventListener("blur", this.onCancel);
    return true;
  }

  /** Extends the drag to the line under the pointer, held inside the list. */
  private track(): void {
    const d = this.drag;
    if (!d) return;
    const r = this.el.getBoundingClientRect();
    const n = this.hooks.lineAt(d.side, clamp(d.x, r.left + 1, r.right - 1), clamp(d.y, r.top + 1, r.bottom - 1));
    if (n === null || n === d.to) return;
    d.to = n;
    d.moved = true;
    this.hooks.preview(range(d));
  }

  /** How far to scroll this frame: towards the edge the pointer is near or past, faster further out. */
  private scrollStep(): number {
    const d = this.drag;
    if (!d) return 0;
    const r = this.el.getBoundingClientRect();
    if (d.y < r.top + EDGE) return -Math.min(MAX_STEP, r.top + EDGE - d.y);
    if (d.y > r.bottom - EDGE) return Math.min(MAX_STEP, d.y - (r.bottom - EDGE));
    return 0;
  }

  private scroll(): void {
    const step = this.scrollStep();
    if (step === 0) {
      this.frame = 0;
      return;
    }
    this.el.scrollTop += step;
    // The list may draw the rows scrolled into view after this frame; tracking again next frame finds them.
    this.track();
    this.frame = requestAnimationFrame(() => this.scroll());
  }
}

function range(d: Drag): Selection {
  return { side: d.side, start: Math.min(d.from, d.to), end: Math.max(d.from, d.to) };
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi);
}
