from dataclasses import dataclass
from enum import Enum
from io import BytesIO
from typing import Union

from pypdf import PdfReader
from pypdf.errors import DependencyError, PyPdfError

_UNREADABLE = (PyPdfError, DependencyError, NotImplementedError)
_ENDS = frozenset(" \t\r\n")


class FileCode(Enum):
    ENCRYPTED = "encrypted"
    TOO_MANY_PAGES = "too_many_pages"
    CORRUPTED = "corrupted"


def trim_ends(value: str) -> str:
    i = 0
    j = len(value)
    while i < j and value[i] in _ENDS:
        i += 1
    while j > i and value[j - 1] in _ENDS:
        j -= 1
    return value[i:j]


def _text(value: str) -> str:
    if type(value) is not str:
        raise TypeError("text")
    text = trim_ends(value)
    if text == "":
        raise ValueError("blank text is not a block")
    return text


@dataclass(frozen=True)
class Heading:
    text: str

    def __post_init__(self) -> None:
        object.__setattr__(self, "text", _text(self.text))


@dataclass(frozen=True)
class Paragraph:
    text: str

    def __post_init__(self) -> None:
        object.__setattr__(self, "text", _text(self.text))


@dataclass(frozen=True)
class Table:
    text: str

    def __post_init__(self) -> None:
        object.__setattr__(self, "text", _text(self.text))


@dataclass(frozen=True)
class Figure:
    text: str

    def __post_init__(self) -> None:
        object.__setattr__(self, "text", _text(self.text))


Block = Union[Heading, Paragraph, Table, Figure]


def _page_no(value: int) -> None:
    if type(value) is not int or value < 1:
        raise ValueError("page_no")


@dataclass(frozen=True)
class TextPage:
    page_no: int
    blocks: tuple[Block, ...]

    def __post_init__(self) -> None:
        _page_no(self.page_no)
        blocks = tuple(self.blocks)
        if len(blocks) < 1:
            raise ValueError("a text page needs a block")
        object.__setattr__(self, "blocks", blocks)


@dataclass(frozen=True)
class EmptyPage:
    page_no: int

    def __post_init__(self) -> None:
        _page_no(self.page_no)


Page = Union[TextPage, EmptyPage]


@dataclass(frozen=True)
class DocumentInfo:
    page_count: int

    def __post_init__(self) -> None:
        if type(self.page_count) is not int or self.page_count < 1:
            raise ValueError("page_count")


@dataclass(frozen=True)
class Ok:
    info: DocumentInfo
    pages: tuple[Page, ...]

    def __post_init__(self) -> None:
        pages = tuple(self.pages)
        if self.info.page_count != len(pages):
            raise ValueError("page_count is len(pages)")
        for index, page in enumerate(pages, start=1):
            if page.page_no != index:
                raise ValueError("page_no")
        object.__setattr__(self, "pages", pages)


@dataclass(frozen=True)
class FileError:
    code: FileCode

    def __post_init__(self) -> None:
        if not isinstance(self.code, FileCode):
            raise TypeError("code")


ParseOutcome = Union[Ok, FileError]


def _utf8(value: str) -> bool:
    try:
        value.encode("utf-8")
    except UnicodeEncodeError:
        return False
    return True


def parse_pdf(data: bytes, max_pages: int) -> ParseOutcome:
    if type(data) is not bytes:
        raise TypeError("data")
    if type(max_pages) is not int:
        raise TypeError("max_pages")
    try:
        reader = PdfReader(BytesIO(data))
        if reader.is_encrypted:
            return FileError(FileCode.ENCRYPTED)
        count = len(reader.pages)
        if max_pages < 1 or count > max_pages:
            return FileError(FileCode.TOO_MANY_PAGES)
        if count < 1:
            return FileError(FileCode.CORRUPTED)
        pages: list[Page] = []
        for index in range(count):
            extracted = reader.pages[index].extract_text()
            trimmed = trim_ends(extracted or "")
            page_no = index + 1
            if trimmed == "":
                pages.append(EmptyPage(page_no))
            elif not _utf8(trimmed):
                return FileError(FileCode.CORRUPTED)
            else:
                pages.append(TextPage(page_no, (Paragraph(trimmed),)))
        return Ok(DocumentInfo(count), tuple(pages))
    except _UNREADABLE:
        return FileError(FileCode.CORRUPTED)
