// Page wiring: session token, layout, keyboard, status polling and
// the page shown after the session ends.

import { Api, type MovedComment, type Scope, HttpError, isGone, type RenameDetection, Superseded, type Target } from "./api";
import { count, h } from "./dom";
import mark from "./favicon.svg";
import { mountIcons } from "./fileicon";
import { FileView, type Step } from "./fileview";
import { FinishDialog } from "./finish";
import { type GoTarget, Palette } from "./palette";
import { clock, State, targetLabel } from "./state";
import { ReviewStrip } from "./strip";
import { FileTree } from "./tree";

const TOKEN_KEY = "rvw.token";

function takeToken(): string {
  const m = /(?:^#|&)token=([^&]+)/.exec(location.hash);
  if (m?.[1]) {
    sessionStorage.setItem(TOKEN_KEY, m[1]);
    // Keep the token out of the visible URL and history.
    history.replaceState(null, "", location.pathname);
    return m[1];
  }
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}

const app = document.getElementById("app")!;

async function main(): Promise<void> {
  const token = takeToken();
  if (!token) {
    endPage("No session token", "Open the URL rvw printed on stderr.");
    return;
  }
  const api = new Api(token);
  let state: State;
  try {
    state = new State(api, await api.session());
  } catch (err) {
    if (err instanceof HttpError && err.status === 401) endPage("Wrong session token", "Open the URL rvw printed on stderr.");
    else if (isGone(err)) endPage("rvw is no longer running", "This review has ended.");
    else endPage("Could not load the review", err instanceof Error ? err.message : String(err));
    return;
  }
  mountIcons();
  new Page(api, state);
}

class Page {
  private readonly tree: FileTree;
  private readonly fileView: FileView;
  private readonly finish: FinishDialog;
  private readonly palette: Palette;
  private readonly filter: HTMLInputElement;
  private readonly banners: HTMLElement;
  private readonly toast: HTMLElement;
  private readonly layoutButton: HTMLButtonElement;
  private readonly filesButton: HTMLButtonElement;
  private readonly sidebar: HTMLElement;
  private readonly totals: HTMLElement;
  private readonly strip: ReviewStrip;
  private readonly wsBox: HTMLInputElement;
  private readonly wrapBox: HTMLInputElement;
  private readonly refreshButton: HTMLButtonElement;
  /** The notice that the change under review changed; null while it has not (REQ-SES-010). */
  private changedBanner: HTMLElement | null = null;
  /** The prompt to reload a page another page's refresh superseded; null until then. */
  private supersededBanner: HTMLElement | null = null;
  private refreshing = false;
  /** The round strip under the top bar; empty when there was no earlier review. */
  private readonly round: HTMLElement;
  /**
   * The scope of REQ-RND-010, Since last review or All changes: transient, never saved. Since last review applies only while
   * some file is changed since review, so it starts there and the control shows only then.
   */
  private scope: Scope = "since";
  /** The shown file was picked at load, not by the human; the marks may move it off a generated file. */
  private autoSelected = false;
  /** The timer of the next scheduled poll; it only schedules the live run. */
  private poll = 0;
  /** The live poll run: a run whose number differs was superseded and stops. */
  private pollRun = 0;
  private ended = false;
  /** Tells the other tabs of this session that one is closing, so they show presence at once (Q-42). */
  private readonly tabs = new BroadcastChannel("rvw");
  private toastTimer = 0;

  constructor(
    private readonly api: Api,
    private readonly state: State,
  ) {
    const header = h("header", "top");
    const source = h("div", "top-source", state.sourceLabel);
    source.title = state.sourceLabel;
    this.totals = h("div", "top-totals");
    this.strip = new ReviewStrip(state, () => this.tree.fullOrder(), (i) => this.run(this.select(i)));
    const progress = h("div", "top-progress");
    progress.append(this.strip.el, this.totals);
    // Below the narrow breakpoint the sidebar folds away; this button shows it over the diff.
    const files = h("button", "btn files-btn", "Files");
    files.setAttribute("aria-expanded", "false");
    files.setAttribute("aria-controls", "files");
    files.addEventListener("click", () => this.showFiles(!app.classList.contains("files-open")));
    this.filesButton = files;
    const search = h("button", "btn search-btn");
    search.append(h("span", "search-label", "Search"), h("kbd", "", "⌘K"));
    search.title = "Search files, lines and text (⌘K)";
    search.addEventListener("click", () => {
      if (!this.finish.isOpen && !this.palette.isOpen) this.palette.open();
    });
    const ws = h("label", "ws-toggle");
    this.wsBox = h("input");
    this.wsBox.type = "checkbox";
    this.wsBox.checked = state.hideWhitespace;
    this.wsBox.addEventListener("change", () => this.run(this.setHideWhitespace(this.wsBox.checked)));
    ws.append(this.wsBox, document.createTextNode("Hide whitespace"));
    ws.title = "Hide hunks that change only whitespace";
    const wrap = h("label", "wrap-toggle");
    this.wrapBox = h("input");
    this.wrapBox.type = "checkbox";
    this.wrapBox.addEventListener("change", () => this.fileView.setWrap(this.wrapBox.checked));
    wrap.append(this.wrapBox, document.createTextNode("Wrap lines"));
    wrap.title = "Wrap long lines instead of scrolling sideways (w)";
    // Both labels share one grid cell, so the button keeps the longer label's width in either mode.
    this.layoutButton = h("button", "btn mode-toggle");
    this.layoutButton.title = "Toggle unified or split view (s)";
    this.layoutButton.addEventListener("click", () => this.toggleLayout());
    this.refreshButton = h("button", "btn refresh-btn", "Refresh");
    this.refreshButton.title = "Take a new snapshot of the same changes and carry the draft over";
    this.refreshButton.addEventListener("click", () => this.run(this.refresh()));
    const finish = h("button", "btn primary finish-btn", "Finish…");
    finish.title = "⌘↵";
    finish.addEventListener("click", () => this.run(this.openFinish()));
    header.append(brand(), source, files, progress, h("span", "spacer"), wrap, ws, search, this.layoutButton, this.refreshButton, finish);
    this.round = h("div", "round-banner");

    this.banners = h("div", "banners");
    const renames = renameNotice(state.renameDetection);
    if (renames) this.banners.append(h("div", "banner warn", renames));
    const moved = movedNotice(state.generation, state.moved);
    if (moved) this.banners.append(moved);

    const sidebar = h("aside", "sidebar");
    sidebar.id = "files";
    this.sidebar = sidebar;
    this.filter = h("input", "filter");
    this.filter.type = "search";
    this.filter.placeholder = "Filter files (/)";
    this.filter.addEventListener("input", () => this.tree?.setFilter(this.filter.value));
    this.filter.addEventListener("keydown", (e) => {
      if (e.key === "Escape" || e.key === "Enter") {
        e.preventDefault();
        // Esc in the overlay closes it, returning focus to the Files button.
        if (e.key === "Escape" && app.classList.contains("files-open")) this.showFiles(false);
        else this.filter.blur();
      }
    });
    const treeEl = h("div", "tree");
    const sidebarHead = h("div", "sidebar-head");
    sidebarHead.append(this.filter);
    sidebar.append(sidebarHead, treeEl);

    const content = h("main", "content");
    const fileHeader = h("div", "file-header");
    const diffEl = h("div", "diff");
    content.append(fileHeader, diffEl);

    const body = h("div", "body");
    body.append(sidebar, content);
    // The overlay closes on a press outside it, and when the window widens past the breakpoint, so
    // it does not come back by itself on the next narrowing.
    content.addEventListener("pointerdown", () => {
      if (app.classList.contains("files-open")) this.showFiles(false);
    });
    matchMedia("(max-width: 900px)").addEventListener("change", () => this.showFiles(false));
    this.toast = h("div", "toast");
    this.toast.hidden = true;
    app.replaceChildren(header, this.round, this.banners, body, this.toast);

    const failed = (err: unknown): void => this.failed(err);
    this.fileView = new FileView(fileHeader, diffEl, state, api, {
      failed,
      since: (i) => this.scope === "since" && this.sinceOffered() && this.state.changedSince(i),
      showAll: () => this.chooseScope("all"),
    });
    this.tree = new FileTree(
      treeEl,
      state,
      (i) => this.run(this.select(i)),
      (files, viewed) => this.run(this.state.setViewed(files, viewed)),
    );
    this.strip.draw();
    this.finish = new FinishDialog(state, api, {
      failed,
      ended: (o) => {
        if (o.kind === "sent") {
          if (o.copied === "stale") this.end("Review sent", "The review changed after it was copied; copy it again below.", o.markdown);
          else this.end(o.copied === "yes" ? "Review sent and copied" : "Review sent", "rvw printed the review for the agent and exited.", o.markdown);
        } else if (o.kind === "cancelled") this.end("Review cancelled", "The draft was deleted and rvw exited.");
        else this.end("Review not delivered", `rvw could not write the result (${o.error}). The draft was kept: run rvw again to resume.`);
      },
    });
    this.palette = new Palette(state, api, {
      go: (t) => this.run(this.goTo(t)),
      current: () => this.fileView.file,
      order: () => this.tree.fullOrder(),
      failed,
    });
    app.append(this.finish.el, this.palette.el);

    state.onChange((change) => {
      switch (change) {
        case "draft":
          this.strip.draw();
          this.tree.refresh();
          this.fileView.refresh();
          this.finish.refresh();
          this.renderTotals();
          break;
        case "counts":
          this.tree.refresh();
          this.renderTotals();
          break;
        case "generated":
          if (this.state.generated.size > 0) this.regroup();
          this.strip.draw();
          this.renderTotals();
          break;
        case "changed":
          this.renderChanged();
          break;
        case "superseded":
          this.superseded();
          break;
        case "whitespace":
          if (this.state.hideWhitespace && this.state.whitespace.size > 0) this.regroup();
          this.renderRound();
          this.renderTotals();
          break;
        case "hide":
          this.wsBox.checked = this.state.hideWhitespace;
          this.regroup();
          this.renderRound();
          this.run(this.fileView.reload());
          break;
      }
    });
    // Once the human has engaged with the file picked at load, it is their choice: the generated
    // marks must not swap it away (regroup). Programmatic scrolls do not count, so scroll is not watched.
    const engaged = (): void => {
      this.autoSelected = false;
    };
    for (const type of ["pointerdown", "wheel"]) content.addEventListener(type, engaged, { capture: true, passive: true });
    document.addEventListener("keydown", (e) => {
      engaged();
      this.onKey(e);
    });
    this.renderLayout();
    this.wrapBox.checked = this.fileView.wrap;
    this.renderTotals();
    this.renderRound();
    const order = this.tree.order();
    const first = order.find((i) => !this.tree.isFolded(i)) ?? order[0];
    if (first !== undefined) this.run(this.select(first, true));
    this.watchPresence();
    this.pollNow();
  }

  /**
   * Page presence (Q-42). Closing or leaving the page sends the close notice; rvw ends the session
   * unless some page makes a request within the close grace. A sibling tab told of the close polls
   * after 1 s rather than at its next regular poll, which may be most of an interval away. The
   * notice is also sent when the page may enter the back/forward cache: a page restored from it
   * polls at once, inside the grace. Nothing is saved here: unsaved editor text is lost.
   */
  private watchPresence(): void {
    window.addEventListener("pagehide", () => {
      if (this.ended) return;
      // The message first: a closing page may be torn down before a later message leaves it.
      this.tabs.postMessage("closing");
      this.api.pageClosed();
    });
    window.addEventListener("pageshow", (e) => {
      if (e.persisted) this.pollNow();
    });
    // Only a visible page asks for the change check, so a page coming into view checks at once.
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") this.pollNow();
    });
    this.tabs.onmessage = (e: MessageEvent) => {
      if (e.data !== "closing" || this.ended) return;
      window.setTimeout(() => this.pollNow(), 1000);
    };
  }

  /** Shows file i; true once it is shown. */
  private async select(i: number, auto = false): Promise<boolean> {
    this.autoSelected = auto;
    this.tree.select(i, false);
    const shown = (await this.fileView.show(i)) === "shown";
    // show is refused while the editor cannot be left; the tree follows the shown file, and expands
    // directories only for a file actually shown.
    this.tree.select(this.fileView.file);
    this.strip.show(this.fileView.file);
    if (shown && !auto) this.showFiles(false);
    return shown;
  }

  /**
   * Shows or hides the file list over the diff; it only matters below the narrow breakpoint. A
   * hidden list cannot scroll, so opening it scrolls the shown file into view and moves focus in.
   */
  private showFiles(open: boolean): void {
    const wasOpen = app.classList.contains("files-open");
    if (open === wasOpen) return;
    app.classList.toggle("files-open", open);
    this.filesButton.setAttribute("aria-expanded", String(open));
    if (open) {
      if (this.fileView.file >= 0) this.tree.select(this.fileView.file);
      this.filter.focus();
    } else if (this.sidebar.contains(document.activeElement) && this.filesButton.offsetParent !== null) {
      // Above the breakpoint the button is hidden and the sidebar stays in view, so focus stays put.
      this.filesButton.focus();
    }
  }

  /**
   * Goes to a palette result. The open editor is left first, as for any move (Q-46); in the shown
   * file the line is revealed without reloading, so context expanded earlier stays.
   */
  private async goTo(t: GoTarget): Promise<void> {
    if (this.ended) return;
    if (t.file === this.fileView.file && this.fileView.loaded) {
      if (!(await this.fileView.leaveEditor())) return;
    } else if (!(await this.select(t.file))) {
      return;
    }
    if (t.line === undefined || this.ended) return;
    const shown = await this.fileView.revealLine(t.side ?? "new", t.line);
    if (shown === "absent") this.notify(`Line ${t.line} is not shown: the file has no such line; the file is open`);
    else if (shown === "hidden") this.notify(`Line ${t.line} is not shown: it is outside the lines this file shows; the file is open`);
  }

  /**
   * Opens the Finish dialog once the diff view's editor is left, so a page has one open editor; a
   * failed save keeps the editor, showing why, and the dialog closed.
   */
  private async openFinish(): Promise<void> {
    if (this.ended || this.finish.isOpen) return;
    if (!(await this.fileView.leaveEditor())) return;
    if (this.ended || this.finish.isOpen) return;
    this.finish.open();
  }

  /** Saves the Hide whitespace setting; a failed save puts the checkbox back. */
  private async setHideWhitespace(hide: boolean): Promise<void> {
    try {
      await this.state.setHideWhitespace(hide);
    } finally {
      this.wsBox.checked = this.state.hideWhitespace;
    }
  }

  /** Since last review is offered while some file is changed since review (REQ-RND-010). */
  private sinceOffered(): boolean {
    return this.state.lastReviewAt !== undefined && this.state.files.some((_, i) => this.state.changedSince(i));
  }

  /**
   * Sets the scope and shows the shown file under it; true once it is shown. A refused
   * reload leaves the file under the earlier scope, so the scope goes back with it; a superseded
   * or failed one does not, since the file was already left.
   */
  private async chooseScope(scope: Scope): Promise<boolean> {
    if (this.scope === scope) return true;
    const prev = this.scope;
    this.scope = scope;
    this.renderRound();
    const i = this.fileView.file;
    // Only a file changed since review looks different under the other scope.
    if (i < 0 || !this.state.changedSince(i)) return true;
    const load = await this.fileView.reload();
    // Only a refusal leaves the file as it was; a superseding show already uses the current scope.
    if (load === "refused" && this.scope === scope) {
      this.scope = prev;
      this.renderRound();
    }
    return load === "shown";
  }

  /** The round strip: the scope and what changed since the last review (REQ-RND-010, REQ-RND-011). */
  private renderRound(): void {
    const s = this.state;
    const at = s.lastReviewAt;
    this.round.replaceChildren();
    this.round.hidden = at === undefined;
    if (at === undefined) return;
    if (this.sinceOffered()) {
      const seg = h("div", "seg");
      seg.setAttribute("role", "group");
      seg.setAttribute("aria-label", "Changes shown");
      const option = (scope: Scope, label: string): HTMLButtonElement => {
        const b = h("button", scope === this.scope ? "on" : "", label);
        b.setAttribute("aria-pressed", String(scope === this.scope));
        b.addEventListener("click", () => this.run(this.chooseScope(scope)));
        return b;
      };
      seg.append(option("since", "Since last review"), option("all", "All changes"));
      this.round.append(seg);
    }
    let changed = 0;
    let fresh = 0;
    let same = 0;
    let ws = 0;
    s.files.forEach((f, i) => {
      if (s.changedSince(i)) changed++;
      else if (f.round === undefined) fresh++;
      if (s.reviewedUnchanged(i)) same++;
      else if (s.hideWhitespace && s.whitespace.has(i)) ws++;
    });
    // Changed and new files are the ones to read, so their counts stand out.
    const items: Parameters<HTMLElement["append"]>[] = [];
    const item = (n: number, rest: string, strong: boolean): void => {
      if (n > 0) items.push(strong ? [h("b", "", count(n)), ` ${rest}`] : [`${count(n)} ${rest}`]);
    };
    item(changed, "changed since", true);
    item(fresh, "new", true);
    item(same, "reviewed unchanged", false);
    item(ws, "whitespace only", false);
    const text = h("span", "round-text", `Your last review was at ${clock(at)}`);
    items.forEach((parts, k) => text.append(k === 0 ? ": " : ", ", ...parts));
    this.round.append(text);
  }

  private toggleLayout(): void {
    this.fileView.setLayout(this.fileView.layout === "unified" ? "split" : "unified");
    this.renderLayout();
  }

  private renderLayout(): void {
    const toSplit = this.fileView.layout === "unified";
    const label = (text: string, shown: boolean): HTMLElement => {
      const el = h("span", "", text);
      if (!shown) el.setAttribute("aria-hidden", "true");
      el.style.visibility = shown ? "" : "hidden";
      return el;
    };
    this.layoutButton.replaceChildren(label("Split view", toSplit), label("Unified view", !toSplit));
  }

  private renderTotals(): void {
    const n = this.state.files.length;
    let added = 0;
    let deleted = 0;
    for (const c of this.state.counts.values()) {
      added += c.added;
      deleted += c.deleted;
    }
    const parts = [`${count(n)} ${n === 1 ? "file" : "files"}`, `${count(this.state.viewed.size)} viewed`];
    if (this.state.counts.size > 0) parts.push(`+${count(added)} −${count(deleted)}${this.state.countsDone ? "" : "…"}`);
    if (this.state.countsError) parts.push("line counts unavailable");
    if (this.state.generatedError) parts.push("generated files not grouped");
    if (this.state.whitespaceError) parts.push("whitespace-only files not grouped");
    this.totals.textContent = parts.join(" · ");
  }

  /** Moves by dir through the tree order from the shown file. */
  private moveFile(dir: Step, pick?: (i: number) => boolean): void {
    const order = this.tree.order();
    const at = order.indexOf(this.fileView.file);
    for (let k = 1; k <= order.length; k++) {
      const n = at + dir * k;
      if (n < 0 || n >= order.length) return;
      const i = order[n]!;
      if (this.tree.isFolded(i)) continue;
      if (!pick || pick(i)) {
        this.run(this.select(i));
        return;
      }
    }
  }

  private async toggleViewed(): Promise<void> {
    const i = this.fileView.file;
    if (i < 0) return;
    const viewed = !this.state.viewed.has(i);
    await this.fileView.setViewed(viewed);
    if (viewed) this.moveFile(1, (j) => !this.state.viewed.has(j));
  }

  private onKey(e: KeyboardEvent): void {
    if (this.ended) return;
    if (e.key.toLowerCase() === "k" && e.metaKey && !e.ctrlKey && !e.altKey) {
      if (this.finish.isOpen || this.palette.isOpen) return;
      if (e.target instanceof Element && e.target.closest("textarea, [contenteditable]")) return;
      e.preventDefault();
      this.palette.open();
      return;
    }
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      if (this.finish.isOpen) {
        if (this.finish.hasEditor()) this.run(this.finish.saveEditor());
        else this.run(this.finish.finish());
      } else if (this.fileView.hasEditor()) {
        this.run(this.fileView.saveEditor());
      } else {
        this.run(this.openFinish());
      }
      return;
    }
    if (e.key === "Escape") {
      // The dialog handles its own Esc; the diff's editor closes here, else the file list overlay.
      if (this.finish.isOpen) return;
      if (this.fileView.hasEditor()) {
        e.preventDefault();
        this.run(this.fileView.discardEditor());
      } else if (app.classList.contains("files-open")) {
        e.preventDefault();
        this.showFiles(false);
      }
      return;
    }
    if (this.finish.isOpen || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.target instanceof Element && e.target.closest("input, textarea, select, [contenteditable]")) return;
    switch (e.key) {
      case "j":
        this.moveFile(1);
        break;
      case "k":
        this.moveFile(-1);
        break;
      case "n":
        this.fileView.moveHunk(1);
        break;
      case "p":
        this.fileView.moveHunk(-1);
        break;
      case "]":
        this.run(this.moveComment(1));
        break;
      case "[":
        this.run(this.moveComment(-1));
        break;
      case "c":
        this.fileView.commentOnCurrent();
        break;
      case "v":
        this.run(this.toggleViewed());
        break;
      case "s":
        this.toggleLayout();
        break;
      case "w":
        this.fileView.setWrap(!this.fileView.wrap);
        this.wrapBox.checked = this.fileView.wrap;
        break;
      case "ArrowLeft":
      case "ArrowRight":
        // Split code text moves only through FileView; unified keeps the browser's native scroll.
        if (!this.fileView.scrollSideways(e.key === "ArrowRight" ? 1 : -1)) return;
        break;
      case "/":
        // Below the breakpoint the filter is in the folded file list.
        if (this.filter.offsetParent === null) this.showFiles(true);
        this.filter.focus();
        this.filter.select();
        break;
      default:
        return;
    }
    e.preventDefault();
  }

  /** Next or previous comment, continuing into the next or previous file that has one. */
  private async moveComment(dir: Step): Promise<void> {
    if (await this.fileView.moveComment(dir)) return;
    const order = this.tree.order();
    const at = order.indexOf(this.fileView.file);
    for (let n = at + dir; n >= 0 && n < order.length; n += dir) {
      const i = order[n]!;
      if (this.state.commentCount(i) === 0) continue;
      if (!(await this.select(i))) return;
      await this.fileView.scrollToEdgeComment(dir === -1);
      return;
    }
  }

  /**
   * Starts a new run of the page's one status poll loop at once, superseding the live run. A
   * superseded run's fetch may still settle; State applies status replies idempotently.
   */
  private pollNow(): void {
    if (this.ended) return;
    window.clearTimeout(this.poll);
    const run = ++this.pollRun;
    void this.pollStatus(run);
  }

  /** Polls the server's status until the session ends or the run is superseded; it never rejects. */
  private async pollStatus(run: number): Promise<void> {
    if (run !== this.pollRun || this.ended) return;
    try {
      await this.state.syncStatus(document.visibilityState === "visible");
    } catch (err) {
      if (run !== this.pollRun || this.ended) return;
      // Other failures are transient: the next poll retries.
      if (isGone(err)) {
        this.failed(err);
        return;
      }
    }
    if (run !== this.pollRun || this.ended) return;
    // While counts or marks stream in, poll faster so the tree fills promptly.
    const settled = this.state.countsDone && this.state.whitespaceDone;
    this.poll = window.setTimeout(() => void this.pollStatus(run), settled ? this.state.statusIntervalMs : Math.min(1000, this.state.statusIntervalMs));
  }

  /**
   * Applies the generated or whitespace-only marks, or the Hide whitespace setting, to the tree. A
   * file the human chose stays shown, expanding its group; the file picked at load is replaced by
   * the first file outside a collapsed group, so groups still start collapsed.
   */
  private regroup(): void {
    if (!this.autoSelected) {
      this.tree.regroup(true);
      return;
    }
    this.tree.regroup(false);
    if (!this.tree.isFolded(this.fileView.file)) {
      this.tree.regroup(true);
      return;
    }
    const first = this.tree.order().find((i) => !this.tree.isFolded(i));
    // Every file folded: keep the shown one and reveal its row.
    if (first === undefined) this.tree.regroup(true);
    else this.run(this.select(first, true));
  }

  /** Shows or removes the notice that the change under review changed, with its Refresh button. */
  private renderChanged(): void {
    if (!this.state.changed) {
      this.changedBanner?.remove();
      this.changedBanner = null;
      return;
    }
    if (this.changedBanner) return;
    const banner = h("div", "banner info changed-banner");
    const button = h("button", "btn", "Refresh");
    button.addEventListener("click", () => this.run(this.refresh()));
    banner.append(h("span", "", "The changes under review have changed since this snapshot was taken. "), button);
    this.changedBanner = banner;
    this.banners.append(banner);
  }

  /**
   * Refreshes the review (REQ-SES-013). The open comment is left first, as moving to another file
   * leaves it, and every write is settled, so nothing unsaved is lost: the page then reloads.
   */
  private async refresh(): Promise<void> {
    if (this.refreshing || this.ended || this.finish.isOpen) return;
    if (!(await this.fileView.leaveEditor())) {
      this.notify("Save or close the open comment, then refresh.", true);
      return;
    }
    this.refreshing = true;
    this.refreshButton.disabled = true;
    try {
      await this.state.settled();
      const r = await this.api.refresh();
      if (!r.unchanged) {
        this.reload();
        return;
      }
      this.notify("Nothing changed: the review already shows the current changes.");
      this.pollNow();
    } finally {
      this.refreshing = false;
      this.refreshButton.disabled = false;
    }
  }

  /**
   * The review moved to another generation (REQ-SES-015). The page reloads at once unless an
   * editor or the Finish dialog may hold text the server has not saved: then it asks first.
   */
  private superseded(): void {
    if (this.ended || this.supersededBanner) return;
    if (!this.fileView.hasEditor() && !this.finish.isOpen) {
      this.reload();
      return;
    }
    const banner = h("div", "banner warn");
    const button = h("button", "btn", "Reload");
    button.addEventListener("click", () => this.reload());
    banner.append(h("span", "", "The review was refreshed in another page, so changes here can no longer be saved. Copy any unsaved text, then reload. "), button);
    this.supersededBanner = banner;
    this.banners.append(banner);
  }

  /** Loads the current generation. The page is not closing, so no close notice is sent. */
  private reload(): void {
    this.ended = true;
    window.clearTimeout(this.poll);
    location.reload();
  }

  private failed(err: unknown): void {
    if (err instanceof Superseded) {
      this.superseded();
      return;
    }
    if (isGone(err)) {
      if (!this.ended) this.end("rvw is no longer running", "This review session has ended. Saved changes are in the draft.");
      return;
    }
    this.notify(err instanceof Error ? err.message : String(err), true);
  }

  /** Shows text in the toast for a while; an error is styled as one. */
  private notify(text: string, error = false): void {
    this.toast.textContent = text;
    this.toast.classList.toggle("info", !error);
    this.toast.hidden = false;
    window.clearTimeout(this.toastTimer);
    this.toastTimer = window.setTimeout(() => (this.toast.hidden = true), 6000);
  }

  /** Runs a task started by an event, reporting its failure. */
  private run(task: Promise<unknown>): void {
    task.catch((err: unknown) => this.failed(err));
  }

  private end(title: string, text: string, markdown?: string): void {
    this.ended = true;
    window.clearTimeout(this.poll);
    this.tabs.close();
    sessionStorage.removeItem(TOKEN_KEY);
    endPage(title, text, markdown);
  }
}

