# Build from the REPO ROOT so the generated Go module clients/corndogs is in the build
# context:  docker build -f corndogs/Dockerfile .
#
# The corndogs server consumes that module via a go.mod `replace ../clients/corndogs`
# (dogfooding the generated package), so it must be present and at the right relative
# path. We mirror the repo layout under /src — /src/corndogs's `../clients/corndogs`
# resolves to /src/clients/corndogs.
FROM golang:1.25 AS builder

WORKDIR /src/corndogs

# The replace target (its go.mod + generated sources). clients/corndogs has no external
# deps, so copying it before `go mod download` keeps that layer cache-friendly.
COPY clients/corndogs/ /src/clients/corndogs/

# Cache deps before copying the server source so source changes don't re-download.
COPY corndogs/go.mod corndogs/go.sum ./
RUN go mod download

COPY corndogs/cmd/ ./cmd
COPY corndogs/server/ ./server
COPY corndogs/main.go ./main.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o main main.go

# Use distroless as minimal base image
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /src/corndogs/main .
USER 65532:65532

ENTRYPOINT ["/main"]
CMD ["run"]
