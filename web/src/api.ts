// The rvw JSON API (internal/server): the UI's types for its replies, and the one client that
// calls it. Every call carries the session token as a bearer token.

export type Side = "old" | "new";
export type Author = "human" | "agent";

/** A saved comment's target. The path is the server's choice for the target's side, shown and never sent. */
export type Target =
  | { target: "review" }
  | { target: "file"; path: string }
  | { target: "line"; path: string; side: Side; line: number }
  | { target: "range"; path: string; side: Side; start: number; end: number };

/** Where a new comment goes: a changed file by index, never a path. */
export type Placement =
  | { target: "review" }
  | { target: "file"; file: number }
  | { target: "line"; file: number; side: Side; line: number }
  | { target: "range"; file: number; side: Side; start: number; end: number };

/** Where a comment is: a saved comment's target, or a new comment's placement. */
export type CommentSite = Target | Placement;

export interface Comment {
  readonly id: string;
  /** The comment's entity tag: an edit names it, so it never overwrites text it did not load. */
  readonly version: number;
  readonly author: Author;
  readonly target: Target;
  readonly body: string;
  /** The changed file the comment targets, or null for a review comment. */
  readonly file: number | null;
  /** The server's result-order key; compare with compareOrder and never interpret it. */
  readonly order: readonly number[];
  /** The earlier comment this one was copied from by Still applies (REQ-CMT-011). */
  readonly copiedFrom: string | undefined;
}

/** Orders comments as the result does, by the server's keys. */
export function compareOrder(a: Comment, b: Comment): number {
  for (let k = 0; k < Math.min(a.order.length, b.order.length); k++) {
    const d = a.order[k]! - b.order[k]!;
    if (d !== 0) return d;
  }
  return a.order.length - b.order.length;
}

/** Git's change letter; the UI shows it as is. */
export type Status = "A" | "D" | "M" | "R" | "T";

/** One changed file, as the UI uses it. */
export interface ChangedFile {
  readonly status: Status;
  /** The path that names the file in the tree and in file comments: the new path, else the old. */
  readonly path: string;
  readonly oldPath: string | undefined;
  readonly newPath: string | undefined;
  /** The old path when the file was renamed, else undefined. */
  readonly renamedFrom: string | undefined;
  readonly oldMode: string | undefined;
  readonly newMode: string | undefined;
  readonly commentable: boolean;
  /** The outermost vendored directory holding the file. */
  readonly vendoredDir: string | undefined;
  /** How the file relates to its last reviewed version; undefined when it was never reviewed. */
  readonly round: FileRound | undefined;
}

/**
 * A file's round state at session start (REQ-RND-008):
 * - changed: its new side differs from the reviewed version's;
 * - base: only its old side differs;
 * - changed_base: both differ;
 * - same: it equals the reviewed version.
 */
export type RoundState = "changed" | "base" | "changed_base" | "same";

export interface FileRound {
  readonly state: RoundState;
  /** The reviewed version was reviewed with whitespace hidden. */
  readonly reviewedWs: boolean;
  readonly reviewedAt: Date;
}

/** Where an earlier comment stands in this snapshot; null until its file is requested. */
export type EarlierStatus = "current" | "outdated" | "unplaced";

/** A comment from the last delivered review: read-only context, never part of this review's result. */
export interface EarlierComment {
  readonly id: string;
  readonly author: Author;
  readonly body: string;
  readonly at: Date;
  /** The target as it was delivered; paths and lines are the reviewed snapshot's. */
  readonly target: Target;
  /** The changed file with the comment's file key, or null. */
  readonly file: number | null;
  /** Known without Git for review and file comments; null for line and range comments of a changed file. */
  readonly status: EarlierStatus | null;
}

/**
 * An earlier line or range comment as the file request places it. Current: start..end are the
 * placed lines on side. Outdated: start..end are the original lines, quoted from the comment's blob.
 */
export interface EarlierPlacement {
  readonly id: string;
  readonly status: EarlierStatus;
  readonly side: Side;
  readonly start: number;
  readonly end: number;
  readonly quote: readonly string[];
  readonly quoteNote: string;
}

/** How far rename detection ran for the changed-file list. */
export type RenameDetection = "full" | "missing_blobs" | "limit_exceeded";

