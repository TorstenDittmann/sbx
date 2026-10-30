# sbx server, compiled from source for `docker compose`.
# Release images are built from packaging/docker/Dockerfile and published to
# ghcr.io/torstendittmann/sbx. Listens on 8080. Mount the Docker socket and a data volume.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sbx ./cmd/sbx \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sbx-agent ./cmd/sbx-agent

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/sbx /usr/local/bin/sbx
COPY --from=build /out/sbx-agent /usr/local/bin/sbx-agent
EXPOSE 8080
VOLUME /data
ENV SBX_ADDR=:8080 \
    SBX_DATA_DIR=/data \
    SBX_AGENT_BIN=/usr/local/bin/sbx-agent
ENTRYPOINT ["sbx"]
CMD ["serve"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s \
  CMD ["/usr/local/bin/sbx", "healthcheck"]
