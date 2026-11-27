FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/sre-aiobs ./cmd/sre-aiobs

# Optional controller image for the explicitly enabled Web collector workflow.
FROM debian:bookworm-slim AS collector-controller
RUN apt-get update && apt-get install -y --no-install-recommends ansible-core openssh-client ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/sre-aiobs /usr/local/bin/sre-aiobs
COPY migrations /migrations
COPY deploy/collectors/check.yml deploy/collectors/filebeat.yml deploy/collectors/filebeat.config.yml /opt/aiobs/playbooks/
COPY deploy/collectors/templates /opt/aiobs/playbooks/templates
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sre-aiobs"]

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sre-aiobs /usr/local/bin/sre-aiobs
COPY migrations /migrations
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sre-aiobs"]
