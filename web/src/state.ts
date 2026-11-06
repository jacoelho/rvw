// The page's copy of the review. The server is authoritative: State is the one writer of every
// fact here, and applies a change only after the server has saved it.

import { type Api, type ChangedFile, type Comment, type CommentSite, compareOrder, type EarlierComment, type EarlierPlacement, type EarlierStatus, HttpError, type LineCount, type Placement, type QuoteLimits, type RenameDetection, type SearchLimits, type Session, type Side } from "./api";

/**
 * What changed, for listeners to refresh only what shows it:
 * - draft: comments or viewed marks;
 * - counts: line counts, or their completion or error;
 * - generated: the generated-file marks arrived;
 * - head: HEAD moved since the review started;
 * - whitespace: the whitespace-only marks arrived;
 * - hide: the Hide whitespace setting changed.
 */
export type Change = "draft" | "counts" | "generated" | "head" | "whitespace" | "hide";

/** The server calls State makes. */
export type ReviewApi = Pick<Api, "session" | "createComment" | "editComment" | "deleteComment" | "setViewed" | "setSummary" | "status" | "setHideWhitespace" | "copyEarlier">;

/**
 * How saving a comment ended. A refused save (412) reloads the comments from the server first.
 * - saved: the server's reply, which the save stored or found already stored;
 * - changed: the server holds this comment, as reloaded, with another target or text;
 * - deleted: the server holds no comment with this ID.
 */
export type SaveOutcome = { kind: "saved"; comment: Comment } | { kind: "changed"; comment: Comment } | { kind: "deleted" };

export class State {
  readonly files: readonly ChangedFile[];
  readonly sourceLabel: string;
  readonly renameDetection: RenameDetection;
  readonly quoteLimits: QuoteLimits;
  readonly searchLimits: SearchLimits;
  /** The status poll interval in milliseconds, set by the server. */
  readonly statusIntervalMs: number;
  /** When the last delivered review was delivered, or undefined when there was none. */
  readonly lastReviewAt: Date | undefined;
  readonly earlier: readonly EarlierComment[];
  /**
   * Files with a comment or an earlier comment when the page loaded. Round groups leave them out,
   * and fix that at load so a file does not move between groups as the human comments.
   */
  readonly commentedAtStart: ReadonlySet<number>;
  private readonly commentById = new Map<string, Comment>();
  private readonly viewedFiles: Set<number>;
  private readonly countByFile = new Map<number, LineCount>();
  private readonly generatedFiles = new Set<number>();
  private summaryValue: string;
  private headHasMoved = false;
  private countsNext = 0;
  private countsComplete = false;
  private countsFailure = "";
  private generatedKnown = false;
  private generatedFailure = "";
  private readonly whitespaceFiles = new Set<number>();
  private whitespaceKnown = false;
  private whitespaceFailure = "";
  private hideWs: boolean;
  private readonly listeners = new Set<(c: Change) => void>();
  /** The last write in flight; writes run one at a time, so they apply in the order they were made. */
  private writing: Promise<unknown> = Promise.resolve();

  constructor(
    private readonly api: ReviewApi,
    s: Session,
  ) {
    this.files = s.files;
    this.sourceLabel = s.source;
    this.renameDetection = s.renameDetection;
    this.quoteLimits = s.quoteLimits;
    this.searchLimits = s.searchLimits;
    this.statusIntervalMs = s.statusIntervalMs;
    this.viewedFiles = new Set(s.viewed);
    this.summaryValue = s.summary;
    this.hideWs = s.hideWhitespace;
    this.lastReviewAt = s.lastReviewAt;
    this.earlier = s.earlier;
    for (const c of s.comments) this.commentById.set(c.id, c);
    const commented = new Set<number>();
    for (const c of s.comments) if (c.file !== null) commented.add(c.file);
    for (const e of s.earlier) if (e.file !== null) commented.add(e.file);
    this.commentedAtStart = commented;
  }

  get comments(): ReadonlyMap<string, Comment> {
    return this.commentById;
  }
  get viewed(): ReadonlySet<number> {
    return this.viewedFiles;
  }
  get counts(): ReadonlyMap<number, LineCount> {
    return this.countByFile;
  }
  /** Generated Go files; empty until the server's marks arrive. */
  get generated(): ReadonlySet<number> {
    return this.generatedFiles;
  }
  get summary(): string {
    return this.summaryValue;
  }
  get headMoved(): boolean {
    return this.headHasMoved;
  }
  get countsDone(): boolean {
    return this.countsComplete;
  }
  get countsError(): string {
    return this.countsFailure;
  }
  get generatedError(): string {
    return this.generatedFailure;
  }
  /** Files whose changes are only whitespace; empty until the server's marks arrive. */
  get whitespace(): ReadonlySet<number> {
    return this.whitespaceFiles;
  }
  get whitespaceDone(): boolean {
    return this.whitespaceKnown;
  }
  get whitespaceError(): string {
    return this.whitespaceFailure;
  }
  get hideWhitespace(): boolean {
    return this.hideWs;
  }

