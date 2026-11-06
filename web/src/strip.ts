// The review strip: every changed file as one cell in tree order, so the human sees at a glance
// how much is left before the review goes back to the agent, and where. It draws on one canvas of
// fixed width, so its cost is bounded by its pixels, not by the number of files; when files
// outnumber pixel columns, a column shows the last file drawn into it and the shown file always
// gets at least two pixels.

import { h } from "./dom";
import type { State } from "./state";

const WIDTH = 220;
const HEIGHT = 10;
/** Cells at least this wide get a 1px gap, so single files read as cells. */
const GAP_MIN = 4;

export class ReviewStrip {
  readonly el: HTMLElement;
  private readonly canvas: HTMLCanvasElement;
  private current = -1;

  constructor(
    private readonly state: State,
    /** Every file index in tree order. */
    private readonly order: () => readonly number[],
    private readonly onSelect: (i: number) => void,
  ) {
    this.el = h("div", "strip");
    this.canvas = h("canvas", "strip-canvas");
    // The tree and the totals carry the same facts for assistive technology.
    this.canvas.setAttribute("aria-hidden", "true");
    this.el.append(this.canvas);
    this.canvas.addEventListener("click", (e) => {
      const i = this.fileAt(e.offsetX);
      if (i !== undefined) this.onSelect(i);
    });
    this.canvas.addEventListener("mousemove", (e) => {
      const i = this.fileAt(e.offsetX);
      const f = i === undefined ? undefined : this.state.files[i];
      this.canvas.title = f ? `${f.path}${this.state.viewed.has(i!) ? " · viewed" : ""}` : "";
    });
    matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => this.draw());
  }

  /** Marks file i as the shown one and redraws. */
  show(i: number): void {
    this.current = i;
    this.draw();
  }

  draw(): void {
    const order = this.order();
    const dpr = window.devicePixelRatio || 1;
    const w = Math.round(WIDTH * dpr);
    const hgt = Math.round(HEIGHT * dpr);
    if (this.canvas.width !== w || this.canvas.height !== hgt) {
      this.canvas.width = w;
      this.canvas.height = hgt;
    }
    const ctx = this.canvas.getContext("2d");
    if (!ctx) return;
    const css = getComputedStyle(this.el);
    const colour = (name: string): string => css.getPropertyValue(name).trim();
    const todo = colour("--strip-todo");
    const done = colour("--strip-done");
    ctx.clearRect(0, 0, w, hgt);
    const n = order.length;
    if (n === 0) return;
    const cell = w / n;
    const gap = cell >= GAP_MIN * dpr ? Math.round(dpr) : 0;
    let at = -1;
    for (let k = 0; k < n; k++) {
      const i = order[k]!;
      if (i === this.current) at = k;
      const x0 = Math.floor(k * cell);
      const x1 = Math.max(x0 + 1, Math.floor((k + 1) * cell) - gap);
      ctx.fillStyle = this.state.viewed.has(i) ? done : todo;
      ctx.fillRect(x0, 0, x1 - x0, hgt);
    }
    if (at >= 0) {
      const x0 = Math.floor(at * cell);
      const x1 = Math.max(x0 + 2 * Math.round(dpr), Math.floor((at + 1) * cell) - gap);
      ctx.fillStyle = colour("--accent");
      ctx.fillRect(Math.min(x0, w - (x1 - x0)), 0, x1 - x0, hgt);
    }
  }

  private fileAt(x: number): number | undefined {
    const order = this.order();
    if (order.length === 0) return undefined;
    const k = Math.min(order.length - 1, Math.max(0, Math.floor((x / (this.canvas.clientWidth || WIDTH)) * order.length)));
    return order[k];
  }
}
