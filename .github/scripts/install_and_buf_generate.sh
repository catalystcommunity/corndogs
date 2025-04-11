# install protoc
PB_REL="https://github.com/protocolbuffers/protobuf/releases"
curl -LO $PB_REL/download/v30.2/protoc-30.2-linux-x86_64.zip

# install buf
GOBIN=/usr/local/bin go install github.com/bufbuild/buf/cmd/buf@v1.50.1

# install plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
python3 -m pip install --upgrade pip
python3 -m pip install grpclib protobuf

# run buf stuff
cd protos
buf lint
# buf dep prune
# buf dep update
echo YO WERE RUNNIN GENERATE
buf generate