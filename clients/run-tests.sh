#!/usr/bin/env bash
#
# End-to-end test the GENERATED corndogs clients in every language against a live
# corndogs server — the multi-language analogue of the Go E2E test. For each client
# package it extracts the CSIL-RPC carrier+call from that package's generated
# genquickstart.md (the exact code we ship to users), wires it to the committed
# package and the csilgen transports/<lang> reference library, points it at a server,
# builds, and runs it. A successful SubmitTask round-trips the whole stack: generated
# client -> codec -> carrier -> transport lib -> HTTP -> server.
#
#   dev workflow:  clone -> clients/install-transport-toolchains.sh -> clients/run-tests.sh
#   CI (reactorcide): same script; toolchains baked into the corndogs-test-env image.
#
# Toolchains come from the shared catalyst-tools bundle (clients/install-transport-
# toolchains.sh; see ../../CLAUDE.md). The transports/<lang> reference libs are fetched
# via git from GitHub at run time (never vendored).
# Languages whose toolchain is absent are SKIPPED, not failed (swift has no Linux
# toolchain here). Set CORNDOGS_E2E=<base-url> to test an already-running server;
# otherwise a local file-backed server is built and started for the run.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"
CLIENTS="${HERE}"
WORK="$(mktemp -d)"
CACHE="${CATALYST_CACHE:-$HOME/.cache/catalyst}"
CSILGEN_GIT="${CSILGEN_GIT:-https://github.com/catalystcommunity/csilgen.git}"
CSILGEN_REF="${CSILGEN_REF:-main}"
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
SERVER="${CORNDOGS_E2E:-http://127.0.0.1:5080}"
server_up() { curl -s -o /dev/null -m 5 -X POST "${SERVER}/csil/v1/rpc" --data-binary "x" 2>/dev/null; }
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
echo "== server: ${SERVER} =="

# --- transports/<lang> via git (not vendored) ------------------------------
mkdir -p "${CACHE}"
TRANSPORTS_SRC="${CACHE}/csilgen"
if [ -d "${TRANSPORTS_SRC}/.git" ]; then
  git -C "${TRANSPORTS_SRC}" fetch --depth 1 origin "${CSILGEN_REF}" -q 2>/dev/null && git -C "${TRANSPORTS_SRC}" checkout -q FETCH_HEAD 2>/dev/null
else
  git clone --depth 1 --branch "${CSILGEN_REF}" "${CSILGEN_GIT}" "${TRANSPORTS_SRC}" 2>/dev/null
fi
T="${TRANSPORTS_SRC}/transports"
[ -d "$T" ] || { echo "error: could not fetch transports from ${CSILGEN_GIT}" >&2; exit 1; }