  /**
   * Whether file i is reviewed and unchanged since (REQ-RND-008). A file reviewed with whitespace
   * hidden may differ from its reviewed version in whitespace, so it counts only while whitespace
   * stays hidden.
   */
  reviewedUnchanged(i: number): boolean {
    const r = this.files[i]?.round;
    return r !== undefined && r.state === "same" && (!r.reviewedWs || this.hideWs);
  }

  /** Whether file i's new side differs from its reviewed version's (REQ-RND-008). */
  changedSince(i: number): boolean {
    const r = this.files[i]?.round;
    return r?.state === "changed" || r?.state === "changed_base";
  }

  /** Whether file i's old side differs from its reviewed version's (REQ-RND-008). */
  baseChanged(i: number): boolean {
    const r = this.files[i]?.round;
    return r?.state === "base" || r?.state === "changed_base";
  }

  /** Whether file i equals its reviewed version but is not reviewed unchanged: whitespace is shown now. */
  reviewedWithWhitespaceHidden(i: number): boolean {
    return this.files[i]?.round?.state === "same" && !this.reviewedUnchanged(i);
  }

  /** IDs of the earlier comments a draft comment was copied from. */
  copiedEarlier(): Set<string> {
    const out = new Set<string>();
    for (const c of this.commentById.values()) if (c.copiedFrom !== undefined) out.add(c.copiedFrom);
    return out;
  }

  earlierFor(i: number): EarlierComment[] {
    return this.earlier.filter((e) => e.file === i);
  }

  onChange(fn: (c: Change) => void): void {
    this.listeners.add(fn);
  }

  /** Creates human comment id at target. */
  createComment(id: string, target: Placement, body: string): Promise<SaveOutcome> {
    return this.write(async () => {
      try {
        return { kind: "saved", comment: this.put(await this.api.createComment(id, target, body)) };
      } catch (err) {
        if (!(err instanceof HttpError && err.status === 412)) throw err;
      }
      return this.refusedOutcome(id);
    });
  }

  /** Edits the body of comment id, which its editor loaded at version. */
  editComment(id: string, version: number, body: string): Promise<SaveOutcome> {
    return this.write(async () => {
      try {
        return { kind: "saved", comment: this.put(await this.api.editComment(id, version, body)) };
      } catch (err) {
        if (!(err instanceof HttpError && err.status === 412)) throw err;
      }
      return this.refusedOutcome(id);
    });
  }

  /**
   * Deletes c as this page shows it. When another page has saved c since, nothing is deleted and
   * the comments reload, so the human sees the newer text and can decide again.
   */
  deleteComment(c: Comment): Promise<void> {
    return this.write(async () => {
      try {
        await this.api.deleteComment(c.id, c.version);
      } catch (err) {
        if (!(err instanceof HttpError && err.status === 412)) throw err;
        await this.reload();
        return;
      }
      this.commentById.delete(c.id);
      this.notify("draft");
    });
  }

  /** Sets the viewed mark of every file in files as one change. */
  setViewed(files: readonly number[], viewed: boolean): Promise<void> {
    return this.write(async () => {
      await this.api.setViewed(files, viewed);
      for (const i of files) {
        if (viewed) this.viewedFiles.add(i);
        else this.viewedFiles.delete(i);
      }
      this.notify("draft");
    });
  }

  setHideWhitespace(hide: boolean): Promise<void> {
    return this.write(async () => {
      await this.api.setHideWhitespace(hide);
      if (this.hideWs === hide) return;
      this.hideWs = hide;
      this.notify("hide");
    });
  }

  /** Copies earlier comment eid into the draft (Still applies); a copy made before is returned as is. */
  copyEarlier(eid: string): Promise<Comment> {
    const id = newCommentId();
    return this.write(async () => this.put(await this.api.copyEarlier(eid, id)));
  }

  setSummary(text: string): Promise<void> {
    return this.write(async () => {
      await this.api.setSummary(text);
      // Only the Finish dialog edits the summary, and it already shows the text; nothing to notify.
      this.summaryValue = text;
    });
  }

  /** Waits for every write made so far; a failed one has already been reported to its caller. */
  async settled(): Promise<void> {
    await this.writing;
  }

