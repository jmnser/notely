# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
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
