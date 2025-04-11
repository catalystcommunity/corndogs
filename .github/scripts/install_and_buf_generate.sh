GOBIN=/usr/local/bin go install github.com/bufbuild/buf/cmd/buf@v1.50.1

go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
python3 -m pip install --upgrade pip
python3 -m pip install grpclib protobuf

cd protos
buf lint
buf mod prune
buf mod update
buf generate