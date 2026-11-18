// The Finish dialog: summary, the overview of every comment with its quoted code, edited in place,
// Delete all, Copy, Send review (with Copy and send in its menu) and Discard review. Each control has
// one consequence: × leaves the dialog, Delete all edits the draft, Copy changes nothing, the Send
// group delivers, and Discard ends the review without delivering.

import { type Api, type Comment, type EarlierComment, type Quote, type Target } from "./api";
import { h } from "./dom";
import { earlierCard } from "./earlier";
import { CommentEditor } from "./editor";
import { VirtualList } from "./scroller";
import { type State, targetLabel } from "./state";

/** The server calls FinishDialog makes beyond the draft: the result, and ending the session. */
export type OutcomeApi = Pick<Api, "resultMarkdown" | "finish" | "cancel" | "quotes">;

export interface FinishHooks {
  failed(err: unknown): void;
  /**
   * The session ended: delivered (with the result), not delivered, or cancelled. A delivered
   * result says whether it was copied first: "stale" when the copied text differs from it.
   */
  ended(outcome: Ending): void;
}

/** How a session ended, as the Finish dialog reports it. */
export type Ending =
  | { kind: "sent"; markdown: string; copied: Copied }
  | { kind: "failed"; error: string }
  | { kind: "cancelled" };

export type Copied = "no" | "yes" | "stale";

/**
 * The rows: the draft's comments, then, apart, the earlier comments no file shows (REQ-CMT-010):
 * review comments and those whose file is not a changed file.
 */
type Row =
  | { k: "comment"; c: Comment }
  | { k: "edit"; ed: CommentEditor; c: Comment | null }
  | { k: "empty" }
  | { k: "earlier-head" }
  | { k: "earlier"; e: EarlierComment };

/**
 * A comment's quote as the dialog knows it: loading, loaded, or failed. Keyed by the target it was
 * read for, so a comment whose target changed is read again.
 */
/** A comment's quoted lines: fetched, still loading, or failed to load. */
type QuoteState = Quote | "loading" | "failed";

type QuoteEntry = { target: string; quote: QuoteState };


export class FinishDialog {
  readonly el: HTMLDialogElement;
  private readonly summary: HTMLTextAreaElement;
  private readonly agentOnly: HTMLInputElement;
  private readonly listTitle: HTMLElement;
  private readonly list: VirtualList;
  private readonly addReview: HTMLButtonElement;
  private readonly status: HTMLElement;
  private readonly sendButton: HTMLButtonElement;
  private readonly sendMore: HTMLButtonElement;
  private readonly menu: HTMLElement;
  private readonly copy: HTMLButtonElement;
  private readonly copyAndSend: HTMLButtonElement;
  private readonly deleteAll: HTMLButtonElement;
  private readonly confirmBar: HTMLElement;
  private readonly confirmText: HTMLElement;
  private readonly confirmYes: HTMLButtonElement;
  /** What the confirmation's action button does, while the confirmation shows. */
  private confirmAction: (() => Promise<void>) | null = null;
  /** The confirmation names a comment count, so a change to the list withdraws it. */
  private confirmCounts = false;
  private rows: Row[] = [];
  private edit: CommentEditor | null = null;
  /** An editor is being left so another can open; clicks meanwhile are ignored. */
  private leaving = false;
  private busy = false;
  /** Quotes read while the dialog is open; cleared when it closes, so it holds at most one per comment. */
  private readonly quotes = new Map<string, QuoteEntry>();
  /** The mounted quote element of each comment, to fill in place when its quote arrives. */
  private readonly quoteEls = new Map<string, HTMLElement>();
  private readonly wanted = new Set<string>();
  private fetchQueued = false;