  /** Fetches the server's status and applies what is new. */
  async syncStatus(): Promise<void> {
    const s = await this.api.status(this.countsNext, this.whitespaceKnown);
    if (s.headMoved && !this.headHasMoved) {
      this.headHasMoved = true;
      this.notify("head");
    }
    // The marks come with the first reply that has them done; counts never precede them.
    if (!this.generatedKnown && s.generated !== null) {
      this.generatedKnown = true;
      this.generatedFailure = s.generatedError;
      for (const i of s.generated) this.generatedFiles.add(i);
      this.notify("generated");
    }
    if (!this.whitespaceKnown && s.whitespace !== null) {
      this.whitespaceKnown = true;
      this.whitespaceFailure = s.whitespaceError;
      for (const i of s.whitespace) this.whitespaceFiles.add(i);
      this.notify("whitespace");
    }
    if (!this.countsComplete) {
      for (const [i, c] of s.counts) this.countByFile.set(i, c);
      this.countsNext = s.next;
      this.countsComplete = s.countsDone;
      this.countsFailure = s.countsError;
      if (s.counts.size > 0 || s.countsDone) this.notify("counts");
    }
  }

  /** Comments in result order. */
  sortedComments(): Comment[] {
    return [...this.commentById.values()].sort(compareOrder);
  }

  commentsFor(i: number): Comment[] {
    return this.sortedComments().filter((c) => c.file === i);
  }

  /** Comment counts by file index, for files with at least one comment. */
  commentsByFile(): Map<number, number> {
    const out = new Map<number, number>();
    for (const c of this.commentById.values()) {
      if (c.file !== null) out.set(c.file, (out.get(c.file) ?? 0) + 1);
    }
    return out;
  }

  commentCount(i: number): number {
    let n = 0;
    for (const c of this.commentById.values()) if (c.file === i) n++;
    return n;
  }

  /**
   * Runs task after every earlier write. The server may serve two requests in either order, so
   * concurrent writes could otherwise leave it and this copy disagreeing on the last value.
   */
  private write<T>(task: () => Promise<T>): Promise<T> {
    const done = this.writing.then(task);
    this.writing = done.catch(() => undefined);
    return done;
  }

  private put(c: Comment): Comment {
    this.commentById.set(c.id, c);
    this.notify("draft");
    return c;
  }

  /** A write of comment id was refused (412): reloads the comments and says what the server holds. */
  private async refusedOutcome(id: string): Promise<SaveOutcome> {
    const comment = (await this.reload()).get(id);
    return comment ? { kind: "changed", comment } : { kind: "deleted" };
  }

  /** Replaces the comments with the server's. */
  private async reload(): Promise<ReadonlyMap<string, Comment>> {
    const s = await this.api.session();
    this.commentById.clear();
    for (const c of s.comments) this.commentById.set(c.id, c);
    this.notify("draft");
    return this.commentById;
  }

  private notify(c: Change): void {
    for (const fn of this.listeners) fn(c);
  }
}

/** A new human comment's ID; the server keys the comment's PUT on it. */
export function newCommentId(): string {
  return `h-${crypto.randomUUID()}`;
}

export function targetLabel(t: CommentSite): string {
  switch (t.target) {
    case "review":
      return "review";
    case "file":
      return "file";
    case "line":
      return `line ${t.line} (${t.side})`;
    case "range":
      return `lines ${t.start}–${t.end} (${t.side})`;
  }
}

/** A new line or range comment on one side of changed file `file`: one line is a line comment. */
export function lineTarget(file: number, side: Side, start: number, end: number): Placement {
  return start === end ? { target: "line", file, side, line: start } : { target: "range", file, side, start, end };
}

/** The last line a line or range target covers, or 0. */
export function targetEnd(t: CommentSite): number {
  return t.target === "line" ? t.line : t.target === "range" ? t.end : 0;
}

/** A review time as the UI shows it: HH:MM, with the date when it is not today. */
export function clock(d: Date): string {
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (d.toDateString() === new Date().toDateString()) return time;
  return `${d.toLocaleDateString([], { month: "short", day: "numeric" })} ${time}`;
}

/** An earlier comment's status: its file request's placement, else the session's, else unplaced. */
export function earlierStatus(e: EarlierComment, p: EarlierPlacement | undefined): EarlierStatus {
  return p?.status ?? e.status ?? "unplaced";
}

/**
 * Where an earlier comment points. A current comment names its placed lines; any other names its
 * lines in the version it was written on.
 */
export function earlierTargetLabel(e: EarlierComment, p: EarlierPlacement | undefined): string {
  const t = e.target;
  if (t.target === "review" || t.target === "file") return t.target;
  const lines = (a: number, b: number): string => (a === b ? `line ${a}` : `lines ${a}–${b}`);
  if (p?.status === "current") return `${lines(p.start, p.end)} (${p.side})`;
  const start = p?.start ?? (t.target === "line" ? t.line : t.start);
  return `${lines(start, p?.end ?? targetEnd(t))} (reviewed version)`;
}