export interface Session {
  readonly source: string;
  readonly renameDetection: RenameDetection;
  readonly files: readonly ChangedFile[];
  readonly comments: readonly Comment[];
  readonly summary: string;
  readonly viewed: readonly number[];
  readonly quoteLimits: QuoteLimits;
  readonly searchLimits: SearchLimits;
  /** How often to poll GET /api/status, in milliseconds; the server derives it from its close grace (Q-42). */
  readonly statusIntervalMs: number;
  /** The draft's Hide whitespace setting (REQ-DIFF-012). */
  readonly hideWhitespace: boolean;
  /** The newest delivery the review ledger records, or undefined when it records none. */
  readonly lastReviewAt: Date | undefined;
  readonly earlier: readonly EarlierComment[];
}

/** How much of a target a quote shows, and how many IDs one quotes request takes. */
export interface QuoteLimits {
  readonly maxLines: number;
  readonly maxIds: number;
}

/** The length rules of search text (REQ-NAV-007); the server owns them and refuses other text. */
export interface SearchLimits {
  readonly minChars: number;
  readonly maxBytes: number;
}

export interface LineCount {
  readonly added: number;
  readonly deleted: number;
  readonly binary: boolean;
}

/** One reply of the status poll. */
export interface StatusReply {
  readonly headMoved: boolean;
  /** Line counts by file index, published since the requested cursor. */
  readonly counts: ReadonlyMap<number, LineCount>;
  /** The cursor for the next poll. */
  readonly next: number;
  readonly countsDone: boolean;
  readonly countsError: string;
  /** Generated Go files' indexes once they are known, else null; every count is published after them. */
  readonly generated: readonly number[] | null;
  readonly generatedError: string;
  /** Whitespace-only files once they are known, else null; absent from replies to a poll that already has them. */
  readonly whitespace: readonly number[] | null;
  readonly whitespaceError: string;
}

/**
 * One matched line of a content search. before, match and after are the line's text around the
 * match; cutStart and cutEnd report that the line goes on beyond them.
 */
export interface SearchMatch {
  /** The changed file's index. */
  readonly file: number;
  readonly side: Side;
  readonly line: number;
  readonly before: string;
  readonly match: string;
  readonly after: string;
  readonly cutStart: boolean;
  readonly cutEnd: boolean;
}

/** A content search's matches, by file index then line; truncated when the search stopped early. */
export interface SearchReply {
  readonly matches: readonly SearchMatch[];
  readonly truncated: boolean;
}

export type FinishReply = { readonly delivered: true; readonly markdown: string } | { readonly delivered: false; readonly error: string };

// Diff payloads keep their compact wire shape: they are the bulk of the traffic and FileView lays
// them out directly.

export interface Span {
  c?: string;
  t: string;
}

/** A changed span [start, end) in UTF-16 code units. */
export type Range = [number, number];

/** One shown line; old or new is absent when the line has no number on that side. */
export interface Line {
  old?: number;
  new?: number;
  t: string;
  n?: boolean;
  s?: Span[];
  w?: Range[];
}

/** Context lines, or a change whose del[k] pairs with add[k]. */
export interface Block {
  ctx?: Line[];
  del?: Line[];
  add?: Line[];
}

/** Unchanged new-side lines from..to, which are old-side lines old_from onwards. */
export interface Gap {
  from: number;
  to: number;
  old_from: number;
}

export interface Hunk {
  header: string;
  gap_before?: Gap;
  blocks: Block[];
}

export interface SideInfo {
  present: boolean;
  lines: number;
  size: number;
}

export type FileKind = "text" | "binary" | "submodule" | "mode" | "unavailable";

/** A file with a line diff. A text file without hunks (a pure rename) has one gap: gap_after. */
export interface TextDiff {
  readonly kind: "text";
  readonly large: boolean;
  readonly highlighted: boolean;
  readonly old: SideInfo;
  readonly new: SideInfo;
  readonly hunks: readonly Hunk[];
  readonly gap_after?: Gap;
}

/** A file without a line diff. */
export interface OtherDiff {
  readonly kind: Exclude<FileKind, "text">;
  readonly old: SideInfo;
  readonly new: SideInfo;
}

export type FileDiff = TextDiff | OtherDiff;

/** Which changes a file is shown as: since, the interdiff from the reviewed version (REQ-RND-010); all, the snapshot's. */
export type Scope = "since" | "all";