  constructor(
    private readonly state: State,
    private readonly api: OutcomeApi,
    private readonly hooks: FinishHooks,
  ) {
    this.el = h("dialog", "finish");

    const head = h("div", "fin-head");
    const titles = h("div", "fin-titles");
    titles.append(h("h2", "", "Finish review"), h("p", "fin-lede", "Finalise your comments and send the review."));
    const close = h("button", "fin-close", "×");
    close.setAttribute("aria-label", "Close");
    close.title = "Close (Esc)";
    close.addEventListener("click", () => this.run(this.close()));
    head.append(titles, close);

    this.summary = h("textarea", "fin-summary");
    this.summary.id = "fin-summary";
    this.summary.rows = 3;
    this.summary.placeholder = "Enter a short summary for the agent…";
    this.summary.value = state.summary;
    this.summary.addEventListener("blur", () => this.run(this.saveSummary()));
    const summaryLabel = h("label", "fin-label", "Summary");
    summaryLabel.htmlFor = this.summary.id;
    summaryLabel.append(h("span", "fin-optional", " (optional)"));

    const listHead = h("div", "fin-list-head");
    this.listTitle = h("span", "", "Comments");
    const filter = h("label", "fin-filter");
    this.agentOnly = h("input", "switch");
    this.agentOnly.type = "checkbox";
    this.agentOnly.setAttribute("role", "switch");
    this.agentOnly.addEventListener("change", () => this.rebuild());
    filter.append(this.agentOnly, document.createTextNode("Agent comments only"));
    this.deleteAll = h("button", "link quiet danger fin-delete-all", "Delete all");
    this.deleteAll.addEventListener("click", () => this.askDelete());
    const titleRow = h("div", "fin-title");
    titleRow.append(this.listTitle, h("span", "fin-sep", "·"), this.deleteAll);
    listHead.append(titleRow, filter);

    const listEl = h("div", "fin-list");
    this.list = new VirtualList(listEl, 32, 90);

    this.addReview = h("button", "fin-add", "Add review comment…");
    this.addReview.addEventListener("click", () => this.run(this.openEdit(null)));

    this.confirmBar = h("div", "fin-confirm");
    this.confirmBar.hidden = true;
    this.confirmText = h("span");
    this.confirmYes = h("button", "btn danger");
    this.confirmYes.addEventListener("click", () => {
      const action = this.confirmAction;
      this.hideConfirm();
      if (action) this.run(action());
    });
    const keep = h("button", "btn", "Keep");
    keep.addEventListener("click", () => this.hideConfirm());
    this.confirmBar.append(this.confirmText, this.confirmYes, keep);

    this.status = h("div", "fin-status");

    this.menu = h("div", "fin-menu");
    this.menu.id = "fin-send-menu";
    this.menu.setAttribute("role", "menu");
    this.menu.hidden = true;
    this.copyAndSend = this.menuItem("Copy and send", () => void this.copyThenSend());
    this.menu.append(this.copyAndSend);
    this.menu.addEventListener("keydown", (e) => this.menuKey(e));

    this.sendButton = h("button", "btn primary fin-send", "Send review");
    this.sendButton.title = "⌘↵";
    this.sendButton.addEventListener("click", () => void this.finish());
    this.sendMore = h("button", "btn primary fin-send-more", "▾");
    this.sendMore.setAttribute("aria-label", "More send options");
    this.sendMore.title = "More send options";
    this.sendMore.setAttribute("aria-haspopup", "menu");
    this.sendMore.setAttribute("aria-controls", this.menu.id);
    this.sendMore.setAttribute("aria-expanded", "false");
    this.sendMore.addEventListener("click", () => (this.menu.hidden ? this.openMenu() : this.closeMenu(true)));
    const sendGroup = h("div", "fin-send-group");
    sendGroup.append(this.sendButton, this.sendMore, this.menu);
    // A press outside the menu closes it, unless it is the toggle, whose click decides.
    this.el.addEventListener("pointerdown", (e) => {
      if (!this.menu.hidden && e.target instanceof Node && !sendGroup.contains(e.target)) this.closeMenu(false);
    });

    this.copy = h("button", "btn", "Copy");
    this.copy.title = "Copy the review as Markdown; the review stays open";
    this.copy.addEventListener("click", () => void this.copyMarkdown());

    const discard = h("button", "link quiet danger fin-discard", "Discard review…");
    discard.addEventListener("click", () =>
      this.ask(
        "Ends rvw without sending: the draft is deleted and the agent is told the review was cancelled.",
        "Discard the review",
        () => this.cancelReview(),
      ),
    );

    const actions = h("div", "fin-actions");
    actions.append(discard, h("span", "spacer"), this.copy, sendGroup);

    this.el.append(head, summaryLabel, this.summary, listHead, listEl, this.addReview, this.confirmBar, this.status, actions);
    this.el.addEventListener("cancel", (e) => {
      // Esc closes the menu, then the inner editor, before the dialog.
      if (!this.menu.hidden) {
        e.preventDefault();
        this.closeMenu(true);
        return;
      }
      if (this.edit) {
        e.preventDefault();
        this.run(this.discardEdit());
      }
    });
    this.el.addEventListener("close", () => {
      this.closeMenu(false);
      this.quotes.clear();
      this.wanted.clear();
      this.run(this.saveSummary());
    });
  }

