FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/yzctl ./cmd/yzctl

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S app \
    && adduser -S -G app app \
    && mkdir -p /app/migrations /data \
    && chown -R app:app /app /data

COPY --from=builder /out/server /usr/local/bin/server
COPY --from=builder /out/yzctl /usr/local/bin/yzctl
COPY --from=builder --chown=app:app /src/migrations /app/migrations

ENV MIGRATIONS_DIR=/app/migrations \
    DATA_DIR=/data \
    HTTP_ADDR=:8080

WORKDIR /app
USER app

EXPOSE 8080

CMD ["server"]
