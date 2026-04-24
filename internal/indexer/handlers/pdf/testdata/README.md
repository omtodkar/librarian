# PDF handler test fixtures

Four small PDFs used by the handler's tests:

| File | Purpose | Observed cascade tier |
|---|---|---|
| `plain.pdf` | 1 page, uniform font, no outline, no tags | tier 4 (pages) |
| `multipage.pdf` | 5 pages of plain text | tier 4 + `MaxPages` cap |
| `bookmarks.pdf` | 3 pages + nested outline | tier 2 (bookmarks) |
| `tagged.pdf` | H1/H2/P with heading-size font — reportlab doesn't emit a real StructTreeRoot, so this exercises tier 3 heuristic in practice. A real tagged PDF from a user's corpus would trigger tier 1 instead. |

Total fixture size: ~10 KB.

## Regenerating

`generate.py` is the source of truth. Re-run with:

```sh
python3 generate.py
```

Requires `reportlab` (`pip3 install --user --break-system-packages reportlab`). The script is pure-Python, MIT-licensed, and kept self-contained. Fixtures are regenerated deterministically enough that only meaningful content diffs appear in PRs.

## Why committed binary fixtures

Pure-Go libraries don't currently generate tagged PDFs with a PDF struct tree. Without committed fixtures there's no way to exercise tier 1. The fixtures are small and stable; regenerating requires one `pip install` but no system dependencies (no LaTeX, no headless browser).