/** What a file request served: the diff, and the round facts that go with it. */
export interface FileReply {
  readonly diff: FileDiff;
  readonly scope: Scope;
  /** Why an interdiff was asked for and not served: "missing" (REQ-RND-012) or another server reason. */
  readonly sinceUnavailable: string | undefined;
  /** Per hunk, whether it changes only whitespace; undefined unless asked for. */
  readonly wsOnly: readonly boolean[] | undefined;
  /** New-side line ranges [from, to] the interdiff adds, for All changes (REQ-RND-013). */
  readonly sinceAdded: readonly (readonly [number, number])[];
  readonly earlier: readonly EarlierPlacement[];
}

/** Which diff a file request asks for. */
export interface FileQuery {
  readonly since: boolean;
  readonly ws: boolean;
}

/** Unchanged lines, each numbered on both sides. */
export interface ContextLines {
  lines: Line[];
  highlighted: boolean;
}

/** The code the result quotes for a line or range comment. */
export interface Quote {
  readonly code: readonly string[];
  /** The result's note on what the quote leaves out; "" when it leaves out nothing. */
  readonly note: string;
}

export class HttpError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

/** The server has stopped or the session ended: no further request can succeed. */
export class ServerGone extends Error {
  constructor(message = "rvw is no longer running") {
    super(message);
  }
}

/**
 * No reply arrived: rvw exited, or the connection failed. Whether the request took effect is
 * unknown, so a caller that can resend it unchanged may; to every other caller it is ServerGone.
 */
export class NoReply extends ServerGone {}

export function isGone(err: unknown): err is ServerGone {
  return err instanceof ServerGone;
}

/**
 * The one client of the rvw server. Every reply is checked and converted here; the diff lines
 * (hunk blocks, context lines) are the exception, cast unchecked because they come from the binary that embeds
 * this page and checking them would cost a pass over every shown line.
 */
export class Api {
  constructor(private readonly token: string) {}

  async session(): Promise<Session> {
    return toSession(await this.json("GET", "/api/session"));
  }

  async file(i: number, q: FileQuery, signal?: AbortSignal): Promise<FileReply> {
    const query = `i=${i}${q.since ? "&scope=since" : ""}${q.ws ? "&ws=1" : ""}`;
    return toFileReply(await this.json("GET", `/api/file?${query}`, undefined, signal));
  }

  /** Unchanged lines from..to of a gap; since asks for a gap of the interdiff. */
  async context(i: number, from: number, to: number, since: boolean): Promise<ContextLines> {
    return toContextLines(await this.json("GET", `/api/file/context?i=${i}&from=${from}&to=${to}${since ? "&scope=since" : ""}`));
  }

  /**
   * Creates comment id at target. One stored with this target and body is returned unchanged, so a
   * create whose reply was lost can be resent; any other comment with this id fails with 412.
   */
  async createComment(id: string, target: Placement, body: string): Promise<Comment> {
    const res = await this.send("PUT", `/api/comments/${encodeURIComponent(id)}`, { target, body }, undefined, { "If-None-Match": "*" });
    return toComment(await res.json());
  }

  /**
   * Edits the body of comment id at version. A stored body equal to body is returned unchanged at
   * any version; a comment deleted, or saved with other text since version, fails with 412.
   */
  async editComment(id: string, version: number, body: string): Promise<Comment> {
    const res = await this.send("PUT", `/api/comments/${encodeURIComponent(id)}`, { body }, undefined, { "If-Match": `"${version}"` });
    return toComment(await res.json());
  }

  /** Deletes comment id at version. A comment saved since version fails with 412; an absent one succeeds. */
  async deleteComment(id: string, version: number): Promise<void> {
    await this.send("DELETE", `/api/comments/${encodeURIComponent(id)}`, undefined, undefined, { "If-Match": `"${version}"` });
  }

  /** Sets the viewed mark of every file in files, in one draft change (REQ-NAV-010). */
  async setViewed(files: readonly number[], viewed: boolean): Promise<void> {
    await this.send("PUT", "/api/viewed", { files, viewed });
  }

  async setHideWhitespace(hide: boolean): Promise<void> {
    await this.send("PUT", "/api/hide-whitespace", { hide });
  }

  /**
   * Copies earlier comment eid into the draft as comment id (REQ-CMT-011). An earlier comment
   * already copied returns its existing copy, so a lost reply can be resent.
   */
  async copyEarlier(eid: string, id: string): Promise<Comment> {
    return toComment(await this.json("POST", `/api/earlier/${encodeURIComponent(eid)}/copy`, { id }));
  }

