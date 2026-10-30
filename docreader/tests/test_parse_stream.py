from concurrent import futures

import grpc
import pytest

from docreader.pb import docreader_pb2 as pb
from docreader.pb import docreader_pb2_grpc as pb_grpc
from docreader.server import DocReaderServicer
from test_pdf import pdf_bytes


def _requests(data: bytes):
    yield pb.ParseRequest(
        header=pb.ParseHeader(
            request_id="r",
            document_id="d",
            source_type=pb.SourceType.PDF,
            file_name="upload.pdf",
            size_bytes=len(data),
            max_pages=300,
        )
    )
    yield pb.ParseRequest(data=data)


@pytest.fixture
def stub(tmp_path):
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
    pb_grpc.add_DocReaderServicer_to_server(
        DocReaderServicer("token", temp_dir=tmp_path),
        server,
    )
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    channel = grpc.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield pb_grpc.DocReaderStub(channel), tmp_path
    finally:
        channel.close()
        server.stop(0)


def test_text_pdf_keeps_physical_pages(stub):
    client, temp = stub
    call = client.Parse(
        _requests(pdf_bytes(["Alpha", "   \n\n  ", "Beta"])),
        metadata=(("x-docreader-token", "token"),),
        timeout=5,
    )
    events = list(call)
    assert call.code() == grpc.StatusCode.OK
    assert [event.WhichOneof("event") for event in events] == [
        "info",
        "page",
        "page",
        "page",
    ]
    assert events[0].info.page_count == 3
    assert events[0].info.has_physical_pages is True
    assert [(event.page.page_no, event.page.has_text) for event in events[1:]] == [
        (1, True),
        (2, False),
        (3, True),
    ]
    assert events[1].page.blocks[0].text == "Alpha"
    assert events[1].page.blocks[0].kind == pb.Block.PARAGRAPH
    assert len(events[2].page.blocks) == 0
    assert events[3].page.blocks[0].text == "Beta"
    assert list(temp.iterdir()) == []


def test_corrupt_stream_is_ok_with_one_corrupted_event(stub):
    client, temp = stub
    call = client.Parse(
        _requests(b"%PDF-1.4\n"),
        metadata=(("x-docreader-token", "token"),),
        timeout=5,
    )
    events = list(call)
    assert call.code() == grpc.StatusCode.OK
    assert [event.WhichOneof("event") for event in events] == ["error"]
    assert events[0].error.code == pb.ParseError.CORRUPTED
    assert b"%PDF-1.4" not in events[0].error.message.encode()
    assert list(temp.iterdir()) == []


def test_wrong_token_is_unauthenticated_and_leaves_no_file(stub):
    client, temp = stub
    with pytest.raises(grpc.RpcError) as caught:
        list(
            client.Parse(
                _requests(b"%PDF-1.4\n"),
                metadata=(("x-docreader-token", "nope"),),
                timeout=5,
            )
        )
    assert caught.value.code() == grpc.StatusCode.UNAUTHENTICATED
    assert list(temp.iterdir()) == []
