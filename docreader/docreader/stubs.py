import subprocess
import sys
import tempfile
from pathlib import Path

_PB = Path(__file__).resolve().parent / "pb"
_PROTO = Path(__file__).resolve().parents[2] / "proto" / "docreader" / "v1" / "docreader.proto"

_HEALTH_PROTO = """syntax = "proto3";

package grpc.health.v1;

message HealthCheckRequest {
  string service = 1;
}

message HealthCheckResponse {
  enum ServingStatus {
    UNKNOWN = 0;
    SERVING = 1;
    NOT_SERVING = 2;
    SERVICE_UNKNOWN = 3;
  }
  ServingStatus status = 1;
}

service Health {
  rpc Check(HealthCheckRequest) returns (HealthCheckResponse);
  rpc Watch(HealthCheckRequest) returns (stream HealthCheckResponse);
}
"""

_READY = (
    "docreader_pb2.py",
    "docreader_pb2_grpc.py",
    "health_pb2.py",
    "health_pb2_grpc.py",
)


def ensure_pb() -> None:
    if all((_PB / name).is_file() for name in _READY):
        return
    if not _PROTO.is_file():
        raise FileNotFoundError(_PROTO)
    _PB.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        health = Path(tmp) / "health.proto"
        health.write_text(_HEALTH_PROTO, encoding="utf-8")
        subprocess.check_call(
            [
                sys.executable,
                "-m",
                "grpc_tools.protoc",
                f"-I{_PROTO.parent}",
                f"-I{tmp}",
                f"--python_out={_PB}",
                f"--grpc_python_out={_PB}",
                str(_PROTO),
                str(health),
            ]
        )
    for name in ("docreader_pb2_grpc.py", "health_pb2_grpc.py"):
        path = _PB / name
        text = path.read_text(encoding="utf-8")
        text = text.replace("import docreader_pb2 as", "from docreader.pb import docreader_pb2 as")
        text = text.replace("import health_pb2 as", "from docreader.pb import health_pb2 as")
        path.write_text(text, encoding="utf-8")
