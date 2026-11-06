// The comment editor shared by the diff view and the Finish dialog: one comment's text, its save
// to the server, and the rules a save follows. Where the editor is shown is the caller's.

import { type Comment, type CommentSite, HttpError, NoReply, type Placement, ServerGone } from "./api";
import { h } from "./dom";
import { newCommentId, type SaveOutcome, type State } from "./state";

/**
 * How a save ended:
 * - saved: the server has the comment;
 * - discarded: a new comment had no text, so nothing was sent;
 * - kept: the save failed or was refused; the editor shows why and stays open.
 */
export type SaveResult = "saved" | "discarded" | "kept";

/** A request on the wire: create a new comment at its placement, or edit a saved comment's version. */
type Save = { create: Placement } | { edit: number };

/**
 * The stored comment the editor's next save assumes: the version and text it loaded, stored, or
 * was shown after a refusal. The text is the textarea's value, which normalises line endings, so
 * it compares with what the textarea shows.
 */
type Base = { version: number; body: string };

/** A refused save: the stored comment differs, or is gone. */
type Refusal = Exclude<SaveOutcome, { kind: "saved" }>;

/**
 * Whether s is nothing but ASCII whitespace: a new editor holding only this was never typed in, so
 * closing it loses nothing. Whether a body is empty is the server's rule; it refuses more.
 */
export function isEmptyInput(s: string): boolean {
  return /^[\t\n\v\f\r ]*$/.test(s);
}

export class CommentEditor {
  /** The editor's virtual-list row key; a retargeted draft keeps its ID, and its owner forgets the old row's height. */
  get key(): string {
    return `ed-${this.id}`;
  }
  readonly textarea: HTMLTextAreaElement;
  readonly error: HTMLElement;
  private errors = 0;
  /** The save in flight, which a second save request joins instead of repeating. */
  private saving: Promise<SaveResult> | null = null;
  /**
   * The last request whose reply was lost, exactly as sent. Whether it was stored is unknown, so
   * the next save resends it first: the server answers a repeated write with the stored comment.
   */
  private lost: { save: Save; body: string } | null = null;

  /**
   * The stored comment the next save edits, or null while the comment is new and the next save
   * creates it at the placement, the editor's target. The editor alone owns it: it adopts a
   * version only from the reply to its own request or, after a 412, from the reloaded comment it
   * then shows (Q-41).
   */
  private base: Base | null;

  private constructor(
    private readonly state: State,
    private readonly failed: (err: unknown) => void,
    /** Fixed when the editor opens, so every save of the comment is the same PUT. */
    readonly id: string,
    /** Where the comment is: a saved comment's target, or a new comment's placement. */
    readonly target: CommentSite,
    version: number | null,
    body: string,
    placeholder: string,
  ) {
    this.textarea = h("textarea", "editor-text");
    this.textarea.value = body;
    this.textarea.rows = 4;
    this.textarea.placeholder = placeholder;
    this.error = h("div", "editor-error");
    this.base = version === null ? null : { version, body: this.textarea.value };
  }

  /** An editor on an existing comment. */
  static edit(state: State, failed: (err: unknown) => void, c: Comment): CommentEditor {
    return new CommentEditor(state, failed, c.id, c.target, c.version, c.body, "Comment (⌘↵ to save, Esc to close)");
  }

  /**
   * An editor on a new comment. Passing the editor it replaces keeps that one's ID and, unless it
   * is blank, its text: retargeting a draft does not make it a different comment.
   */
  static create(state: State, failed: (err: unknown) => void, target: Placement, replaces: CommentEditor | null = null): CommentEditor {
    const carried = replaces && replaces.base === null && !replaces.saved ? replaces : null;
    const body = carried && !isEmptyInput(carried.textarea.value) ? carried.textarea.value : "";
    const placeholder = target.target === "review" ? "Review comment (⌘↵ to save, Esc to close)" : "Comment (⌘↵ to save, Esc to close)";
    return new CommentEditor(state, failed, carried?.id ?? newCommentId(), target, null, body, placeholder);
  }

