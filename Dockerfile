# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
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
    GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o cmd/notely-server/web/app.wasm ./cmd/notely-server && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/ ./cmd/notely-server && \
    mkdir /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
ARG VERSION REVISION
LABEL org.opencontainers.image.title="notely" \
      org.opencontainers.image.source="https://github.com/jmnser/notely" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"
USER 65532:65532
# Group 0 and g=u let OpenShift's random UIDs write the data too.
COPY --from=build --chown=65532:0 --chmod=0770 /out/data /data
COPY --from=build /out/notely-server /notely-server
VOLUME /data
EXPOSE 8000
ENTRYPOINT ["/notely-server"]
CMD ["-addr=:8000", "-data=/data"]
