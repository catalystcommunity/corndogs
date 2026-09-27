"""Unit tests for the pure logic in plugin_corndogs_jobs.py.

Run from the repository root:

    python3 -m unittest discover -s .reactorcide/tests

The plugin imports runnerlib modules (src.logging, src.plugins). These tests
replace them with small stand-ins, so they need no runnerlib install.
"""

from __future__ import annotations

import importlib.util
import sys
import types
import unittest
from pathlib import Path


def _load_plugin():
    logging_mod = types.ModuleType("src.logging")
    logging_mod.log_stdout = lambda message: None
    plugins_mod = types.ModuleType("src.plugins")

    class Plugin:
        def __init__(self, name: str, priority: int = 0) -> None:
            self.name, self.priority = name, priority

    plugins_mod.Plugin = Plugin
    plugins_mod.PluginContext = object
    plugins_mod.PluginPhase = types.SimpleNamespace(POST_SOURCE_PREP="post_source_prep")
    src = types.ModuleType("src")
    sys.modules.update({"src": src, "src.logging": logging_mod, "src.plugins": plugins_mod})

    path = Path(__file__).resolve().parents[1] / "plugins" / "plugin_corndogs_jobs.py"
    spec = importlib.util.spec_from_file_location("plugin_corndogs_jobs", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


P = _load_plugin()

CHART = """apiVersion: v2
name: corndogs
type: application
version: "0.5.5"
appVersion: "0.7.4"

dependencies:
  - name: postgresql
    version: ~12.1.6
"""


class ChangeDetection(unittest.TestCase):
    def test_server_check_follows_server_csil_and_go_client(self):
        prefixes = P.CHECK_PATHS["test-server"]
        self.assertTrue(P.changes_are_relevant(["corndogs/server/tls.go"], prefixes))
        self.assertTrue(P.changes_are_relevant(["csil/corndogs.csil"], prefixes))
        self.assertTrue(P.changes_are_relevant(["clients/corndogs/transport.go"], prefixes))
        self.assertFalse(P.changes_are_relevant(["clients/rust/src/transport.rs"], prefixes))
        self.assertFalse(P.changes_are_relevant(["helm_chart/chart/values.yaml", "README.md"],
                                                prefixes))

    def test_ci_change_runs_every_check(self):
        for check, prefixes in P.CHECK_PATHS.items():
            with self.subTest(check=check):
                self.assertTrue(P.changes_are_relevant([".reactorcide/workflows/pr.yaml"],
                                                       prefixes))

    def test_helm_check_ignores_server_only_change(self):
        prefixes = P.CHECK_PATHS["helm-validate"]
        self.assertFalse(P.changes_are_relevant(["corndogs/APIDOCS.md"], prefixes))
        self.assertTrue(P.changes_are_relevant(["helm_chart/chart/templates/x.yaml"], prefixes))

    def test_commit_check_runs_for_any_change(self):
        prefixes = P.CHECK_PATHS["conventional-commits"]
        self.assertTrue(P.changes_are_relevant(["README.md"], prefixes))

    def test_ci_tests_follow_ci_paths_only(self):
        prefixes = P.CHECK_PATHS["ci-tests"]
        self.assertFalse(P.changes_are_relevant(["corndogs/main.go"], prefixes))
        self.assertTrue(P.changes_are_relevant([".reactorcide/plugins/x.py"], prefixes))

    def test_every_check_has_a_job(self):
        self.assertEqual(set(P.CHECK_PATHS), set(P.PR_CHECKS))


class CommitSubjects(unittest.TestCase):
    def test_accepts_conventional_subjects(self):
        for subject in ("fix: a", "feat(chart)!: b", "ci: c", "norelease: d", "docs(api): e"):
            with self.subTest(subject=subject):
                self.assertTrue(P.CONVENTIONAL_SUBJECT.match(subject))

    def test_refuses_other_subjects(self):
        for subject in ("Fix: a", "fix a", "feature: b", "fix(): c", "fix:no space"):
            with self.subTest(subject=subject):
                self.assertFalse(P.CONVENTIONAL_SUBJECT.match(subject))


class Versions(unittest.TestCase):
    def test_tag_version(self):
        self.assertEqual(P.tag_version("corndogs/v0.7.5"), "0.7.5")
        with self.assertRaises(ValueError):
            P.tag_version("v0.7.5")

    def test_next_patch(self):
        self.assertEqual(P.next_patch_tag("helm_chart/v0.5.9"), "helm_chart/v0.5.10")

    def test_latest_tag_uses_semantic_order(self):
        tags = ["helm_chart/v0.5.9", "helm_chart/v0.5.10", "corndogs/v9.0.0", "helm_chart/v0.4.99"]
        self.assertEqual(P.latest_tag(tags, "helm_chart/"), "helm_chart/v0.5.10")
        self.assertIsNone(P.latest_tag(["corndogs/v1.0.0"], "helm_chart/"))

    def test_last_json_object(self):
        out = 'log line\n{"New_release_published":"true","New_release_git_tag":"x/v1.0.0"}\n\n'
        self.assertEqual(P.last_json_object(out)["New_release_git_tag"], "x/v1.0.0")
        self.assertIsNone(P.last_json_object("nothing here"))


class ChartRelease(unittest.TestCase):
    def test_semver_result_wins(self):
        self.assertEqual(P.chart_release_tag("helm_chart/v0.6.0", "helm_chart/v0.5.5",
                                             "0.7.5", "0.7.4"), "helm_chart/v0.6.0")

    def test_changed_app_version_gives_patch(self):
        # The gap that left chart 0.5.5 on appVersion 0.7.4 after server 0.7.5.
        self.assertEqual(P.chart_release_tag(None, "helm_chart/v0.5.5", "0.7.5", "0.7.4"),
                         "helm_chart/v0.5.6")

    def test_no_change_gives_no_release(self):
        self.assertIsNone(P.chart_release_tag(None, "helm_chart/v0.5.6", "0.7.5", "0.7.5"))
        self.assertIsNone(P.chart_release_tag(None, None, "0.7.5", None))


class ChartFields(unittest.TestCase):
    def test_read_fields(self):
        self.assertEqual(P.chart_field(CHART, "version"), "0.5.5")
        self.assertEqual(P.chart_field(CHART, "appVersion"), "0.7.4")

    def test_set_field_touches_one_top_level_line(self):
        updated = P.set_chart_field(CHART, "version", "0.5.6")
        self.assertIn('version: "0.5.6"\nappVersion: "0.7.4"', updated)
        # The dependency version is indented, so it does not change.
        self.assertIn("    version: ~12.1.6", updated)
        updated = P.set_chart_field(updated, "appVersion", "0.7.5")
        self.assertEqual(P.chart_field(updated, "appVersion"), "0.7.5")

    def test_missing_field_is_an_error(self):
        with self.assertRaises(RuntimeError):
            P.set_chart_field("name: x\n", "appVersion", "1.0.0")


class Workflows(unittest.TestCase):
    """The workflow files and the plugin must agree on the job names."""

    def test_every_node_selects_a_known_job(self):
        try:
            import yaml
        except ImportError:
            self.skipTest("PyYAML is not installed")
        root = Path(__file__).resolve().parents[1] / "workflows"
        known = set(P.PR_CHECKS) | set(P.RELEASES)
        seen = set()
        for path in root.glob("*.yaml"):
            doc = yaml.safe_load(path.read_text())
            for node, spec in doc["jobs"].items():
                job = spec.get("environment", {}).get("CORNDOGS_JOB")
                with self.subTest(workflow=path.name, node=node):
                    self.assertIn(job, known)
                    self.assertEqual(node, f"corndogs-{job}")
                seen.add(job)
        self.assertEqual(seen, known)


if __name__ == "__main__":
    unittest.main()
