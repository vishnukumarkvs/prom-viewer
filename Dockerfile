# Release image, built and pushed by GoReleaser (see .goreleaser.yaml).
#
# GoReleaser cross-compiles both binaries ahead of time and places them in the
# build context under $TARGETPLATFORM, so this image only copies them in. Do
# not add a builder stage or a compiler here: it duplicates work and breaks the
# release build.
#
# This file cannot be built on its own. For a self-contained build, use
# Dockerfile.example:
#
#   docker build -f Dockerfile.example -t prom-viewer:local .

FROM alpine:latest
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 promviewer \
    && adduser -S -D -H -u 10001 -G promviewer promviewer
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/prom-viewer /prom-viewer
COPY $TARGETPLATFORM/promviewerctl /promviewerctl
USER promviewer
EXPOSE 9099
ENTRYPOINT ["/prom-viewer"]
