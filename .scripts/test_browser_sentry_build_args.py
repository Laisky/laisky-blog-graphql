#!/usr/bin/env python3
"""Behavioral contracts for public browser arguments and unchanged release controls."""

import copy
import importlib.util
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import unittest

from test_deploy_workflow import expression, load_workflow, workflow_fixture, workflow_step

ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / '.scripts/prepare_browser_sentry.py'
sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location('browser_args', HELPER)
CONFIG = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CONFIG)
DSN = 'https://0123456789abcdef0123456789abcdef@o123.ingest.sentry.io/456'
SHA = 'abcdef0123456789abcdef0123456789abcdef01'


class ArgumentsContract(unittest.TestCase):
    """ArgumentsContract executes the preparation code with synthetic public inputs."""

    def invoke(self, values, initial=''):
        """invoke runs the real CLI without inheriting credentials or making network calls."""
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'output'
            output.write_text(initial)
            environment = {'PATH': os.defpath, **values}
            result = subprocess.run(
                [sys.executable, str(HELPER), '--github-output', str(output)],
                env=environment, capture_output=True, text=True, timeout=10, check=False,
            )
            return result, output.read_text()

    def test_missing_and_off_emit_exactly_five_blank_fields(self):
        for enabled in ('', 'false', 'TRUE', 'true\n', 'private-value'):
            with self.subTest(enabled=enabled):
                values = CONFIG.prepare_arguments(enabled, 'private\nDSN', 'private', 'private')
                self.assertEqual(values, dict.fromkeys(CONFIG.FIELDS, ''))
                result, output = self.invoke({'MCP_SENTRY_ENABLED': enabled, 'MCP_SENTRY_DSN': 'private'})
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout + result.stderr, '')
                self.assertEqual(output, CONFIG.output_record(values))

    def test_valid_inputs_and_derived_site_release(self):
        for environment in ('development', 'staging', 'production'):
            with self.subTest(environment=environment):
                result, output = self.invoke({
                    'MCP_SENTRY_ENABLED': 'true', 'MCP_SENTRY_DSN': DSN,
                    'MCP_SENTRY_ENVIRONMENT': environment, 'SOURCE_SHA': SHA,
                    'SENTRY_AUTH_TOKEN': 'private-secret', 'VITE_SENTRY_SITE': 'sso',
                    'VITE_SENTRY_RELEASE': 'private-release', 'MCP_SENTRY_RELEASE': 'private-release',
                })
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout + result.stderr, '')
                expected = dict(zip(CONFIG.FIELDS, ('true', 'mcp', DSN, environment, SHA)))
                self.assertEqual(output, CONFIG.output_record(expected))
                self.assertNotIn('private', output)
                self.assertLess(len(output), 700)
                self.assertEqual(output.splitlines()[1:-1], [name + '=' + expected[name] for name in CONFIG.FIELDS])

    def test_invalid_opt_in_is_static_and_does_not_modify_output(self):
        bad_dsns = (
            '', 'private-secret', DSN.replace('https:', 'http:'), DSN.replace('@', ':private@'),
            DSN.replace('ingest.sentry.io', 'evil.test'), DSN + '?private=token', DSN + '#private',
            DSN.replace('.io/', '.io:8443/'), DSN + '\nVITE_OTHER=private',
            DSN.replace('o123', 'O123'), DSN.replace('456', 'private'), 'x' * 257,
            DSN.replace('https', 'HTTPS'), DSN.replace('012345', 'Ａ12345'),
        )
        cases = [(dsn, 'staging', SHA) for dsn in bad_dsns]
        cases += [(DSN, environment, SHA) for environment in ('', 'private@example.com', 'staging\nprivate', 'x' * 10000)]
        cases += [(DSN, 'staging', sha) for sha in ('', SHA[:7], SHA.upper(), 'private', SHA + '\nprivate')]
        for dsn, environment, sha in cases:
            with self.subTest(dsn=dsn[:30], environment=environment[:30], sha=sha[:30]):
                result, output = self.invoke({
                    'MCP_SENTRY_ENABLED': 'true', 'MCP_SENTRY_DSN': dsn,
                    'MCP_SENTRY_ENVIRONMENT': environment, 'SOURCE_SHA': sha,
                }, 'previous-output\n')
                self.assertEqual(result.returncode, 2)
                self.assertEqual(result.stdout, '')
                self.assertEqual(result.stderr, CONFIG.INVALID + '\n')
                self.assertEqual(output, 'previous-output\n')

    def test_unwritable_output_and_bad_cli_do_not_echo_input(self):
        result = subprocess.run([sys.executable, str(HELPER), 'private'], capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, '')
        self.assertEqual(result.stderr, CONFIG.INVALID + '\n')
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(
                [sys.executable, str(HELPER), '--github-output', directory], env={'PATH': os.defpath},
                capture_output=True, text=True, check=False,
            )
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, '')
        self.assertEqual(result.stderr, CONFIG.INVALID + '\n')

    def test_project_identifier_length_boundary(self):
        for size, accepted in ((20, True), (21, False)):
            result, output = self.invoke({
                'MCP_SENTRY_ENABLED': 'true', 'MCP_SENTRY_DSN': DSN.rsplit('/', 1)[0] + '/' + '1' * size,
                'MCP_SENTRY_ENVIRONMENT': 'staging', 'SOURCE_SHA': SHA,
            })
            self.assertEqual(result.returncode, 0 if accepted else 2)
            self.assertEqual(result.stdout, '')
            self.assertEqual(result.stderr, '' if accepted else CONFIG.INVALID + '\n')
            self.assertEqual(bool(output), accepted)


