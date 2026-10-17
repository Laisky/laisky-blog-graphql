#!/usr/bin/env python3
"""Run the actual SSH deployment payload with disposable Docker/Compose mocks."""
import copy
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import yaml
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


def load_workflow(path):
    """load_workflow parses YAML scalars as strings, including GitHub's `on` key."""
    return yaml.load(path.read_text(), Loader=yaml.BaseLoader)


def workflow_step(job, action):
    """workflow_step returns the unique step using the named action, independent of its display name."""
    matches = [step for step in job['steps'] if step.get('uses', '').startswith(action + '@')]
    if len(matches) != 1:
        raise AssertionError('Expected exactly one ' + action + ' step')
    return matches[0]


def expression(value, required=False):
    """expression removes optional GitHub delimiters without altering quoted literals."""
    value = value.strip()
    if value.startswith('${{') and value.endswith('}}'):
        value = value[3:-2].strip()
    elif required:
        raise AssertionError('Expected a GitHub expression, not a literal value')
    return value


def workflow_fixture(model, indent=2):
    """workflow_fixture emits disposable YAML with literal scripts and schema booleans for equivalence tests."""
    model = copy.deepcopy(model)
    def restore_booleans(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key in ('push', 'cancel-in-progress', 'persist-credentials') and item in ('true', 'false'):
                    value[key] = item == 'true'
                else:
                    restore_booleans(item)
        elif isinstance(value, list):
            for item in value:
                restore_booleans(item)
    restore_booleans(model)
    for event, settings in model.get('on', {}).items():
        if settings == '':
            model['on'][event] = None
    class FixtureDumper(yaml.SafeDumper):
        pass
    def literal(dumper, value):
        return dumper.represent_scalar('tag:yaml.org,2002:str', value, style='|' if '\n' in value else None)
    FixtureDumper.add_representer(str, literal)
    return yaml.dump(model, Dumper=FixtureDumper, sort_keys=False, indent=indent)


def deployment_payload(directory, run_attempt=1, workflow=None):
    model = load_workflow(WORKFLOW if workflow is None else workflow)
    payload = workflow_step(model['jobs']['deploy'], 'appleboy/ssh-action')['with']['script']
    payload = payload.replace('/home/laisky/repo/VPS', str(directory))
    payload = re.sub(r"\$\{\{\s*github.run_id\s*\}\}", '12345', payload)
    return re.sub(r"\$\{\{\s*github.run_attempt\s*\}\}", str(run_attempt), payload)


def release_job_decisions(workflow, message):
    """release_job_decisions executes the real prefix check and evaluates supported job selectors."""
    jobs = load_workflow(workflow)['jobs']
    if expression(jobs['check_skip']['outputs']['should_skip'], required=True) != 'steps.check.outputs.should_skip':
        raise AssertionError('Skip job output must resolve to the real check step output')
    checks = [step for step in jobs['check_skip']['steps'] if step.get('id') == 'check']
    if len(checks) != 1:
        raise AssertionError('Expected exactly one commit-prefix check step')
    check = checks[0]
    if expression(check['env']['COMMIT_MSG'], required=True) != 'github.event.head_commit.message':
        raise AssertionError('Commit message must be passed through the environment')
    script = check['run']
    if '${{' in script:
        raise AssertionError('Do not interpolate commit text into shell source')
    with tempfile.TemporaryDirectory(prefix="release-selector-contract-") as directory:
        output = Path(directory) / "outputs"
        result = subprocess.run(
            ["bash"], input=script, text=True, capture_output=True, timeout=5,
            env={"PATH": os.environ["PATH"], "COMMIT_MSG": message, "GITHUB_OUTPUT": str(output)},
        )
        if result.returncode:
            raise AssertionError(result.stderr)
        outputs = dict(line.split("=", 1) for line in output.read_text().splitlines())
    skip = outputs["should_skip"]

    def selected(name):
        """selected evaluates the workflow's restricted release expression grammar."""
        condition = expression(jobs[name]['if'])
        decisions = []
        for predicate in condition.split('&&'):
            if re.fullmatch(r"\s*needs\.check_skip\.outputs\.should_skip\s*!=\s*'true'\s*", predicate):
                decisions.append(skip.casefold() != 'true')
                continue
            marker = re.fullmatch(r"\s*!contains\(\s*github\.event\.head_commit\.message\s*,\s*'([^']*)'\s*\)\s*", predicate)
            if marker is None:
                raise AssertionError('Unsupported release selector: ' + predicate)
            decisions.append(marker.group(1).casefold() not in message.casefold())
        return all(decisions)

    return tuple(selected(name) for name in ("build_latest", "build_latest_cache", "deploy"))


class DeploymentContract(unittest.TestCase):
    def run_deploy(self, scenario, run_attempt=1, workflow=None):
        with tempfile.TemporaryDirectory(prefix="deploy-contract-") as directory:
            root = Path(directory)
            for executable in ("docker", "docker-compose"):
                path = root / executable
                path.write_text(MOCK)
                path.chmod(0o755)
            result = subprocess.run(
                ["bash", "-c", deployment_payload(root, run_attempt, workflow)],
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

    def assert_deploy_settings(self, workflow):
        """assert_deploy_settings checks actual build dependencies and deployment fields, not comments."""
        deployment = load_workflow(workflow)['jobs']['deploy']
        needs = deployment['needs']
        needs = [needs] if isinstance(needs, str) else needs
        self.assertTrue({'check_skip', 'build_latest'}.issubset(needs))
        self.assertEqual(deployment['concurrency']['group'], 'deploy-b1')
        self.assertEqual(deployment['concurrency']['cancel-in-progress'], 'false')
        step = workflow_step(deployment, 'appleboy/ssh-action')
        self.assertIsInstance(step['with']['script'], str)
        self.assertNotEqual(step['with'].get('script_stop', 'false'), 'true')

    def test_shared_b1_concurrency_does_not_cancel_deploys(self):
        self.assert_deploy_settings(WORKFLOW)

    def test_comments_cannot_mask_unsafe_deployment_fields(self):
        """test_comments_cannot_mask_unsafe_deployment_fields rejects real concurrency and dependency changes."""
        model = load_workflow(WORKFLOW)
        variants = []
        for field, value in [('group', 'unsafe-${{ github.ref }}'), ('cancel-in-progress', 'true')]:
            candidate = copy.deepcopy(model)
            candidate['jobs']['deploy']['concurrency'][field] = value
            variants.append(candidate)
        candidate = copy.deepcopy(model)
        candidate['jobs']['deploy']['needs'] = ['check_skip']
        variants.append(candidate)
        candidate = copy.deepcopy(model)
        workflow_step(candidate['jobs']['deploy'], 'appleboy/ssh-action')['uses'] = 'unrelated/action@v1'
        variants.append(candidate)
        candidate = copy.deepcopy(model)
        workflow_step(candidate['jobs']['deploy'], 'appleboy/ssh-action')['with']['script_stop'] = 'true'
        variants.append(candidate)
        with tempfile.TemporaryDirectory(prefix='deploy-field-mutations-') as directory:
            path = Path(directory) / 'ci.yml'
            for index, candidate in enumerate(variants):
                with self.subTest(mutation=index):
                    path.write_text(workflow_fixture(candidate) + '\n# group: deploy-b1\n# cancel-in-progress: false\n')
                    with self.assertRaises((AssertionError, KeyError)):
                        self.assert_deploy_settings(path)

    def test_skip_job_output_and_environment_bindings_are_enforced(self):
        """test_skip_job_output_and_environment_bindings_are_enforced keeps simulation tied to actual GitHub value flow."""
        model = load_workflow(WORKFLOW)
        variants = []
        for value in ('${{ steps.check.outputs.missing }}', 'steps.check.outputs.should_skip'):
            candidate = copy.deepcopy(model)
            candidate['jobs']['check_skip']['outputs']['should_skip'] = value
            variants.append(candidate)
        candidate = copy.deepcopy(model)
        check = next(step for step in candidate['jobs']['check_skip']['steps'] if step.get('id') == 'check')
        check['env']['COMMIT_MSG'] = 'github.event.head_commit.message'
        variants.append(candidate)
        with tempfile.TemporaryDirectory(prefix='skip-binding-mutations-') as directory:
            path = Path(directory) / 'ci.yml'
            for candidate in variants:
                path.write_text(workflow_fixture(candidate))
                with self.assertRaises((AssertionError, KeyError)):
                    release_job_decisions(path, 'docs: must skip')

    def test_equivalent_yaml_and_expression_edits_preserve_deployment(self):
        """test_equivalent_yaml_and_expression_edits_preserve_deployment accepts formatting while exercising real shell outcomes."""
        model = load_workflow(WORKFLOW)
        variants = ['# Documentation-only comment.\n' + workflow_fixture(model), workflow_fixture(model, indent=4)]
        candidate = copy.deepcopy(model)
        step = workflow_step(candidate['jobs']['deploy'], 'appleboy/ssh-action')
        step['name'] = step.get('name', '') + ' reviewed'
        variants.append(workflow_fixture(candidate))
        for trailing_newline in (True, False):
            candidate = copy.deepcopy(model)
            step = workflow_step(candidate['jobs']['deploy'], 'appleboy/ssh-action')
            step['with']['script'] = step['with']['script'].rstrip('\n') + ('\n' if trailing_newline else '')
            variants.append(workflow_fixture(candidate))
        for delimiters in (True, False):
            candidate = copy.deepcopy(model)
            for job in ('build_latest', 'build_latest_cache', 'deploy'):
                condition = expression(candidate['jobs'][job]['if'])
                candidate['jobs'][job]['if'] = '${{ ' + condition + ' }}' if delimiters else condition
            variants.append(workflow_fixture(candidate))
        candidate = copy.deepcopy(model)
        for job in ('build_latest', 'build_latest_cache', 'deploy'):
            candidate['jobs'][job]['if'] = re.sub(r'\s*!=\s*', '  !=  ', expression(candidate['jobs'][job]['if']))
        variants.append(workflow_fixture(candidate))
        candidate = copy.deepcopy(model)
        workflow_step(candidate['jobs']['deploy'], 'appleboy/ssh-action')['with']['script_stop'] = 'false'
        variants.append(workflow_fixture(candidate))
        with tempfile.TemporaryDirectory(prefix='deploy-equivalent-yaml-') as directory:
            path = Path(directory) / 'ci.yml'
            for index, source in enumerate(variants):
                with self.subTest(edit=index):
                    path.write_text(source)
                    self.assert_deploy_settings(path)
                    self.assertEqual(release_job_decisions(path, 'fix: release [publish only]'), (True, True, True))
                    self.assertEqual(release_job_decisions(path, 'docs: existing skip'), (False, False, False))
                    success, calls = self.run_deploy('success', workflow=path)
                    self.assertEqual(success.returncode, 0, success.stderr)
                    self.assertEqual(sum('up' in call for call in calls), 1)
                    failure, calls = self.run_deploy('up_failure', workflow=path)
                    self.assertEqual(failure.returncode, 17, failure.stderr)
                    self.assertEqual(sum('up' in call for call in calls), 2)
                    self.assertIn('Rollback restored', failure.stderr)

    def test_reruns_retain_distinct_rollback_images(self):
        tags = []
        for attempt in (1, 2):
            result, calls = self.run_deploy("success", run_attempt=attempt)
            self.assertEqual(result.returncode, 0, result.stderr)
            tags.extend(call[-1] for call in calls if call[1:2] == ["tag"])
        self.assertEqual(tags, ["ppcelery/laisky-blog-graphql:rollback-ci-12345-1",
                                "ppcelery/laisky-blog-graphql:rollback-ci-12345-2"])

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

    def test_release_selectors_deploy_latest_even_for_publish_only_commits(self):
        """test_release_selectors_deploy_latest_even_for_publish_only_commits executes actual selectors."""
        cases = [
            ("fix: ordinary release", (True, True, True)),
            ("fix: docs wording without a skipped prefix", (True, True, True)),
            ("fix: quoted '$()' commit text", (True, True, True)),
            ("fix: release [publish only]", (True, True, True)),
            ("fix: release [PUBLISH ONLY]", (True, True, True)),
            ("fix: release\n\n[publish only]", (True, True, True)),
        ]
        for prefix in ("docs:", "doc:", "tests:", "test:", "ci:"):
            cases.append((prefix + " existing skip", (False, False, False)))
            cases.append((prefix + " existing skip [publish only]", (False, False, False)))
        for message, expected in cases:
            with self.subTest(message=message):
                self.assertEqual(release_job_decisions(ROOT / ".github/workflows/ci.yml", message), expected)


if __name__ == "__main__":
    unittest.main(verbosity=2)
