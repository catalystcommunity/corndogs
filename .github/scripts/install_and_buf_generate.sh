# install protoc
PB_REL="https://github.com/protocolbuffers/protobuf/releases"
curl -LO https://github.com/protocolbuffers/protobuf/releases/download/v3.12.4/protoc-v3.12.4-linux-x86_64.zip
unzip protoc-v3.12.4-linux-x86_64.zip -d $HOME/.local
export PATH="$PATH:$HOME/.local/bin"


# install buf
GOBIN=/usr/local/bin go install github.com/bufbuild/buf/cmd/buf@v1.50.1

# install plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@1.5.1
python3 -m pip install --upgrade pip
python3 -m pip install grpclib protobuf

# run buf stuff
cd protos
buf lint
# buf dep prune
# buf dep update
echo YO WERE RUNNIN GENERATE
buf generate