  get isOpen(): boolean {
    return this.el.open;
  }

  open(): void {
    // Only this dialog edits the summary, so its text is the saved summary or one whose save failed.
    this.hideConfirm();
    this.status.textContent = "";
    this.syncBusy();
    this.el.showModal();
    this.rebuild();
    this.summary.focus();
  }

  /** Closes the dialog once its editor is discarded; a failed discard keeps both open. */
  async close(): Promise<void> {
    if (!(await this.discardEdit())) return;
    this.el.close();
  }

  /** Re-renders after comments changed; a stale editor closes (Q-41). */
  refresh(): void {
    if (this.edit?.stale) this.edit = null;
    for (const id of this.quotes.keys()) if (!this.state.comments.has(id)) this.quotes.delete(id);
    if (this.el.open) this.rebuild();
  }

  hasEditor(): boolean {
    return this.edit !== null;
  }

  /** Saves the open editor. Returns false when saving failed and the editor stays open. */
  async saveEditor(): Promise<boolean> {
    const ed = this.edit;
    if (!ed) return true;
    return this.settleEdit(ed, (await ed.save()) !== "kept");
  }

  /** Closes ed after it was saved or discarded; otherwise shows why it failed, keeping it open. */
  private settleEdit(ed: CommentEditor, ok: boolean): boolean {
    if (!ok) {
      this.list.refresh();
      return false;
    }
    if (this.edit === ed) this.closeEdit();
    return true;
  }

  /**
   * Saves the summary and the dialog's open editor, so Copy and Send build the result from what is
   * shown. The diff view's editor was left before the dialog opened.
   */
  private async savePending(): Promise<void> {
    if (!(await this.saveEditor())) throw new Error("Save or close the open comment editor first.");
    await this.saveSummary();
    // A delete or viewed mark still queued must reach the server before the result is built.
    await this.state.settled();
  }

  private async saveSummary(): Promise<void> {
    const text = this.summary.value;
    if (text === this.state.summary) return;
    await this.state.setSummary(text);
  }

  private setBusy(busy: boolean): void {
    this.busy = busy;
    this.syncBusy();
  }

  private syncBusy(): void {
    this.sendButton.disabled = this.busy;
    this.sendMore.disabled = this.busy;
    this.copy.disabled = this.busy;
    this.copyAndSend.disabled = this.busy;
  }

  /**
   * The one copy path (Q-43): saves what the dialog shows, then puts the Markdown result for the
   * saved draft on the clipboard. Resolves to the copied text, or to null when the clipboard
   * refused the write and the text is shown selectable instead; rejects when saving or building
   * the result failed, so unsaved text is never shown as the result. The clipboard write starts
   * before any await so the click's user activation survives; the save and fetch happen inside
   * the clipboard item's promise.
   */
  private copyResult(): Promise<string | null> {
    const saved = this.savePending();
    const blob = saved.then(() => this.api.resultMarkdown());
    // The failure is reported through saved or the awaits below, not as an unhandled rejection.
    blob.catch(() => undefined);
    let write: Promise<void>;
    try {
      write = navigator.clipboard.write([new ClipboardItem({ "text/plain": blob })]);
    } catch (err) {
      write = Promise.reject(err);
    }
    return write.then(
      async () => (await blob).text(),
      async () => {
        await saved;
        // Refused writes (Safari without activation) fall back to selectable text.
        this.showFallback(await (await blob).text());
        return null;
      },
    );
  }

