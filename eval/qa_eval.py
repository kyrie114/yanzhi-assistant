#!/usr/bin/env python3
"""NFR-10 (AC-06-06) 与 OQ-08 阈值校准评测脚本。

支持两种运行模式：
  1. --mode mock : 零外部依赖，本地启动内置 SSE 替身服务器并校验全部 20 题解析、悬空出处=0 与阈值扫描逻辑（可用于 CI 冒烟）。
  2. --mode live : 对接真实 Docker Compose / PostgreSQL 服务与真实模型，跑 20 题评测集并对 retrieval_threshold 做步长扫描，
                   以「零悬空出处 + 最低误答率（宁可拒答不编造）」为目标推荐最佳阈值。
"""

from __future__ import annotations

import argparse
import http.server
import json
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

REFUSAL_MARKER = "知识库中未找到相关依据"


def parse_sse_lines(raw_bytes: bytes) -> list[dict[str, Any]]:
    text = raw_bytes.decode("utf-8", errors="replace")
    events: list[dict[str, Any]] = []
    cur_event = ""
    cur_data: list[str] = []
    for line in text.splitlines():
        if line == "":
            if cur_event or cur_data:
                raw_data = "\n".join(cur_data)
                try:
                    parsed = json.loads(raw_data) if raw_data else {}
                except json.JSONDecodeError:
                    parsed = {"raw": raw_data}
                events.append({"event": cur_event or "message", "data": parsed})
                cur_event = ""
                cur_data = []
            continue
        if line.startswith("event:"):
            cur_event = line[len("event:") :].strip()
        elif line.startswith("data:"):
            cur_data.append(line[len("data:") :].strip())
    if cur_event or cur_data:
        raw_data = "\n".join(cur_data)
        try:
            parsed = json.loads(raw_data) if raw_data else {}
        except json.JSONDecodeError:
            parsed = {"raw": raw_data}
        events.append({"event": cur_event or "message", "data": parsed})
    return events


def evaluate_turn_events(case: dict[str, Any], events: list[dict[str, Any]]) -> dict[str, Any]:
    retrieval_pairs: set[tuple[str, str]] = set()
    answer_parts: list[str] = []
    citations: list[dict[str, Any]] = []
    done_meta: dict[str, Any] = {}
    errors: list[Any] = []

    for evt in events:
        name = evt["event"]
        data = evt["data"]
        if not isinstance(data, dict):
            continue
        if name == "retrieval":
            for item in data.get("items") or []:
                doc_id = str(item.get("document_id") or "")
                chunk_id = str(item.get("chunk_id") or "")
                if doc_id and chunk_id:
                    retrieval_pairs.add((doc_id, chunk_id))
        elif name == "token":
            delta = data.get("delta")
            if isinstance(delta, str):
                answer_parts.append(delta)
        elif name == "replace":
            txt = data.get("text")
            if isinstance(txt, str):
                answer_parts = [txt]
        elif name == "citations":
            citations = list(data.get("items") or [])
        elif name == "done":
            done_meta = data
        elif name == "error":
            errors.append(data)

    answer = "".join(answer_parts)
    dangling = 0
    for cit in citations:
        doc_id = str(cit.get("document_id") or "")
        chunk_id = str(cit.get("chunk_id") or "")
        if not doc_id or not chunk_id or (doc_id, chunk_id) not in retrieval_pairs:
            dangling += 1

    is_refused = done_meta.get("answer_kind") == "refused" or REFUSAL_MARKER in answer
    expect_refusal = bool(case.get("expect_refusal"))
    false_answer = expect_refusal and not is_refused
    false_refusal = (not expect_refusal) and is_refused
    missing_citation = (not expect_refusal) and (not is_refused) and len(citations) < int(case.get("min_citations", 1))

    return {
        "id": case["id"],
        "category": case["category"],
        "expect_refusal": expect_refusal,
        "is_refused": is_refused,
        "retrieval_count": len(retrieval_pairs),
        "citation_count": len(citations),
        "dangling_citations": dangling,
        "false_answer": false_answer,
        "false_refusal": false_refusal,
        "missing_citation": missing_citation,
        "errors": errors,
    }


