# Corndogs clients

The directories under `clients` contain the supported Corndogs client packages.
The [`csil/corndogs.csil`](../csil/corndogs.csil) contract generates their types,
codecs, and service methods. Corndogs supplies a TCP transport and a README for
each package.

Clients are available for Go, Python, Rust, TypeScript, Java, Kotlin, C#, Dart,
Ruby, Elixir, OCaml, Zig, C, and Swift.

## Transport

Corndogs uses CSIL-RPC over a persistent TCP connection. Each frame has a 4-byte
big-endian length prefix. HTTP is not available for RPC. The server uses HTTP
only for health checks and Prometheus metrics.

Open the README in a language directory for installation and usage instructions.
For example, the Go client needs only a server address:

```go
import corndogs "github.com/CatalystCommunity/corndogs/clients/corndogs"

client := corndogs.New("corndogs.example.com:5080")
response, err := client.SubmitTask(ctx, corndogs.SubmitTaskRequest{
	Queue: "q",
})
```

Use `corndogs.NewCluster` in Go when you have multiple seed nodes.

## Generated and maintained files

The generation script replaces generated types, codecs, and service methods.
It preserves each Corndogs TCP transport and README. Do not edit a file with a
generated-code notice. Change the CSIL contract and regenerate it.

The generated generic quick-start uses an HTTP carrier, which the Corndogs
server does not support. The generation script removes that file. Use the
Corndogs README in each client package.

## Regenerate clients

Install the `csilgen` revision in [`generate.sh`](../csil/generate.sh). Then run:

```sh
./csil/generate.sh
```

## Test clients

`run-tests.sh` tests each available language client against a local Corndogs
server. It skips a language if its toolchain is not installed.

```sh
./clients/install-transport-toolchains.sh
source ~/.local/catalyst-tools/env.sh
./clients/run-tests.sh
```

Set `CORNDOGS_E2E` to the address of an existing test server. Set `LANGS` to a
space-separated list if you want to test selected languages.
