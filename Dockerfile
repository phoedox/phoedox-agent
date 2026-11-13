FROM golang:1.24 AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/phoedox-agent ./cmd/phoedox-agent

# Docker mode: mount /proc (read-only) and optionally the Docker socket for
# container data and self-update via a short-lived helper container.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/phoedox-agent /usr/local/bin/phoedox-agent
ENTRYPOINT ["/usr/local/bin/phoedox-agent", "run"]
