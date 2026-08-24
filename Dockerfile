# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
COPY go.mod ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/prom-viewer ./cmd/prom-viewer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/prom-viewer /prom-viewer
USER nonroot:nonroot
EXPOSE 9099
ENTRYPOINT ["/prom-viewer"]
