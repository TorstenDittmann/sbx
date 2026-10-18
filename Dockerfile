# sbx server. Listens on 8080. Mount the Docker socket and a data volume.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sbx ./cmd/sbx

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/sbx /usr/local/bin/sbx
EXPOSE 8080
VOLUME /data
ENV SBX_ADDR=:8080 \
    SBX_DATA_DIR=/data
ENTRYPOINT ["sbx"]
CMD ["serve"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s \
  CMD ["/usr/local/bin/sbx", "healthcheck"]
