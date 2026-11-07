// The changed-file tree with its filter, vendored directories, generated-file groups and the
// round groups (reviewed unchanged, whitespace only) last. Rows are virtual.

import { count as fmt, h } from "./dom";
import { fileIcon, folderIcon } from "./fileicon";
import { VirtualList } from "./scroller";
import type { State } from "./state";

const ROW_HEIGHT = 28;

type TreeRow =
  | { kind: "dir"; key: string; name: string; depth: number; collapsed: boolean; vendored: boolean; count: number }
  /** A directory's generated files, collapsed into one row. */
  | { kind: "generated"; key: string; depth: number; collapsed: boolean; count: number }
  /** A round group: files set apart at the end of the tree, with their directories. */
  | { kind: "round"; key: string; label: string; depth: number; collapsed: boolean; count: number }
  /** The rule between the tree and the round groups; not a tree-row, it is not selectable. */
  | { kind: "sep"; depth: number }
  | { kind: "file"; i: number; name: string; depth: number };

/** Key suffix of a generated group row; a NUL never occurs in a path, so it cannot collide. */
const GENERATED_KEY = "\0generated";

/** The round groups, in tree order; their keys start with NUL, so they cannot collide with a path. */
const ROUND_GROUPS = [
  { key: "\0reviewed", label: "Reviewed, unchanged" },
  { key: "\0whitespace", label: "Whitespace only" },
] as const;

interface Dir {
  name: string;
  dirs: Map<string, Dir>;
  files: { i: number; name: string }[];
  generated: { i: number; name: string }[];
  /** The server named this directory a file's vendored_dir. */
  vendored: boolean;
  /** Files at any depth below. */
  count: number;
}

const newDir = (name: string): Dir => ({ name, dirs: new Map(), files: [], generated: [], vendored: false, count: 0 });

export class FileTree {
  private readonly list: VirtualList;
  private rows: TreeRow[] = [];
  private filter = "";
  /** The human's collapse choice per directory key; a directory without one follows its default. */
  private readonly toggled = new Map<string, boolean>();
  /** The same for vendored and generated rows while filtering; a new filter clears it, so matches show. */
  private filterToggled = new Map<string, boolean>();
  private selected = -1;
  private fileOrder: number[] = [];
  /** Every file in tree order, whatever the filter. */
  private allOrder: number[] = [];
  /** Files below a collapsed vendored directory or generated group. */
  private folded = new Set<number>();
  /** Each file's directory keys, outermost first. */
  private dirsOf = new Map<number, string[]>();
  /** The files below each directory or generated group row, by row key. */
  private members = new Map<string, number[]>();
  /** Comment counts by file index, recomputed on each render pass. */
  private comments = new Map<number, number>();