class WorkflowContract(unittest.TestCase):
    """WorkflowContract checks public argument flow and actual release boundaries without snapshotting unrelated YAML."""

    @classmethod
    def setUpClass(cls):
        cls.ci = (ROOT / '.github/workflows/ci.yml').read_text()
        cls.contract = (ROOT / '.github/workflows/deployment-contract.yml').read_text()
        cls.dockerfile = (ROOT / 'Dockerfile').read_text()

    def assert_release_workflow(self, model):
        """assert_release_workflow protects publishing scope and explicit public-only argument forwarding."""
        self.assertEqual(set(model['on']), {'push'})
        triggers = model['on']['push']
        self.assertNotIn('tags', triggers)
        self.assertIn(triggers.get('tags-ignore'), (None, ['**']))
        self.assertEqual(set(triggers['branches']), {'master', 'main', 'test/ci'})
        self.assertEqual(set(triggers['paths-ignore']), {'docs/**', 'doc/**', 'tests/**', 'test/**', 'ci/**'})
        jobs = model['jobs']
        build = jobs['build_latest']
        needs = build['needs']
        self.assertIn('check_skip', [needs] if isinstance(needs, str) else needs)
        bridge = [step for step in build['steps'] if step.get('id') == 'browser_sentry']
        self.assertEqual(len(bridge), 1)
        bridge = bridge[0]
        expected = {name: 'vars.' + name for name in ('MCP_SENTRY_ENABLED', 'MCP_SENTRY_DSN', 'MCP_SENTRY_ENVIRONMENT')}
        expected['SOURCE_SHA'] = 'github.sha'
        self.assertEqual({name: expression(value, required=True) for name, value in bridge['env'].items()}, expected)
        command = shlex.split(bridge['run'])
        self.assertEqual(command[0], 'python3')
        command = [command[0]] + [argument for argument in command[1:] if argument not in ('-B', '-u')]
        self.assertEqual(command, ['python3', '.scripts/prepare_browser_sentry.py', '--github-output', '$GITHUB_OUTPUT'])
        self.assertEqual(expression(build['outputs']['browser_sentry_build_args'], required=True), 'steps.browser_sentry.outputs.build_args')
        publish = workflow_step(build, 'docker/build-push-action')['with']
        self.assertEqual(expression(publish['build-args'], required=True), 'steps.browser_sentry.outputs.build_args')
        self.assertEqual(publish['push'], 'true')
        self.assertEqual(publish['context'], '.')
        self.assertEqual({tag.strip() for tag in publish['tags'].splitlines() if tag.strip()}, {'ppcelery/laisky-blog-graphql:latest', 'ppcelery/laisky-blog-graphql:${{ steps.vars.outputs.short_sha }}'})
        cache = jobs['build_latest_cache']
        needs = cache['needs']
        self.assertTrue({'check_skip', 'build_latest'}.issubset([needs] if isinstance(needs, str) else needs))
        cache_build = workflow_step(cache, 'docker/build-push-action')['with']
        self.assertEqual(expression(cache_build['build-args'], required=True), 'needs.build_latest.outputs.browser_sentry_build_args')
        self.assertEqual(cache_build['push'], 'false')
        self.assertEqual(cache_build['context'], '.')

    def assert_contract_workflow(self, model):
        """assert_contract_workflow checks read-only contract triggers and real script commands."""
        self.assertEqual(set(model['on']), {'pull_request', 'workflow_dispatch'})
        required = {'.github/workflows/ci.yml', '.github/workflows/deployment-contract.yml',
                    '.scripts/test_deploy_workflow.py', '.scripts/prepare_browser_sentry.py',
                    '.scripts/test_browser_sentry_build_args.py', '.scripts/requirements-deployment-tests.txt', 'Dockerfile'}
        self.assertTrue(required.issubset(model['on']['pull_request']['paths']))
        self.assertTrue({'branches', 'branches-ignore', 'paths-ignore'}.isdisjoint(model['on']['pull_request']))
        self.assertEqual(model['permissions'], {'contents': 'read'})
        job = model['jobs']['shell-contract']
        checkout = workflow_step(job, 'actions/checkout')
        self.assertEqual(checkout['with']['persist-credentials'], 'false')
        commands = [shlex.split(line, comments=True) for step in job['steps'] for line in step.get('run', '').splitlines()]
        for script in ('.scripts/test_deploy_workflow.py', '.scripts/test_browser_sentry_build_args.py'):
            self.assertTrue(any(command and command[0] == 'python3' and script in command[1:] for command in commands))
        self.assertTrue(any(command[:4] == ['python3', '-m', 'pip', 'install'] and
                            '.scripts/requirements-deployment-tests.txt' in command for command in commands))

    def assert_docker_arguments(self, source):
        """assert_docker_arguments treats independent public declarations as a set while excluding credential arguments."""
        declarations = re.findall(r'^\s*ARG\s+(VITE_SENTRY_[A-Z]+)\s*(?:#.*)?$', source, re.MULTILINE | re.IGNORECASE)
        self.assertEqual(set(declarations), set(CONFIG.FIELDS))
        self.assertNotRegex(source, r'(?im)^\s*(?:ARG|ENV)\s+.*SENTRY.*TOKEN')
        self.assertNotIn('os.environ.items', HELPER.read_text())

    def test_public_argument_flow_and_release_boundaries(self):
        self.assert_release_workflow(load_workflow(ROOT / '.github/workflows/ci.yml'))

    def test_contract_triggers_permissions_and_commands(self):
        self.assert_contract_workflow(load_workflow(ROOT / '.github/workflows/deployment-contract.yml'))

    def test_public_docker_arguments_exclude_credentials(self):
        self.assert_docker_arguments(self.dockerfile)

    def test_equivalent_metadata_and_ordering_are_accepted(self):
        """test_equivalent_metadata_and_ordering_are_accepted proves comments, names and declaration order are incidental."""
        model = load_workflow(ROOT / '.github/workflows/ci.yml')
        variants = ['# Workflow documentation.\n' + workflow_fixture(model)]
        candidate = copy.deepcopy(model)
        bridge = next(step for step in candidate['jobs']['build_latest']['steps'] if step.get('id') == 'browser_sentry')
        bridge['name'] = bridge.get('name', '') + ' reviewed'
        variants.append(workflow_fixture(candidate))
        candidate = copy.deepcopy(model)
        bridge = next(step for step in candidate['jobs']['build_latest']['steps'] if step.get('id') == 'browser_sentry')
        bridge['env'] = dict(reversed(list(bridge['env'].items())))
        variants.append(workflow_fixture(candidate))
        candidate = copy.deepcopy(model)
        candidate['on']['push']['tags-ignore'] = ['**']
        variants.append(workflow_fixture(candidate))
        with tempfile.TemporaryDirectory(prefix='public-workflow-equivalence-') as directory:
            path = Path(directory) / 'workflow.yml'
            for source in variants:
                path.write_text(source)
                self.assert_release_workflow(load_workflow(path))
            contract = load_workflow(ROOT / '.github/workflows/deployment-contract.yml')
            contract['name'] = contract.get('name', '') + ' reviewed'
            path.write_text(workflow_fixture(contract))
            self.assert_contract_workflow(load_workflow(path))
        lines = self.dockerfile.splitlines(True)
        positions = [index for index, line in enumerate(lines) if re.match(r'^\s*ARG\s+VITE_SENTRY_', line, re.IGNORECASE)]
        declarations = [lines[index] for index in positions]
        for index, declaration in zip(positions, reversed(declarations)):
            lines[index] = declaration
        self.assert_docker_arguments(''.join(lines))

    def test_unsafe_release_and_contract_changes_are_rejected(self):
        """test_unsafe_release_and_contract_changes_are_rejected distinguishes safety changes from harmless formatting."""
        model = load_workflow(ROOT / '.github/workflows/ci.yml')
        variants = []
        for event in ('pull_request_target', 'workflow_dispatch'):
            candidate = copy.deepcopy(model)
            candidate['on'][event] = {}
            variants.append(candidate)
        for field, patterns in [('tags', ['**']), ('tags-ignore', ['private-*'])]:
            candidate = copy.deepcopy(model)
            candidate['on']['push'][field] = patterns
            variants.append(candidate)
        candidate = copy.deepcopy(model)
        bridge = next(step for step in candidate['jobs']['build_latest']['steps'] if step.get('id') == 'browser_sentry')
        bridge['env']['MCP_SENTRY_DSN'] = '${{ secrets.SENTRY_AUTH_TOKEN }}'
        variants.append(candidate)
        candidate = copy.deepcopy(model)
        candidate['jobs']['build_latest']['outputs']['browser_sentry_build_args'] = 'private-value'
        variants.append(candidate)
        candidate = copy.deepcopy(model)
        workflow_step(candidate['jobs']['build_latest_cache'], 'docker/build-push-action')['with']['push'] = 'true'
        variants.append(candidate)
        candidate = copy.deepcopy(model)
        candidate['on']['push']['branches'] = ['unsafe-release-branch']
        variants.append(candidate)
        contract = load_workflow(ROOT / '.github/workflows/deployment-contract.yml')
        contract_variants = []
        candidate = copy.deepcopy(contract)
        candidate['permissions']['contents'] = 'write'
        contract_variants.append(candidate)
        candidate = copy.deepcopy(contract)
        candidate['on']['push'] = {}
        contract_variants.append(candidate)
        candidate = copy.deepcopy(contract)
        candidate['on']['pull_request']['paths'].remove('Dockerfile')
        contract_variants.append(candidate)
        candidate = copy.deepcopy(contract)
        candidate['on']['pull_request']['branches'] = ['restricted-base']
        contract_variants.append(candidate)
        with tempfile.TemporaryDirectory(prefix='public-workflow-mutations-') as directory:
            path = Path(directory) / 'workflow.yml'
            for candidate in variants:
                path.write_text(workflow_fixture(candidate))
                with self.assertRaises((AssertionError, KeyError)):
                    self.assert_release_workflow(load_workflow(path))
            for candidate in contract_variants:
                path.write_text(workflow_fixture(candidate))
                with self.assertRaises((AssertionError, KeyError)):
                    self.assert_contract_workflow(load_workflow(path))
        with self.assertRaises(AssertionError):
            self.assert_docker_arguments(self.dockerfile + '\nARG SENTRY_AUTH_TOKEN\n')


if __name__ == '__main__':
    unittest.main(verbosity=2)