  /** Whether the server holds the comment, as State knows it; where an editor's row goes follows this. */
  get saved(): boolean {
    return this.state.comments.has(this.id);
  }

  /**
   * Whether the editor no longer edits a comment a host can show it on: an edit whose comment is
   * gone, or a new comment whose ID State holds at another target. Hosts close a stale editor.
   */
  get stale(): boolean {
    if (this.base !== null) return !this.saved;
    const stored = this.state.comments.get(this.id);
    return stored !== undefined && !placedAt(stored, this.target);
  }

  /** Counts the editor's error messages, so a host can tell a new one from the one it rendered. */
  get errorRevision(): number {
    return this.errors;
  }

  /** An edit whose text equals its base's text, with no request to resend: saving it sends nothing. */
  get unchanged(): boolean {
    return this.base !== null && this.lost === null && this.textarea.value === this.base.body;
  }

  get isSaving(): boolean {
    return this.saving !== null;
  }

  /** A new comment with no text and no create sent, which closing loses nothing of. */
  get isBlank(): boolean {
    return this.base === null && !this.saved && this.lost === null && isEmptyInput(this.textarea.value);
  }

  /**
   * Whether a create was sent whose reply was lost. Its target is then fixed, as a saved comment's
   * is: the server may hold the comment there, and a create elsewhere under its ID is refused.
   */
  get targetFixed(): boolean {
    return this.saved || (this.lost !== null && "create" in this.lost.save);
  }

  /** Shows why the editor's target did not move. */
  refuseRetarget(): void {
    this.setError("This comment may already be saved here; save it, or delete it and start a new one to comment elsewhere.");
  }

  /**
   * Leaves the editor: a blank new comment needs nothing; any other is saved. Returns false when
   * the save failed and the editor shows why.
   */
  async leave(): Promise<boolean> {
    if (this.isBlank) return true;
    return (await this.save()) !== "kept";
  }

  save(): Promise<SaveResult> {
    return this.begin(() => this.persist());
  }

  /**
   * Closes the editor without saving its text. A request whose reply was lost is resent first, so
   * the page learns whether the server holds it: otherwise the result could carry a comment, or an
   * edit, the human was never shown. Returns false when that resend failed and the editor shows why.
   */
  async discard(): Promise<boolean> {
    while (this.saving) await this.saving;
    if (this.lost === null) return true;
    return (await this.begin(() => this.resendLost())) !== "kept";
  }

  /** Starts task as the editor's one save in flight, or joins the save already in flight. */
  private begin(task: () => Promise<SaveResult>): Promise<SaveResult> {
    if (!this.saving) {
      // The text is read-only while it is sent, so a second save can join this one without
      // dropping text typed in the meantime.
      this.textarea.readOnly = true;
      this.saving = task().finally(() => {
        this.saving = null;
        this.textarea.readOnly = false;
      });
    }
    return this.saving;
  }

  /**
   * Resends the lost request. Any reply settles it, even a refusal, which updates the editor as a
   * save's refusal does: the human asked to close the editor, so it closes either way.
   */
  private async resendLost(): Promise<SaveResult> {
    const lost = this.lost;
    if (!lost) return "saved";
    try {
      const outcome = await this.send(lost.save, lost.body);
      if (outcome.kind === "saved") this.base = { version: outcome.comment.version, body: lost.body };
      else this.refused(outcome);
      return "saved";
    } catch (err) {
      return this.failure(err);
    }
  }

  private async persist(): Promise<SaveResult> {
    const body = this.textarea.value;
    if (this.isBlank) return "discarded";
    // Nothing to store; sending would only risk refusing a version another tab has since changed.
    if (this.unchanged) return "saved";
    try {
      const lost = this.lost;
      if (lost) {
        const outcome = await this.send(lost.save, lost.body);
        if (outcome.kind !== "saved") return this.refused(outcome);
        this.base = { version: outcome.comment.version, body: lost.body };
        if (body === lost.body) return "saved";
      }
      const outcome = await this.send(this.request(), body);
      if (outcome.kind !== "saved") return this.refused(outcome);
      // A later save from this editor edits what this one saved. The text is read-only while it
      // is sent, so body is still the textarea's value.
      this.base = { version: outcome.comment.version, body };
      return "saved";
    } catch (err) {
      return this.failure(err);
    }
  }

