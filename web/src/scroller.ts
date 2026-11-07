// A virtual list: it keeps only the visible rows plus OVERSCAN rows in the document, so its cost
// stays bounded however long the list is. Fixed rows have rowHeight; variable rows (comments,
// editors, wrapped lines) are estimated until mounted, measured when mounted and again whenever
// their size changes while mounted, and their heights are cached by key.
// A shown row's size change keeps the first visible row in place; replacing the rows keeps a row
// in view with the same id in place.

export const OVERSCAN = 50;

/** Where scrollToIndex puts a row: at the top, centred, or the least scroll that shows it. */
export type Align = "start" | "center" | "nearest";

export interface ListSource {
  count(): number;
  /** Variable rows are measured; fixed rows are rowHeight tall. */
  variable(i: number): boolean;
  /** Stable key a variable row's height is cached under; it changes when the row's content does. */
  key(i: number): string;
  /**
   * The row's identity, which outlives content changes (a comment and its editor share one):
   * replacing the rows keeps a row in view with the same id in place on screen. An id that is the
   * row's index anchors by position.
   */
  id(i: number): string;
  /** A variable row's height before it is measured; the list's default estimate when absent. */
  estimate?(i: number): number;
  render(i: number): HTMLElement;
}

/** A row with its top and bottom relative to the viewport, where it was drawn. */
type Pin = { i: number; y: number; bottom: number };
type Shown = { rank: number; y: number; bottom: number };

export class VirtualList {
  readonly el: HTMLElement;
  private readonly inner: HTMLElement;
  private source: ListSource | null = null;
  private offsets = new Float64Array(1);
  private readonly heights = new Map<string, number>();
  private mounted = new Map<number, HTMLElement>();
  /** Each mounted row's id when it was rendered, which a source change no longer reports. */
  private readonly mountedIds = new Map<number, string>();
  /** The key of each mounted variable row, which the resize observer reports by element. */
  private readonly rowKeys = new Map<Element, string>();
  private readonly rows: ResizeObserver;
  /** A recorded height changed since the offsets were last computed. */
  private stale = false;
  private frame = 0;

  constructor(
    el: HTMLElement,
    private readonly rowHeight: number,
    private readonly estimate = 80,
  ) {
    this.el = el;
    this.inner = document.createElement("div");
    this.inner.className = "vl-inner";
    el.replaceChildren(this.inner);
    el.addEventListener("scroll", () => {
      el.style.setProperty("--scroll-left", `${el.scrollLeft}px`);
      this.schedule();
    }, { passive: true });
    new ResizeObserver(() => this.schedule()).observe(el);
    // A mounted row that grows or shrinks (a resized textarea, reflowed text) is re-measured; the
    // relayout waits for the next frame so it never runs inside the observer callback.
    this.rows = new ResizeObserver((entries) => {
      for (const e of entries) {
        const key = this.rowKeys.get(e.target);
        if (key !== undefined && e.target instanceof HTMLElement) this.record(key, e.target.offsetHeight);
      }
      if (this.stale) this.schedule();
    });
  }

  /** Replaces the rows. Mounted rows are discarded; heights of rows still present are kept. */
  setSource(source: ListSource): void {
    const shown = this.shownRows();
    this.source = source;
    this.remount(shown);
  }

  /** Re-renders every mounted row after the source's data changed in place. */
  refresh(): void {
    this.remount(null);
  }

  /**
   * Discards and redraws the mounted rows. Detaching an element drops its focus, so a focused
   * element that a row renders again (an open editor's textarea) gets its focus and selection back:
   * the human keeps typing, and their keys never reach the page's shortcuts.
   */
  private remount(shown: Map<string, Shown> | null): void {
    const active = document.activeElement;
    const focused = active instanceof HTMLElement && this.inner.contains(active) ? active : null;
    const text = focused instanceof HTMLTextAreaElement || focused instanceof HTMLInputElement ? focused : null;
    const sel = text ? { start: text.selectionStart, end: text.selectionEnd, dir: text.selectionDirection } : null;
    this.layout();
    const pin = shown ? this.keepShown(shown) : null;
    this.clear();
    this.draw(pin);
    if (!focused?.isConnected || document.activeElement === focused) return;
    focused.focus({ preventScroll: true });
    if (text && sel && sel.start !== null && sel.end !== null) text.setSelectionRange(sel.start, sel.end, sel.dir ?? undefined);
  }

