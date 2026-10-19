# Release image. GoReleaser copies the already-built static binaries into
# the build context; this file does not compile Go.
#
# linux/<arch>/sbx is always present. sbx-agent is present once
# cmd/sbx-agent is part of the release. The entrypoint exports SBX_AGENT_BIN
# only when that binary was copied, which is how injection works out of the box.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/sbx /usr/local/bin/sbx
COPY ${TARGETPLATFORM}/ /tmp/sbx-bins/
COPY packaging/docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN set -eu; \
    if [ -f /tmp/sbx-bins/sbx-agent ]; then mv /tmp/sbx-bins/sbx-agent /usr/local/bin/sbx-agent; fi; \
    rm -rf /tmp/sbx-bins; \
    chmod 755 /usr/local/bin/sbx /usr/local/bin/docker-entrypoint.sh; \
    if [ -f /usr/local/bin/sbx-agent ]; then chmod 755 /usr/local/bin/sbx-agent; fi
EXPOSE 8080
VOLUME /data
ENV SBX_ADDR=:8080 \
    SBX_DATA_DIR=/data
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s \
  CMD ["/usr/local/bin/sbx", "healthcheck"]
