#!/usr/bin/env python3
"""Generate test fixture PDFs for the pdf handler.

Re-run with `python3 testdata/generate.py` whenever the fixtures need
refreshing. The output files (.pdf) are committed to the repo; this
script is the source of truth for what they contain.

reportlab is pure-Python, MIT-licensed, and doesn't require cgo, so it's
a fine ingestion-side tool even if the project itself avoids it.
"""
import os

from reportlab.pdfgen import canvas
from reportlab.lib.pagesizes import letter
from reportlab.pdfbase.pdfdoc import PDFDictionary, PDFName, PDFArray, PDFString
from reportlab.platypus import (
    SimpleDocTemplate,
    Paragraph,
    Spacer,
    PageBreak,
)
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle

HERE = os.path.dirname(os.path.abspath(__file__))


def plain():
    """One-page uniform font. No outline, no tags. Triggers tier-4 fallback."""
    path = os.path.join(HERE, "plain.pdf")
    c = canvas.Canvas(path, pagesize=letter)
    c.setFont("Helvetica", 12)
    c.drawString(72, 720, "Hello world.")
    c.drawString(72, 700, "This is a plain PDF without headings or bookmarks.")
    c.save()


def multipage():
    """Five pages of plain text — for max_pages cap test + flat fallback."""
    path = os.path.join(HERE, "multipage.pdf")
    c = canvas.Canvas(path, pagesize=letter)
    for i in range(1, 6):
        c.setFont("Helvetica", 12)
        c.drawString(72, 720, f"Page {i} body text.")
        c.drawString(72, 700, f"Content specific to page {i}.")
        c.showPage()
    c.save()


def bookmarks():
    """Three-page PDF with an outline referencing each page at depth 0 + 1.

    Triggers tier-2 cascade. Body text is a unique phrase per page so the
    bookmark-to-range extraction can be asserted.
    """
    path = os.path.join(HERE, "bookmarks.pdf")
    c = canvas.Canvas(path, pagesize=letter)
    for i, (title, body) in enumerate(
        [
            ("Introduction", "Alpha content on page one."),
            ("Methods", "Bravo content on page two."),
            ("Results", "Charlie content on page three."),
        ]
    ):
        c.setFont("Helvetica", 12)
        c.drawString(72, 720, body)
        # Key must be unique per bookmark; setDestination + bookmarkPage
        # together anchor the outline to this page.
        key = f"sec{i}"
        c.bookmarkPage(key)
        c.addOutlineEntry(title, key, level=0, closed=False)
        if i == 1:
            # Nested child bookmark under "Methods".
            sub_key = "sec1_sub"
            c.bookmarkPage(sub_key)
            c.addOutlineEntry("Sub-method", sub_key, level=1, closed=False)
        c.showPage()
    c.save()


def tagged():
    """One-page tagged PDF with <H1>/<H2>/<P>/<L> — triggers tier-1 cascade.

    reportlab's "structure" support is limited; we use Platypus tagged
    paragraphs + enable the document-level tagged flag. PDFium's
    FPDF_StructTree_GetForPage reads this.
    """
    path = os.path.join(HERE, "tagged.pdf")
    styles = getSampleStyleSheet()
    h1 = styles["Heading1"]
    h2 = styles["Heading2"]
    body = styles["BodyText"]

    doc = SimpleDocTemplate(path, pagesize=letter)
    # Enable document-level PDF/UA-ish tagging. reportlab's canvas
    # underpinning respects this and emits a StructTreeRoot.
    doc._docTags = True  # internal flag; harmless if absent.

    story = [
        Paragraph("Document Title", h1),
        Spacer(1, 12),
        Paragraph("Overview", h2),
        Paragraph("This is the overview paragraph.", body),
        Spacer(1, 6),
        Paragraph("Details", h2),
        Paragraph("This is the details paragraph.", body),
    ]
    doc.build(story)


if __name__ == "__main__":
    plain()
    multipage()
    bookmarks()
    tagged()
    print("Generated fixtures in", HERE)
