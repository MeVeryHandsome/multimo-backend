# syntax=docker/dockerfile:1
FROM golang:1.24-bookworm AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath \
      -ldflags "-s -w -X multimo-backend/internal/consts.Version=${VERSION}" \
      -o /out/multimo .

FROM debian:bookworm-slim
# ffmpeg: video decode / keyframes; poppler-utils: PDF text; catdoc: legacy .doc
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ffmpeg poppler-utils catdoc ca-certificates tzdata curl \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home /app multimo \
 && mkdir -p /data && chown multimo /data
WORKDIR /app
COPY --from=build /out/multimo /app/multimo
COPY manifest/config /app/manifest/config
USER multimo
ENV TZ=Asia/Shanghai HTTP_ADDR=:8000 DATA_DIR=/data
EXPOSE 8000
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=5 \
  CMD curl -fsS http://127.0.0.1:8000/api/v1/system/health >/dev/null || exit 1
ENTRYPOINT ["/app/multimo"]
