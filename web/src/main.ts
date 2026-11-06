// Page wiring: session token, layout, keyboard, status polling and
// the page shown after the session ends.

import { Api, type Comment, HttpError, isGone, type RenameDetection } from "./api";
import { h } from "./dom";
import { FileView } from "./fileview";
import { FinishDialog } from "./finish";
import { State } from "./state";
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
  new Page(api, state);
}

class Page {
  private readonly tree: FileTree;
  private readonly view: FileView;
  private readonly finish: FinishDialog;
  private readonly filter: HTMLInputElement;
  private readonly banners: HTMLElement;
  private readonly toast: HTMLElement;
  private readonly modeButton: HTMLButtonElement;
  private readonly totals: HTMLElement;
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
    this.totals = h("div", "top-totals");
    // Both labels share one grid cell, so the button keeps the longer label's width in either mode.
    this.modeButton = h("button", "btn mode-toggle");
    this.modeButton.title = "Toggle unified or split view (s)";
    this.modeButton.addEventListener("click", () => this.toggleMode());
    const finish = h("button", "btn primary", "Finish…");
    finish.title = "⌘↵";
    finish.addEventListener("click", () => this.run(this.openFinish()));
    header.append(source, this.totals, h("span", "spacer"), this.modeButton, finish);

    this.banners = h("div", "banners");
    const renames = renameNotice(state.renameDetection);
    if (renames) this.banners.append(h("div", "banner warn", renames));

