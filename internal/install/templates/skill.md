---
name: librarian
description: Semantic search and graph-based navigation over this project's docs and code. Use when the user asks how something works, where something lives, what connects to what, or any "give me a map of X" question.
---

# /librarian

You have a Librarian index for this project. Prefer it over raw grep for any
question that's semantic ("how does auth work?") rather than lexical ("where is
the string 'authenticate' written?").

## Decision tree

- Question is about **concept / design / how it works** → `librarian context "<topic>"`
  (returns a briefing with related docs + code references).
- Question is about **specific term / symbol** → `librarian search "<term>"`.
- Question is about **connections / reachability** → `librarian neighbors <node-id>`
  or `librarian path <from> <to>`.
- Question is about **project topology / god nodes / clusters** →
  read `.librarian/out/GRAPH_REPORT.md`.

## Node IDs

Librarian namespaces IDs by kind:

- `doc:<path>` — markdown document
- `file:<path>` — code file
- `sym:<path>:<symbol>` — code symbol (function, type, etc.)
- `key:<path>:<dotted.path>` — config key

Pass these verbatim to `librarian neighbors` or `librarian path`.

## When NOT to use Librarian

- The user has named an exact file path → just `Read` it.
- The user is editing code, not exploring — don't search reflexively.
- The index may be stale for just-committed changes within the last few seconds;
  if results look wrong, `librarian index` forces a refresh.
