# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/prom-viewer ./cmd/prom-viewer
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/promviewerctl ./cmd/promviewerctl

FROM alpine:latest
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 promviewer \
    && adduser -S -D -H -u 10001 -G promviewer promviewer
COPY --from=build --chown=promviewer:promviewer /out/prom-viewer /prom-viewer
COPY --from=build --chown=promviewer:promviewer /out/promviewerctl /promviewerctl
USER promviewer
EXPOSE 9099
ENTRYPOINT ["/prom-viewer"]