/**
 * rvw's mark: the favicon itself, inlined so the end page still shows it after the server has
 * gone. It carries the name, so no text sits beside it.
 */
function brand(): HTMLElement {
  const el = h("div", "brand");
  el.innerHTML = mark;
  const svg = el.firstElementChild!;
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", "rvw");
  return el;
}

function endPage(title: string, text: string, markdown?: string): void {
  const page = h("div", "end");
  page.append(brand(), h("h1", "", title), h("p", "", text));
  if (markdown) {
    const area = h("textarea", "end-md");
    area.readOnly = true;
    area.value = markdown;
    const copy = h("button", "btn", "Copy as Markdown");
    const note = h("span", "end-note");
    copy.addEventListener("click", () => {
      navigator.clipboard.writeText(markdown).then(
        () => (note.textContent = "Copied."),
        () => {
          note.textContent = "Copy failed; select the text and copy it.";
          area.focus();
          area.select();
        },
      );
    });
    page.append(copy, note, area);
  }
  app.replaceChildren(page);
}

main().catch((err: unknown) => endPage("Could not load the review", err instanceof Error ? err.message : String(err)));

const MOVED_KEY = "rvw.moved-dismissed";

/**
 * The dismissable list of the comments a refresh moved to a wider target (REQ-DRF-006), or null
 * when there are none or this tab dismissed the list for this generation.
 */
