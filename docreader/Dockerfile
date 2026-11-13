FROM python:3.12-slim

WORKDIR /app/docreader

RUN pip install --no-cache-dir \
    pypdf==6.21.4 \
    grpcio==1.85.0 \
    grpcio-tools==1.85.0 \
    protobuf==7.36.2

COPY proto /app/proto
COPY docreader /app/docreader

ENV PYTHONPATH=/app/docreader \
    PYTHONUNBUFFERED=1

RUN python -c "from docreader.stubs import ensure_pb; ensure_pb()" \
    && addgroup --system app \
    && adduser --system --ingroup app app \
    && chown -R app:app /app/docreader

USER app

EXPOSE 50051

CMD ["python", "-m", "docreader"]
