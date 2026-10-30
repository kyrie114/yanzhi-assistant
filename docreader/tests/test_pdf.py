from io import BytesIO

from pypdf import PdfWriter
from pypdf.generic import DecodedStreamObject, DictionaryObject, NameObject

from docreader.parser.pdf import (
    DocumentInfo,
    EmptyPage,
    FileCode,
    FileError,
    Ok,
    Paragraph,
    TextPage,
    parse_pdf,
)


def _literal(text: str) -> bytes:
    raw = text.encode("latin-1")
    escaped = raw.replace(b"\\", b"\\\\").replace(b"(", b"\\(").replace(b")", b"\\)")
    return b"(" + escaped + b")"


def pdf_bytes(texts: list[str | None], password: str | None = None) -> bytes:
    writer = PdfWriter()
    for text in texts:
        page = writer.add_blank_page(width=612, height=792)
        if text is None:
            continue
        font = DictionaryObject(
            {
                NameObject("/Type"): NameObject("/Font"),
                NameObject("/Subtype"): NameObject("/Type1"),
                NameObject("/BaseFont"): NameObject("/Helvetica"),
            }
        )
        page[NameObject("/Resources")] = DictionaryObject(
            {NameObject("/Font"): DictionaryObject({NameObject("/F1"): writer._add_object(font)})}
        )
        stream = DecodedStreamObject()
        stream.set_data(b"BT /F1 12 Tf 72 720 Td " + _literal(text) + b" Tj ET")
        page[NameObject("/Contents")] = writer._add_object(stream)
    if password is not None:
        writer.encrypt(password)
    buf = BytesIO()
    writer.write(buf)
    return buf.getvalue()


def test_pages_keep_numbers_and_paragraph_text():
    data = pdf_bytes(["Alpha", "   \n\n  ", "Beta"])
    outcome = parse_pdf(data, max_pages=300)
    assert outcome == Ok(
        info=DocumentInfo(page_count=3),
        pages=(
            TextPage(1, (Paragraph("Alpha"),)),
            EmptyPage(2),
            TextPage(3, (Paragraph("Beta"),)),
        ),
    )
    assert parse_pdf(data, max_pages=300) == outcome


def test_encrypted_hides_plaintext():
    data = pdf_bytes(["SecretText"], password="pw")
    outcome = parse_pdf(data, max_pages=300)
    assert outcome == FileError(FileCode.ENCRYPTED)
    assert "SecretText" not in repr(outcome)

    empty_password = pdf_bytes(["OpenMe"], password="")
    opened = parse_pdf(empty_password, max_pages=300)
    assert opened == FileError(FileCode.ENCRYPTED)
    assert "OpenMe" not in repr(opened)


def test_truncated_pdf_is_corrupted():
    assert parse_pdf(b"%PDF-1.4\n", max_pages=300) == FileError(FileCode.CORRUPTED)


def test_page_cap_returns_no_pages():
    data = pdf_bytes(["Alpha", "Beta"])
    assert parse_pdf(data, max_pages=1) == FileError(FileCode.TOO_MANY_PAGES)