  /**
   * The ids of the rows in view with their top relative to the viewport, ranked as CSS scroll
   * anchoring ranks them: the partly hidden top row first, then the rows below it. Positions are
   * where the rows were drawn: the offsets may already hold heights measured since.
   */
  private shownRows(): Map<string, Shown> {
    const top = this.el.scrollTop;
    const view = this.el.clientHeight;
    const rows: { id: string; y: number; bottom: number }[] = [];
    for (const [i, el] of this.mounted) {
      const id = this.mountedIds.get(i);
      const y = el.offsetTop - top;
      const bottom = y + el.offsetHeight;
      if (id !== undefined && y < view && bottom > 0) rows.push({ id, y, bottom });
    }
    rows.sort((a, b) => a.y - b.y);
    const shown = new Map<string, Shown>();
    for (const r of rows) if (!shown.has(r.id)) shown.set(r.id, { rank: shown.size, y: r.y, bottom: r.bottom });
    return shown;
  }

  /**
   * Scrolls so the best-ranked shown row that the new rows still have is where it was drawn, and
   * returns it with that position.
   */
  private keepShown(shown: Map<string, Shown>): Pin | null {
    const src = this.source;
    if (!src || shown.size === 0) return null;
    let best: (Shown & { i: number }) | null = null;
    for (let i = 0, n = this.count(); i < n; i++) {
      const s = shown.get(src.id(i));
      if (s && (!best || s.rank < best.rank)) {
        best = { i, ...s };
        if (s.rank === 0) break;
      }
    }
    if (!best) return null;
    const pin = { i: best.i, y: best.y, bottom: best.bottom };
    this.el.scrollTop = this.scrollFor(pin);
    return pin;
  }

  /**
   * The scroll position that puts pin's row where it was drawn: its top stays put, unless the row
   * shrank so much that it would then lie wholly above the view (a wrapped line mostly scrolled past
   * when the window widens); its bottom then stays put, but its top never drops below the view's top.
   */
  private scrollFor(pin: Pin): number {
    const top = this.offsets[pin.i] ?? 0;
    const height = (this.offsets[pin.i + 1] ?? top) - top;
    const y = pin.y + height > 0 ? pin.y : Math.min(0, pin.bottom - height);
    return Math.max(0, top - y);
  }

  /** Forgets a variable row's measured height, e.g. after its content changed. */
  forget(key: string): void {
    this.heights.delete(key);
  }

  count(): number {
    return this.source?.count() ?? 0;
  }

  /** Index of the first row whose bottom is below the top of the viewport. */
  firstVisible(): number {
    return this.indexAt(this.el.scrollTop);
  }

  lastVisible(): number {
    return this.indexAt(this.el.scrollTop + this.el.clientHeight - 1);
  }

  scrollToIndex(i: number, align: Align = "nearest"): void {
    const n = this.count();
    if (n === 0) return;
    i = Math.max(0, Math.min(i, n - 1));
    const top = this.offsets[i] ?? 0;
    const bottom = this.offsets[i + 1] ?? top;
    const view = this.el.clientHeight;
    let to = this.el.scrollTop;
    if (align === "start") to = top;
    else if (align === "center") to = top - (view - (bottom - top)) / 2;
    else if (top < to) to = top;
    else if (bottom > to + view) to = bottom - view;
    this.el.scrollTop = Math.max(0, to);
    this.draw();
  }

  /** The mounted element for row i, if any. */
  element(i: number): HTMLElement | undefined {
    return this.mounted.get(i);
  }

  private schedule(): void {
    if (this.frame) return;
    this.frame = requestAnimationFrame(() => {
      this.frame = 0;
      this.draw();
    });
  }

  private record(key: string, height: number): void {
    // A row is never empty; 0 means the list is hidden (a closed dialog), not a new height.
    if (height === 0 || this.heights.get(key) === height) return;
    this.heights.set(key, height);
    this.stale = true;
  }