  constructor(
    private readonly el: HTMLElement,
    private readonly state: State,
    private readonly onSelect: (i: number) => void,
    /** Sets the viewed mark of files below a directory or group row, as one change. */
    private readonly onViewed: (files: number[], viewed: boolean) => void,
  ) {
    this.list = new VirtualList(el, ROW_HEIGHT);
    el.addEventListener("click", (e) => {
      const row = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-row]") : null;
      if (!row) return;
      const r = this.rows[Number(row.dataset["row"])];
      if (!r || r.kind === "sep") return;
      if (e.target instanceof HTMLInputElement) {
        // The box shows the state the server saved; it changes when the save lands.
        e.preventDefault();
        const files = r.kind === "file" ? [r.i] : (this.members.get(r.key) ?? []);
        const anyClear = files.some((i) => !this.state.viewed.has(i));
        if (files.length > 0) this.onViewed(files, anyClear);
        return;
      }
      if (r.kind !== "file") {
        const hides = r.kind !== "dir" || r.vendored;
        (hides && this.filter ? this.filterToggled : this.toggled).set(r.key, !r.collapsed);
        this.rebuild();
      } else {
        this.onSelect(r.i);
      }
    });
    this.rebuild();
  }

  setFilter(text: string): void {
    this.filter = text.toLowerCase();
    this.filterToggled = new Map();
    this.rebuild();
  }

  /** Selects file i; with reveal, it also expands the directories that hide it. */
  select(i: number, reveal = true): void {
    this.selected = i;
    if (reveal && !this.rows.some((r) => r.kind === "file" && r.i === i)) {
      for (const key of this.dirsOf.get(i) ?? []) {
        this.toggled.set(key, false);
        this.filterToggled.set(key, false);
      }
      this.rebuild();
    }
    this.list.refresh();
    const at = this.rows.findIndex((r) => r.kind === "file" && r.i === i);
    if (at >= 0) this.list.scrollToIndex(at);
  }

  /** File indexes in tree order, honouring the filter but not collapsed directories. */
  order(): number[] {
    return this.fileOrder;
  }

  /** Every file index in tree order, ignoring the filter. */
  fullOrder(): readonly number[] {
    return this.allOrder;
  }

  /** Reports whether file i is below a collapsed vendored directory, generated group or round group; next and previous file skip it. */
  isFolded(i: number): boolean {
    return this.folded.has(i);
  }

  refresh(): void {
    this.countComments();
    this.list.refresh();
  }

  private countComments(): void {
    this.comments = this.state.commentsByFile();
    this.el.classList.toggle("no-comments", this.comments.size === 0);
  }

  /**
   * Rebuilds the rows after the generated or whitespace marks arrive, or the round groups change. The selected file keeps its place on screen
   * while rows around it regroup; with reveal, a selected file in a collapsed group expands it.
   */
  regroup(reveal: boolean): void {
    const rowOf = (): number => this.rows.findIndex((r) => r.kind === "file" && r.i === this.selected);
    const before = rowOf();
    const offset = before * ROW_HEIGHT - this.el.scrollTop;
    this.rebuild();
    if (this.selected < 0) return;
    if (reveal && rowOf() < 0) {
      for (const key of this.dirsOf.get(this.selected) ?? []) {
        this.toggled.set(key, false);
        this.filterToggled.set(key, false);
      }
      this.rebuild();
    }
    const after = rowOf();
    if (before >= 0 && after >= 0) this.el.scrollTop = Math.max(0, after * ROW_HEIGHT - offset);
    else if (after >= 0) this.list.scrollToIndex(after);
    this.list.refresh();
  }

  private matching(): { i: number; path: string }[] {
    const out: { i: number; path: string }[] = [];
    this.state.files.forEach((f, i) => {
      const old = f.oldPath ?? "";
      if (!this.filter || f.path.toLowerCase().includes(this.filter) || old.toLowerCase().includes(this.filter)) {
        out.push({ i, path: f.path });
      }
    });
    return out;
  }

  /**
   * The round group of file i, or -1. Precedence: vendored, then reviewed unchanged, then
   * generated, then whitespace only. A file with a comment or earlier comment at load stays put.
   */
  private roundGroupOf(i: number): number {
    const s = this.state;
    if (s.files[i]?.vendoredDir !== undefined || s.commentedAtStart.has(i)) return -1;
    if (s.reviewedUnchanged(i)) return 0;
    if (s.generated.has(i)) return -1;
    if (s.hideWhitespace && s.whitespace.has(i)) return 1;
    return -1;
  }

  /** Splits entries into the main tree's and each round group's. */
  private partition(entries: readonly { i: number; path: string }[]): { i: number; path: string }[][] {
    const out: { i: number; path: string }[][] = [[], ...ROUND_GROUPS.map(() => [])];
    for (const e of entries) out[this.roundGroupOf(e.i) + 1]!.push(e);
    return out;
  }

  /** The directory tree of entries; with split, generated files go apart in their directory's group. */
  private dirsFor(entries: readonly { i: number; path: string }[], split = true): Dir {
    const root = newDir("");
    for (const { i, path } of entries) {
      const vendoredDir = this.state.files[i]?.vendoredDir;
      const parts = path.split("/");
      const name = parts.pop() ?? path;
      let d = root;
      let at = "";
      for (const p of parts) {
        at = at ? `${at}/${p}` : p;
        let next = d.dirs.get(p);
        if (!next) {
          next = newDir(p);
          d.dirs.set(p, next);
        }
        if (at === vendoredDir) next.vendored = true;
        next.count++;
        d = next;
      }
      (split && this.state.generated.has(i) ? d.generated : d.files).push({ i, name });
    }
    return root;
  }

  private rebuild(): void {
    const [main = [], ...groups] = this.partition(this.matching());
    const root = this.dirsFor(main);
    const rows: TreeRow[] = [];
    const folded = new Set<number>();
    const dirsOf = new Map<number, string[]>();
    const walk = (d: Dir, depth: number, prefix: string, dirs: string[], shown: boolean, inFolded: boolean): void => {
      for (const sub of d.dirs.values()) {
        // Collapse chains of single-child directories into one row. A vendored directory is a row
        // of its own: a chain neither runs into it nor out of it, so its key is its path.
        let name = sub.name;
        let node = sub;
        while (!node.vendored && node.files.length === 0 && node.generated.length === 0 && node.dirs.size === 1) {
          const [next] = node.dirs.values();
          if (!next || next.vendored) break;
          node = next;
          name += "/" + node.name;
        }
        const key = prefix + name;
        // A vendored directory starts collapsed. While filtering it starts expanded whatever the
        // human chose before, so matches stay visible.
        const collapsed =
          node.vendored && this.filter ? (this.filterToggled.get(key) ?? false) : (this.toggled.get(key) ?? node.vendored);
        if (shown) rows.push({ kind: "dir", key, name, depth, collapsed, vendored: node.vendored, count: node.count });
        walk(node, depth + 1, key + "/", [...dirs, key], shown && !collapsed, inFolded || (collapsed && node.vendored));
      }
      const file = (f: { i: number; name: string }, fileDepth: number, fileDirs: string[], fileShown: boolean, fileFolded: boolean): void => {
        dirsOf.set(f.i, fileDirs);
        if (fileFolded) folded.add(f.i);
        if (fileShown) rows.push({ kind: "file", i: f.i, name: f.name, depth: fileDepth });
      };
      for (const f of d.files) file(f, depth, dirs, shown, inFolded);
      if (d.generated.length > 0) {
        // Same defaults as a vendored directory: collapsed, but expanded while filtering.
        const key = prefix + GENERATED_KEY;
        const collapsed = this.filter ? (this.filterToggled.get(key) ?? false) : (this.toggled.get(key) ?? true);
        if (shown) rows.push({ kind: "generated", key, depth, collapsed, count: d.generated.length });
        for (const f of d.generated) file(f, depth + 1, [...dirs, key], shown && !collapsed, inFolded || collapsed);
      }
    };
    walk(root, 0, "", [], true, false);
    const order = orderOf(root);
    let sep = false;
    groups.forEach((entries, g) => {
      if (entries.length === 0) return;
      if (!sep) rows.push({ kind: "sep", depth: 0 });
      sep = true;
      const { key, label } = ROUND_GROUPS[g]!;
      // Collapsed by default, expanded while filtering (REQ-NAV-008), like a generated group.
      const collapsed = this.filter ? (this.filterToggled.get(key) ?? false) : (this.toggled.get(key) ?? true);
      rows.push({ kind: "round", key, label, depth: 0, collapsed, count: entries.length });
      // A round group outranks the generated group, so its files are not split again.
      const d = this.dirsFor(entries, false);
      walk(d, 1, key + "/", [key], !collapsed, collapsed);
      orderOf(d, order);
    });
    this.rows = rows;
    this.fileOrder = order;
    this.allOrder = this.filter ? this.orderAll() : this.fileOrder;
    this.folded = folded;
    this.dirsOf = dirsOf;
    const members = new Map<string, number[]>();
    for (const [i, keys] of dirsOf) {
      for (const k of keys) {
        const m = members.get(k);
        if (m) m.push(i);
        else members.set(k, [i]);
      }
    }
    this.members = members;
    this.countComments();
    this.list.setSource({
      count: () => this.rows.length,
      variable: () => false,
      key: (i) => String(i),
      // The tree keeps the selected row in place itself (regroup).
      id: (i) => String(i),
      render: (i) => this.renderRow(i),
    });
  }

  /** Every file in tree order, ignoring the filter. */
  private orderAll(): number[] {
    const [main = [], ...groups] = this.partition(this.state.files.map((f, i) => ({ i, path: f.path })));
    const out = orderOf(this.dirsFor(main));
    for (const g of groups) orderOf(this.dirsFor(g, false), out);
    return out;
  }

  private renderRow(n: number): HTMLElement {
    const r = this.rows[n]!;
    if (r.kind === "sep") {
      const sep = h("div", "tree-sep");
      sep.setAttribute("role", "separator");
      return sep;
    }
    const row = h("div", "tree-row");
    row.dataset["row"] = String(n);
    row.style.paddingLeft = `${8 + r.depth * 14}px`;
    if (r.kind !== "file") {
      row.classList.add("tree-dir");
      const twisty = h("span", r.collapsed ? "tree-twisty collapsed" : "tree-twisty");
      const label = (kind: string): string => `${kind} · ${r.count} ${r.count === 1 ? "file" : "files"}`;
      // The title carries the full text of a name or label truncated by a narrow sidebar.
      if (r.kind === "generated") {
        row.classList.add("tree-group");
        row.title = label("generated");
        row.append(twisty, h("span", "tree-name tree-hidden-label", row.title));
      } else if (r.kind === "round") {
        row.classList.add("tree-round");
        row.title = `${r.label} (${r.count})`;
        row.append(twisty, h("span", "tree-name tree-hidden-label", row.title));
      } else {
        row.title = r.vendored ? `${r.key}/ · ${label("vendored")}` : `${r.key}/`;
        row.append(twisty, folderIcon(!r.collapsed), h("span", "tree-name", r.name + "/"));
        if (r.vendored) row.append(h("span", "tree-meta tree-hidden-label", label("vendored")));
      }
      // A collapsed row shows its hidden files' comments in the file rows' comment column. The
      // slots are always present, so the row's label does not shift when it is toggled.
      let n = 0;
      if (r.collapsed) for (const i of this.members.get(r.key) ?? []) n += this.comments.get(i) ?? 0;
      const files = this.members.get(r.key) ?? [];
      const all = files.length > 0 && files.every((i) => this.state.viewed.has(i));
      const meta = h("span", "tree-meta");
      if (this.state.lastReviewAt !== undefined) meta.append(h("span", "rtag"));
      meta.append(commentSlot(n), viewedBox(all, all ? "Clear viewed below" : "Mark viewed below", row.title));
      row.append(meta);
      return row;
    }
    const f = this.state.files[r.i]!;
    if (r.i === this.selected) row.classList.add("selected");
    if (this.state.viewed.has(r.i)) row.classList.add("viewed");
    row.title = f.renamedFrom !== undefined ? `${f.renamedFrom} → ${f.path}` : f.path;
    row.append(h("span", `tree-status st-${f.status}`, f.status), fileIcon(f.path), h("span", "tree-name", r.name));
    // Every slot is always rendered, empty when it has nothing to show, so columns line up across rows.
    const meta = h("span", "tree-meta");
    const add = h("span", "cnt add");
    const del = h("span", "cnt del");
    const count = this.state.counts.get(r.i);
    if (count && (count.binary || (count.added === 0 && count.deleted === 0))) {
      add.textContent = count.binary ? "bin" : "0";
      add.classList.add("plain");
    } else if (count) {
      if (count.added > 0) add.textContent = `+${fmt(count.added)}`;
      if (count.deleted > 0) del.textContent = `−${fmt(count.deleted)}`;
    }
    // The state-tag column shows only once there was an earlier review.
    if (this.state.lastReviewAt !== undefined) meta.append(roundTag(this.state, r.i));
    meta.append(add, del, commentSlot(this.comments.get(r.i) ?? 0));
    const viewed = this.state.viewed.has(r.i);
    meta.append(viewedBox(viewed, viewed ? "Clear viewed" : "Mark viewed", row.title));
    row.append(meta);
    return row;
  }
}