  async setSummary(summary: string): Promise<void> {
    await this.send("PUT", "/api/summary", { summary });
  }

  /** The quotes of the line and range comments among ids, at most QuoteLimits.maxIds of them. */
  async quotes(ids: readonly string[]): Promise<Map<string, Quote>> {
    const query = ids.map((id) => `id=${encodeURIComponent(id)}`).join("&");
    return toQuotes(await this.json("GET", `/api/quotes?${query}`));
  }

  async resultMarkdown(): Promise<Blob> {
    const res = await this.send("GET", "/api/result.md");
    return new Blob([await res.text()], { type: "text/plain" });
  }

  async finish(): Promise<FinishReply> {
    return toFinishReply(await this.json("POST", "/api/finish"));
  }

  async cancel(): Promise<void> {
    await this.send("POST", "/api/cancel");
  }

  /**
   * Searches the changed files' shown sides for text (REQ-NAV-007). files are the session's changed
   * files: every match must name one of them and a side it has.
   */
  async search(text: string, files: readonly ChangedFile[], signal: AbortSignal): Promise<SearchReply> {
    return toSearchReply(await this.json("GET", `/api/search?q=${encodeURIComponent(text)}`, undefined, signal), files);
  }

  /** The status since the counts cursor; wsKnown leaves out the whitespace-only marks already received. */
  async status(since: number, wsKnown: boolean): Promise<StatusReply> {
    return toStatus(await this.json("GET", `/api/status?since=${since}${wsKnown ? "&ws_known=1" : ""}`));
  }

  /**
   * Tells rvw this page is going away (Q-42). Fire-and-forget: it is sent from pagehide, where the
   * page may be gone before any reply, so keepalive lets the request outlive the page and nothing
   * waits on or reports its result. A lost notice leaves the session running, as before Q-42.
   */
  pageClosed(): void {
    void fetch("/api/page-closed", {
      method: "POST",
      headers: { Authorization: `Bearer ${this.token}` },
      cache: "no-store",
      keepalive: true,
    }).catch(() => undefined);
  }