  private async copyMarkdown(): Promise<void> {
    try {
      if ((await this.copyResult()) !== null) this.status.textContent = "Copied the review as Markdown.";
    } catch (err) {
      this.copyFailed(err);
    }
  }

  private copyFailed(err: unknown): void {
    this.status.textContent = `Copy failed: ${err instanceof Error ? err.message : String(err)}`;
    this.hooks.failed(err);
  }

  private showFallback(markdown: string): void {
    this.status.replaceChildren(h("div", "", "Copy failed; select and copy the text below."));
    const area = h("textarea", "fin-fallback");
    area.readOnly = true;
    area.value = markdown;
    this.status.append(area);
    area.focus();
    area.select();
  }

  /** Sends the review (Send, and ⌘↵ in the dialog). */
  async finish(): Promise<void> {
    if (this.busy) return;
    this.setBusy(true);
    try {
      await this.send(null);
    } finally {
      this.setBusy(false);
    }
  }

  /** Copies the result and sends the review only once the clipboard holds it (REQ-OUT-008). */
  private async copyThenSend(): Promise<void> {
    // Busy from the click on, so a second click neither copies nor sends again.
    if (this.busy) return;
    this.setBusy(true);
    try {
      let copied: string | null;
      try {
        copied = await this.copyResult();
      } catch (err) {
        this.copyFailed(err);
        return;
      }
      if (copied === null) return;
      await this.send(copied);
    } finally {
      this.setBusy(false);
    }
  }

  /**
   * The one path that finishes the review. copied is the text Copy and send put on the
   * clipboard, or null; the result is compared with it because the draft may change between the
   * copy and the finish.
   */
  private async send(copied: string | null): Promise<void> {
    this.status.textContent = "Sending…";
    try {
      await this.savePending();
      const reply = await this.api.finish();
      this.el.close();
      if (!reply.delivered) {
        this.hooks.ended({ kind: "failed", error: reply.error });
        return;
      }
      const state: Copied = copied === null ? "no" : copied === reply.markdown ? "yes" : "stale";
      this.hooks.ended({ kind: "sent", markdown: reply.markdown, copied: state });
    } catch (err) {
      this.status.textContent = err instanceof Error ? err.message : String(err);
      this.hooks.failed(err);
    }
  }

  /** The comments Delete all removes: those the list shows under the filter. */
  private shownComments(): Comment[] {
    const all = this.state.sortedComments();
    return this.agentOnly.checked ? all.filter((c) => c.author === "agent") : all;
  }

  /** Asks to delete the shown comments, naming how many. */
  private askDelete(): void {
    const n = this.shownComments().length;
    if (n === 0 || this.edit) return;
    const what = this.agentOnly.checked ? (n === 1 ? "agent comment" : "agent comments") : n === 1 ? "comment" : "comments";
    this.ask(`Delete ${n} ${what}? This cannot be undone.`, `Delete ${n} ${what}`, () => this.deleteComments(), true);
  }

  /**
   * Deletes the shown comments as this page shows them (REQ-CMT-013). When another page saved one
   * since, nothing is deleted, the list shows the reloaded comments and the human decides again.
   */
  private async deleteComments(): Promise<void> {
    const shown = this.shownComments();
    if (shown.length === 0 || this.edit) return;
    const deleted = await this.state.deleteComments(shown);
    this.status.textContent = deleted ? "" : "Another page changed a comment meanwhile; nothing was deleted. Check the list and delete again.";
  }

  /** Shows the confirmation bar for a destructive action; action runs only from its button. */
  private ask(text: string, label: string, action: () => Promise<void>, counts = false): void {
    this.confirmText.textContent = text;
    this.confirmYes.textContent = label;
    this.confirmAction = action;
    this.confirmCounts = counts;
    this.confirmBar.hidden = false;
    this.confirmYes.focus();
  }

