# Librarian

This project uses **Librarian** for semantic search and graph-based code/doc navigation.
State lives in `.librarian/`; all librarian-owned files are under that directory.

## When to reach for Librarian

Before `grep`, `find`, or opening files at random, try:

```sh
librarian search "<topic>"          # semantic search over docs + code
librarian context "<topic>"         # deep briefing: related docs + code refs
librarian neighbors <node>          # what does X connect to?
librarian path <from> <to>          # how do these pieces relate?
```

For architecture or "how does X work" questions, `librarian context` returns a
curated briefing — faster and denser than reading files one by one.

## Graph report

`.librarian/out/GRAPH_REPORT.md` is a topology snapshot: god nodes
(highest-connected), communities (clusters of related work), and surprising
connections (edges that bridge otherwise-separate areas). Read it first when
orienting in an unfamiliar codebase.

## Re-indexing

The graph stays fresh automatically on every `git commit` (post-commit hook).
To rebuild manually: `librarian index && librarian report`.

## Workspace layout

- `.librarian/config.yaml` — indexing + embedding config (safe to commit)
- `.librarian/rules.md` — this file (safe to commit)
- `.librarian/skill.md` — `/librarian` slash-skill definition (safe to commit)
- `.librarian/out/GRAPH_REPORT.md|graph.html|graph.json` — generated reports
- `.librarian/librarian.db` — local SQLite index (gitignored)
