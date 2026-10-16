# syntax=docker/dockerfile:1

# Two images from one build: docker build --target api|web .

FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,source=go.mod,target=go.mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o cmd/notely-web/web/app.wasm ./cmd/notely-web && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/ ./cmd/... && \
    mkdir /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3 AS base
ARG VERSION REVISION
LABEL org.opencontainers.image.source="https://github.com/jmnser/notely" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"
USER 65532:65532

FROM base AS api
LABEL org.opencontainers.image.title="notely-api"
# Group 0 and g=u let OpenShift's random UIDs write the data too.
COPY --from=build --chown=65532:0 --chmod=0770 /out/data /data
COPY --from=build /out/notely-api /notely-api
VOLUME /data
EXPOSE 8001
ENTRYPOINT ["/notely-api"]
CMD ["-addr=:8001", "-data=/data"]

FROM base AS web
LABEL org.opencontainers.image.title="notely-web"
COPY --from=build /out/notely-web /notely-web
EXPOSE 8000
ENTRYPOINT ["/notely-web"]
CMD ["-addr=:8000", "-api=http://notely-api:8001"]