  private hideConfirm(): void {
    this.confirmBar.hidden = true;
    this.confirmAction = null;
    this.confirmCounts = false;
  }

  private menuItem(label: string, act: () => void): HTMLButtonElement {
    const b = h("button", "fin-menu-item", label);
    b.setAttribute("role", "menuitem");
    b.tabIndex = -1;
    // The action runs in the click itself, so a copy keeps the click's user activation.
    b.addEventListener("click", () => {
      this.closeMenu(true);
      act();
    });
    return b;
  }

  private menuItems(): HTMLButtonElement[] {
    return [...this.menu.querySelectorAll<HTMLButtonElement>(".fin-menu-item")].filter((b) => !b.disabled);
  }

  private openMenu(): void {
    this.menu.hidden = false;
    this.sendMore.setAttribute("aria-expanded", "true");
    this.menuItems()[0]?.focus();
  }

  /** Closes the menu; refocus returns focus to its button, as after a keyboard choice or Esc. */
  private closeMenu(refocus: boolean): void {
    if (this.menu.hidden) return;
    this.menu.hidden = true;
    this.sendMore.setAttribute("aria-expanded", "false");
    if (refocus) this.sendMore.focus();
  }

  private menuKey(e: KeyboardEvent): void {
    const items = this.menuItems();
    const at = items.indexOf(document.activeElement as HTMLButtonElement);
    let next: number;
    if (e.key === "ArrowDown") next = (at + 1) % items.length;
    else if (e.key === "ArrowUp") next = (at - 1 + items.length) % items.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = items.length - 1;
    else if (e.key === "Tab") {
      this.closeMenu(false);
      return;
    } else return;
    e.preventDefault();
    items[next]?.focus();
  }

  private async cancelReview(): Promise<void> {
    try {
      await this.api.cancel();
      this.el.close();
      this.hooks.ended({ kind: "cancelled" });
    } catch (err) {
      this.status.textContent = err instanceof Error ? err.message : String(err);
      this.hooks.failed(err);
    }
  }

  /**
   * Opens an editor on comment c, or on a new review comment when c is null, once the open editor
   * is left: saved, or closed when blank. A failed save keeps that editor open, showing why.
   */
  private async openEdit(c: Comment | null): Promise<void> {
    const left = this.edit;
    if (c && left?.saved && left.id === c.id) return;
    // A save in flight, this editor's or one started by an earlier click, decides first; the
    // click that started it opens the next editor.
    if (left?.isSaving || this.leaving) return;
    if (left) {
      this.leaving = true;
      let ok: boolean;
      try {
        ok = await left.leave();
        if (!ok) this.list.refresh();
      } finally {
        this.leaving = false;
      }
      if (!ok) return;
    }
    // The dialog closed, or another editor opened, while the open one was left.
    if (!this.el.open || (this.edit !== left && this.edit !== null)) return;
    const failed = (err: unknown): void => this.hooks.failed(err);
    const ed = c ? CommentEditor.edit(this.state, failed, c) : CommentEditor.create(this.state, failed, { target: "review" });
    this.edit = ed;
    this.rebuild();
    // A new review comment's editor is the first row, maybe scrolled away. An existing comment's
    // editor replaces the row the human just clicked, so the list stays put: aligning the row
    // would jump whenever its quote fills the view. Typing still scrolls the caret into view.
    if (!c) this.list.scrollToIndex(0, "start");
    requestAnimationFrame(() => ed.textarea.focus({ preventScroll: c !== null }));
  }

  /**
   * Closes the open editor without saving its text (see CommentEditor.discard). Returns false when
   * the editor stays open, showing why.
   */
  private async discardEdit(): Promise<boolean> {
    const ed = this.edit;
    if (!ed) return true;
    return this.settleEdit(ed, await ed.discard());
  }

  private closeEdit(): void {
    if (!this.edit) return;
    this.edit = null;
    this.rebuild();
  }