  private async json(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<unknown> {
    const res = await this.send(method, path, body, signal);
    return res.json();
  }

  private async send(method: string, path: string, body?: unknown, signal?: AbortSignal, extra?: Record<string, string>): Promise<Response> {
    const headers: Record<string, string> = { ...extra, Authorization: `Bearer ${this.token}` };
    const init: RequestInit = { method, headers, cache: "no-store" };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    if (signal) init.signal = signal;
    let res: Response;
    try {
      res = await fetch(path, init);
    } catch (err) {
      if (signal?.aborted) throw err;
      // No reply at all: the session ended in another tab, rvw exited, or the connection failed.
      throw new NoReply();
    }
    if (!res.ok) {
      const text = (await res.text()).trim();
      if (res.status === 410) throw new ServerGone(text || undefined);
      throw new HttpError(res.status, text || `${res.status} ${res.statusText}`);
    }
    return res;
  }
}

// Reply decoders. A reply that does not match its shape is a server bug, reported as such.

type Obj = Record<string, unknown>;

export class BadReply extends Error {
  constructor(what: string) {
    super(`unexpected server reply: ${what}`);
  }
}

function obj(v: unknown, what: string): Obj {
  if (typeof v !== "object" || v === null || Array.isArray(v)) throw new BadReply(what);
  return v as Obj;
}

function arr(v: unknown, what: string): unknown[] {
  if (!Array.isArray(v)) throw new BadReply(what);
  return v;
}

function str(v: unknown, what: string): string {
  if (typeof v !== "string") throw new BadReply(what);
  return v;
}

function optStr(v: unknown, what: string): string | undefined {
  return v === undefined ? undefined : str(v, what);
}

function num(v: unknown, what: string): number {
  if (typeof v !== "number" || !Number.isInteger(v)) throw new BadReply(what);
  return v;
}

function bool(v: unknown, what: string): boolean {
  if (typeof v !== "boolean") throw new BadReply(what);
  return v;
}

function oneOf<T extends string>(v: unknown, values: readonly T[], what: string): T {
  const found = values.find((x) => x === v);
  if (found === undefined) throw new BadReply(what);
  return found;
}

const SIDES = ["old", "new"] as const;

function toTarget(v: unknown): Target {
  const o = obj(v, "comment target");
  switch (o["target"]) {
    case "review":
      return { target: "review" };
    case "file":
      return { target: "file", path: str(o["path"], "target path") };
    case "line":
      return { target: "line", path: str(o["path"], "target path"), side: oneOf(o["side"], SIDES, "target side"), line: num(o["line"], "target line") };
    case "range":
      return {
        target: "range",
        path: str(o["path"], "target path"),
        side: oneOf(o["side"], SIDES, "target side"),
        start: num(o["start"], "target start"),
        end: num(o["end"], "target end"),
      };
    default:
      throw new BadReply("comment target");
  }
}

function toComment(v: unknown): Comment {
  const o = obj(v, "comment");
  return {
    id: str(o["id"], "comment id"),
    version: num(o["version"], "comment version"),
    author: oneOf(o["author"], AUTHORS, "comment author"),
    target: toTarget(o["target"]),
    body: str(o["body"], "comment body"),
    file: o["file"] === null ? null : num(o["file"], "comment file"),
    order: arr(o["order"], "comment order").map((n) => num(n, "comment order")),
    copiedFrom: optStr(o["copied_from"], "copied_from"),
  };
}

const AUTHORS = ["human", "agent"] as const;
const ROUND_STATES = ["changed", "base", "changed_base", "same"] as const;
const EARLIER_STATUSES = ["current", "outdated", "unplaced"] as const;

function date(v: unknown, what: string): Date {
  const d = new Date(str(v, what));
  if (Number.isNaN(d.getTime())) throw new BadReply(what);
  return d;
}

function toRound(v: unknown): FileRound | undefined {
  if (v === undefined) return undefined;
  const o = obj(v, "file round");
  return {
    state: oneOf(o["state"], ROUND_STATES, "round state"),
    reviewedWs: bool(o["reviewed_ws"], "reviewed_ws"),
    reviewedAt: date(o["reviewed_at"], "reviewed_at"),
  };
}

function toEarlier(v: unknown, files: number): EarlierComment {
  const o = obj(v, "earlier comment");
  const file = o["file"] === null ? null : num(o["file"], "earlier file");
  if (file !== null && (file < 0 || file >= files)) throw new BadReply(`earlier file ${file}`);
  return {
    id: str(o["id"], "earlier id"),
    author: oneOf(o["author"], AUTHORS, "earlier author"),
    body: str(o["body"], "earlier body"),
    at: date(o["at"], "earlier at"),
    target: toTarget(o["target"]),
    file,
    status: o["status"] === null || o["status"] === undefined ? null : oneOf(o["status"], EARLIER_STATUSES, "earlier status"),
  };
}

function toPlacement(v: unknown): EarlierPlacement {
  const o = obj(v, "earlier placement");
  return {
    id: str(o["id"], "placement id"),
    status: oneOf(o["status"], EARLIER_STATUSES, "placement status"),
    side: oneOf(o["side"], SIDES, "placement side"),
    start: num(o["start"], "placement start"),
    end: num(o["end"], "placement end"),
    quote: o["quote"] === undefined ? [] : arr(o["quote"], "placement quote").map((l) => str(l, "placement quote line")),
    quoteNote: optStr(o["quote_note"], "quote_note") ?? "",
  };
}

function toFile(v: unknown): ChangedFile {
  const o = obj(v, "file");
  const oldPath = optStr(o["old_path"], "old_path");
  const newPath = optStr(o["new_path"], "new_path");
  const path = newPath ?? oldPath;
  if (path === undefined) throw new BadReply("file without a path");
  return {
    status: oneOf(o["status"], ["A", "D", "M", "R", "T"] as const, "file status"),
    path,
    oldPath,
    newPath,
    renamedFrom: oldPath !== undefined && newPath !== undefined && oldPath !== newPath ? oldPath : undefined,
    oldMode: optStr(o["old_mode"], "old_mode"),
    newMode: optStr(o["new_mode"], "new_mode"),
    commentable: bool(o["commentable"], "commentable"),
    vendoredDir: optStr(o["vendored_dir"], "vendored_dir"),
    round: toRound(o["round"]),
  };
}

function toSession(v: unknown): Session {
  const o = obj(v, "session");
  const files = arr(o["files"], "files").map(toFile);
  return {
    source: str(o["source"], "source"),
    renameDetection: oneOf(o["rename_detection"], ["full", "missing_blobs", "limit_exceeded"] as const, "rename_detection"),
    files,
    comments: arr(o["comments"], "comments").map(toComment),
    summary: str(o["summary"], "summary"),
    viewed: arr(o["viewed"], "viewed").map((i) => num(i, "viewed index")),
    quoteLimits: toQuoteLimits(o["quotes"]),
    searchLimits: toSearchLimits(o["search"]),
    statusIntervalMs: toStatusInterval(o["status_interval_ms"]),
    hideWhitespace: bool(o["hide_whitespace"], "hide_whitespace"),
    lastReviewAt: o["last_review_at"] === undefined ? undefined : date(o["last_review_at"], "last_review_at"),
    earlier: o["earlier"] === undefined ? [] : arr(o["earlier"], "earlier").map((e) => toEarlier(e, files.length)),
  };
}

function toStatusInterval(v: unknown): number {
  const ms = num(v, "status_interval_ms");
  if (ms < 1) throw new BadReply(`status_interval_ms ${ms}`);
  return ms;
}

function toQuoteLimits(v: unknown): QuoteLimits {
  const o = obj(v, "quote limits");
  const maxLines = num(o["max_lines"], "max_lines");
  const maxIds = num(o["max_ids"], "max_ids");
  if (!Number.isInteger(maxLines) || maxLines < 1 || !Number.isInteger(maxIds) || maxIds < 1) {
    throw new BadReply(`quote limits ${maxLines}, ${maxIds}`);
  }
  return { maxLines, maxIds };
}

function toSearchLimits(v: unknown): SearchLimits {
  const o = obj(v, "search limits");
  const minChars = num(o["min_chars"], "min_chars");
  const maxBytes = num(o["max_bytes"], "max_bytes");
  if (!Number.isInteger(minChars) || minChars < 1 || !Number.isInteger(maxBytes) || maxBytes < minChars) {
    throw new BadReply(`search limits ${minChars}, ${maxBytes}`);
  }
  return { minChars, maxBytes };
}

function toStatus(v: unknown): StatusReply {
  const o = obj(v, "status");
  const counts = new Map<number, LineCount>();
  for (const c of arr(o["counts"], "counts")) {
    const co = obj(c, "line count");
    counts.set(num(co["i"], "line count index"), {
      added: num(co["added"], "added"),
      deleted: num(co["deleted"], "deleted"),
      binary: co["binary"] === undefined ? false : bool(co["binary"], "binary"),
    });
  }
  const generatedDone = bool(o["generated_done"], "generated_done");
  const generated = o["generated"] === undefined ? [] : arr(o["generated"], "generated").map((i) => num(i, "generated index"));
  // Absent when the poll said it already has the marks, or the server has not finished them.
  const whitespaceDone = o["whitespace_done"] === undefined ? false : bool(o["whitespace_done"], "whitespace_done");
  const whitespace = o["whitespace"] === undefined ? [] : arr(o["whitespace"], "whitespace").map((i) => num(i, "whitespace index"));
  return {
    headMoved: bool(o["head_moved"], "head_moved"),
    counts,
    next: num(o["next"], "next"),
    countsDone: bool(o["counts_done"], "counts_done"),
    countsError: optStr(o["counts_error"], "counts_error") ?? "",
    generated: generatedDone ? generated : null,
    generatedError: optStr(o["generated_error"], "generated_error") ?? "",
    whitespace: whitespaceDone ? whitespace : null,
    whitespaceError: optStr(o["whitespace_error"], "whitespace_error") ?? "",
  };
}

const FILE_KINDS = ["text", "binary", "submodule", "mode", "unavailable"] as const;

function toSide(v: unknown, what: string): SideInfo {
  const o = obj(v, what);
  return { present: bool(o["present"], `${what} present`), lines: num(o["lines"], `${what} lines`), size: num(o["size"], `${what} size`) };
}

function toGap(v: unknown): Gap | undefined {
  if (v === undefined) return undefined;
  const o = obj(v, "gap");
  return { from: num(o["from"], "gap from"), to: num(o["to"], "gap to"), old_from: num(o["old_from"], "gap old_from") };
}

/** Checks the reply's shape; the lines themselves are cast, as Api's comment explains. */
function toContextLines(v: unknown): ContextLines {
  const o = obj(v, "context lines");
  return { lines: arr(o["lines"], "context lines") as Line[], highlighted: bool(o["highlighted"], "highlighted") };
}

/** Checks the diff's shape down to its hunks; the hunks' lines are not walked (see Api). */
function toFileDiff(v: unknown): FileDiff {
  const o = obj(v, "file diff");
  const kind = oneOf(o["kind"], FILE_KINDS, "file kind");
  const old = toSide(o["old"], "old side");
  const neu = toSide(o["new"], "new side");
  if (kind !== "text") return { kind, old, new: neu };
  const hunks = arr(o["hunks"], "hunks").map((h): Hunk => {
    const ho = obj(h, "hunk");
    const gap = toGap(ho["gap_before"]);
    return {
      header: str(ho["header"], "hunk header"),
      blocks: arr(ho["blocks"], "hunk blocks") as Block[],
      ...(gap ? { gap_before: gap } : {}),
    };
  });
  const gapAfter = toGap(o["gap_after"]);
  return {
    kind,
    large: bool(o["large"], "large"),
    highlighted: bool(o["highlighted"], "highlighted"),
    old,
    new: neu,
    hunks,
    ...(gapAfter ? { gap_after: gapAfter } : {}),
  };
}

function toRanges(v: unknown): (readonly [number, number])[] {
  if (v === undefined) return [];
  return arr(v, "since_added").map((r) => {
    const [a, b, ...rest] = arr(r, "since_added range");
    const from = num(a, "since_added from");
    const to = num(b, "since_added to");
    if (rest.length > 0 || from < 1 || to < from) throw new BadReply("since_added range");
    return [from, to] as const;
  });
}

function toFileReply(v: unknown): FileReply {
  const o = obj(v, "file reply");
  const diff = toFileDiff(o);
  const wsOnly = o["ws_only"] === undefined ? undefined : arr(o["ws_only"], "ws_only").map((b) => bool(b, "ws_only"));
  if (wsOnly && (diff.kind !== "text" || wsOnly.length !== diff.hunks.length)) throw new BadReply("ws_only does not match the hunks");
  return {
    diff,
    scope: o["scope"] === undefined ? "all" : oneOf(o["scope"], ["all", "since"] as const, "scope"),
    sinceUnavailable: optStr(o["since_unavailable"], "since_unavailable"),
    wsOnly,
    sinceAdded: toRanges(o["since_added"]),
    earlier: o["earlier"] === undefined ? [] : arr(o["earlier"], "earlier placements").map(toPlacement),
  };
}

function toQuotes(v: unknown): Map<string, Quote> {
  const out = new Map<string, Quote>();
  for (const [id, q] of Object.entries(obj(v, "quotes"))) {
    const o = obj(q, "quote");
    out.set(id, {
      code: arr(o["code"], "quote code").map((l) => str(l, "quote line")),
      note: optStr(o["note"], "quote note") ?? "",
    });
  }
  return out;
}

function toFinishReply(v: unknown): FinishReply {
  const o = obj(v, "finish reply");
  if (bool(o["delivered"], "delivered")) return { delivered: true, markdown: optStr(o["markdown"], "markdown") ?? "" };
  return { delivered: false, error: optStr(o["error"], "error") ?? "unknown error" };
}

function toSearchReply(v: unknown, files: readonly ChangedFile[]): SearchReply {
  const o = obj(v, "search reply");
  const matches = arr(o["matches"], "search matches").map((m): SearchMatch => {
    const mo = obj(m, "search match");
    const file = num(mo["file"], "match file");
    const f = files[file];
    if (!f) throw new BadReply(`match file ${file}`);
    const side = oneOf(mo["side"], SIDES, "match side");
    if ((side === "new" ? f.newPath : f.oldPath) === undefined) throw new BadReply(`match side ${side} of file ${file}`);
    const line = num(mo["line"], "match line");
    if (line < 1) throw new BadReply(`match line ${line}`);
    const match = str(mo["match"], "match text");
    if (match === "") throw new BadReply("empty match text");
    return {
      file,
      side,
      line,
      before: str(mo["before"], "match before"),
      match,
      after: str(mo["after"], "match after"),
      cutStart: bool(mo["cut_start"], "match cut_start"),
      cutEnd: bool(mo["cut_end"], "match cut_end"),
    };
  });
  return { matches, truncated: bool(o["truncated"], "truncated") };
}
