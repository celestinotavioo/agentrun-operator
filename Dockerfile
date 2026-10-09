# One Dockerfile, two images:  --build-arg BIN=manager  |  --build-arg BIN=agent
ARG GO_VERSION=1.27
# mirror.gcr.io: Docker Hub images without Docker Hub rate limits (a room full of workshop laptops shares one IP).
ARG GO_IMAGE=mirror.gcr.io/library/golang

FROM --platform=$BUILDPLATFORM ${GO_IMAGE}:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
ARG BIN=manager
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${BIN}

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/app /app
USER 65532:65532
ENTRYPOINT ["/app"]