    const sidebar = h("aside", "sidebar");
    this.filter = h("input", "filter");
    this.filter.type = "search";
    this.filter.placeholder = "Filter files (/)";
    this.filter.addEventListener("input", () => this.tree?.setFilter(this.filter.value));
    this.filter.addEventListener("keydown", (e) => {
      if (e.key === "Escape" || e.key === "Enter") {
        e.preventDefault();
        this.filter.blur();
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
    this.toast = h("div", "toast");
    this.toast.hidden = true;
    app.replaceChildren(header, this.banners, body, this.toast);

    const failed = (err: unknown): void => this.failed(err);
    this.view = new FileView(fileHeader, diffEl, state, api, { failed });
    this.tree = new FileTree(treeEl, state, (i) => this.run(this.select(i)));
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
    app.append(this.finish.el);

    state.onChange((change) => {
      switch (change) {
        case "draft":
          this.tree.refresh();
          this.view.refresh();
          this.finish.refresh();
          this.renderTotals();
          break;
        case "counts":
          this.tree.refresh();
          this.renderTotals();
          break;
        case "generated":
          if (this.state.generated.size > 0) this.regroup();
          this.renderTotals();
          break;
        case "head":
          this.banners.append(h("div", "banner info", "HEAD has moved since this review started. The review still shows the original snapshot."));
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
    this.renderMode();
    this.renderTotals();
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
    this.tabs.onmessage = (e: MessageEvent) => {
      if (e.data !== "closing" || this.ended) return;
      window.setTimeout(() => this.pollNow(), 1000);
    };
  }

  private async select(i: number, auto = false): Promise<void> {
    this.autoSelected = auto;
    this.tree.select(i, false);
    await this.view.show(i);
    // show is refused while the editor cannot be left; the tree follows the shown file, and expands
    // directories only for a file actually shown.
    this.tree.select(this.view.file);
  }

  /**
   * Opens the Finish dialog once the diff view's editor is left, so a page has one open editor; a
   * failed save keeps the editor, showing why, and the dialog closed.
   */
  private async openFinish(): Promise<void> {
    if (this.ended || this.finish.isOpen) return;
    if (!(await this.view.leaveEditor())) return;
    if (this.ended || this.finish.isOpen) return;
    this.finish.open();
  }

  private toggleMode(): void {
    this.view.setMode(this.view.viewMode === "unified" ? "split" : "unified");
    this.renderMode();
  }

  private renderMode(): void {
    const toSplit = this.view.viewMode === "unified";
    const label = (text: string, shown: boolean): HTMLElement => {
      const el = h("span", "", text);
      if (!shown) el.setAttribute("aria-hidden", "true");
      el.style.visibility = shown ? "" : "hidden";
      return el;
    };
    this.modeButton.replaceChildren(label("Split view", toSplit), label("Unified view", !toSplit));
  }

  private renderTotals(): void {
    const n = this.state.files.length;
    let added = 0;
    let deleted = 0;
    for (const c of this.state.counts.values()) {
      added += c.added;
      deleted += c.deleted;
    }
    const parts = [`${n} ${n === 1 ? "file" : "files"}`, `${this.state.viewed.size} viewed`];
    if (this.state.counts.size > 0) parts.push(`+${added} −${deleted}${this.state.countsDone ? "" : "…"}`);
    if (this.state.countsError) parts.push("line counts unavailable");
    if (this.state.generatedError) parts.push("generated files not grouped");
    this.totals.textContent = parts.join(" · ");
  }

  /** Moves by dir through the tree order from the shown file. */
  private moveFile(dir: 1 | -1, pick?: (i: number) => boolean): void {
    const order = this.tree.order();
    const at = order.indexOf(this.view.file);
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
    const i = this.view.file;
    if (i < 0) return;
    const viewed = !this.state.viewed.has(i);
    await this.view.setViewed(viewed);
    if (viewed) this.moveFile(1, (j) => !this.state.viewed.has(j));
  }

  private onKey(e: KeyboardEvent): void {
    if (this.ended) return;
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      if (this.finish.isOpen) {
        if (this.finish.hasEditor()) this.run(this.finish.saveEditor());
        else this.run(this.finish.finish());
      } else if (this.view.hasEditor()) {
        this.run(this.view.saveEditor());
      } else {
        this.run(this.openFinish());
      }
      return;
    }
    if (e.key === "Escape") {
      // The dialog handles its own Esc; the diff's editor closes here.
      if (!this.finish.isOpen && this.view.hasEditor()) {
        e.preventDefault();
        this.run(this.view.discardEditor());
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
        this.view.moveHunk(1);
        break;
      case "p":
        this.view.moveHunk(-1);
        break;
      case "]":
        this.run(this.moveComment(1));
        break;
      case "[":
        this.run(this.moveComment(-1));
        break;
      case "c":
        this.view.commentOnCurrent();
        break;
      case "v":
        this.run(this.toggleViewed());
        break;
      case "s":
        this.toggleMode();
        break;
      case "ArrowLeft":
      case "ArrowRight":
        // Split code text moves only through FileView; unified keeps the browser's native scroll.
        if (!this.view.scrollSideways(e.key === "ArrowRight" ? 1 : -1)) return;
        break;
      case "/":
        this.filter.focus();
        this.filter.select();
        break;
      default:
        return;
    }
    e.preventDefault();
  }

  /** Next or previous comment, continuing into the next or previous file that has one. */
  private async moveComment(dir: 1 | -1): Promise<void> {
    if (this.view.moveComment(dir)) return;
    const order = this.tree.order();
    const at = order.indexOf(this.view.file);
    for (let n = at + dir; n >= 0 && n < order.length; n += dir) {
      const i = order[n]!;
      if (this.state.commentCount(i) === 0) continue;
      await this.select(i);
      this.view.scrollToEdgeComment(dir === -1);
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
      await this.state.syncStatus();
    } catch (err) {
      if (run !== this.pollRun || this.ended) return;
      // Other failures are transient: the next poll retries.
      if (isGone(err)) {
        this.failed(err);
        return;
      }
    }
    if (run !== this.pollRun || this.ended) return;
    // While counts stream in, poll faster so the tree fills promptly.
    this.poll = window.setTimeout(() => void this.pollStatus(run), this.state.countsDone ? this.state.statusIntervalMs : Math.min(1000, this.state.statusIntervalMs));
  }

  /**
   * Applies the generated marks to the tree. A file the human chose stays shown, expanding its group;
   * the file picked at load is replaced by the first file outside a collapsed group, so groups still
   * start collapsed.
   */
  private regroup(): void {
    if (!this.autoSelected || !this.state.generated.has(this.view.file)) {
      this.tree.regroup(true);
      return;
    }
    this.tree.regroup(false);
    const first = this.tree.order().find((i) => !this.tree.isFolded(i));
    // Every file folded: keep the shown one and reveal its row.
    if (first === undefined) this.tree.regroup(true);
    else this.run(this.select(first, true));
  }

  private failed(err: unknown): void {
    if (isGone(err)) {
      if (!this.ended) this.end("rvw is no longer running", "This review session has ended. Saved changes are in the draft.");
      return;
    }
    const msg = err instanceof Error ? err.message : String(err);
    this.toast.textContent = msg;
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

function endPage(title: string, text: string, markdown?: string): void {
  const page = h("div", "end");
  page.append(h("h1", "", title), h("p", "", text));
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