  private rebuild(): void {
    this.quoteEls.clear();
    const ed = this.edit;
    const rows: Row[] = [];
    // A saved comment's editor takes its comment's row. The open editor shows whatever the
    // filter: it holds the human's text.
    if (ed && !ed.saved) rows.push({ k: "edit", ed, c: null });
    for (const c of this.state.sortedComments()) {
      if (ed?.id === c.id) {
        rows.push({ k: "edit", ed, c });
        continue;
      }
      if (this.agentOnly.checked && c.author !== "agent") continue;
      rows.push({ k: "comment", c });
    }
    if (rows.length === 0) rows.push({ k: "empty" });
    const earlier = this.state.earlier.filter((e) => e.file === null);
    if (earlier.length > 0) rows.push({ k: "earlier-head" }, ...earlier.map((e) => ({ k: "earlier" as const, e })));
    this.rows = rows;
    const shown = this.shownComments().length;
    this.listTitle.textContent = `Comments (${this.state.sortedComments().length})`;
    this.deleteAll.textContent = this.agentOnly.checked ? "Delete agent comments" : "Delete all";
    this.deleteAll.hidden = shown === 0;
    this.deleteAll.disabled = ed !== null;
    this.deleteAll.title = ed ? "Save or close the open editor first" : "";
    // The delete confirmation names a count; any change to the list makes the human confirm again.
    if (this.confirmCounts) this.hideConfirm();
    this.list.setSource({
      count: () => this.rows.length,
      variable: () => true,
      key: (n) => {
        const r = this.rows[n]!;
        if (r.k === "comment") return `c:${r.c.id}:${r.c.author}:${r.c.body.length}`;
        if (r.k === "edit") return `e:${r.c?.id ?? ""}:${r.ed.errorRevision}`;
        if (r.k === "earlier") return `p:${r.e.id}`;
        return r.k;
      },
      // A comment and its editor are one row; a new review comment's editor is its own.
      id: (n) => {
        const r = this.rows[n]!;
        if (r.k === "comment") return `c:${r.c.id}`;
        if (r.k === "edit") return r.c ? `c:${r.c.id}` : `e:${r.ed.id}`;
        if (r.k === "earlier") return `p:${r.e.id}`;
        return r.k;
      },
      render: (n) => this.renderRow(this.rows[n]!),
    });
  }

  private renderRow(r: Row): HTMLElement {
    if (r.k === "empty") return h("div", "fin-empty", this.agentOnly.checked ? "No agent comments." : "No comments.");
    if (r.k === "earlier-head") {
      const head = h("div", "fin-list-head fin-earlier-head");
      head.append(h("span", "fin-title", "Earlier review"), h("span", "fin-earlier-note", "Read-only; not part of this review"));
      return head;
    }
    if (r.k === "earlier") {
      const row = h("div", "fin-comment earlier");
      const t = r.e.target;
      const where = t.target === "review" ? "review" : t.target === "file" ? t.path : `${t.path} · ${t.target === "line" ? `line ${t.line}` : `lines ${t.start}–${t.end}`} (reviewed version)`;
      row.append(earlierCard(this.state, r.e, undefined, (task) => this.run(task), where));
      return row;
    }
    // The list measures rows by their own box, so the gap between cards is the row's padding.
    const row = h("div", `fin-comment author-${r.c?.author ?? "human"}`);
    const el = h("div", "c-card");
    row.append(el);
    const head = h("div", "c-head");
    head.append(h("span", `badge ${r.c?.author ?? "human"}`, r.c?.author === "agent" ? "Agent" : "You"));
    head.append(h("span", "c-target", r.c ? this.where(r.c) : "review"));
    el.append(head);
    if (r.c) this.appendQuote(el, r.c);
    if (r.k === "edit") {
      const save = h("button", "btn primary", "Save");
      save.addEventListener("click", () => this.run(this.saveEditor()));
      const close = h("button", "btn", "Close");
      close.addEventListener("click", () => this.run(this.discardEdit()));
      const bar = h("div", "editor-bar");
      bar.append(save, close);
      el.append(r.ed.textarea, r.ed.error, bar);
      return row;
    }
    const c = r.c;
    const edit = h("button", "link quiet", "Edit");
    edit.addEventListener("click", () => this.run(this.openEdit(c)));
    const del = h("button", "link quiet danger", "Delete");
    del.addEventListener("click", () => this.run(this.state.deleteComment(c)));
    head.append(h("span", "spacer"), edit, del);
    const body = h("div", "c-body editable", c.body);
    body.title = "Click to edit";
    // A click that ends a text selection is the human copying, not asking to edit.
    body.addEventListener("click", () => {
      if (document.getSelection()?.isCollapsed ?? true) this.run(this.openEdit(c));
    });
    el.append(body);
    return row;
  }

