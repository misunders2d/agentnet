# AgentNet Hub image: the same agentnet program as the laptop client, run as
# `agentnet hub serve` with all state in the /data volume.
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agentnet /usr/local/bin/agentnet
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV AGENTNET_DATA=/data AGENTNET_LISTEN=:8443
VOLUME /data
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/agentnet"]
CMD ["hub", "serve"]