function movedNotice(generation: number, moved: readonly MovedComment[]): HTMLElement | null {
  if (moved.length === 0 || sessionStorage.getItem(MOVED_KEY) === String(generation)) return null;
  const banner = h("div", "banner info moved-banner");
  const close = h("button", "btn", "Dismiss");
  close.addEventListener("click", () => {
    sessionStorage.setItem(MOVED_KEY, String(generation));
    banner.remove();
  });
  const list = h("ul", "moved-list");
  for (const m of moved) list.append(h("li", "", `${where(m.from)} → ${where(m.to)}`));
  const n = moved.length === 1 ? "1 comment" : `${moved.length} comments`;
  banner.append(h("span", "", `The refresh moved ${n}: the lines they were on changed or left the change.`), close, list);
  return banner;
}

function where(t: Target): string {
  if (t.target === "review") return "the review";
  return t.target === "file" ? t.path : `${t.path} · ${targetLabel(t)}`;
}

/** The banner for incomplete rename detection, or null when renames were fully detected. */
function renameNotice(d: RenameDetection): string | null {
  switch (d) {
    case "full":
      return null;
    case "missing_blobs":
      return "Renames were not detected: some blobs are missing locally, so renamed files show as a delete and an add.";
    case "limit_exceeded":
      return "Only exact renames were detected: too many files changed for Git's rename detection, so renamed files that were also edited show as a delete and an add.";
  }
}
