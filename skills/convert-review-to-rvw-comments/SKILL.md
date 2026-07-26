---
name: convert-review-to-rvw-comments
description: Convert generic LLM code-review output into the active comment-export JSON accepted on stdin by rvw diff and rvw pr. Use when a user wants to seed rvw with review findings, turn Markdown or prose review feedback into rvw comments, or prepare an LLM review for piping into rvw.
---

# Convert Review to RVW Comments

Convert actionable review findings into one exact rvw comment-export document. Emit the document;
do not run `rvw`, open its TUI, or write a file unless the user separately asks.

## Workflow

1. Read the supplied review, including relevant preceding conversation or an explicitly named file.
2. Extract actionable findings. Ignore approval decisions, praise, summaries, test logs, and
   non-actionable observations.
3. Deduplicate only findings that identify the same issue at the same target. Keep distinct issues
   as distinct comments.
4. Choose the narrowest reliable target without inventing a path, side, or line.
5. Rewrite each finding as a concise, self-contained review comment.
6. Check the document shape and limits, then emit it as JSON.

## Map Targets

Use these target shapes exactly:

```json
{"type":"review"}
{"type":"file","path":"internal/app.go"}
{"type":"line","path":"internal/app.go","side":"new","line":42}
{"type":"range","path":"internal/app.go","side":"new","start":42,"end":45}
```

Apply this fallback order:

- Use `range` only for an exact inclusive contiguous span on one known side. Require `start < end`.
- Use `line` for one exact positive source line on a known side.
- Use `file` when the repository-relative path is reliable but the line or side is absent,
  ambiguous, or potentially outside the patch context.
- Use `review` for cross-file or repository-wide findings, or when no path is reliable.

Treat a line in current or added code as side `new`. Use side `old` only when the review explicitly
identifies removed or old-side code. Fall back to `file` when the side is uncertain.

Extract paths from plain `path:line` references, finding headings, or Markdown file links. Remove
workspace prefixes, URL syntax, and line suffixes so `path` is the exact repository-relative Git
path. Preserve path spelling and case. JSON-escape quotes, backslashes, and control bytes. Preserve
explicit rvw byte-preserving `\u00XX` path escapes rather than interpreting them as Unicode.

Never guess a nearby changed line merely to produce an inline comment. `rvw` will reject any path
or endpoint that is inactive under the selected source, filtering, and configured diff context.

## Write Comment Bodies

Lead with the concrete issue. Add supported rationale and the smallest fix using this compact form:

```markdown
<issue>

Risk: <concrete impact>

Smallest fix: <smallest supported correction>
```

Omit a section when the source review does not support it; do not invent rationale or a fix. Remove
severity/category labels, review decisions, repeated evidence, and test bookkeeping unless they are
part of the technical problem. Preserve identifiers and short code fragments needed to understand
the comment.

Require a non-whitespace, valid UTF-8 body. Permit line feeds, but do not emit raw terminal control
characters, bidirectional controls, or the replacement rune. Represent such evidence visibly in a
code span, for example `\u001b` or `\u202e`.

## Build the Document

Emit exactly one JSON object with one `comments` array. Every entry must contain only `target`,
`binding`, and `comment`, with `binding` always equal to `active`:

```json
{
  "comments": [
    {
      "target": {
        "type": "line",
        "path": "internal/app.go",
        "side": "new",
        "line": 42
      },
      "binding": "active",
      "comment": "This error is discarded, so the caller can continue with incomplete state.\n\nRisk: A failed write is reported as successful.\n\nSmallest fix: Return the write error to the caller."
    }
  ]
}
```

Preserve duplicate findings as separate comments unless they were deduplicated as the same issue.
When there are no actionable findings, emit `{"comments":[]}`.

Enforce all rvw limits before emitting:

- at most 4,096 comments;
- at most 65,536 UTF-8 bytes in one comment body;
- at most 4 MiB across all comment bodies;
- at most 16 MiB in the complete JSON document;
- positive line numbers representable as unsigned 32-bit integers.

Do not truncate, split, or silently omit content to meet a limit. Report the exact blocking limit
instead of emitting an invalid partial document.

## Respond

Place one fenced `json` block first, with no commentary inside it. The fence is not part of the
stdin document. Add an `Omitted:` note after the block only when actionable-looking material could
not be represented safely or reliably. Do not list ordinary praise, summaries, or duplicates as
omissions.