def summarize_threshold(threshold: float, results: list[dict[str, Any]]) -> dict[str, Any]:
    total = len(results)
    dangling = sum(r["dangling_citations"] for r in results)
    false_answers = sum(1 for r in results if r["false_answer"])
    false_refusals = sum(1 for r in results if r["false_refusal"])
    missing_cits = sum(1 for r in results if r["missing_citation"])
    out_total = sum(1 for r in results if r["expect_refusal"])
    ans_total = total - out_total
    return {
        "threshold": round(threshold, 2),
        "total": total,
        "dangling_citations": dangling,
        "false_answers": false_answers,
        "false_answer_rate": round(false_answers / max(1, out_total), 4),
        "false_refusals": false_refusals,
        "false_refusal_rate": round(false_refusals / max(1, ans_total), 4),
        "missing_citations": missing_cits,
        "errors": sum(len(r["errors"]) for r in results),
    }


def recommend_threshold(summaries: list[dict[str, Any]]) -> dict[str, Any]:
    # 1a 目标：零悬空出处 + 优先压低误答（宁可拒答不编造），在此前提下尽量降低误拒率
    valid = [s for s in summaries if s["dangling_citations"] == 0 and s["errors"] == 0]
    pool = valid or summaries
    best = min(pool, key=lambda s: (s["false_answers"], s["false_refusals"], abs(s["threshold"] - 0.30)))
    return best


def http_json(url: str, method: str = "GET", token: str = "", body: Any = None) -> tuple[int, Any]:
    data = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read()
            return resp.status, json.loads(raw.decode("utf-8")) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read()
        return e.code, json.loads(raw.decode("utf-8")) if raw else {"raw": str(e)}


def http_sse(url: str, token: str, body: Any) -> list[dict[str, Any]]:
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "text/event-stream")
    req.add_header("Authorization", f"Bearer {token}")
    with urllib.request.urlopen(req, timeout=120) as resp:
        raw = resp.read()
    return parse_sse_lines(raw)


def start_mock_server() -> tuple[http.server.ThreadingHTTPServer, str]:
    state = {"threshold": 0.30}

    class MockHandler(http.server.BaseHTTPRequestHandler):
        def log_message(self, format: str, *args: Any) -> None:
            return

        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            payload = json.loads(self.rfile.read(length).decode("utf-8") or "{}") if length else {}
            if self.path == "/api/v1/auth/login":
                self._send_json(200, {"access_token": "mock-token"})
                return
            if self.path == "/api/v1/qa/sessions":
                self._send_json(201, {"id": "sess-mock-1", "title": payload.get("title", "eval")})
                return
            if self.path.endswith("/turns"):
                q = str(payload.get("question", ""))
                is_oos = any(k in q for k in ("AlphaFold3", "巴黎奥运会", "加密货币", "临床医学", "烹饪食谱"))
                th = state["threshold"]
                # 模拟阈值过低（<0.25）时产生一次误答，过高（>0.40）时产生一次误拒
                if is_oos and th >= 0.25:
                    sse = (
                        'id: 1\nevent: retrieval\ndata: {"items":[]}\n\n'
                        f'id: 2\nevent: replace\ndata: {{"text":"{REFUSAL_MARKER}"}}\n\n'
                        'id: 3\nevent: citations\ndata: {"items":[]}\n\n'
                        'id: 4\nevent: done\ndata: {"answer_kind":"refused","session_id":"sess-mock-1"}\n\n'
                    )
                elif (not is_oos) and th > 0.40 and "硬件" in q:
                    sse = (
                        'id: 1\nevent: retrieval\ndata: {"items":[]}\n\n'
                        f'id: 2\nevent: replace\ndata: {{"text":"{REFUSAL_MARKER}"}}\n\n'
                        'id: 3\nevent: citations\ndata: {"items":[]}\n\n'
                        'id: 4\nevent: done\ndata: {"answer_kind":"refused","session_id":"sess-mock-1"}\n\n'
                    )
                else:
                    ret_item = {"alias": "c1", "chunk_id": "chunk-1", "document_id": "doc-1", "label": "Paper, 第 1 页"}
                    cit_item = {"chunk_id": "chunk-1", "document_id": "doc-1", "label": "Paper, 第 1 页", "origin": "kb", "graph_supplement": False}
                    sse = (
                        f'id: 1\nevent: retrieval\ndata: {json.dumps({"items": [ret_item]})}\n\n'
                        'id: 2\nevent: token\ndata: {"delta":"依据见论文分析 [c1]。"}\n\n'
                        f'id: 3\nevent: citations\ndata: {json.dumps({"items": [cit_item]})}\n\n'
                        'id: 4\nevent: done\ndata: {"answer_kind":"kb","session_id":"sess-mock-1"}\n\n'
                    )
                raw = sse.encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream; charset=utf-8")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
                return
            self._send_json(404, {"code": "NOT_FOUND"})

        def do_PATCH(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            payload = json.loads(self.rfile.read(length).decode("utf-8") or "{}") if length else {}
            if self.path.endswith("/settings"):
                state["threshold"] = float(payload.get("retrieval_threshold", 0.30))
                self._send_json(200, {"id": "kb-mock-1", "retrieval_threshold": state["threshold"]})
                return
            self._send_json(404, {"code": "NOT_FOUND"})

        def _send_json(self, code: int, obj: Any) -> None:
            raw = json.dumps(obj).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MockHandler)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    return srv, f"http://127.0.0.1:{srv.server_port}"


