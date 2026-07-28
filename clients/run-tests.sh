#!/usr/bin/env bash
#
# End-to-end test the corndogs clients in every language against a live corndogs
# server — the multi-language analogue of the Go E2E test. A successful
# SubmitTask/GetNextTask round-trips the whole stack: client -> codec -> our TCP
# transport -> server.
#
# Transport: corndogs speaks CSIL-RPC over TCP (StreamCarrier, 4-byte length prefix)
# on :5080 — HTTP is only for health/metrics. Each language ships its OWN ready-to-use
# TCP transport (clients/<lang>/transport.*, with a heartbeat) + README examples, and
# this harness drives THAT shipped transport — the exact thing a user gets — not the
# csilgen genquickstart HTTP carrier (which the server no longer serves). Every driver
# is self-contained: it builds a tiny round-trip against the committed package and
# points it at $CORNDOGS_ADDR. No csilgen transports/<lang> checkout is needed anymore.
#
#   dev workflow:  clone -> clients/install-transport-toolchains.sh -> clients/run-tests.sh
#   CI (reactorcide): same script; toolchains baked into the corndogs-test-env image.
#
# Toolchains come from the shared catalyst-tools bundle (clients/install-transport-
# toolchains.sh; see ../../CLAUDE.md). Languages whose toolchain is absent are SKIPPED,
# not failed (e.g. swift has no Linux toolchain here). Set CORNDOGS_E2E=<addr>
# (host:port, or a URL whose scheme is stripped) to test an already-running server;
# otherwise a local file-backed server is built and started for the run.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"
CLIENTS="${HERE}"
WORK="$(mktemp -d)"
ALL_LANGS="go python ruby rust typescript elixir dart ocaml zig c csharp java kotlin swift"
LANGS="${LANGS:-$ALL_LANGS}"   # override to test a subset, e.g. LANGS="go python"

# --- toolchains from the shared catalyst-tools bundle (../../CLAUDE.md) -----
# Set up once with clients/install-transport-toolchains.sh. Provides zig, jdk17 + gradle
# (Kotlin builds via Gradle), dotnet, dart, go, node, opam/ocaml. Ruby + Elixir are
# system installs; Rust is the host rustup/cargo.
CATALYST_TOOLS="${CATALYST_TOOLS:-$HOME/.local/catalyst-tools}"
[ -f "$CATALYST_TOOLS/env.sh" ] && . "$CATALYST_TOOLS/env.sh"

declare -A RESULT
have() { command -v "$1" >/dev/null 2>&1; }

