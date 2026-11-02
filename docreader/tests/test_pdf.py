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


def test_encrypt_word_in_trailer_comment_keeps_text():
    data = pdf_bytes(["Alpha"])
    start = data.rfind(b"startxref")
    trailer = data.rfind(b"trailer", 0, start)
    line = data.find(b"\n", trailer)
    commented = data[: line + 1] + b"% note /Encrypt not a key\n" + data[line + 1 :]
    outcome = parse_pdf(commented, max_pages=300)
    assert outcome == Ok(
        info=DocumentInfo(page_count=1),
        pages=(TextPage(1, (Paragraph("Alpha"),)),),
    )


def test_unreadable_pdf_is_corrupted():
    base = pdf_bytes(["Alpha"])
    unknown = base.replace(b"/Length ", b"/Filter /NotARealFilter /Length ", 1)
    brotli = base.replace(b"/Length ", b"/Filter /BrotliDecode /Length ", 1)
    xref = bytearray(pdf_bytes(["Alpha", "Beta"]))
    needle = b"xref\n0 10\n"
    at = xref.find(needle) + needle.index(b"1")
    xref[at] = 0
    assert parse_pdf(unknown, max_pages=300) == FileError(FileCode.CORRUPTED)
    assert parse_pdf(brotli, max_pages=300) == FileError(FileCode.CORRUPTED)
    assert parse_pdf(bytes(xref), max_pages=300) == FileError(FileCode.CORRUPTED)


_SUR_CMAP = b"""/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<00> <FF>
endcodespacerange
1 beginbfchar
<41> <D800>
endbfchar
endcmap
CMapName currentdict /CMap defineresource pop
end
end
"""


def surrogate_pdf() -> bytes:
    cmap = (
        b"6 0 obj\n<< /Length "
        + str(len(_SUR_CMAP)).encode()
        + b" >>\nstream\n"
        + _SUR_CMAP
        + b"\nendstream\nendobj\n"
    )
    font = (
        b"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica "
        b"/ToUnicode 6 0 R >>\nendobj\n"
    )
    stream = b"BT /F1 12 Tf 72 720 Td (A) Tj ET"
    content = (
        b"4 0 obj\n<< /Length "
        + str(len(stream)).encode()
        + b" >>\nstream\n"
        + stream
        + b"\nendstream\nendobj\n"
    )
    objs = [
        b"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
        b"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
        b"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
        b"/Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n",
        content,
        font,
        cmap,
    ]
    parts = [b"%PDF-1.4\n"]
    offsets = []
    cursor = len(parts[0])
    for obj in objs:
        offsets.append(cursor)
        parts.append(obj)
        cursor += len(obj)
    xref_at = cursor
    n = len(objs) + 1
    xref = [f"xref\n0 {n}\n".encode(), b"0000000000 65535 f \n"]
    for off in offsets:
        xref.append(f"{off:010d} 00000 n \n".encode())
    parts.extend(xref)
    parts.append(
        f"trailer\n<< /Size {n} /Root 1 0 R >>\nstartxref\n{xref_at}\n%%EOF\n".encode()
    )
    return b"".join(parts)


def test_lone_surrogate_is_corrupted():
    assert parse_pdf(surrogate_pdf(), max_pages=300) == FileError(FileCode.CORRUPTED)
