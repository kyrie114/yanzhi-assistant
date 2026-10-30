import os
import tempfile
from concurrent import futures
from pathlib import Path

from docreader.stubs import ensure_pb

ensure_pb()

import grpc
from docreader.parser.pdf import (
    EmptyPage,
    Figure,
    FileCode,
    FileError,
    Heading,
    Ok,
    Paragraph,
    Table,
    TextPage,
    parse_pdf,
)
from docreader.pb import docreader_pb2 as pb
from docreader.pb import docreader_pb2_grpc as pb_grpc
from docreader.pb import health_pb2
from docreader.pb import health_pb2_grpc

_MAX_MESSAGE = 64 << 20

_KIND = {
    Heading: pb.Block.HEADING,
    Paragraph: pb.Block.PARAGRAPH,
    Table: pb.Block.TABLE,
    Figure: pb.Block.FIGURE,
}

_CODE = {
    FileCode.ENCRYPTED: pb.ParseError.ENCRYPTED,
    FileCode.TOO_MANY_PAGES: pb.ParseError.TOO_MANY_PAGES,
    FileCode.CORRUPTED: pb.ParseError.CORRUPTED,
}

_ERROR_TEXT = {
    pb.ParseError.ENCRYPTED: "encrypted",
    pb.ParseError.TOO_MANY_PAGES: "too_many_pages",
    pb.ParseError.CORRUPTED: "corrupted",
    pb.ParseError.UNSUPPORTED_FORMAT: "unsupported_format",
}


def _call_token(context) -> str | None:
    for key, value in context.invocation_metadata():
        if key == "x-docreader-token":
            return value
    return None


def _drain(request_iterator):
    header = None
    chunks = []
    for request in request_iterator:
        part = request.WhichOneof("part")
        if part == "header" and header is None:
            header = request.header
        elif part == "data":
            chunks.append(request.data)
    return header, b"".join(chunks)


def _error(code: int) -> pb.ParseEvent:
    return pb.ParseEvent(error=pb.ParseError(code=code, message=_ERROR_TEXT[code]))


def _events(outcome):
    if isinstance(outcome, FileError):
        yield _error(_CODE[outcome.code])
        return
    if not isinstance(outcome, Ok):
        raise TypeError(type(outcome))
    yield pb.ParseEvent(
        info=pb.DocumentInfo(
            page_count=outcome.info.page_count,
            has_physical_pages=True,
        )
    )
    for page in outcome.pages:
        if isinstance(page, EmptyPage):
            yield pb.ParseEvent(page=pb.Page(page_no=page.page_no, has_text=False))
            continue
        if not isinstance(page, TextPage):
            raise TypeError(type(page))
        blocks = [
            pb.Block(kind=_KIND[type(block)], text=block.text) for block in page.blocks
        ]
        yield pb.ParseEvent(
            page=pb.Page(page_no=page.page_no, has_text=True, blocks=blocks)
        )


class _Health(health_pb2_grpc.HealthServicer):
    def Check(self, request, context):
        return health_pb2.HealthCheckResponse(
            status=health_pb2.HealthCheckResponse.SERVING
        )


class DocReaderServicer(pb_grpc.DocReaderServicer):
    def __init__(self, token: str, temp_dir: str | os.PathLike[str] | None = None) -> None:
        self._token = token
        self._temp_dir = None if temp_dir is None else Path(temp_dir)

    def Parse(self, request_iterator, context):
        if _call_token(context) != self._token:
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "unauthenticated")
        header, body = _drain(request_iterator)
        if header is None or body == b"":
            yield _error(pb.ParseError.CORRUPTED)
            return
        if header.source_type != pb.SourceType.PDF:
            yield _error(pb.ParseError.UNSUPPORTED_FORMAT)
            return
        outcome = self._parse_spooled(body, header.max_pages)
        yield from _events(outcome)

    def _parse_spooled(self, body: bytes, max_pages: int):
        base = self._temp_dir if self._temp_dir is not None else Path(tempfile.gettempdir())
        fd, name = tempfile.mkstemp(prefix="docreader-", dir=base)
        os.close(fd)
        path = Path(name)
        try:
            with path.open("wb") as handle:
                handle.write(body)
            read_back = path.read_bytes()
            return parse_pdf(read_back, max_pages)
        finally:
            path.unlink(missing_ok=True)


def serve(listen: str, token: str) -> None:
    if token == "":
        raise ValueError("empty token")
    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=8),
        options=[
            ("grpc.max_send_message_length", _MAX_MESSAGE),
            ("grpc.max_receive_message_length", _MAX_MESSAGE),
        ],
    )
    pb_grpc.add_DocReaderServicer_to_server(DocReaderServicer(token), server)
    health_pb2_grpc.add_HealthServicer_to_server(_Health(), server)
    server.add_insecure_port(listen)
    server.start()
    server.wait_for_termination()


def main() -> None:
    listen = os.environ.get("DOCREADER_LISTEN", "0.0.0.0:50051")
    token = os.environ.get("DOCREADER_TOKEN", "")
    serve(listen, token)


if __name__ == "__main__":
    main()
