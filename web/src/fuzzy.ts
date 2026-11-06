// Fuzzy ranking of changed files for the search palette. Pure: no DOM, no state.

/** A file the palette can rank, in tree order. */
export interface Candidate {
  readonly i: number;
  readonly path: string;
  /** The old path of a renamed file, also matched. */
  readonly oldPath?: string | undefined;
}

/** A ranked file: which of its paths matched, and the matched UTF-16 positions in that path. */
export interface Ranked {
  readonly i: number;
  readonly on: "path" | "oldPath";
  readonly positions: readonly number[];
}

export const RANK_LIMIT = 50;

// Weights, in priority order: a contiguous run beats a basename match, which beats word
// boundaries, which beat short gaps; path length only breaks what remains.
const CONTIGUOUS = 16;
const BASENAME = 8;
const BASENAME_PREFIX = 12;
const BOUNDARY = 6;
const GAP = 1;
const LENGTH = 0.001;

/**
 * The files whose path, or old path for a rename, holds the query's characters in order, ignoring
 * case: best first, ties in candidate order, at most limit. An empty query keeps the first limit
 * candidates in order.
 */
export function rankFiles(files: readonly Candidate[], query: string, limit = RANK_LIMIT): Ranked[] {
  if (query === "") return files.slice(0, limit).map((f) => ({ i: f.i, on: "path", positions: [] }));
  const q = fold(query);
  const scored: { r: Ranked; score: number; at: number }[] = [];
  files.forEach((f, at) => {
    let best = match(f.path, q);
    let on: Ranked["on"] = "path";
    if (f.oldPath !== undefined && f.oldPath !== f.path) {
      const old = match(f.oldPath, q);
      if (old && (!best || old.score > best.score)) {
        best = old;
        on = "oldPath";
      }
    }
    if (best) scored.push({ r: { i: f.i, on, positions: best.positions }, score: best.score, at });
  });
  scored.sort((a, b) => b.score - a.score || a.at - b.at);
  return scored.slice(0, limit).map((s) => s.r);
}

/**
 * The best-scoring placement of q (lowercase) as a subsequence of s, or null. best[k][j] is the
 * best score with q[k] at s[j]; a gap's penalty is linear, so the best predecessor across a gap is
 * a running maximum and the search is O(|q|·|s|).
 */
function match(s: string, q: string): { score: number; positions: number[] } | null {
  const n = s.length;
  const m = q.length;
  if (m > n) return null;
  const lower = fold(s);
  const base = s.lastIndexOf("/") + 1;
  const charScore = (j: number): number => {
    let sc = 1;
    if (isBoundary(s, j)) sc += BOUNDARY;
    if (j >= base) sc += BASENAME;
    if (j === base) sc += BASENAME_PREFIX;
    return sc;
  };
  const NONE = -Infinity;
  let prev = new Float64Array(n).fill(NONE);
  const from: Int32Array[] = [];
  for (let k = 0; k < m; k++) {
    const cur = new Float64Array(n).fill(NONE);
    const back = new Int32Array(n).fill(-1);
    const c = q.charCodeAt(k);
    // The best prev[j'] + j' for j' < j - 1: a predecessor across a gap of j - j' - 1 characters.
    let gapBest = NONE;
    let gapAt = -1;
    for (let j = 0; j < n; j++) {
      if (k > 0 && j >= 2) {
        const v = prev[j - 2]! + (j - 2) * GAP;
        if (v > gapBest) {
          gapBest = v;
          gapAt = j - 2;
        }
      }
      if (lower.charCodeAt(j) !== c) continue;
      if (k === 0) {
        cur[j] = charScore(j);
        continue;
      }
      let sc = NONE;
      let at = -1;
      if (j >= 1 && prev[j - 1]! > NONE) {
        sc = prev[j - 1]! + CONTIGUOUS;
        at = j - 1;
      }
      if (gapAt >= 0) {
        const v = gapBest - (j - 1) * GAP;
        if (v > sc) {
          sc = v;
          at = gapAt;
        }
      }
      if (at >= 0) {
        cur[j] = sc + charScore(j);
        back[j] = at;
      }
    }
    from.push(back);
    prev = cur;
  }
  let end = -1;
  let score = NONE;
  for (let j = 0; j < n; j++) {
    if (prev[j]! > score) {
      score = prev[j]!;
      end = j;
    }
  }
  if (end < 0) return null;
  const positions = new Array<number>(m);
  for (let k = m - 1, j = end; k >= 0; k--) {
    positions[k] = j;
    j = from[k]![j]!;
  }
  return { score: score - n * LENGTH, positions };
}

/**
 * s lowercased one UTF-16 unit at a time, so its positions index s. A unit whose lowercase is not
 * a single unit, such as İ (U+0130), is kept as is rather than changing the length.
 */
function fold(s: string): string {
  let out = "";
  for (let j = 0; j < s.length; j++) {
    const c = s[j]!;
    const l = c.toLowerCase();
    out += l.length === 1 ? l : c;
  }
  return out;
}

/** Whether s[j] starts a word: the start, after / _ - . or a space, or a camelCase hump. */
function isBoundary(s: string, j: number): boolean {
  if (j === 0) return true;
  const p = s[j - 1]!;
  if ("/_-. ".includes(p)) return true;
  const c = s[j]!;
  return p >= "a" && p <= "z" && c >= "A" && c <= "Z";
}
