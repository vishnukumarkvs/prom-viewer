# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN go build -trimpath -ldflags="-s -w" -o /out/prom-viewer ./cmd/prom-viewer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/prom-viewer /prom-viewer
USER nonroot:nonroot
EXPOSE 9099
ENTRYPOINT ["/prom-viewer"]
