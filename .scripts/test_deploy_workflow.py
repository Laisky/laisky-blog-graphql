#!/usr/bin/env python3
"""Run the actual SSH deployment payload with disposable Docker/Compose mocks."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/ci.yml"
MOCK = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
root = pathlib.Path(os.environ["MOCK_ROOT"])
scenario = os.environ["SCENARIO"]
args = sys.argv[1:]
with (root / "calls").open("a") as f:
    f.write(json.dumps([pathlib.Path(sys.argv[0]).name] + args) + "\n")
if pathlib.Path(sys.argv[0]).name == "docker-compose":
    print("services.derper.volumes contains unsupported option: 'create_host_path'", file=sys.stderr)
    sys.exit(0 if scenario == "success" else 17)
state = root / "restored"
if args[:2] == ["compose", "version"]:
    sys.exit(127 if scenario == "missing_v2" else 0)
if "config" in args:
    sys.exit(17 if scenario == "invalid_config" else 0)
if args[0] == "pull":
    sys.exit(19 if scenario == "pull_failure" else 0)
if args[0] == "tag":
    if scenario == "snapshot_failure" and "rollback-ci" in args[-1]: sys.exit(23)
    if args[-1].endswith(":latest"):
        if scenario == "restore_tag_failure": sys.exit(29)
        state.touch()
    sys.exit(0)
if args[:2] == ["image", "inspect"]:
    print("sha256:old" if state.exists() else "sha256:new")
    sys.exit(0)
if args[0] == "inspect":
    if scenario == "previous_inspect_failure" and not (root / "started").exists(): sys.exit(31)
    if "{{.Image}}" in args:
        if scenario == "inspect_failure" and (root / "started").exists() and not state.exists(): sys.exit(31)
        if state.exists() and scenario == "rollback_wrong_image": print("sha256:wrong")
        elif not (root / "started").exists() or state.exists(): print("sha256:old")
        elif scenario in ("wrong_image", "restore_tag_failure"): print("sha256:wrong")
        else: print("sha256:new")
    elif "{{.State.Running}}" in args:
        print("false" if scenario == "not_running" and not state.exists() else "true")
    sys.exit(0)
if "up" in args:
    (root / "started").touch()
    if state.exists(): sys.exit(29 if scenario == "rollback_failure" else 0)
    sys.exit(17 if scenario in ("up_failure", "rollback_failure", "first_deploy_failure", "diagnostics_failure", "rollback_wrong_image") else 0)
if "ps" in args:
    if "-q" not in args and scenario == "diagnostics_failure": sys.exit(41)
    if "-q" in args:
        if scenario == "previous_ps_failure" and not (root / "started").exists(): sys.exit(37)
        if scenario == "verify_ps_failure" and (root / "started").exists() and not state.exists(): sys.exit(37)
        if scenario == "multiple_previous" and not (root / "started").exists():
            print("fixture-container\nsecond-container"); sys.exit(0)
        if scenario == "empty_new" and (root / "started").exists() and not state.exists():
            sys.exit(0)
        if scenario == "multiple_new" and (root / "started").exists() and not state.exists():
            print("fixture-container\nsecond-container"); sys.exit(0)
        if scenario == "first_deploy_failure" and not (root / "started").exists(): print("")
        else: print("fixture-container")
    sys.exit(0)
print("Unexpected mock command: " + repr(args), file=sys.stderr)
sys.exit(99)
'''


def deployment_payload(directory):
    workflow = WORKFLOW.read_text()
    marker = "          script: |\n"
    if workflow.count(marker) != 1:
        raise AssertionError("Expected exactly one SSH deployment payload")
    payload = textwrap.dedent(workflow.split(marker, 1)[1])
    payload = payload.replace("/home/laisky/repo/VPS", str(directory))
    return re.sub(r"\$\{\{\s*github.run_id\s*\}\}", "12345", payload)


class DeploymentContract(unittest.TestCase):
    def run_deploy(self, scenario):
        with tempfile.TemporaryDirectory(prefix="deploy-contract-") as directory:
            root = Path(directory)
            for executable in ("docker", "docker-compose"):
                path = root / executable
                path.write_text(MOCK)
                path.chmod(0o755)
            result = subprocess.run(
                ["bash", "-c", deployment_payload(root)],
                env={**os.environ, "PATH": directory + os.pathsep + os.environ["PATH"],
                     "MOCK_ROOT": directory, "SCENARIO": scenario},
                capture_output=True, text=True, timeout=10,
            )
            calls = [json.loads(line) for line in (root / "calls").read_text().splitlines()]
            return result, calls

    def assert_no_up(self, calls):
        self.assertFalse(any("up" in call for call in calls), calls)

    def test_success(self):
        result, calls = self.run_deploy("success")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any("up" in call for call in calls))
        self.assertFalse(any(call[0] == "docker-compose" for call in calls), calls)
        self.assertTrue(any("--wait" in call for call in calls), calls)

    def test_recreation_is_scoped_and_never_pulls_again(self):
        for scenario in ("success", "up_failure"):
            with self.subTest(scenario=scenario):
                result, calls = self.run_deploy(scenario)
                for call in calls:
                    if "up" in call:
                        self.assertEqual(call[-1], "graphql")
                        self.assertIn("--no-deps", call)
                        self.assertIn("--force-recreate", call)
                        self.assertIn("--wait", call)
                        self.assertEqual(call[call.index("--pull") + 1], "never")

    def test_preflight_container_errors_stop_before_pull(self):
        for scenario, status in (("previous_ps_failure", 37), ("previous_inspect_failure", 31),
                                 ("multiple_previous", 1)):
            with self.subTest(scenario=scenario):
                result, calls = self.run_deploy(scenario)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assert_no_up(calls)
                self.assertFalse(any("pull" in call for call in calls))

    def test_missing_or_ambiguous_new_container_rolls_back(self):
        for scenario, status in (("empty_new", 1), ("multiple_new", 1), ("verify_ps_failure", 37)):
            with self.subTest(scenario=scenario):
                result, calls = self.run_deploy(scenario)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual(sum("up" in call for call in calls), 2)
                self.assertIn("Rollback restored", result.stderr)

    def test_shared_b1_concurrency_does_not_cancel_deploys(self):
        deployment = WORKFLOW.read_text().split("  deploy:\n", 1)[1]
        self.assertIn("group: deploy-b1", deployment)
        self.assertIn("cancel-in-progress: false", deployment)
        self.assertNotIn("script_stop:", deployment)

    def test_missing_v2_stops_before_pull(self):
        result, calls = self.run_deploy("missing_v2")
        self.assertNotEqual(result.returncode, 0)
        self.assert_no_up(calls)
        self.assertFalse(any("pull" in call for call in calls))

    def test_invalid_config_stops_before_pull(self):
        result, calls = self.run_deploy("invalid_config")
        self.assertEqual(result.returncode, 17)
        self.assert_no_up(calls)
        self.assertFalse(any("pull" in call for call in calls))

    def test_pull_failure_stops_before_recreation(self):
        result, calls = self.run_deploy("pull_failure")
        self.assertEqual(result.returncode, 19)
        self.assert_no_up(calls)
        self.assertFalse(any(call[1:2] == ["tag"] and call[-1].endswith(":latest") for call in calls))

    def test_snapshot_failure_stops_before_pull(self):
        result, calls = self.run_deploy("snapshot_failure")
        self.assertEqual(result.returncode, 23)
        self.assert_no_up(calls)
        self.assertFalse(any("pull" in call for call in calls))

    def test_recreation_failure_survives_successful_rollback(self):
        result, calls = self.run_deploy("up_failure")
        self.assertEqual(result.returncode, 17)
        self.assertEqual(sum("up" in call for call in calls), 2)
        self.assertIn("Rollback restored the previous image", result.stderr)
        self.assertTrue(any("--pull" in call and "never" in call for call in calls), calls)

    def test_rollback_failure_preserves_deploy_status(self):
        result, calls = self.run_deploy("rollback_failure")
        self.assertEqual(result.returncode, 17)
        self.assertEqual(sum("up" in call for call in calls), 2)
        self.assertIn("Rollback failed", result.stderr)

    def test_diagnostics_failure_preserves_original_status(self):
        result, calls = self.run_deploy("diagnostics_failure")
        self.assertEqual(result.returncode, 17)
        self.assertEqual(sum("up" in call for call in calls), 2)

    def test_inspect_failure_is_preserved_after_rollback(self):
        result, calls = self.run_deploy("inspect_failure")
        self.assertEqual(result.returncode, 31)
        self.assertEqual(sum("up" in call for call in calls), 2)

    def test_wrong_rollback_image_is_not_reported_as_recovered(self):
        result, calls = self.run_deploy("rollback_wrong_image")
        self.assertEqual(result.returncode, 17)
        self.assertIn("Rollback failed", result.stderr)
        self.assertNotIn("Rollback restored", result.stderr)

    def test_wrong_image_rolls_back_and_fails(self):
        result, calls = self.run_deploy("wrong_image")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum("up" in call for call in calls), 2)
        self.assertIn("Rollback restored", result.stderr)

    def test_not_running_rolls_back_and_fails(self):
        result, calls = self.run_deploy("not_running")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum("up" in call for call in calls), 2)

    def test_failed_restore_tag_does_not_recreate_again(self):
        result, calls = self.run_deploy("restore_tag_failure")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum("up" in call for call in calls), 1)
        self.assertIn("Rollback failed", result.stderr)

    def test_first_deploy_failure_has_no_previous_image(self):
        result, calls = self.run_deploy("first_deploy_failure")
        self.assertEqual(result.returncode, 17)
        self.assertEqual(sum("up" in call for call in calls), 1)
        self.assertIn("No previous container", result.stderr)

    def test_recovery_never_removes_saved_rollback_image(self):
        result, calls = self.run_deploy("up_failure")
        self.assertEqual(result.returncode, 17)
        self.assertTrue(any("rollback-ci-12345" in " ".join(call) for call in calls))
        self.assertFalse(any("rmi" in call or "prune" in call or "down" in call for call in calls))


if __name__ == "__main__":
    unittest.main(verbosity=2)
