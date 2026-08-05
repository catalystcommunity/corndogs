# Build from the repository root because go.mod uses ../clients/corndogs.
# Run: docker build -f corndogs/Dockerfile .
FROM golang:1.25 AS builder

WORKDIR /src/corndogs

COPY clients/corndogs/ /src/clients/corndogs/

# Download dependencies before you copy files that change frequently.
COPY corndogs/go.mod corndogs/go.sum ./
RUN go mod download

COPY corndogs/cmd/ ./cmd
COPY corndogs/server/ ./server
COPY corndogs/main.go ./main.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o main main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /src/corndogs/main .
USER 65532:65532

ENTRYPOINT ["/main"]
CMD ["run"]
