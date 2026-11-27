FROM golang:1.24.13-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/sre-aiobs ./cmd/sre-aiobs

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sre-aiobs /usr/local/bin/sre-aiobs
COPY migrations /migrations
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sre-aiobs"]