  /**
   * Appends the code a line or range comment quotes. The target fixes the code's height, and the
   * note row's when lines are left out, before the quote arrives; only a note on bytes cut from a
   * short quote adds a row on arrival, and the list keeps the visible rows in place when it does.
   */
  private appendQuote(el: HTMLElement, c: Comment): void {
    const t = c.target;
    if (t.target !== "line" && t.target !== "range") return;
    const span = t.target === "line" ? 1 : t.end - t.start + 1;
    const { maxLines } = this.state.quoteLimits;
    const block = h("div", "fin-quote");
    block.style.setProperty("--lines", String(Math.min(span, maxLines)));
    el.append(block);
    if (span > maxLines) el.append(h("div", "fin-quote-note"));
    this.quoteEls.set(c.id, block);
    const entry = this.quotes.get(c.id);
    if (entry && entry.target === targetKey(t)) {
      fillQuote(block, entry.quote);
      return;
    }
    this.quotes.set(c.id, { target: targetKey(t), quote: "loading" });
    this.wanted.add(c.id);
    if (!this.fetchQueued) {
      this.fetchQueued = true;
      // One request for every row mounted in this pass.
      queueMicrotask(() => this.run(this.fetchQuotes()));
    }
  }

  private async fetchQuotes(): Promise<void> {
    this.fetchQueued = false;
    const ids = [...this.wanted];
    this.wanted.clear();
    const { maxIds } = this.state.quoteLimits;
    for (let at = 0; at < ids.length; at += maxIds) {
      const batch = ids.slice(at, at + maxIds);
      let got: Map<string, Quote> | null = null;
      try {
        got = await this.api.quotes(batch);
      } catch (err) {
        this.hooks.failed(err);
      }
      for (const id of batch) {
        const entry = this.quotes.get(id);
        // Closed, deleted or re-targeted meanwhile: the reply is for nothing shown.
        if (!entry || entry.quote !== "loading") continue;
        entry.quote = got?.get(id) ?? "failed";
        const block = this.quoteEls.get(id);
        if (block?.isConnected) fillQuote(block, entry.quote);
      }
    }
  }

  private where(c: Comment): string {
    const t = c.target;
    if (t.target === "review") return "review";
    return t.target === "file" ? t.path : `${t.path} · ${targetLabel(t)}`;
  }

  /** Runs a task started by an event, reporting its failure. */
  private run(task: Promise<unknown>): void {
    task.catch((err: unknown) => this.hooks.failed(err));
  }
}

function targetKey(t: Target): string {
  return JSON.stringify(t);
}

function fillQuote(block: HTMLElement, q: QuoteState): void {
  if (q === "loading") return;
  block.classList.toggle("failed", q === "failed");
  if (q === "failed") {
    block.replaceChildren(h("div", "fin-quote-line", "Could not load the code."));
    return;
  }
  block.replaceChildren(...q.code.map((l) => h("div", "fin-quote-line", l === "" ? " " : l)));
  let note = block.nextElementSibling;
  if (!(note instanceof HTMLElement && note.classList.contains("fin-quote-note"))) {
    if (q.note === "") return;
    note = h("div", "fin-quote-note");
    block.after(note);
  }
  note.textContent = q.note;
}