def run_eval(base_url: str, email: str, password: str, kb_id: str, thresholds: list[float], cases: list[dict[str, Any]]) -> dict[str, Any]:
    code, login_res = http_json(f"{base_url}/api/v1/auth/login", "POST", body={"email": email, "password": password})
    if code != 200 or not login_res.get("access_token"):
        raise RuntimeError(f"login failed ({code}): {login_res}")
    token = login_res["access_token"]

    summaries: list[dict[str, Any]] = []
    details_by_threshold: dict[str, list[dict[str, Any]]] = {}

    for th in thresholds:
        p_code, p_res = http_json(
            f"{base_url}/api/v1/kbs/{kb_id}/settings",
            "PATCH",
            token=token,
            body={"retrieval_threshold": th},
        )
        if p_code != 200:
            raise RuntimeError(f"patch threshold {th} failed ({p_code}): {p_res}")

        s_code, sess_res = http_json(
            f"{base_url}/api/v1/qa/sessions",
            "POST",
            token=token,
            body={"title": f"eval-th-{th:.2f}"},
        )
        if s_code not in (200, 201) or not sess_res.get("id"):
            raise RuntimeError(f"create session failed ({s_code}): {sess_res}")
        sess_id = sess_res["id"]

        case_results: list[dict[str, Any]] = []
        for c in cases:
            events = http_sse(
                f"{base_url}/api/v1/qa/sessions/{sess_id}/turns",
                token=token,
                body={
                    "question": c["question"],
                    "scope": {"type": "kb", "kb_id": kb_id},
                    "web_search": False,
                },
            )
            case_results.append(evaluate_turn_events(c, events))

        summaries.append(summarize_threshold(th, case_results))
        details_by_threshold[f"{th:.2f}"] = case_results

    return {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "kb_id": kb_id,
        "thresholds": summaries,
        "recommended": recommend_threshold(summaries),
        "details": details_by_threshold,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="NFR-10 20 题评测与 OQ-08 阈值扫描")
    parser.add_argument("--mode", choices=["mock", "live"], default="mock")
    parser.add_argument("--dataset", default=str(Path(__file__).with_name("dataset_20.json")))
    parser.add_argument("--base-url", default="http://127.0.0.1:8080")
    parser.add_argument("--email", default="eval@example.edu.cn")
    parser.add_argument("--password", default="Abc12345")
    parser.add_argument("--kb-id", default="kb-mock-1")
    parser.add_argument("--sweep", default="0.15,0.20,0.25,0.30,0.35,0.40,0.45,0.50,0.55")
    parser.add_argument("--out", default="")
    args = parser.parse_args()

    cases = json.loads(Path(args.dataset).read_text(encoding="utf-8"))
    thresholds = [float(x.strip()) for x in args.sweep.split(",") if x.strip()]

    srv = None
    base_url = args.base_url.rstrip("/")
    if args.mode == "mock":
        srv, base_url = start_mock_server()

    try:
        report = run_eval(base_url, args.email, args.password, args.kb_id, thresholds, cases)
    finally:
        if srv is not None:
            srv.shutdown()

    report["mode"] = args.mode
    if args.out:
        Path(args.out).write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")

    rec = report["recommended"]
    print(json.dumps({"mode": args.mode, "recommended": rec, "thresholds": report["thresholds"]}, ensure_ascii=False, indent=2))
    if rec["dangling_citations"] != 0:
        print(f"FAIL: dangling_citations={rec['dangling_citations']} != 0", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
