#!/usr/bin/env python3
"""NFR-03 (AC-03-01) 10 篇 20 页文本 PDF 入库耗时计时脚本（纯标准库，零额外依赖）。

用法：
  1. 本地冒烟（--dry-run）：验证生成 10 份合法 20 页文本型 PDF（不连接服务端）。
  2. 真机实测：上传 10 份 20 页 PDF 到指定 KB 并轮询状态直至全部变为「已完成」，校验单篇耗时 <= 60s（不含图谱抽取）。
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.request
import uuid


def build_text_pdf(pages: int, doc_tag: str) -> bytes:
    """生成仅用标准 PDF Helvetica 字体的合法 N 页文本 PDF（无第三方库依赖）。"""
    objs: list[bytes] = []
    # 1: Catalog, 2: Pages, 3: Font
    objs.append(b"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
    page_refs = " ".join(f"{4 + i * 2} 0 R" for i in range(pages)).encode("ascii")
    objs.append(b"2 0 obj\n<< /Type /Pages /Kids [" + page_refs + b"] /Count " + str(pages).encode("ascii") + b" >>\nendobj\n")
    objs.append(b"3 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

    for i in range(pages):
        page_obj_id = 4 + i * 2
        content_obj_id = 5 + i * 2
        text = (
            f"{doc_tag} page {i + 1}: LoRA low rank adaptation reduces memory footprint "
            f"while preserving ZephyrBench evaluation accuracy on physical page {i + 1}."
        )
        stream = f"BT /F1 11 Tf 48 740 Td ({text}) Tj ET".encode("latin-1")
        objs.append(
            f"{page_obj_id} 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
            f"/Resources << /Font << /F1 3 0 R >> >> /Contents {content_obj_id} 0 R >>\nendobj\n".encode("ascii")
        )
        objs.append(
            f"{content_obj_id} 0 obj\n<< /Length {len(stream)} >>\nstream\n".encode("ascii")
            + stream
            + b"\nendstream\nendobj\n"
        )

    parts = [b"%PDF-1.4\n"]
    offsets = []
    cursor = len(parts[0])
    for obj in objs:
        offsets.append(cursor)
        parts.append(obj)
        cursor += len(obj)

    xref_pos = cursor
    total_objs = len(objs) + 1
    parts.append(f"xref\n0 {total_objs}\n0000000000 65535 f \n".encode("ascii"))
    for off in offsets:
        parts.append(f"{off:010d} 00000 n \n".encode("ascii"))
    parts.append(f"trailer\n<< /Size {total_objs} /Root 1 0 R >>\nstartxref\n{xref_pos}\n%%EOF\n".encode("ascii"))
    return b"".join(parts)


def http_json(url: str, method: str = "GET", token: str = "", body: object = None) -> tuple[int, dict]:
    data = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    with urllib.request.urlopen(req, timeout=30) as resp:
        raw = resp.read()
        return resp.status, json.loads(raw.decode("utf-8")) if raw else {}


def upload_pdf(base_url: str, token: str, kb_id: str, title: str, pdf_data: bytes) -> dict:
    boundary = f"----yanzhi-{uuid.uuid4().hex}"
    body = bytearray()
    body.extend(f"--{boundary}\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\n{title}\r\n".encode("utf-8"))
    body.extend(
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{title}.pdf\"\r\n"
        f"Content-Type: application/pdf\r\n\r\n".encode("utf-8")
    )
    body.extend(pdf_data)
    body.extend(f"\r\n--{boundary}--\r\n".encode("utf-8"))

    req = urllib.request.Request(f"{base_url}/api/v1/kbs/{kb_id}/documents", data=bytes(body), method="POST")
    req.add_header("Content-Type", f"multipart/form-data; boundary={boundary}")
    req.add_header("Authorization", f"Bearer {token}")
    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read().decode("utf-8"))


def main() -> int:
    parser = argparse.ArgumentParser(description="NFR-03 10 篇 20 页文本 PDF 入库计时脚本")
    parser.add_argument("--dry-run", action="store_true", help="仅生成并校验 10 份 20 页 PDF 字节")
    parser.add_argument("--base-url", default="http://127.0.0.1")
    parser.add_argument("--email", default="perf-user-01@example.edu.cn")
    parser.add_argument("--password", default="PerfPass123")
    parser.add_argument("--count", type=int, default=10)
    parser.add_argument("--pages", type=int, default=20)
    parser.add_argument("--limit-seconds", type=float, default=60.0)
    args = parser.parse_args()

    pdfs = [build_text_pdf(args.pages, f"NFR03-Doc-{i + 1:02d}-{uuid.uuid4().hex[:6]}") for i in range(args.count)]
    if args.dry_run:
        assert len(pdfs) == args.count
        assert all(p.startswith(b"%PDF-1.4") and p.count(b"/Type /Page ") == args.pages for p in pdfs)
        print(json.dumps({"dry_run": True, "count": len(pdfs), "pages_per_pdf": args.pages, "avg_bytes": sum(len(p) for p in pdfs) // len(pdfs)}))
        return 0

    base = args.base_url.rstrip("/")
    _, login_res = http_json(f"{base}/api/v1/auth/login", "POST", body={"email": args.email, "password": args.password})
    token = login_res["access_token"]
    _, kb = http_json(f"{base}/api/v1/kbs", "POST", token=token, body={"name": f"NFR03-Bench-{int(time.time())}"})
    kb_id = kb["id"]

    results = []
    for idx, pdf_data in enumerate(pdfs):
        t0 = time.monotonic()
        doc = upload_pdf(base, token, kb_id, f"NFR03-20Pages-{idx + 1:02d}", pdf_data)
        doc_id = doc["id"]
        status = doc["status"]
        while status in ("待处理", "解析中", "向量化中"):
            time.sleep(0.5)
            _, latest = http_json(f"{base}/api/v1/documents/{doc_id}", token=token)
            status = latest["status"]
            if time.monotonic() - t0 > args.limit_seconds * 2:
                break
        elapsed = round(time.monotonic() - t0, 3)
        results.append({"index": idx + 1, "doc_id": doc_id, "status": status, "elapsed_seconds": elapsed})

    max_elapsed = max(r["elapsed_seconds"] for r in results)
    failed = [r for r in results if r["status"] != "已完成" or r["elapsed_seconds"] > args.limit_seconds]
    print(json.dumps({"max_elapsed_seconds": max_elapsed, "limit_seconds": args.limit_seconds, "failed_count": len(failed), "items": results}, ensure_ascii=False, indent=2))
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
