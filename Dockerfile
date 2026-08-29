# syntax=docker/dockerfile:1.10@sha256:865e5dd094beca432e8c0a1d5e1c465db5f998dca4e439981029b3b81fb39ed5

FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/quarry-api ./cmd/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/quarry-dispatcher ./cmd/dispatcher && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/quarry-worker ./cmd/worker && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/goose github.com/pressly/goose/v3/cmd/goose

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS api
COPY --from=build --chown=nonroot:nonroot /out/quarry-api /quarry-api
USER nonroot:nonroot
ENTRYPOINT ["/quarry-api"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS dispatcher
COPY --from=build --chown=nonroot:nonroot /out/quarry-dispatcher /quarry-dispatcher
USER nonroot:nonroot
ENTRYPOINT ["/quarry-dispatcher"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS worker
COPY --from=build --chown=nonroot:nonroot /out/quarry-worker /quarry-worker
USER nonroot:nonroot
ENTRYPOINT ["/quarry-worker"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS migration
COPY --from=build --chown=nonroot:nonroot /out/goose /goose
COPY --chown=nonroot:nonroot internal/store/postgres/migrations /migrations
ENV GOOSE_DRIVER=postgres
ENV GOOSE_MIGRATION_DIR=/migrations
USER nonroot:nonroot
ENTRYPOINT ["/goose"]
CMD ["up"]