  /** The next save's request: an edit of the base, or, for a new comment, a create at its placement. */
  private request(): Save {
    if (this.base) return { edit: this.base.version };
    // Only create() makes an editor without a base, and it targets a placement.
    if (!isPlacement(this.target)) throw new Error("a new comment's editor has no placement");
    return { create: this.target };
  }

  /** Shows why a request failed; the editor stays open. */
  private failure(err: unknown): SaveResult {
    if (err instanceof HttpError && err.status === 422) {
      // The server's reason: the editor stays open on the text it refused.
      this.setError(err.message);
      return "kept";
    }
    if (err instanceof NoReply) {
      // Not ServerGone: rvw may still run, and the status poll ends the page if it does not.
      this.setError("rvw did not answer; save again to retry.");
      return "kept";
    }
    this.setError(err instanceof Error ? err.message : String(err));
    this.failed(err);
    return "kept";
  }

  /** Sends one save, remembering it when its reply is lost; any reply settles it. */
  private async send(save: Save, body: string): Promise<SaveOutcome> {
    try {
      const outcome = "edit" in save ? await this.state.editComment(this.id, save.edit, body) : await this.state.createComment(this.id, save.create, body);
      this.lost = null;
      return outcome;
    } catch (err) {
      const replied = err instanceof HttpError || (err instanceof ServerGone && !(err instanceof NoReply));
      this.lost = replied ? null : { save, body };
      throw err;
    }
  }

  /**
   * Handles a 412, after State reloaded the comments (Q-41): the editor shows the stored comment it
   * was writing, or, when that is gone or another comment holds its ID, says so.
   */
  private refused(outcome: Refusal): SaveResult {
    if (outcome.kind === "changed") {
      if (this.base !== null || placedAt(outcome.comment, this.target)) return this.rebase(outcome.comment);
      // Another writer reused this page-generated ID at another target. The editor is stale, so
      // the reload's refresh has already closed it; the toast reports why.
      const msg = "Another comment already uses this comment's ID; your text was not saved.";
      this.setError(msg);
      this.failed(new Error(msg));
      return "kept";
    }
    if (this.base !== null) {
      const msg = "This comment was deleted.";
      this.setError(msg);
      // The reload that found it gone closes an editor on it, so the reason is reported too.
      this.failed(new Error(msg));
      return "kept";
    }
    // Another writer created this ID and deleted it again; the next save creates the comment.
    this.setError("Another writer saved and then deleted this comment; save again to create it.");
    return "kept";
  }

  /**
   * Shows stored, the comment this editor was writing as the server now holds it, and edits that
   * from now on. The human's refused text stays in the error until the editor closes.
   */
  private rebase(stored: Comment): SaveResult {
    const mine = this.textarea.value;
    this.textarea.value = stored.body;
    this.base = { version: stored.version, body: this.textarea.value };
    const lostText = isEmptyInput(mine) || mine === this.textarea.value ? "" : ` Your text was not saved:\n${mine}`;
    this.setError(`This comment was saved differently elsewhere; the editor now shows the saved text.${lostText}`);
    return "kept";
  }

  private setError(text: string): void {
    this.error.textContent = text;
    this.errors++;
  }
}

/** Whether at is a new comment's placement rather than a saved comment's target. */
function isPlacement(at: CommentSite): at is Placement {
  return !("path" in at);
}

/** Whether the stored comment c is at at: the same kind, file and lines. */
function placedAt(c: Comment, at: CommentSite): boolean {
  const t = c.target;
  if (t.target !== at.target) return false;
  if (isPlacement(at)) {
    if (c.file !== ("file" in at ? at.file : null)) return false;
  } else if (("path" in t ? t.path : null) !== at.path) {
    return false;
  }
  if (t.target === "line" && at.target === "line") return t.side === at.side && t.line === at.line;
  if (t.target === "range" && at.target === "range") return t.side === at.side && t.start === at.start && t.end === at.end;
  return true;
}