cleanup() { rm -rf "$WORK"; [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null; }
trap cleanup EXIT

# --- server ----------------------------------------------------------------
# The client transport dials a bare host:port over TCP. Accept a URL form for
# CORNDOGS_E2E (strip scheme + any path) so an http://host:5080 value still works.
ADDR="${CORNDOGS_E2E:-127.0.0.1:5080}"
ADDR="${ADDR#*://}"; ADDR="${ADDR%%/*}"
HOST="${ADDR%%:*}"; PORT="${ADDR##*:}"
export CORNDOGS_ADDR="${ADDR}"
# Liveness is a raw TCP connect to the RPC port (no HTTP on :5080 anymore).
server_up() { (exec 3<>"/dev/tcp/${HOST}/${PORT}") 2>/dev/null; }
if ! server_up; then
  if [ -n "${CORNDOGS_E2E:-}" ]; then echo "error: CORNDOGS_E2E=${CORNDOGS_E2E} not reachable" >&2; exit 1; fi
  echo "== starting a local file-backed corndogs server on :5080 =="
  ( cd "${ROOT}/corndogs" && go build -o "${WORK}/corndogs" . ) || { echo "server build failed" >&2; exit 1; }
  mkdir -p "${WORK}/data"
  STORAGE_BACKEND=file CORNDOGS_FILESTORE_DIR="${WORK}/data" "${WORK}/corndogs" run >"${WORK}/server.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 20); do server_up && break; sleep 0.5; done
  server_up || { echo "server failed to start; see ${WORK}/server.log" >&2; exit 1; }
fi
echo "== server (CSIL-RPC/TCP): ${ADDR} =="

pass() { RESULT[$1]="PASS"; echo "  [$1] PASS"; }
fail() { RESULT[$1]="FAIL"; echo "  [$1] FAIL"; }
skip() { RESULT[$1]="SKIP — $2"; echo "  [$1] SKIP — $2"; }

# --- per-language drivers --------------------------------------------------
# Each driver builds a minimal SubmitTask/GetNextTask round-trip using that language's
# OWN shipped TCP transport (clients/<lang>/transport.*) pointed at $CORNDOGS_ADDR, the
# same code a user runs out of the box. go/python are the CI-gated pair; the rest run
# on a dev machine via catalyst-tools. A driver skips (never fails) when its toolchain
# is absent.
run_go() {
  have go || { skip go "no go toolchain"; return; }
  local w="${WORK}/go"; mkdir -p "$w"
  cat >"$w/main.go" <<'GOEOF'
package main

import (
	"context"
	"fmt"
	"os"

	corndogs "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

func main() {
	c := corndogs.New(os.Getenv("CORNDOGS_ADDR")) // CSIL-RPC over TCP, our shipped transport
	ctx := context.Background()
	if _, err := c.SubmitTask(ctx, corndogs.SubmitTaskRequest{
		Queue: "e2e-go", CurrentState: "s", AutoTargetState: "w", Timeout: -1, Payload: []byte("hi"),
	}); err != nil {
		fmt.Println("submit:", err)
		os.Exit(1)
	}
	r, err := c.GetNextTask(ctx, corndogs.GetNextTaskRequest{Queue: "e2e-go", CurrentState: "s"})
	if err != nil || r.Delivery == nil {
		fmt.Println("claim:", err)
		os.Exit(1)
	}
	fmt.Println("ok", r.Delivery.Task.Uuid)
}
GOEOF
  ( cd "$w"
    go mod init corndogs_rpc_test >/dev/null 2>&1
    go mod edit -replace "github.com/CatalystCommunity/corndogs/clients/corndogs=${CLIENTS}/corndogs"
    go mod tidy >/dev/null 2>&1 && go run . ) >"$w/out.log" 2>&1 && pass go || { fail go; sed 's/^/    /' "$w/out.log" | tail -8; }
}

run_python() {
  have python3 || { skip python "no python3"; return; }
  local w="${WORK}/python"; mkdir -p "$w"
  cat >"$w/main.py" <<'PYEOF'
import os
from corndogs import CorndogsClient, SubmitTaskRequest, GetNextTaskRequest
from corndogs.transport import TcpTransport  # our shipped CSIL-RPC/TCP transport

tr = TcpTransport(os.environ["CORNDOGS_ADDR"])
client = CorndogsClient(tr)
client.submit_task(SubmitTaskRequest(
    queue="e2e-py", current_state="s", auto_target_state="w",
    timeout=-1, payload=b"hi", priority=0))
delivery = client.get_next_task(GetNextTaskRequest(
    queue="e2e-py", current_state="s",
    override_timeout=0, override_current_state="", override_auto_target_state="")).delivery
assert delivery is not None, "no task claimed"
print("ok")
PYEOF
  PYTHONPATH="${CLIENTS}/python" python3 "$w/main.py" >"$w/out.log" 2>&1 && pass python || { fail python; sed 's/^/    /' "$w/out.log" | tail -8; }
}

run_ruby() {
  have ruby || { skip ruby "no ruby"; return; }
  local w="${WORK}/ruby"; mkdir -p "$w"
  cat >"$w/main.rb" <<'RBEOF'
require "corndogs"
require "transport"

tr = TcpTransport.new(ENV.fetch("CORNDOGS_ADDR"))
client = CorndogsClient.new(tr)

client.submit_task(SubmitTaskRequest.new(
  queue: "e2e-ruby", current_state: "s", auto_target_state: "w",
  timeout: -1, payload: "hi".b, priority: 0
))
delivery = client.get_next_task(GetNextTaskRequest.new(
  queue: "e2e-ruby", current_state: "s",
  override_timeout: 0, override_current_state: "", override_auto_target_state: ""
)).delivery
raise "no task claimed" if delivery.nil?

puts "ok"
RBEOF
  ruby -I"${CLIENTS}/ruby/lib" "$w/main.rb" >"$w/out.log" 2>&1 && pass ruby || { fail ruby; sed 's/^/    /' "$w/out.log" | tail -8; }
}

run_rust() {
  have cargo || { skip rust "no cargo"; return; }
  local w="${WORK}/rust"; mkdir -p "$w/src"
  cat >"$w/src/main.rs" <<'RUSTEOF'
use corndogs::{CorndogsClient, SubmitTaskRequest, GetNextTaskRequest};
use corndogs::transport::Transport;

fn main() {
    let addr = std::env::var("CORNDOGS_ADDR").expect("CORNDOGS_ADDR not set");
    let tr = Transport::connect(addr).expect("connect"); // our shipped CSIL-RPC/TCP transport
    let client = CorndogsClient::new(tr);

    client.submit_task(SubmitTaskRequest {
        queue: "e2e-rust".into(), current_state: "s".into(),
        auto_target_state: "w".into(), timeout: -1, payload: b"hi".to_vec(), priority: 0,
    }).expect("submit_task");

    let got = client.get_next_task(GetNextTaskRequest {
        queue: "e2e-rust".into(), current_state: "s".into(),
        override_timeout: 0, override_current_state: String::new(), override_auto_target_state: String::new(),
    }).expect("get_next_task");

    if got.delivery.is_none() {
        eprintln!("claim: no task returned");
        std::process::exit(1);
    }

    println!("ok");
}
RUSTEOF
  cat >"$w/Cargo.toml" <<EOF
[package]
name = "corndogs_rpc_test"
version = "0.0.0"
edition = "2021"
[dependencies]
corndogs = { path = "${CLIENTS}/rust" }
EOF
  ( cd "$w" && cargo run -q ) >"$w/out.log" 2>&1 && pass rust || { fail rust; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_c() {
  have cc || { skip c "no c compiler"; return; }
  local w="${WORK}/c"; mkdir -p "$w"
  cat >"$w/main.c" <<'CEOF'
#include "client.gen.h"
#include "transport.h"
#include <stdio.h>
#include <stdlib.h>

int main(void) {
    const char *addr = getenv("CORNDOGS_ADDR");
    if (!addr) { fprintf(stderr, "CORNDOGS_ADDR not set\n"); return 1; }

    corndogs_transport_t *tr = corndogs_transport_connect(addr, 0);
    if (!tr) { fprintf(stderr, "connect failed\n"); return 1; }
    CsilgenTransport client = corndogs_transport_seam(tr);

    SubmitTaskRequest sub = {
        .queue = "e2e-c", .current_state = "s", .auto_target_state = "w",
        .timeout = -1, .payload = {0}, .priority = 0,
    };
    SubmitTaskResponse sub_resp;
    CsilCodecArena *owner = NULL;
    if (csil_corndogs_submit_task(&client, &sub, &sub_resp, &owner) != 0) {
        fprintf(stderr, "submit_task failed: %s\n", corndogs_transport_last_error(tr));
        corndogs_transport_close(tr);
        return 1;
    }
    csil_codec_arena_free(owner);

    GetNextTaskRequest next = {
        .queue = "e2e-c", .current_state = "s",
        .override_timeout = 0, .override_current_state = "", .override_auto_target_state = "",
    };
    GetNextTaskResponse next_resp;
    owner = NULL;
    if (csil_corndogs_get_next_task(&client, &next, &next_resp, &owner) != 0) {
        fprintf(stderr, "get_next_task failed: %s\n", corndogs_transport_last_error(tr));
        corndogs_transport_close(tr);
        return 1;
    }
    if (!next_resp.delivery) {
        fprintf(stderr, "get_next_task returned no task\n");
        csil_codec_arena_free(owner);
        corndogs_transport_close(tr);
        return 1;
    }
    csil_codec_arena_free(owner);

    printf("ok\n");
    corndogs_transport_close(tr);
    return 0;
}
CEOF
  cc -std=c11 -o "$w/test" "$w/main.c" "${CLIENTS}/c/transport.c" -I"${CLIENTS}/c" -pthread \
    >"$w/out.log" 2>&1 && "$w/test" >>"$w/out.log" 2>&1 && pass c || { fail c; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_dart() {
  have dart || { skip dart "no dart"; return; }
  local w="${WORK}/dart"; mkdir -p "$w/bin"
  cat >"$w/bin/main.dart" <<'DARTEOF'
import 'dart:io';
import 'dart:typed_data';

import 'package:corndogs/corndogs.dart';
import 'package:corndogs/transport.dart'; // our shipped CSIL-RPC/TCP transport

Future<void> main() async {
  final addr = Platform.environment['CORNDOGS_ADDR']!;
  final tr = await TcpTransport.connect(addr);
  tr.startHeartbeat();
  final client = CorndogsAsyncClient(tr);
  try {
    await client.submitTask(SubmitTaskRequest(
      queue: 'e2e-dart', currentState: 's', autoTargetState: 'w',
      timeout: -1, payload: Uint8List(0), priority: 0,
    ));
    final got = await client.getNextTask(GetNextTaskRequest(
      queue: 'e2e-dart', currentState: 's',
      overrideTimeout: 0, overrideCurrentState: '', overrideAutoTargetState: '',
    ));
    if (got.delivery == null) {
      stderr.writeln('claim: no task returned');
      await tr.close();
      exit(1);
    }
    print('ok');
  } catch (e) {
    stderr.writeln('error: $e');
    await tr.close();
    exit(1);
  }
  await tr.close();
}
DARTEOF
  cat >"$w/pubspec.yaml" <<EOF
name: corndogs_rpc_test
environment:
  sdk: '>=3.0.0 <4.0.0'
dependencies:
  corndogs:
    path: ${CLIENTS}/dart
EOF
  ( cd "$w" && dart pub get >/dev/null 2>&1 && dart run bin/main.dart ) >"$w/out.log" 2>&1 && pass dart || { fail dart; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_csharp() {
  have dotnet || { skip csharp "no dotnet"; return; }
  local w="${WORK}/csharp"; mkdir -p "$w"
  cat >"$w/Program.cs" <<'CSEOF'
using System;
using System.Text;
using corndogs;

var addr = Environment.GetEnvironmentVariable("CORNDOGS_ADDR")
    ?? throw new InvalidOperationException("CORNDOGS_ADDR not set");
var client = Corndogs.Connect(addr); // our shipped CSIL-RPC/TCP transport (Transport.cs)

client.SubmitTask(new SubmitTaskRequest
{
    Queue = "e2e-csharp", CurrentState = "s", AutoTargetState = "w",
    Timeout = -1, Payload = Encoding.UTF8.GetBytes("hi"), Priority = 0,
});
var delivery = client.GetNextTask(new GetNextTaskRequest
{
    Queue = "e2e-csharp", CurrentState = "s",
    OverrideTimeout = 0, OverrideCurrentState = "", OverrideAutoTargetState = "",
}).Delivery;
if (delivery is null)
{
    Console.WriteLine("claim: no task returned");
    Environment.Exit(1);
}
Console.WriteLine("ok");
CSEOF
  cat >"$w/test.csproj" <<EOF
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net8.0</TargetFramework>
    <Nullable>enable</Nullable>
    <ImplicitUsings>disable</ImplicitUsings>
    <LangVersion>latest</LangVersion>
  </PropertyGroup>
  <ItemGroup>
    <ProjectReference Include="${CLIENTS}/csharp/corndogs.csproj" />
  </ItemGroup>
</Project>
EOF
  ( cd "$w" && dotnet run ) >"$w/out.log" 2>&1 && pass csharp || { fail csharp; sed 's/^/    /' "$w/out.log" | tail -12; }
}

run_elixir() {
  have mix || { skip elixir "no elixir/mix"; return; }
  local w="${WORK}/elixir"; mkdir -p "$w"
  # A real mix project (not Mix.install in the script): mix compiles the path dep
  # first, so the generated structs exist when the driver script is compiled+run.
  # Drives Corndogs' OWN shipped TCP transport (gen_tcp-based, no :inets/:ssl needed,
  # so no extra_applications and no ${T}/elixir csilgen transport dep).
  cat >"$w/mix.exs" <<EOF
defmodule CorndogsRpcTest.MixProject do
  use Mix.Project
  def project, do: [app: :corndogs_rpc_test, version: "0.0.0", elixir: "~> 1.14", deps: deps()]
  defp deps, do: [{:corndogs, path: "${CLIENTS}/elixir"}]
end
EOF
  cat >"$w/run.exs" <<'EXEOF'
transport = Corndogs.Transport.connect(System.get_env("CORNDOGS_ADDR"))
client = Csilgen.Generated.CorndogsClient.new(transport)

Csilgen.Generated.CorndogsClient.submit_task(client, %Csilgen.Generated.SubmitTaskRequest{
  queue: "e2e-elixir",
  current_state: "s",
  auto_target_state: "w",
  timeout: -1,
  payload: "hi",
  priority: 0
})

resp =
  Csilgen.Generated.CorndogsClient.get_next_task(client, %Csilgen.Generated.GetNextTaskRequest{
    queue: "e2e-elixir",
    current_state: "s",
    override_timeout: 0,
    override_current_state: "",
    override_auto_target_state: ""
  })

if is_nil(resp.delivery) do
  IO.puts("claim: no task returned")
  System.halt(1)
end

IO.puts("ok")
EXEOF
  ( cd "$w" && mix deps.get >/dev/null 2>&1 && mix run run.exs ) >"$w/out.log" 2>&1 && pass elixir || { fail elixir; sed 's/^/    /' "$w/out.log" | tail -12; }
}

run_zig() {
  have zig || { skip zig "no zig"; return; }
  local w="${WORK}/zig"; mkdir -p "$w"
  cp "${CLIENTS}"/zig/*.gen.zig "${CLIENTS}/zig/transport.zig" "$w/"   # self-contained: gen files + shipped transport (relative @imports)
  cat >"$w/main.zig" <<'ZIGEOF'
const std = @import("std");
const client = @import("client.gen.zig");
const types = @import("types.gen.zig");
const transport = @import("transport.zig");

pub fn main() !void {
    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    defer _ = gpa.deinit();
    const alloc = gpa.allocator();

    const addr = try std.process.getEnvVarOwned(alloc, "CORNDOGS_ADDR");
    defer alloc.free(addr);

    var tr = transport.Transport.connect(alloc, addr) catch |err| {
        std.debug.print("connect: {}\n", .{err});
        std.process.exit(1);
    };
    defer tr.close();
    const svc = client.CorndogsClient.init(tr.asCsilgenTransport());

    var arena = std.heap.ArenaAllocator.init(alloc);
    defer arena.deinit();
    const a = arena.allocator();

    var submitted: types.SubmitTaskResponse = undefined;
    svc.submit_task(a, &types.SubmitTaskRequest{
        .queue = "e2e-zig", .current_state = "s", .auto_target_state = "w",
        .timeout = -1, .payload = "hi", .priority = 0,
    }, &submitted) catch |err| {
        std.debug.print("submit: {}\n", .{err});
        std.process.exit(1);
    };

    var next: types.GetNextTaskResponse = undefined;
    svc.get_next_task(a, &types.GetNextTaskRequest{
        .queue = "e2e-zig", .current_state = "s",
        .override_timeout = 0, .override_current_state = "", .override_auto_target_state = "",
    }, &next) catch |err| {
        std.debug.print("claim: {}\n", .{err});
        std.process.exit(1);
    };
    if (next.delivery == null) {
        std.debug.print("claim: no task returned\n", .{});
        std.process.exit(1);
    }

    std.debug.print("ok\n", .{});
}
ZIGEOF
  ( cd "$w" && zig build-exe --name test -Mroot=main.zig && ./test ) >"$w/out.log" 2>&1 && pass zig || { fail zig; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_ocaml() {
  have dune || { skip ocaml "no dune"; return; }
  local w="${WORK}/ocaml"; mkdir -p "$w/bin"
  echo '(lang dune 3.0)' >"$w/dune-project"
  cp -r "${CLIENTS}/ocaml/lib" "$w/corndogs_src"          # dune: (name corndogs), libraries unix + threads.posix
  touch "$w/corndogs.opam"                                # satisfy the lib's (public_name ...)
  cat >"$w/bin/main.ml" <<'OCAMLEOF'
open Corndogs

let () =
  let addr = Sys.getenv "CORNDOGS_ADDR" in
  let tr = Transport.connect addr in                      (* our shipped CSIL-RPC/TCP transport *)
  let client = Client.make_client ~call:(Transport.call tr) in
  let submit_req : Types.submit_task_request =
    { queue = "e2e-ocaml"; current_state = "s"; auto_target_state = "w";
      timeout = -1L; payload = Bytes.of_string "hi"; priority = 0L }
  in
  match Client.Corndogs_service.submit_task client submit_req with
  | Error e -> Printf.eprintf "submit: %s\n" e; exit 1
  | Ok _ ->
    let next_req : Types.get_next_task_request =
      { queue = "e2e-ocaml"; current_state = "s";
        override_timeout = 0L; override_current_state = ""; override_auto_target_state = "" }
    in
    (match Client.Corndogs_service.get_next_task client next_req with
     | Error e -> Printf.eprintf "claim: %s\n" e; exit 1
     | Ok { delivery = None } -> Printf.eprintf "claim: no task\n"; exit 1
     | Ok { delivery = Some _ } ->
       Transport.close tr;
       print_endline "ok")
OCAMLEOF
  echo '(executable (name main) (libraries corndogs))' >"$w/bin/dune"
  ( cd "$w" && dune exec bin/main.exe ) >"$w/out.log" 2>&1 && pass ocaml || { fail ocaml; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_typescript() {
  have node && have npm || { skip typescript "no node/npm"; return; }
  local w="${WORK}/typescript"; mkdir -p "$w"
  # Build the shipped package (tsc -> dist), then a CJS test project depending on it.
  ( cd "${CLIENTS}/typescript" && npm install --no-audit --no-fund >/dev/null 2>&1 && npm run build >/dev/null 2>&1 )
  cat >"$w/main.ts" <<'TSEOF'
import { AsyncApiClient } from "corndogs";
import { TcpTransport } from "corndogs/transport";

async function main() {
  const tr = new TcpTransport(process.env.CORNDOGS_ADDR!); // our shipped CSIL-RPC/TCP transport
  const client = new AsyncApiClient(tr);
  try {
    await client.corndogs.submitTask({
      queue: "e2e-typescript", currentState: "s", autoTargetState: "w",
      timeout: -1, payload: new TextEncoder().encode("hi"), priority: 0,
    });
    const { delivery } = await client.corndogs.getNextTask({
      queue: "e2e-typescript", currentState: "s",
      overrideTimeout: 0, overrideCurrentState: "", overrideAutoTargetState: "",
    });
    if (!delivery) {
      console.error("claim: no task returned");
      process.exit(1);
    }
    console.log("ok");
  } finally {
    await tr.close(); // let node exit
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
TSEOF
  ( cd "$w"
    npm init -y >/dev/null 2>&1
    npm install --no-audit --no-fund "file:${CLIENTS}/typescript" typescript @types/node >/dev/null 2>&1
    npx tsc main.ts --module nodenext --moduleResolution nodenext --target es2022 --skipLibCheck --esModuleInterop --types node >/dev/null 2>&1
    node main.js ) >"$w/out.log" 2>&1 && pass typescript || { fail typescript; sed 's/^/    /' "$w/out.log" | tail -12; }
}

run_java() {
  have javac || { skip java "no javac"; return; }
  local w="${WORK}/java"; mkdir -p "$w/out"
  cat >"$w/RpcExample.java" <<'JAVAEOF'
package csilgen.generated;

public class RpcExample {
    public static void main(String[] args) throws Exception {
        try (TcpTransport tr = TcpTransport.connect(System.getenv("CORNDOGS_ADDR"))) {
            CorndogsClient client = new CorndogsClient(tr);
            client.submitTask(new SubmitTaskRequest(
                "e2e-java", "s", "w", -1L, "hi".getBytes(), 0L));
            GetNextTaskResponse got = client.getNextTask(new GetNextTaskRequest(
                "e2e-java", "s", 0L, "", ""));
            if (got.delivery() == null) {
                System.out.println("claim: no task");
                System.exit(1);
            }
            System.out.println("ok");
        } catch (Exception e) {
            e.printStackTrace();
            System.exit(1);
        }
    }
}
JAVAEOF
  find "${CLIENTS}/java/src/main/java" -name '*.java' >"$w/srcs"
  echo "$w/RpcExample.java" >>"$w/srcs"
  ( javac -d "$w/out" @"$w/srcs" && java -cp "$w/out" csilgen.generated.RpcExample ) >"$w/out.log" 2>&1 && pass java || { fail java; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_kotlin() {
  have gradle || { skip kotlin "no gradle (catalyst-tools)"; return; }
  local w="${WORK}/kotlin"; mkdir -p "$w/test-src"
  cat >"$w/test-src/Main.kt" <<'KTEOF'
package community.catalyst.csilgen.generated

fun main() {
    val addr = System.getenv("CORNDOGS_ADDR") ?: error("CORNDOGS_ADDR not set")
    val tr = TcpTransport(addr)        // shipped CSIL-RPC/TCP transport, with heartbeat
    tr.startHeartbeat()
    val client = CorndogsClient(tr)

    client.submitTask(SubmitTaskRequest(
        queue = "e2e-kotlin", currentState = "s", autoTargetState = "w",
        timeout = -1, payload = "hi".toByteArray(), priority = 0,
    ))
    val delivery = client.getNextTask(GetNextTaskRequest(
        queue = "e2e-kotlin", currentState = "s",
        overrideTimeout = 0, overrideCurrentState = "", overrideAutoTargetState = "",
    )).delivery
    if (delivery == null) {
        System.err.println("claim: no task returned")
        kotlin.system.exitProcess(1)
    }
    println("ok")
}
KTEOF
  echo 'rootProject.name = "corndogs_rpc_test"' >"$w/settings.gradle.kts"
  cat >"$w/build.gradle.kts" <<EOF
plugins { kotlin("jvm") version "2.0.21"; application }
repositories { mavenCentral() }
kotlin { jvmToolchain(17) }
sourceSets.main {
  kotlin.srcDir("${CLIENTS}/kotlin/src/main/kotlin")  // includes shipped TcpTransport.kt
  kotlin.srcDir("$w/test-src")
}
application { mainClass.set("community.catalyst.csilgen.generated.MainKt") }
EOF
  ( cd "$w" && gradle -q --console=plain --no-daemon run ) >"$w/out.log" 2>&1 && pass kotlin || { fail kotlin; sed 's/^/    /' "$w/out.log" | tail -14; }
}

run_swift() {
  have swiftc || { skip swift "no swiftc"; return; }
  local w="${WORK}/swift"; mkdir -p "$w/Sources/CorndogsRpcTest"
  cat >"$w/Package.swift" <<EOF
// swift-tools-version:5.9
import PackageDescription
let package = Package(
    name: "corndogs_rpc_test",
    dependencies: [
        .package(path: "${CLIENTS}/swift")
    ],
    targets: [
        .executableTarget(
            name: "CorndogsRpcTest",
            dependencies: [.product(name: "corndogs", package: "corndogs")]
        )
    ]
)
EOF
  cat >"$w/Sources/CorndogsRpcTest/main.swift" <<'SWIFTEOF'
import Foundation
import Corndogs

guard let addr = ProcessInfo.processInfo.environment["CORNDOGS_ADDR"] else {
    print("CORNDOGS_ADDR not set"); exit(1)
}

let tr = TcpTransport.connect(addr)   // our shipped CSIL-RPC/TCP transport
let client = CorndogsClient(transport: tr)

do {
    _ = try client.submitTask(SubmitTaskRequest(
        queue: "e2e-swift", currentState: "s", autoTargetState: "w",
        timeout: -1, payload: Array("hi".utf8), priority: 0))

    let next = try client.getNextTask(GetNextTaskRequest(
        queue: "e2e-swift", currentState: "s",
        overrideTimeout: 0, overrideCurrentState: "", overrideAutoTargetState: ""))

    guard next.delivery != nil else {
        print("claim: no task returned"); exit(1)
    }
    print("ok")
} catch {
    print("error: \(error)"); exit(1)
}
SWIFTEOF
  ( cd "$w" && swift run ) >"$w/out.log" 2>&1 && pass swift || { fail swift; sed 's/^/    /' "$w/out.log" | tail -10; }
}

echo "== running client E2E (work dir ${WORK}) =="
for lang in $LANGS; do
  echo "-- ${lang} --"
  case "$lang" in
    go) run_go;; python) run_python;; ruby) run_ruby;; rust) run_rust;;
    c) run_c;; dart) run_dart;; csharp) run_csharp;; elixir) run_elixir;;
    zig) run_zig;; ocaml) run_ocaml;; typescript) run_typescript;;
    java) run_java;; kotlin) run_kotlin;; swift) run_swift;;
    *) skip "$lang" "driver not yet implemented";;
  esac
done

# --- summary ---------------------------------------------------------------
echo; echo "== summary =="
fails=0
for lang in $LANGS; do
  printf "  %-11s %s\n" "$lang" "${RESULT[$lang]:-?}"
  case "${RESULT[$lang]:-}" in FAIL) fails=$((fails+1));; esac
done
[ "$fails" -eq 0 ] && echo "OK" || { echo "${fails} language(s) failed"; exit 1; }
