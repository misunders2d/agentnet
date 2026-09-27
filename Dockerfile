# AgentNet Hub image: the same agentnet program as the laptop client, run as
# `agentnet hub serve` with all state in the /data volume.
#
# Normally the binary is compiled in the `build` stage. With
# --build-arg BUILD_STAGE=prebuilt the image is assembled from an `agentnet`
# binary placed next to this Dockerfile in the build context instead (used to
# compile under explicit resource limits); the runtime image is identical.
ARG BUILD_STAGE=build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/misunders2d/agentnet/internal/protocol.Version=${VERSION}" \
      -o /out/agentnet ./cmd/agentnet \
 && mkdir -p /out/data

FROM scratch AS prebuilt
COPY agentnet /out/agentnet
WORKDIR /out/data

FROM ${BUILD_STAGE} AS binary

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=binary /out/agentnet /usr/local/bin/agentnet
COPY --from=binary --chown=65532:65532 /out/data /data
USER 65532:65532
# PORT (not AGENTNET_LISTEN) so a platform's own PORT replaces it.
ENV AGENTNET_DATA=/data PORT=8443
VOLUME /data
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/agentnet"]
CMD ["hub", "serve"]
