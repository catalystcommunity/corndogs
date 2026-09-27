"""Runnerlib lifecycle jobs for the corndogs repository.

Each CI job in this repository is one branch of this plugin. The ``CORNDOGS_JOB``
environment variable in the workflow node selects the branch. The workflow
files hold no shell: they start ``runnerlib run --job-command true``, and this
plugin does the work in the ``POST_SOURCE_PREP`` phase.

Reactorcide filters events and paths for a whole workflow only. Thus the pull
request checks share one workflow, and each check examines the changed files
itself. A check with no relevant change logs the reason and succeeds.

Runnerlib loads this file from the trusted CI checkout (``/job/ci``). The code
under test is in ``/job/src``. This file runs commands from that tree with an
argument list and never imports code from it.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tarfile
import time
import zipfile
import urllib.error
import urllib.request
from pathlib import Path
from typing import Callable, Dict, List, Mapping, Optional, Sequence, Tuple

from src.logging import log_stdout
from src.plugins import Plugin, PluginContext, PluginPhase


GITHUB_API = "https://api.github.com"
GITHUB_UPLOADS = "https://uploads.github.com"

# Tool pins. Change a pin in one place; each download is checked.
SEMVER_TAGS_VERSION = "v0.4.0"
HELM_VERSION = "3.19.0"
CRANE_VERSION = "0.20.3"
DOCKER_VERSION = "27.5.1"
# PostgreSQL for the server test: the zonky embedded build from Maven Central.
# It runs from a directory with no install and no root. A pull request that
# changes .reactorcide/ runs under a profile with no root, so apt is not an
# option. Change the version and the digest together.
POSTGRES_VERSION = "17.11.0"
POSTGRES_JAR_SHA256 = "0dd7b72b6f335b8ecfb355fa24c5781e8a93edd09880bb77eb52ebbf29b3e96d"

CHART_FILE = Path("helm_chart/chart/Chart.yaml")
PUSH_ATTEMPTS = 5

# The commit types that the repository accepts. semver-tags reads feat and fix
# (and "!" or BREAKING CHANGE); the other types do not make a release.
CONVENTIONAL_SUBJECT = re.compile(
    r"^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert|norelease)"
    r"(\([^()]+\))?!?: .+"
)

SEMVER_TAG = re.compile(r"^(?P<prefix>.+/)v(?P<major>\d+)\.(?P<minor>\d+)\.(?P<patch>\d+)$")

# The paths whose commits release the server. The server image compiles all of
# them: the CSIL contract and the generated Go module (a replace directive in
# corndogs/go.mod). semver-tags cannot combine directories into one tag: it
# tags each directory by its last path segment. Thus the server version is
# computed here (next_release_tag).
SERVER_RELEASE_PATHS = ("corndogs", "csil", "clients/corndogs")
RELEASE_TYPES = {"feat": "minor", "fix": "patch", "perf": "patch"}
SUBJECT_TYPE = re.compile(r"^(?P<type>[a-z]+)(\([^()]+\))?(?P<breaking>!)?: ")

# The paths that make each pull request check run. A change under
# .reactorcide/ runs every check, because it can change how any check works.
CI_PATHS = (".reactorcide/",)
CHECK_PATHS: Dict[str, Tuple[str, ...]] = {
    "conventional-commits": ("",),  # every change
    # The server builds against the Go client module through a replace
    # directive, so a change there is a server change.
    "test-server": ("corndogs/", "csil/", "clients/corndogs/"),
    "client-tests": ("clients/", "csil/", "corndogs/"),
    "csil-gen-check": ("csil/", "clients/"),
    "helm-validate": ("helm_chart/",),
    "ci-tests": (),  # .reactorcide/ only
}


# ---------------------------------------------------------------------------
# Command helpers
# ---------------------------------------------------------------------------


def _run(
    args: Sequence[str | Path],
    *,
    cwd: Path,
    env: Mapping[str, str] | None = None,
    capture: bool = False,
    check: bool = True,
    show: Optional[str] = None,
) -> subprocess.CompletedProcess[str]:
    """Run a command and write its output to the job log.

    The output is always captured and then logged again. Runnerlib collects
    the log calls of a plugin. Output that a child writes to the container
    stdout can arrive in a different order, or not at all. A failed command
    then shows only an exit status.

    ``show`` replaces the logged command line. Use it when an argument
    contains a credential.
    """
    command = [str(arg) for arg in args]
    log_stdout(f"+ {show if show is not None else ' '.join(command)}")
    command_env = os.environ.copy()
    if env:
        command_env.update(env)
    completed = subprocess.run(
        command,
        cwd=cwd,
        env=command_env,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    if not capture or completed.returncode != 0:
        for line in (completed.stdout or "").splitlines():
            log_stdout(f"  {line}")
    if check and completed.returncode != 0:
        name = show if show is not None else " ".join(command)
        raise RuntimeError(f"{name} failed with exit status {completed.returncode}")
    return completed


def _section(title: str) -> None:
    log_stdout("")
    log_stdout(f"=== {title} ===")


def _require(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is not set; this job needs it")
    return value


def _repo_root(context: PluginContext) -> Path:
    configured = Path(context.config.code_dir)
    if configured.exists():
        return configured.resolve()
    source_path = context.metadata.get("source_path")
    if source_path:
        return Path(source_path).resolve()
    return Path("/job/src")


def _ci_root() -> Path:
    """The trusted CI checkout that holds this plugin."""
    return Path(__file__).resolve().parents[2]


def _home() -> Path:
    """A writable home directory.

    Some execution profiles run the job as a user that cannot write the image
    home directory. Fall back to /tmp/home then.
    """
    home = Path(os.environ.get("HOME", "/tmp/home"))
    try:
        home.mkdir(parents=True, exist_ok=True)
        probe = home / ".corndogs-ci-write-probe"
        probe.write_text("")
        probe.unlink()
    except OSError:
        home = Path("/tmp/home")
        home.mkdir(parents=True, exist_ok=True)
    os.environ["HOME"] = str(home)
    return home


def _local_bin() -> Path:
    local_bin = _home() / ".local" / "bin"
    local_bin.mkdir(parents=True, exist_ok=True)
    path = os.environ.get("PATH", "")
    if str(local_bin) not in path.split(":"):
        os.environ["PATH"] = f"{local_bin}:{path}"
    return local_bin


def _download(url: str, dest: Path, sha256: Optional[str] = None) -> Path:
    log_stdout(f"Download {url}")
    with urllib.request.urlopen(url, timeout=120) as response, dest.open("wb") as out:
        shutil.copyfileobj(response, out)
    if sha256 is not None:
        digest = hashlib.sha256(dest.read_bytes()).hexdigest()
        if digest != sha256.lower():
            dest.unlink()
            raise RuntimeError(f"{url}: SHA-256 {digest} does not match {sha256}")
    return dest


def _fetch_text(url: str) -> str:
    with urllib.request.urlopen(url, timeout=60) as response:
        return response.read().decode()


def tar_member(tar: tarfile.TarFile, member: str) -> tarfile.TarInfo:
    """Find a regular file in an archive, with or without a leading "./".

    Release archives differ: semver-tags stores "./semver-tags", crane
    stores "crane". ``TarFile.extractfile`` matches the stored name exactly.
    """
    wanted = member.removeprefix("./")
    for info in tar.getmembers():
        if info.isfile() and info.name.removeprefix("./") == wanted:
            return info
    raise RuntimeError(f"{member} is not in {tar.name}")


def _install_from_tar(name: str, archive: Path, member: str) -> Path:
    target = _local_bin() / name
    with tarfile.open(archive) as tar:
        extracted = tar.extractfile(tar_member(tar, member))
        target.write_bytes(extracted.read())
    target.chmod(0o755)
    archive.unlink()
    return target


def _go_env() -> None:
    """Point the Go caches at writable directories.

    The image sets GOPATH=/go, which belongs to root. A job can run as a user
    that cannot write it.
    """
    cache = _home() / ".cache" / "corndogs-ci"
    os.environ["GOPATH"] = str(cache / "go")
    os.environ["GOMODCACHE"] = str(cache / "go" / "pkg" / "mod")
    os.environ["GOCACHE"] = str(cache / "go-build")
    flags = os.environ.get("GOFLAGS", "")
    if "-buildvcs=false" not in flags:
        os.environ["GOFLAGS"] = f"{flags} -buildvcs=false".strip()
    for key in ("GOMODCACHE", "GOCACHE"):
        Path(os.environ[key]).mkdir(parents=True, exist_ok=True)


def _wait_for_port(host: str, port: int, seconds: int) -> bool:
    for _ in range(seconds):
        try:
            with socket.create_connection((host, port), timeout=1):
                return True
        except OSError:
            time.sleep(1)
    return False


# ---------------------------------------------------------------------------
# Change detection for pull request checks
# ---------------------------------------------------------------------------


def path_is_relevant(path: str, prefixes: Sequence[str]) -> bool:
    """Report whether one changed path is under one of the prefixes."""
    return any(path.startswith(prefix) for prefix in (*prefixes, *CI_PATHS))


def changes_are_relevant(changed: Sequence[str], prefixes: Sequence[str]) -> bool:
    return any(path_is_relevant(path, prefixes) for path in changed)


def _changed_files(root: Path) -> Optional[List[str]]:
    """The files that the pull request changes, or None if unknown.

    The diff is from the merge base of the pull request base commit. None
    means that the job must run, because it cannot prove that it is not
    necessary.
    """
    base = os.environ.get("REACTORCIDE_DIFF_BASE", "").strip()
    if not base:
        _run(["git", "fetch", "--quiet", "origin", "main"], cwd=root, check=False)
        base = "origin/main"
    elif _run(["git", "cat-file", "-e", f"{base}^{{commit}}"], cwd=root,
              capture=True, check=False).returncode != 0:
        _run(["git", "fetch", "--quiet", "origin", base], cwd=root, check=False)
    listed = _run(["git", "diff", "--name-only", f"{base}...HEAD"], cwd=root,
                  capture=True, check=False)
    if listed.returncode != 0:
        return None
    return [line for line in listed.stdout.splitlines() if line.strip()]


def _should_run(root: Path, check: str) -> bool:
    if os.environ.get("CORNDOGS_FORCE", "") == "1":
        log_stdout("CORNDOGS_FORCE=1: run without a change check")
        return True
    changed = _changed_files(root)
    if changed is None:
        log_stdout("The changed files are not known: run the check")
        return True
    prefixes = CHECK_PATHS[check]
    if changes_are_relevant(changed, prefixes):
        return True
    log_stdout(f"Skip {check}: no change under {', '.join(p for p in prefixes if p)} "
               f"or {', '.join(CI_PATHS)} ({len(changed)} files changed)")
    return False


# ---------------------------------------------------------------------------
# Pull request checks
# ---------------------------------------------------------------------------


def _conventional_commits(root: Path) -> None:
    _section("Check the commit subjects")
    base = os.environ.get("REACTORCIDE_DIFF_BASE", "").strip() or "origin/main"
    _run(["git", "fetch", "--quiet", "origin", "main"], cwd=root, check=False)
    listed = _run(["git", "log", "--format=%H %P%x09%s", f"{base}..HEAD"], cwd=root,
                  capture=True)
    bad: List[str] = []
    for line in listed.stdout.splitlines():
        if not line.strip():
            continue
        shas, _, subject = line.partition("\t")
        sha, *parents = shas.split()
        if len(parents) > 1:
            log_stdout(f"  skip  {sha[:8]} (merge commit)")
            continue
        if CONVENTIONAL_SUBJECT.match(subject):
            log_stdout(f"  ok    {sha[:8]} {subject}")
        else:
            bad.append(f"{sha[:8]} {subject}")
    if bad:
        log_stdout("")
        log_stdout("These subjects are not Conventional Commits:")
        for entry in bad:
            log_stdout(f"  {entry}")
        log_stdout("Use: type(scope)?: description. Types: feat, fix, docs, style, "
                   "refactor, perf, test, build, ci, chore, revert, norelease")
        raise RuntimeError("one or more commit subjects are not Conventional Commits")


def _install_postgres() -> Path:
    """Install the pinned PostgreSQL build in /tmp and return its bin directory."""
    target = Path("/tmp/pgsql")
    if (target / "bin" / "initdb").exists():
        return target / "bin"
    name = f"embedded-postgres-binaries-linux-amd64-{POSTGRES_VERSION}.jar"
    url = ("https://repo1.maven.org/maven2/io/zonky/test/postgres/"
           f"embedded-postgres-binaries-linux-amd64/{POSTGRES_VERSION}/{name}")
    jar = _download(url, Path("/tmp") / name, POSTGRES_JAR_SHA256)
    with zipfile.ZipFile(jar) as archive, archive.open("postgres-linux-x86_64.txz") as txz:
        with tarfile.open(fileobj=txz, mode="r:xz") as tar:
            tar.extractall(target, filter="tar")
    jar.unlink()
    return target / "bin"


def _start_postgres(root: Path) -> Callable[[], None]:
    """Start a PostgreSQL cluster for this job. Return a function that stops it.

    Trust authentication is safe because the cluster listens on 127.0.0.1 and
    stops with the job.
    """
    _section(f"Install PostgreSQL {POSTGRES_VERSION}")
    pg_bin = _install_postgres()

    _section("Start PostgreSQL on 127.0.0.1:5432")
    # The build has only initdb, pg_ctl, and postgres: no client tools. Create
    # the database in single-user mode before the server starts.
    data_dir = Path("/tmp/pgdata")
    if data_dir.exists():
        shutil.rmtree(data_dir)
    _run([pg_bin / "initdb", "-D", data_dir, "--auth=trust", "--username=postgres",
          "--encoding=UTF8", "--locale=C"], cwd=root)
    log_stdout("+ postgres --single: CREATE DATABASE corndogs")
    created = subprocess.run([str(pg_bin / "postgres"), "--single", "-D", str(data_dir), "postgres"],
                             input="CREATE DATABASE corndogs;\n", text=True, check=False,
                             stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if created.returncode != 0 or "ERROR" in created.stdout:
        for line in created.stdout.splitlines():
            log_stdout(f"  {line}")
        raise RuntimeError("could not create the corndogs database")
    _run([pg_bin / "pg_ctl", "-D", data_dir, "-l", "/tmp/pg.log", "-w", "-t", "60",
          "-o", "-k /tmp -h 127.0.0.1 -p 5432", "start"], cwd=root)
    if not _wait_for_port("127.0.0.1", 5432, 30):
        log_stdout(Path("/tmp/pg.log").read_text(errors="replace"))
        raise RuntimeError("PostgreSQL did not accept connections in 30 seconds")

    def stop() -> None:
        _run([pg_bin / "pg_ctl", "-D", data_dir, "stop"], cwd=root, check=False)

    return stop


def _test_server(root: Path) -> None:
    """Build the server, run it against PostgreSQL, and run every Go test.

    ``go test ./...`` includes the store unit tests and the integration tests
    in corndogs/test, which call the live server on 127.0.0.1:5080.
    """
    _go_env()
    stop_postgres = _start_postgres(root)
    server_dir = root / "corndogs"
    env = {
        "STORAGE_BACKEND": "postgres",
        "DATABASE_HOST": "127.0.0.1",
        "DATABASE_PORT": "5432",
        "DATABASE_USER": "postgres",
        "DATABASE_PASSWORD": "unused-trust-auth",
        "DATABASE_NAME": "corndogs",
        "DATABASE_SSL_MODE": "disable",
        "CORNDOGS_LISTEN": "127.0.0.1:5080",
        "CORNDOGS_HTTP_LISTEN": "127.0.0.1:8080",
    }
    server: Optional[subprocess.Popen[str]] = None
    log_path = Path("/tmp/corndogs-server.log")
    try:
        _section("Build the server")
        _run(["go", "build", "-o", "/tmp/corndogs", "."], cwd=server_dir)

        _section("Start the server")
        with log_path.open("w") as log_file:
            server = subprocess.Popen(
                ["/tmp/corndogs", "run"], cwd=server_dir, env={**os.environ, **env},
                stdout=log_file, stderr=subprocess.STDOUT, text=True,
            )
        if not _wait_for_port("127.0.0.1", 5080, 60):
            raise RuntimeError("the server did not listen on 127.0.0.1:5080 in 60 seconds")

        _section("Run the Go tests")
        _run(["go", "test", "-count=1", "./..."], cwd=server_dir, env=env)
    finally:
        if server is not None:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
            log_stdout("Server log (last 40 lines):")
            for line in log_path.read_text(errors="replace").splitlines()[-40:]:
                log_stdout(f"  {line}")
        stop_postgres()


def _client_tests(root: Path) -> None:
    """Run the Go and Python clients end to end against a live file server."""
    _go_env()
    cache = _home() / ".cache" / "catalyst"
    cache.mkdir(parents=True, exist_ok=True)
    _section("Client end-to-end tests (go, python)")
    _run(["bash", "clients/run-tests.sh"], cwd=root,
         env={"LANGS": "go python", "CATALYST_CACHE": str(cache)})


def _csilgen_release(root: Path) -> str:
    text = (root / "csil" / "generate.sh").read_text()
    match = re.search(r'^CSILGEN_RELEASE="([^"]+)"', text, re.MULTILINE)
    if not match:
        raise RuntimeError("csil/generate.sh does not set CSILGEN_RELEASE")
    return match.group(1)


def _csil_gen_check(root: Path) -> None:
    """Regenerate every client and fail if the committed code differs."""
    release = _install_csilgen(_csilgen_release(root))
    _section(f"Validate and regenerate with {release}")
    _run(["csilgen", "--version"], cwd=root)
    _run(["csilgen", "validate", "--input", "csil/corndogs.csil"], cwd=root)
    _run(["bash", "csil/generate.sh"], cwd=root)
    status = _run(["git", "status", "--porcelain", "--", "clients/"], cwd=root, capture=True)
    if status.stdout.strip():
        _run(["git", "status", "--short", "--", "clients/"], cwd=root, check=False)
        _run(["git", "diff", "--stat", "--", "clients/"], cwd=root, check=False)
        raise RuntimeError("the generated clients are stale. Run ./csil/generate.sh "
                           "and commit the result, including new files")
    log_stdout("The generated clients match the CSIL contract")


def _install_csilgen(release: str) -> str:
    """Install exactly the pinned csilgen release and its generators.

    csilgen's own installer (tools.sh install-all) installs only the newest
    release. A new csilgen release would then fail this check with no change
    in corndogs. Thus this job downloads the assets of the pinned release and
    checks each one against the SHA-256 digest that GitHub records.
    """
    match = re.fullmatch(r"csilgen/v(\d+\.\d+\.\d+)", release)
    if not match:
        raise RuntimeError(f"CSILGEN_RELEASE {release!r} is not csilgen/vX.Y.Z")
    version = match.group(1)
    _section(f"Install {release}")
    info = json.loads(_fetch_text(
        f"{GITHUB_API}/repos/catalystcommunity/csilgen/releases/tags/"
        f"{release.replace('/', '%2F')}"))
    assets = {asset["name"]: asset for asset in info.get("assets", [])}

    def fetch(name: str) -> Path:
        asset = assets.get(name)
        if asset is None:
            raise RuntimeError(f"{release} has no asset {name}")
        digest = str(asset.get("digest", ""))
        if not digest.startswith("sha256:"):
            raise RuntimeError(f"{release} asset {name} has no SHA-256 digest")
        return _download(asset["browser_download_url"], Path("/tmp") / name,
                         digest.split(":", 1)[1])

    cli = fetch(f"csilgen-{version}-x86_64-unknown-linux-gnu.tar.gz")
    _install_from_tar("csilgen", cli, "csilgen")

    generators = _home() / ".csilgen" / "generators"
    if generators.exists():
        shutil.rmtree(generators)
    generators.mkdir(parents=True)
    bundle = fetch(f"csilgen-generators-{version}.tar.gz")
    with tarfile.open(bundle) as tar:
        for member in tar.getmembers():
            if member.isfile() and member.name.endswith(".wasm") and "/" not in member.name:
                extracted = tar.extractfile(member)
                (generators / member.name).write_bytes(extracted.read())
    bundle.unlink()
    count = len(list(generators.glob("*.wasm")))
    if count == 0:
        raise RuntimeError(f"{release} generator bundle has no .wasm files")
    log_stdout(f"Installed csilgen {version} and {count} generators")
    return release


def _install_helm() -> None:
    if shutil.which("helm"):
        return
    name = f"helm-v{HELM_VERSION}-linux-amd64.tar.gz"
    expected = _fetch_text(f"https://get.helm.sh/{name}.sha256sum").split()[0]
    archive = _download(f"https://get.helm.sh/{name}", Path("/tmp") / name, expected)
    _install_from_tar("helm", archive, "linux-amd64/helm")


def _helm_validate(root: Path) -> None:
    _install_helm()
    chart = "./helm_chart/chart"
    _section("Helm lint and template")
    _run(["helm", "version", "--short"], cwd=root)
    _run(["helm", "repo", "add", "bitnami", "https://charts.bitnami.com/bitnami"], cwd=root)
    _run(["helm", "dependency", "update", chart], cwd=root)
    _run(["helm", "lint", chart], cwd=root)
    scenarios = {
        "default": [],
        "file backend": ["--set", "storage.backend=file", "--set", "postgresql.enabled=false"],
        "tls": ["--set", "tls.enabled=true", "--set", "tls.secretName=corndogs-tls",
                "--set", "tls.caKey=ca.crt", "--set", "timeoutCron.enabled=true",
                "--set", "timeoutCron.schedule=*/5 * * * *"],
    }
    for name, flags in scenarios.items():
        log_stdout(f"Template: {name}")
        _run(["helm", "template", "ci", chart, *flags], cwd=root, capture=True)
    refused = {
        "file backend with 2 replicas": ["--set", "storage.backend=file",
                                         "--set", "replicaCount=2"],
        "tls without a Secret": ["--set", "tls.enabled=true"],
    }
    for name, flags in refused.items():
        result = _run(["helm", "template", "ci", chart, *flags], cwd=root,
                      capture=True, check=False)
        if result.returncode == 0:
            raise RuntimeError(f"the chart accepted an invalid configuration: {name}")
        log_stdout(f"Refused as expected: {name}")
    log_stdout("The Helm chart is valid")


def _ci_tests(root: Path) -> None:
    """Run the unit tests of this plugin from the CI checkout."""
    _section("CI plugin unit tests")
    ci = _ci_root()
    _run([sys.executable, "-m", "unittest", "discover", "-v", "-s", ".reactorcide/tests"],
         cwd=ci, env={"PYTHONDONTWRITEBYTECODE": "1"})


# ---------------------------------------------------------------------------
# Release helpers
# ---------------------------------------------------------------------------


def _github(method: str, path: str, body: dict | None = None, *,
            base: str = GITHUB_API, data: bytes | None = None,
            content_type: str = "application/json") -> dict:
    token = _require("GITHUB_PAT")
    payload = data if data is not None else (json.dumps(body).encode() if body is not None else None)
    request = urllib.request.Request(base + path, data=payload, method=method)
    request.add_header("Authorization", f"Bearer {token}")
    request.add_header("Accept", "application/vnd.github+json")
    request.add_header("X-GitHub-Api-Version", "2022-11-28")
    if payload is not None:
        request.add_header("Content-Type", content_type)
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            raw = response.read().decode()
    except urllib.error.HTTPError as err:
        detail = err.read().decode(errors="replace")[:500]
        raise RuntimeError(f"GitHub {method} {path}: HTTP {err.code}: {detail}") from None
    return json.loads(raw) if raw else {}


def _probe_github(repository: str) -> None:
    """Fail before a publish step if the token cannot push to the repository."""
    repo = _github("GET", f"/repos/{repository}")
    if not repo.get("permissions", {}).get("push"):
        raise RuntimeError(f"the GitHub token cannot push to {repository}")
    log_stdout(f"GitHub token can push to {repository}")


def _configure_git(root: Path, repository: str) -> None:
    token = _require("GITHUB_PAT")
    _run(["git", "config", "user.name", "catalystcommunityci"], cwd=root)
    _run(["git", "config", "user.email", "ci@catalystcommunity.org"], cwd=root)
    _run(["git", "remote", "set-url", "origin",
          f"https://x-access-token:{token}@github.com/{repository}.git"], cwd=root,
         show="git remote set-url origin https://x-access-token:***@github.com/"
              f"{repository}.git")
    _run(["git", "fetch", "--quiet", "--tags", "--force", "origin"], cwd=root)


def _install_semver_tags() -> None:
    if shutil.which("semver-tags"):
        return
    url = (f"https://github.com/catalystcommunity/semver-tags/releases/download/"
           f"{SEMVER_TAGS_VERSION}/semver-tags.tar.gz")
    archive = _download(url, Path("/tmp/semver-tags.tar.gz"))
    _install_from_tar("semver-tags", archive, "semver-tags")


def last_json_object(output: str) -> Optional[dict]:
    """semver-tags prints its result as the last JSON object in its output."""
    for line in reversed(output.splitlines()):
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            return value
    return None


def _semver_next(root: Path, directory: str) -> Optional[str]:
    """The next tag for commits under ``directory``, or None for no release.

    ``--dry_run`` computes the tag and creates nothing. The release creates
    the tag itself, on the bump commit, so that the tag is the HEAD of main.
    """
    _install_semver_tags()
    result = _run(["semver-tags", "run", "--dry_run", "--output_json",
                   "--directories", directory], cwd=root, capture=True)
    data = last_json_object(result.stdout)
    if data is None:
        for line in result.stdout.splitlines():
            log_stdout(f"  {line}")
        raise RuntimeError("semver-tags gave no JSON result")
    if str(data.get("New_release_published")) != "true":
        return None
    tag = str(data.get("New_release_git_tag", ""))
    if not SEMVER_TAG.match(tag):
        raise RuntimeError(f"semver-tags gave an unexpected tag: {tag!r}")
    return tag


def release_bump(commits: Sequence[Tuple[str, str]]) -> Optional[str]:
    """The version part to bump for (subject, body) commits, or None.

    The Conventional Commits rules that semver-tags uses: "!" after the type,
    or "BREAKING CHANGE" in the body, bumps major; feat bumps minor; fix and
    perf bump patch. Other types release nothing.
    """
    order = {"patch": 1, "minor": 2, "major": 3}
    bump: Optional[str] = None
    for subject, body in commits:
        match = SUBJECT_TYPE.match(subject)
        if not match:
            continue
        if match["breaking"] or "BREAKING CHANGE" in body or "BREAKING-CHANGE" in body:
            part: Optional[str] = "major"
        else:
            part = RELEASE_TYPES.get(match["type"])
        if part and (bump is None or order[part] > order[bump]):
            bump = part
    return bump


def bump_tag(tag: str, part: str) -> str:
    match = SEMVER_TAG.match(tag)
    if not match:
        raise ValueError(f"not a release tag: {tag!r}")
    major, minor, patch = int(match["major"]), int(match["minor"]), int(match["patch"])
    if part == "major":
        major, minor, patch = major + 1, 0, 0
    elif part == "minor":
        minor, patch = minor + 1, 0
    else:
        patch += 1
    return f"{match['prefix']}v{major}.{minor}.{patch}"


def next_release_tag(root: Path, prefix: str, paths: Sequence[str]) -> Optional[str]:
    """The next ``prefix`` tag for release commits under any of ``paths``."""
    tags = _run(["git", "tag", "--list", f"{prefix}v*"], cwd=root, capture=True).stdout.split()
    last = latest_tag(tags, prefix)
    if last is None:
        raise RuntimeError(f"no {prefix}v* tag exists; create the first one by hand")
    listed = _run(["git", "log", "--no-merges", "--format=%s%x1f%b%x1e", f"{last}..HEAD", "--", *paths],
                  cwd=root, capture=True)
    commits = []
    for record in listed.stdout.split("\x1e"):
        subject, _, body = record.strip("\n").partition("\x1f")
        if subject:
            commits.append((subject, body))
    bump = release_bump(commits)
    log_stdout(f"{len(commits)} commits under {', '.join(paths)} since {last}; bump: {bump or 'none'}")
    return bump_tag(last, bump) if bump else None


def tag_version(tag: str) -> str:
    match = SEMVER_TAG.match(tag)
    if not match:
        raise ValueError(f"not a release tag: {tag!r}")
    return f"{match['major']}.{match['minor']}.{match['patch']}"


def next_patch_tag(tag: str) -> str:
    match = SEMVER_TAG.match(tag)
    if not match:
        raise ValueError(f"not a release tag: {tag!r}")
    return f"{match['prefix']}v{match['major']}.{match['minor']}.{int(match['patch']) + 1}"


def latest_tag(tags: Sequence[str], prefix: str) -> Optional[str]:
    """The highest semantic version among the tags that start with prefix."""
    found = []
    for tag in tags:
        match = SEMVER_TAG.match(tag.strip())
        if match and match["prefix"] == prefix:
            found.append(((int(match["major"]), int(match["minor"]), int(match["patch"])),
                          tag.strip()))
    return max(found)[1] if found else None


def chart_field(text: str, field: str) -> Optional[str]:
    match = re.search(rf'^{field}:\s*"?([^"\s]+)"?\s*$', text, re.MULTILINE)
    return match.group(1) if match else None


def set_chart_field(text: str, field: str, value: str) -> str:
    updated, count = re.subn(rf"^{field}:.*$", f'{field}: "{value}"', text,
                             count=1, flags=re.MULTILINE)
    if count != 1:
        raise RuntimeError(f"{CHART_FILE} has no top-level {field} line")
    return updated


def _commit_and_push_tag(root: Path, field: str, version: str, tag: str,
                         message: str) -> None:
    """Commit one Chart.yaml field on the current main, tag it, and push both.

    The other release node edits the next line of Chart.yaml. A rebase of the
    commit onto its push conflicts, because git treats adjacent lines as one
    change. Thus each attempt does not rebase: it checks out the current
    origin/main, edits the field again, and commits. ``git checkout`` stops if
    the tree has changes, so it cannot discard work. The branch and the tag go
    in one atomic push, so the tag never lands without its commit.
    """
    for attempt in range(1, PUSH_ATTEMPTS + 1):
        _run(["git", "fetch", "--quiet", "origin", "main"], cwd=root)
        _run(["git", "checkout", "--quiet", "--detach", "FETCH_HEAD"], cwd=root)
        chart = root / CHART_FILE
        chart.write_text(set_chart_field(chart.read_text(), field, version))
        _run(["git", "add", str(CHART_FILE)], cwd=root)
        if _run(["git", "diff", "--cached", "--quiet"], cwd=root, check=False).returncode:
            _run(["git", "commit", "--quiet", "-m", message], cwd=root)
        else:
            log_stdout(f"{field} is already {version} on main")
        _run(["git", "tag", "-f", tag], cwd=root)
        pushed = _run(["git", "push", "--atomic", "origin", "HEAD:main", f"refs/tags/{tag}"],
                      cwd=root, check=False)
        if pushed.returncode == 0:
            log_stdout(f"Pushed {tag} and main")
            return
        _run(["git", "tag", "-d", tag], cwd=root, check=False)
        log_stdout(f"Push attempt {attempt} failed; main moved or the remote refused")
        time.sleep(attempt * 3)
    raise RuntimeError(f"could not push {tag} and main after {PUSH_ATTEMPTS} attempts")


def _create_github_release(repository: str, tag: str, *, notes: Optional[str] = None,
                           asset: Optional[Path] = None) -> None:
    body: dict = {"tag_name": tag, "name": tag}
    if notes is None:
        body["generate_release_notes"] = True
    else:
        body["body"] = notes
    release = _github("POST", f"/repos/{repository}/releases", body)
    log_stdout(f"GitHub release: {release.get('html_url')}")
    if asset is not None:
        _github("POST", f"/repos/{repository}/releases/{release['id']}/assets?name={asset.name}",
                base=GITHUB_UPLOADS, data=asset.read_bytes(), content_type="application/gzip")
        log_stdout(f"Uploaded {asset.name}")


# ---------------------------------------------------------------------------
# Release jobs
# ---------------------------------------------------------------------------


def _install_release_tools() -> None:
    if not shutil.which("crane"):
        name = "go-containerregistry_Linux_x86_64.tar.gz"
        base = (f"https://github.com/google/go-containerregistry/releases/download/"
                f"v{CRANE_VERSION}")
        sums = _fetch_text(f"{base}/checksums.txt")
        expected = next((line.split()[0] for line in sums.splitlines()
                         if line.strip().endswith(name)), None)
        if expected is None:
            raise RuntimeError(f"checksums.txt for crane {CRANE_VERSION} has no {name}")
        archive = _download(f"{base}/{name}", Path("/tmp") / name, expected)
        _install_from_tar("crane", archive, "crane")
    if not shutil.which("docker"):
        archive = _download(
            f"https://download.docker.com/linux/static/stable/x86_64/docker-{DOCKER_VERSION}.tgz",
            Path("/tmp/docker.tgz"))
        _install_from_tar("docker", archive, "docker/docker")


def _write_registry_auth(registry: str) -> None:
    docker_dir = _home() / ".docker"
    docker_dir.mkdir(parents=True, exist_ok=True)
    auth = base64.b64encode(
        f"{_require('REGISTRY_USER')}:{_require('REGISTRY_PASSWORD')}".encode()).decode()
    config = docker_dir / "config.json"
    config.write_text(json.dumps({"auths": {registry: {"auth": auth}}}))
    config.chmod(0o600)


def _release_server(root: Path) -> None:
    """Release the server when commits under SERVER_RELEASE_PATHS call for it.

    Order: compute the version, build and push the image, then commit the
    chart appVersion, tag that commit, and push both. The tag goes last
    because it is the step that announces the release.
    """
    repository = _require("REACTORCIDE_REPO")
    _configure_git(root, repository)
    tag = next_release_tag(root, "corndogs/", SERVER_RELEASE_PATHS)
    if tag is None:
        log_stdout("No server release: no feat, fix, or perf commit under "
                   f"{', '.join(p + '/' for p in SERVER_RELEASE_PATHS)} since the last tag")
        return
    version = tag_version(tag)
    registry = _require("REGISTRY")
    image = f"{registry}/{_require('IMAGE_PATH')}"
    log_stdout(f"Release {tag}: image {image}:{version}")

    _probe_github(repository)
    _install_release_tools()
    _write_registry_auth(registry)
    if not os.environ.get("DOCKER_HOST"):
        raise RuntimeError("DOCKER_HOST is not set; this job needs the docker capability")
    for _ in range(30):
        if _run(["docker", "info"], cwd=root, capture=True, check=False).returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError("the docker service did not answer in 30 seconds")

    _section(f"Build and push {image}:{version}")
    # The build context is the repository root, because the server module
    # replaces the Go client module in ../clients/corndogs.
    _run(["docker", "build", "-f", "corndogs/Dockerfile", "-t", f"{image}:{version}", "."],
         cwd=root)
    image_tar = Path("/tmp/corndogs-image.tar")
    _run(["docker", "save", f"{image}:{version}", "-o", image_tar], cwd=root)
    for published in (version, "latest"):
        _run(["crane", "push", image_tar, f"{image}:{published}"], cwd=root)
    image_tar.unlink()

    _section(f"Set the chart appVersion to {version}, tag, and push")
    _commit_and_push_tag(root, "appVersion", version, tag,
                         f"ci: bump corndogs appVersion to {version}")
    _create_github_release(repository, tag)
    log_stdout(f"Released {tag}")


def chart_release_tag(semver_tag: Optional[str], last_chart_tag: Optional[str],
                      app_version_now: Optional[str],
                      app_version_released: Optional[str]) -> Optional[str]:
    """Decide the chart release tag, or None for no release.

    A feat or fix commit under helm_chart/ gives the semver-tags result. A
    server release changes only appVersion, in a ci: commit that semver-tags
    ignores. The published chart must still point at the new server, so a
    changed appVersion gives a patch release.
    """
    if semver_tag is not None:
        return semver_tag
    if last_chart_tag is None:
        return None
    if app_version_now and app_version_now != app_version_released:
        return next_patch_tag(last_chart_tag)
    return None


def _release_helm(root: Path) -> None:
    """Release the chart after the server node finishes.

    The node runs on the current main, which includes the appVersion commit
    that the server node pushed a moment ago.
    """
    repository = _require("REACTORCIDE_REPO")
    charts_repo = _require("CHARTS_REPO")
    _configure_git(root, repository)
    _run(["git", "fetch", "--quiet", "origin", "main"], cwd=root)
    _run(["git", "checkout", "--quiet", "--detach", "FETCH_HEAD"], cwd=root)

    tags = _run(["git", "tag", "--list", "helm_chart/v*"], cwd=root, capture=True).stdout.split()
    last = latest_tag(tags, "helm_chart/")
    released_app = None
    if last is not None:
        shown = _run(["git", "show", f"{last}:{CHART_FILE}"], cwd=root, capture=True, check=False)
        released_app = chart_field(shown.stdout, "appVersion") if shown.returncode == 0 else None
    app_now = chart_field((root / CHART_FILE).read_text(), "appVersion")
    tag = chart_release_tag(_semver_next(root, "helm_chart"), last, app_now, released_app)
    if tag is None:
        log_stdout(f"No chart release: no feat or fix commit under helm_chart/, and "
                   f"appVersion {app_now} is the same as in {last}")
        return
    version = tag_version(tag)
    log_stdout(f"Release {tag}: chart {version}, appVersion {app_now} "
               f"(last chart {last}, appVersion {released_app})")

    _probe_github(repository)
    _probe_github(charts_repo)
    _install_helm()

    _section(f"Set the chart version to {version}, tag, and push")
    _commit_and_push_tag(root, "version", version, tag, f"ci: bump chart version to {version}")

    _section("Package the chart")
    _run(["helm", "repo", "add", "bitnami", "https://charts.bitnami.com/bitnami"], cwd=root)
    _run(["helm", "dependency", "update", "./helm_chart/chart"], cwd=root)
    out_dir = Path("/tmp/chart-package")
    out_dir.mkdir(parents=True, exist_ok=True)
    _run(["helm", "package", "./helm_chart/chart", "--destination", out_dir], cwd=root)
    package = out_dir / f"corndogs-{version}.tgz"
    if not package.exists():
        raise RuntimeError(f"helm package did not write {package.name}")

    _create_github_release(repository, tag, notes=f"Helm chart {version} (appVersion {app_now})",
                           asset=package)
    _push_to_charts_repo(charts_repo, package, version)
    log_stdout(f"Released {tag}")


def _push_to_charts_repo(charts_repo: str, package: Path, version: str) -> None:
    """Add the packaged chart to the charts repository.

    That repository builds its index when main changes, so this adds the file
    only.
    """
    _section(f"Push {package.name} to {charts_repo}")
    token = _require("CHARTS_GITHUB_PAT")
    clone = Path("/tmp/charts-repo")
    if clone.exists():
        shutil.rmtree(clone)
    _run(["git", "clone", "--quiet", f"https://x-access-token:{token}@github.com/{charts_repo}.git",
          clone], cwd=Path("/tmp"),
         show=f"git clone https://x-access-token:***@github.com/{charts_repo}.git {clone}")
    _run(["git", "config", "user.name", "catalystcommunityci"], cwd=clone)
    _run(["git", "config", "user.email", "ci@catalystcommunity.org"], cwd=clone)
    for attempt in range(1, PUSH_ATTEMPTS + 1):
        _run(["git", "fetch", "--quiet", "origin", "main"], cwd=clone)
        _run(["git", "checkout", "--quiet", "--detach", "FETCH_HEAD"], cwd=clone)
        shutil.copy2(package, clone / package.name)
        _run(["git", "add", package.name], cwd=clone)
        if _run(["git", "diff", "--cached", "--quiet"], cwd=clone, check=False).returncode == 0:
            log_stdout(f"{package.name} is already in {charts_repo}")
            return
        _run(["git", "commit", "--quiet", "-m", f"chore: add corndogs {version}"], cwd=clone)
        if _run(["git", "push", "origin", "HEAD:main"], cwd=clone, check=False).returncode == 0:
            return
        time.sleep(attempt * 3)
    raise RuntimeError(f"could not push {package.name} to {charts_repo}")


# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------


PR_CHECKS: Dict[str, Callable[[Path], None]] = {
    "conventional-commits": _conventional_commits,
    "test-server": _test_server,
    "client-tests": _client_tests,
    "csil-gen-check": _csil_gen_check,
    "helm-validate": _helm_validate,
    "ci-tests": _ci_tests,
}
RELEASES: Dict[str, Callable[[Path], None]] = {
    "release-server": _release_server,
    "release-helm": _release_helm,
}


class CorndogsJobsPlugin(Plugin):
    """Run the corndogs job that CORNDOGS_JOB selects."""

    def __init__(self) -> None:
        super().__init__(name="corndogs_jobs", priority=100)

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.POST_SOURCE_PREP]

    def execute(self, context: PluginContext) -> None:
        job = os.environ.get("CORNDOGS_JOB", "").strip()
        if not job:
            return  # A job that does not use this plugin.
        root = _repo_root(context)

        # git refuses a directory that another user owns, which is what a
        # checkout mounted into a container looks like.
        count = int(os.environ.get("GIT_CONFIG_COUNT", "0"))
        os.environ[f"GIT_CONFIG_KEY_{count}"] = "safe.directory"
        os.environ[f"GIT_CONFIG_VALUE_{count}"] = "*"
        os.environ["GIT_CONFIG_COUNT"] = str(count + 1)

        log_stdout(f"corndogs job: {job} (source {root}, event "
                   f"{os.environ.get('REACTORCIDE_EVENT_TYPE', 'unknown')})")
        if job in PR_CHECKS:
            if _should_run(root, job):
                PR_CHECKS[job](root)
        elif job in RELEASES:
            RELEASES[job](root)
        else:
            raise RuntimeError(f"unknown CORNDOGS_JOB value: {job}")