/** A row's viewed checkbox; one control for file and directory rows, so both read alike. */
function viewedBox(checked: boolean, title: string, name: string): HTMLElement {
  const box = document.createElement("input");
  box.type = "checkbox";
  box.className = "view-check";
  box.checked = checked;
  box.title = title;
  box.setAttribute("aria-label", `${title}: ${name}`);
  const slot = h("span", "tree-viewed");
  slot.append(box);
  return slot;
}

/** File i's round tag: what changed since the last review, or why it is grouped as it is. */
function roundTag(s: State, i: number): HTMLElement {
  const tag = h("span", "rtag");
  if (!s.files[i]?.round) return tag;
  const set = (text: string, title: string): HTMLElement => {
    tag.textContent = text;
    tag.title = title;
    return tag;
  };
  const base = s.baseChanged(i);
  if (s.changedSince(i)) {
    return base ? set("Δ base", "Changed since review; base changed") : set("Δ", "Changed since review");
  }
  if (base) return set("base", "Base changed");
  if (s.reviewedWithWhitespaceHidden(i)) return set("ws", "Reviewed with whitespace hidden");
  return tag;
}

const SVG_NS = "http://www.w3.org/2000/svg";

/** The comment-count slot: a monochrome bubble and the count, or empty when there are none. */
function commentSlot(n: number): HTMLElement {
  const slot = h("span", "tree-comments");
  if (n === 0) return slot;
  slot.setAttribute("role", "img");
  slot.setAttribute("aria-label", `${n} ${n === 1 ? "comment" : "comments"}`);
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS(SVG_NS, "path");
  path.setAttribute("d", "M2 2.5h12a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1H7l-3 3v-3H2a1 1 0 0 1-1-1v-7a1 1 0 0 1 1-1z");
  svg.append(path);
  const count = h("span", "", String(n));
  count.setAttribute("aria-hidden", "true");
  slot.append(svg, count);
  return slot;
}

/**
 * The tree order of d's files: each subdirectory's in turn, then d's own files, then its generated
 * group. Collapsing single-child directory chains into one row leaves this order unchanged.
 */
function orderOf(d: Dir, out: number[] = []): number[] {
  for (const sub of d.dirs.values()) orderOf(sub, out);
  for (const f of d.files) out.push(f.i);
  for (const f of d.generated) out.push(f.i);
  return out;
}
