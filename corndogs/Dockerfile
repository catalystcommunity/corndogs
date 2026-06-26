FROM golang:1.25 AS builder

WORKDIR /workspace
# copy relevant things
COPY go.mod .
COPY go.sum .
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download
COPY cmd/ ./cmd
COPY server/ ./server
COPY main.go ./main.go
# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o main main.go

# Use distroless as minimal base image
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/main .
USER 65532:65532

ENTRYPOINT ["/main"]
CMD ["run"]
