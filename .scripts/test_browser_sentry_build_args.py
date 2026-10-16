#!/usr/bin/env python3
"""Behavioral contracts for public browser arguments and unchanged release controls."""

import hashlib
import importlib.util
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

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
    """WorkflowContract permits only public argument forwarding in the release workflow."""

    @classmethod
    def setUpClass(cls):
        """setUpClass reads repository workflow fixtures, never runner secrets."""
        cls.ci = (ROOT / '.github/workflows/ci.yml').read_text()
        cls.contract = (ROOT / '.github/workflows/deployment-contract.yml').read_text()

    def test_release_workflow_diff_is_exactly_the_argument_bridge(self):
        original = self.ci
        original = original.replace('    outputs:\n      browser_sentry_build_args: ${{ steps.browser_sentry.outputs.build_args }}\n', '', 1)
        step = '''      - name: Prepare public MCP browser reporting arguments
        id: browser_sentry
        env:
          MCP_SENTRY_ENABLED: ${{ vars.MCP_SENTRY_ENABLED }}
          MCP_SENTRY_DSN: ${{ vars.MCP_SENTRY_DSN }}
          MCP_SENTRY_ENVIRONMENT: ${{ vars.MCP_SENTRY_ENVIRONMENT }}
          SOURCE_SHA: ${{ github.sha }}
        run: python3 .scripts/prepare_browser_sentry.py --github-output "$GITHUB_OUTPUT"

'''
        self.assertEqual(original.count(step), 1)
        original = original.replace(step, '', 1)
        for reference in ('steps.browser_sentry.outputs.build_args', 'needs.build_latest.outputs.browser_sentry_build_args'):
            line = '          build-args: ${{ ' + reference + ' }}\n'
            self.assertEqual(original.count(line), 1)
            original = original.replace(line, '', 1)
        baseline = subprocess.run(
            ['git', 'show', '4471050910d21fe5bca26bd9be0af6fc5ad9e0e5:.github/workflows/ci.yml'],
            cwd=ROOT, capture_output=True, text=True, check=False,
        )
        # A checked-in digest also verifies shallow CI clones where the original commit is unavailable.
        self.assertEqual(hashlib.sha256(original.encode()).hexdigest(), 'de272b53428256e6a0916125a3aae39e7bdacc673f8b44167f397dfabeb2ccda')
        if baseline.returncode == 0:
            self.assertEqual(original, baseline.stdout)

    def test_contract_trigger_and_permissions_unchanged(self):
        header = self.contract.split('jobs:\n')[0]
        for path in ('.scripts/prepare_browser_sentry.py', '.scripts/test_browser_sentry_build_args.py'):
            line = "      - '" + path + "'\n"
            self.assertEqual(header.count(line), 1)
            header = header.replace(line, '', 1)
        self.assertEqual(hashlib.sha256(header.encode()).hexdigest(),
                         '8561e718f0afea83c3f69c3b1c7f04b3adad06abe0f6d284de46856362d7f082')
        self.assertIn('python3 .scripts/test_deploy_workflow.py\n', self.contract)
        self.assertIn('python3 .scripts/test_browser_sentry_build_args.py\n', self.contract)

    def test_exact_public_inputs_and_docker_builder_arguments(self):
        variables = re.findall(r'\$\{\{ vars\.([^ }]+) \}\}', self.ci)
        self.assertEqual(variables, ['MCP_SENTRY_ENABLED', 'MCP_SENTRY_DSN', 'MCP_SENTRY_ENVIRONMENT'])
        dockerfile = (ROOT / 'Dockerfile').read_text()
        self.assertEqual(re.findall(r'^ARG (VITE_SENTRY_[A-Z]+)$', dockerfile, re.MULTILINE), list(CONFIG.FIELDS))
        self.assertNotRegex(dockerfile, r'(?im)^(?:ARG|ENV) .*SENTRY.*TOKEN')
        self.assertNotIn('os.environ.items', HELPER.read_text())


if __name__ == '__main__':
    unittest.main(verbosity=2)
