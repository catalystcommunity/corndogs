# Corndogs CI

Reactorcide runs the CI for this repository. There are two workflows. One
Python plugin does the work of each job.

```text
.reactorcide/
  workflows/pr.yaml        pull request checks (id corndogs-pr)
  workflows/release.yaml   releases after a merge (id corndogs-release)
  plugins/plugin_corndogs_jobs.py
  tests/                   unit tests for the plugin
  secret-grants.yaml       record of the secret grants on the coordinator
```

Each workflow node starts `runnerlib run --job-command true`. The plugin runs
in the `POST_SOURCE_PREP` phase and does the job that `CORNDOGS_JOB` selects.

## Pull request checks

| Node | Runs when the pull request changes | Does |
| --- | --- | --- |
| `corndogs-conventional-commits` | any file | Checks each commit subject. |
| `corndogs-test-server` | `corndogs/`, `csil/`, `clients/corndogs/` | Builds the server and runs all Go tests against PostgreSQL and a live server. |
| `corndogs-client-tests` | `clients/`, `csil/`, `corndogs/` | Runs the Go and Python clients end to end. |
| `corndogs-csil-gen-check` | `csil/`, `clients/` | Regenerates the clients with the pinned csilgen and fails on a difference. |
| `corndogs-helm-validate` | `helm_chart/` | Lints and renders the chart, and checks that it refuses invalid values. |
| `corndogs-ci-tests` | `.reactorcide/` only | Runs the plugin unit tests. |

Reactorcide filters paths for a whole workflow only. Thus each check examines
the changed files itself (`CHECK_PATHS` in the plugin). A check with no
relevant change logs the reason and succeeds. A change under `.reactorcide/`
runs every check.

## Releases

`corndogs-release-server` runs first. If commits under `corndogs/` call for a
release, it builds and pushes the image, sets the chart `appVersion` on main,
tags that commit `corndogs/vX.Y.Z`, and makes the GitHub release.

`corndogs-release-helm` runs next, on the new main, even if the server node
failed. It releases the chart (`helm_chart/vX.Y.Z`) for a `feat` or `fix`
commit under `helm_chart/`. It also releases a patch when `appVersion`
changed since the last chart release, so each server release gives a chart
that points at it. It pushes the package to `catalystcommunity/charts`.

The release workflow runs after a merge that changes `corndogs/`, `csil/`,
`clients/corndogs/`, `helm_chart/`, or `.reactorcide/`. A retry of a failed
run uses the CI files of that run. Thus to run a fixed release plugin, merge
the fix: its merge runs the release again.

The two nodes run one after the other, so they never push `Chart.yaml` at the
same time. Each push attempt starts from the current main and edits the line
again. It never rebases, because the `version` and `appVersion` lines are
adjacent and a rebase conflicts.

The release nodes read secrets. They are not in the pull request workflow,
because a pull request that changes `.reactorcide/` runs under a profile that
denies secrets.

## Keep these names

- The coordinator CI policy names the workflow id `corndogs-pr`.
- The secret grants name the nodes `corndogs-release-server` and
  `corndogs-release-helm`.

If you change a name, change the coordinator policy or grant first.

## Test locally

```sh
python3 -m unittest discover -s .reactorcide/tests
```

To run one check in the runner image, write a job file with the node fields
and `CORNDOGS_FORCE: "1"`, which runs the check without a change test. Then:

```sh
reactorcide run-local --source-dir . --ci-dir . /path/to/job.yaml
```

No check needs root. The server test downloads a pinned PostgreSQL build
(zonky, from Maven Central) and runs it from `/tmp`, because a pull request
that changes `.reactorcide/` runs under a profile with no root. Add
`--user runner` to test as the worker user. Run the csilgen check on a copy
of the repository, because it regenerates the clients in place.

The csilgen check installs the release that `csil/generate.sh` pins, from
the GitHub release assets, and checks each SHA-256 digest. A new csilgen
release does not change this check.