  private unmount(el: HTMLElement): void {
    if (this.rowKeys.delete(el)) this.rows.unobserve(el);
    el.remove();
  }

  private heightOf(i: number): number {
    const src = this.source;
    if (!src || !src.variable(i)) return this.rowHeight;
    return this.heights.get(src.key(i)) ?? src.estimate?.(i) ?? this.estimate;
  }

  private layout(): void {
    this.stale = false;
    const n = this.count();
    const offsets = new Float64Array(n + 1);
    const live = new Set<string>();
    for (let i = 0; i < n; i++) {
      offsets[i + 1] = (offsets[i] ?? 0) + this.heightOf(i);
      if (this.source?.variable(i)) live.add(this.source.key(i));
    }
    this.offsets = offsets;
    // Heights are bounded by the rows the list has now, not every row it ever showed.
    for (const key of this.heights.keys()) if (!live.has(key)) this.heights.delete(key);
    this.inner.style.height = `${offsets[n] ?? 0}px`;
  }

  /**
   * Recomputes the offsets after shown rows changed size, keeping pin, else the first visible row,
   * at the same place on screen, so a row above it that grows or shrinks does not move what is shown.
   */
  private relayout(pin: Pin | null = null): void {
    let at = pin;
    if (!at) {
      const i = this.indexAt(this.el.scrollTop);
      const y = (this.offsets[i] ?? 0) - this.el.scrollTop;
      at = { i, y, bottom: y + (this.offsets[i + 1] ?? 0) - (this.offsets[i] ?? 0) };
    }
    this.layout();
    const to = this.scrollFor(at);
    if (to !== this.el.scrollTop) this.el.scrollTop = to;
  }

  private indexAt(y: number): number {
    const n = this.count();
    let lo = 0;
    let hi = n;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if ((this.offsets[mid + 1] ?? 0) <= y) lo = mid + 1;
      else hi = mid;
    }
    return Math.min(lo, Math.max(0, n - 1));
  }

  private clear(): void {
    for (const el of this.mounted.values()) this.unmount(el);
    this.mounted.clear();
    this.mountedIds.clear();
  }

  /** Draws the rows in view; pin, a row the caller placed on screen, keeps its place while fresh rows are measured. */
  private draw(pin: Pin | null = null): void {
    const src = this.source;
    const n = this.count();
    if (!src || n === 0) {
      this.clear();
      return;
    }
    // Only the observer leaves the offsets stale between draws: a shown row changed size.
    if (this.stale) this.relayout();
    const first = this.indexAt(this.el.scrollTop);
    const last = this.indexAt(this.el.scrollTop + this.el.clientHeight);
    const before = OVERSCAN >> 1;
    const start = Math.max(0, first - before);
    const end = Math.min(n - 1, last + (OVERSCAN - before));
    for (const [i, el] of this.mounted) {
      if (i < start || i > end) {
        this.unmount(el);
        this.mounted.delete(i);
        this.mountedIds.delete(i);
      }
    }
    const fresh: number[] = [];
    for (let i = start; i <= end; i++) {
      let el = this.mounted.get(i);
      if (!el) {
        el = src.render(i);
        el.classList.add("vl-row");
        this.mounted.set(i, el);
        this.mountedIds.set(i, src.id(i));
        this.inner.append(el);
        if (src.variable(i)) fresh.push(i);
      }
      el.style.top = `${this.offsets[i] ?? 0}px`;
      if (!src.variable(i)) el.style.height = `${this.rowHeight}px`;
    }
    // Measure newly mounted variable rows now, so they are placed before the next paint; later
    // size changes arrive through the observer.
    for (const i of fresh) {
      const el = this.mounted.get(i);
      if (!el) continue;
      const key = src.key(i);
      this.record(key, el.offsetHeight);
      this.rowKeys.set(el, key);
      this.rows.observe(el);
    }
    // Fresh rows replace their estimates before any paint. Rows above the view were placed by
    // estimates too, so the first visible row keeps its place while they are corrected.
    if (this.stale) {
      this.relayout(pin);
      for (const [i, el] of this.mounted) el.style.top = `${this.offsets[i] ?? 0}px`;
      this.schedule();
    }
  }
}