# Extract the first fenced code block under the "## CSIL-RPC" heading of a
# genquickstart.md, and point its base URL at our server.
extract_rpc() {
  awk '
    /^## CSIL-RPC/ {sec=1; next}
    sec && /^## / {exit}
    sec && /^```/ {fence++; if (fence==1) next; else exit}
    sec && fence==1 {print}
  ' "$1" | sed "s#http://localhost:5080#${SERVER}#g; s#http://127.0.0.1:5080#${SERVER}#g"
}

pass() { RESULT[$1]="PASS"; echo "  [$1] PASS"; }
fail() { RESULT[$1]="FAIL"; echo "  [$1] FAIL"; }
skip() { RESULT[$1]="SKIP — $2"; echo "  [$1] SKIP — $2"; }

# --- per-language drivers --------------------------------------------------
run_go() {
  have go || { skip go "no go toolchain"; return; }
  local w="${WORK}/go"; mkdir -p "$w"
  extract_rpc "${CLIENTS}/corndogs/genquickstart.md" >"$w/main.go"
  ( cd "$w"
    go mod init corndogs_rpc_test >/dev/null 2>&1
    go mod edit -replace "github.com/CatalystCommunity/corndogs/clients/corndogs=${CLIENTS}/corndogs"
    go mod edit -replace "github.com/catalystcommunity/csilgen/transports/go=${T}/go"
    go mod tidy >/dev/null 2>&1 && go run . ) >"$w/out.log" 2>&1 && pass go || { fail go; sed 's/^/    /' "$w/out.log" | tail -8; }
}

run_python() {
  have python3 || { skip python "no python3"; return; }
  local w="${WORK}/python"; mkdir -p "$w"
  extract_rpc "${CLIENTS}/python/genquickstart.md" >"$w/main.py"
  PYTHONPATH="${CLIENTS}/python:${T}/python" python3 "$w/main.py" >"$w/out.log" 2>&1 && pass python || { fail python; sed 's/^/    /' "$w/out.log" | tail -8; }
}

run_ruby() {
  have ruby || { skip ruby "no ruby"; return; }
  local w="${WORK}/ruby"; mkdir -p "$w"
  extract_rpc "${CLIENTS}/ruby/genquickstart.md" >"$w/main.rb"
  ruby -I"${CLIENTS}/ruby/lib" -I"${T}/ruby/lib" "$w/main.rb" >"$w/out.log" 2>&1 && pass ruby || { fail ruby; sed 's/^/    /' "$w/out.log" | tail -8; }
}

run_rust() {
  have cargo || { skip rust "no cargo"; return; }
  local w="${WORK}/rust"; mkdir -p "$w/src"
  extract_rpc "${CLIENTS}/rust/genquickstart.md" >"$w/src/main.rs"
  cat >"$w/Cargo.toml" <<EOF
[package]
name = "corndogs_rpc_test"
version = "0.0.0"
edition = "2021"
[dependencies]
corndogs = { path = "${CLIENTS}/rust" }
csilgen-transport = { path = "${T}/rust" }
ureq = "2"
EOF
  ( cd "$w" && cargo run -q ) >"$w/out.log" 2>&1 && pass rust || { fail rust; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_c() {
  have cc || { skip c "no c compiler"; return; }
  local w="${WORK}/c"; mkdir -p "$w"
  extract_rpc "${CLIENTS}/c/genquickstart.md" >"$w/main.c"
  cc -std=c11 -o "$w/test" "$w/main.c" "${T}"/c/src/*.c -I"${CLIENTS}/c" -I"${T}/c/include" \
    >"$w/out.log" 2>&1 && "$w/test" >>"$w/out.log" 2>&1 && pass c || { fail c; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_dart() {
  have dart || { skip dart "no dart"; return; }
  local w="${WORK}/dart"; mkdir -p "$w/bin"
  extract_rpc "${CLIENTS}/dart/genquickstart.md" >"$w/bin/main.dart"
  cat >"$w/pubspec.yaml" <<EOF
name: corndogs_rpc_test
environment:
  sdk: '>=3.0.0 <4.0.0'
dependencies:
  corndogs:
    path: ${CLIENTS}/dart
  csilgen_transport:
    path: ${T}/dart
EOF
  ( cd "$w" && dart pub get >/dev/null 2>&1 && dart run bin/main.dart ) >"$w/out.log" 2>&1 && pass dart || { fail dart; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_csharp() {
  have dotnet || { skip csharp "no dotnet"; return; }
  local w="${WORK}/csharp"; mkdir -p "$w"
  extract_rpc "${CLIENTS}/csharp/genquickstart.md" >"$w/Program.cs"
  local tp; tp="$(find "${T}/csharp/src" -name '*.csproj' | head -1)"
  # The genquickstart exposes CsilRpcExample.Run() (a carrier + example), not a Main —
  # add an entry point that calls it.
  printf 'public static class CsilRpcEntry { public static void Main() => CsilRpcExample.Run(); }\n' >"$w/Main.cs"
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
    <ProjectReference Include="${tp}" />
  </ItemGroup>
</Project>
EOF
  ( cd "$w" && dotnet run ) >"$w/out.log" 2>&1 && pass csharp || { fail csharp; sed 's/^/    /' "$w/out.log" | tail -12; }
}

run_elixir() {
  have mix || { skip elixir "no elixir/mix"; return; }
  # The genquickstart carrier uses OTP :inets (standard on a normal Erlang/CI image).
  # Some minimal Erlang builds (e.g. this Arch box) ship without it — skip, don't fail.
  erl -noshell -eval 'case code:which(inets) of non_existing -> halt(1); _ -> halt(0) end' -s init stop >/dev/null 2>&1 \
    || { skip elixir "Erlang built without the :inets app (genquickstart carrier needs it)"; return; }
  local w="${WORK}/elixir"; mkdir -p "$w"
  # A real mix project (not Mix.install in the script): mix compiles the path deps
  # first, so the generated structs exist when the quickstart script is compiled+run.
  cat >"$w/mix.exs" <<EOF
defmodule CorndogsRpcTest.MixProject do
  use Mix.Project
  def project, do: [app: :corndogs_rpc_test, version: "0.0.0", elixir: "~> 1.14", deps: deps()]
  # The genquickstart carrier uses OTP :inets/:ssl; Elixir 1.15+ prunes the code path to
  # declared apps, so they must be listed (a real consumer's mix.exs needs the same).
  def application, do: [extra_applications: [:inets, :ssl]]
  defp deps, do: [{:corndogs, path: "${CLIENTS}/elixir"}, {:csilgen_transport, path: "${T}/elixir"}]
end
EOF
  extract_rpc "${CLIENTS}/elixir/genquickstart.md" >"$w/run.exs"
  ( cd "$w" && mix deps.get >/dev/null 2>&1 && mix run run.exs ) >"$w/out.log" 2>&1 && pass elixir || { fail elixir; sed 's/^/    /' "$w/out.log" | tail -12; }
}

run_zig() {
  have zig || { skip zig "no zig"; return; }
  local w="${WORK}/zig"; mkdir -p "$w"
  cp "${CLIENTS}"/zig/*.gen.zig "$w/"            # client.gen.zig / types.gen.zig (relative @imports)
  extract_rpc "${CLIENTS}/zig/genquickstart.md" >"$w/main.zig"
  ( cd "$w" && zig build-exe --name test \
      --dep csilgen_transport -Mroot=main.zig -Mcsilgen_transport="${T}/zig/src/root.zig" \
      && ./test ) >"$w/out.log" 2>&1 && pass zig || { fail zig; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_ocaml() {
  have dune || { skip ocaml "no dune"; return; }
  local w="${WORK}/ocaml"; mkdir -p "$w/bin"
  echo '(lang dune 3.0)' >"$w/dune-project"
  cp -r "${CLIENTS}/ocaml/lib" "$w/corndogs_src"          # dune: (name corndogs)
  cp -r "${T}/ocaml/lib" "$w/transport_src"               # dune: (name csilgen_transport)
  touch "$w/corndogs.opam" "$w/csilgen-transport.opam"    # satisfy the libs' (public_name ...)
  extract_rpc "${CLIENTS}/ocaml/genquickstart.md" >"$w/bin/main.ml"
  echo '(executable (name main) (libraries corndogs csilgen_transport))' >"$w/bin/dune"
  ( cd "$w" && dune exec bin/main.exe ) >"$w/out.log" 2>&1 && pass ocaml || { fail ocaml; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_typescript() {
  have node && have npm || { skip typescript "no node/npm"; return; }
  local w="${WORK}/typescript"; mkdir -p "$w"
  # Build the two packages (tsc -> dist), then a CJS test project that depends on both.
  ( cd "${CLIENTS}/typescript" && npm install --no-audit --no-fund >/dev/null 2>&1 && npm run build >/dev/null 2>&1 )
  ( cd "${T}/typescript" && npm install --no-audit --no-fund >/dev/null 2>&1 && npm run build >/dev/null 2>&1 )
  extract_rpc "${CLIENTS}/typescript/genquickstart.md" >"$w/main.ts"
  ( cd "$w"
    npm init -y >/dev/null 2>&1
    npm install --no-audit --no-fund "file:${CLIENTS}/typescript" "file:${T}/typescript" typescript @types/node >/dev/null 2>&1
    npx tsc main.ts --module nodenext --moduleResolution nodenext --target es2022 --skipLibCheck --esModuleInterop >/dev/null 2>&1
    node main.js ) >"$w/out.log" 2>&1 && pass typescript || { fail typescript; sed 's/^/    /' "$w/out.log" | tail -12; }
}

# Java (Maven) and Kotlin (Gradle) can't consume a *git URL* as a managed dependency,
# but git-cloning the transport and compiling its sources directly works fine (same as
# C and C#). So we javac/kotlinc the package sources + transport sources + the quickstart
# together — no package manager involved.
run_java() {
  have javac || { skip java "no javac"; return; }
  local w="${WORK}/java"; mkdir -p "$w/out"
  extract_rpc "${CLIENTS}/java/genquickstart.md" >"$w/RpcExample.java"  # public class RpcExample { main }
  { find "${CLIENTS}/java/src/main/java" -name '*.java'; find "${T}/java/src/main/java" -name '*.java'; echo "$w/RpcExample.java"; } >"$w/srcs"
  ( javac -d "$w/out" @"$w/srcs" && java -cp "$w/out" csilgen.generated.RpcExample ) >"$w/out.log" 2>&1 && pass java || { fail java; sed 's/^/    /' "$w/out.log" | tail -10; }
}

run_kotlin() {
  have gradle || { skip kotlin "no gradle (catalyst-tools)"; return; }
  local w="${WORK}/kotlin"; mkdir -p "$w/test-src"
  extract_rpc "${CLIENTS}/kotlin/genquickstart.md" >"$w/test-src/Main.kt"  # package … generated; fun main -> MainKt
  echo 'rootProject.name = "corndogs_rpc_test"' >"$w/settings.gradle.kts"
  cat >"$w/build.gradle.kts" <<EOF
plugins { kotlin("jvm") version "2.0.21"; application }
repositories { mavenCentral() }
kotlin { jvmToolchain(17) }
sourceSets.main {
  kotlin.srcDir("${CLIENTS}/kotlin/src/main/kotlin")
  kotlin.srcDir("${T}/kotlin/src/main/kotlin")
  kotlin.srcDir("$w/test-src")
}
application { mainClass.set("community.catalyst.csilgen.generated.MainKt") }
EOF
  ( cd "$w" && gradle -q --console=plain --no-daemon run ) >"$w/out.log" 2>&1 && pass kotlin || { fail kotlin; sed 's/^/    /' "$w/out.log" | tail -14; }
}

echo "== running client E2E (work dir ${WORK}) =="
for lang in $LANGS; do
  echo "-- ${lang} --"
  case "$lang" in
    go) run_go;; python) run_python;; ruby) run_ruby;; rust) run_rust;;
    c) run_c;; dart) run_dart;; csharp) run_csharp;; elixir) run_elixir;;
    zig) run_zig;; ocaml) run_ocaml;; typescript) run_typescript;;
    java) run_java;; kotlin) run_kotlin;;
    swift) skip swift "no swift toolchain on linux dev image";;
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
