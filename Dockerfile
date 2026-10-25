# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d530cf60723abf1ffae0cd16bb546b379ec0550d2f3815e5b16ed69dbc00fba4 AS build
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
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
