#!/usr/bin/env python3
"""Exercise Docker's real context filtering and the real application build identity."""

import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
MODULE = "github.com/Laisky/laisky-blog-graphql"
CANARY = b"synthetic-build-auth-canary-never-a-real-credential"
PRIVATE_GIT_PATHS = (
    ".git/config", ".git/config.lock", ".git/config.worktree", ".git/config.worktree.lock",
    ".git/hooks/private-hook", ".git/private-checkout-metadata",
    ".git/logs/private-log", ".git/worktrees/private/config",
    ".git/modules/private/config", ".git/objects/info/alternates",
    ".git/objects/info/http-alternates",
)


def run(command, cwd, *, env=None, input_text=None):
    """run executes only the owned synthetic fixture and retains useful failures."""
    child_env = dict(os.environ, **(env or {}))
    child_env = {key: value for key, value in child_env.items() if not key.startswith("GIT_")}
    child_env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
                     GIT_CONFIG_NOSYSTEM="1", GIT_TERMINAL_PROMPT="0",
                     GOWORK="off", GOENV="off")
    result = subprocess.run(command, cwd=cwd, env=child_env, input=input_text, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180)
    if result.returncode:
        raise AssertionError(f"{command[0]} failed ({result.returncode}):\n{result.stdout}")
    return result.stdout


def docker_run_commands(source):
    """docker_run_commands joins Dockerfile shell continuations without fixing their text."""
    logical = []
    pending = ""
    for line in source.splitlines():
        stripped = line.strip()
        if not pending and (not stripped or stripped.startswith("#")):
            continue
        pending += stripped.removesuffix("\\") + (" " if stripped.endswith("\\") else "")
        if not stripped.endswith("\\"):
            if pending.upper().startswith("RUN "):
                logical.append(pending[4:])
            pending = ""
    return logical


class DockerBuildIdentityTests(unittest.TestCase):
    """DockerBuildIdentityTests checks behavior rather than a whole-file snapshot."""

    @classmethod
    def setUpClass(cls):
        """setUpClass requires real tools; a missing prerequisite must not report green."""
        for name in ("docker", "git", "go"):
            if shutil.which(name) is None:
                raise RuntimeError(f"Required build-identity test tool is missing: {name}")
        cls.dockerfile = (ROOT / "Dockerfile").read_text()
        commands = docker_run_commands(cls.dockerfile)
        cls.build_command, = [command for command in commands if "go build" in command]
        installs = [shlex.split(command) for command in commands[:commands.index(cls.build_command)]
                    if "apt-get install" in command]
        if not any("git" in command for command in installs):
            raise AssertionError("The Go builder must install Git for standard VCS stamping")

    def fixture(self, directory):
        """fixture creates a complete synthetic checkout containing the actual build identity code."""
        directory.mkdir(mode=0o700)
        for name in (".dockerignore", ".gitignore", "Dockerfile"):
            shutil.copyfile(ROOT / name, directory / name)
        package = directory / "library/buildidentity"
        package.mkdir(parents=True)
        for source in (ROOT / "library/buildidentity").glob("*.go"):
            if not source.name.endswith("_test.go"):
                shutil.copyfile(source, package / source.name)
        version, = [line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines()
                    if line.strip().startswith("github.com/Laisky/errors/v2 ")]
        (directory / "go.mod").write_text(
            f"module {MODULE}\n\ngo 1.27.0\n\nrequire github.com/Laisky/errors/v2 {version}\n")
        (directory / "go.sum").write_text("\n".join(
            line for line in (ROOT / "go.sum").read_text().splitlines()
            if line.startswith("github.com/Laisky/errors/v2 " + version + " ") or
            line.startswith("github.com/Laisky/errors/v2 " + version + "/go.mod ")) + "\n")
        (directory / "main.go").write_text('''package main
import (
    "encoding/json"
    "os"
    "github.com/Laisky/laisky-blog-graphql/library/buildidentity"
)
func main() {
    source, err := buildidentity.ProcessSource("laisky-blog-graphql")
    if err != nil { panic(err) }
    if err = json.NewEncoder(os.Stdout).Encode(source); err != nil { panic(err) }
}
''')
        run(["git", "init", "-q"], directory)
        run(["git", "add", "--all"], directory)
        run(["git", "-c", "user.name=Synthetic Fixture", "-c", "user.email=fixture@example.invalid",
             "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
             "commit", "-qm", "synthetic build identity"], directory)
        revision = run(["git", "rev-parse", "HEAD"], directory).strip()
        for name in PRIVATE_GIT_PATHS:
            path = directory / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"[http]\n\textraheader = " + CANARY + b"\n")
        return revision

    def export_context(self, directory, destination):
        """export_context uses actual BuildKit COPY filtering, with no base image or execution."""
        env = dict(os.environ, BUILDX_GIT_INFO="false", BUILDX_GIT_LABELS="false")
        run(["docker", "buildx", "build", "--progress=plain", "--file", "-",
             "--output", "type=local,dest=" + str(destination), str(directory)],
            directory, env=env, input_text="FROM scratch\nCOPY . /\n")
        for name in PRIVATE_GIT_PATHS:
            self.assertFalse((destination / name).exists(), name)
        for path in destination.rglob("*"):
            if path.is_file():
                self.assertNotIn(CANARY, path.read_bytes(), str(path.relative_to(destination)))

    def build_source(self, directory):
        """build_source executes the production build command and actual ProcessSource."""
        run(["sh", "-ec", self.build_command], directory)
        return json.loads(run([str(directory / "main")], directory))

    def test_release_checkout_preserves_exact_public_revision(self):
        """test_release_checkout_preserves_exact_public_revision rejects unknown or dirty release identity."""
        with tempfile.TemporaryDirectory(prefix="graphql-build-identity-") as temporary:
            root = Path(temporary)
            expected = self.fixture(root / "context")
            self.export_context(root / "context", root / "export")
            source = self.build_source(root / "export")
            self.assertEqual(source.get("revision"), expected)
            self.assertFalse(source.get("modified", False))
            self.assertTrue((root / "export/.git/HEAD").is_file())
            self.assertTrue((root / "export/.gitignore").is_file())
            self.assertTrue(source.get("go_version"))
            self.assertEqual(source["service"], "laisky-blog-graphql")

    def test_modified_source_is_reported_truthfully(self):
        """test_modified_source_is_reported_truthfully distinguishes the same commit with changed bytes."""
        with tempfile.TemporaryDirectory(prefix="graphql-build-identity-dirty-") as temporary:
            root = Path(temporary)
            expected = self.fixture(root / "context")
            with (root / "context/main.go").open("a") as stream:
                stream.write("\n// Synthetic tracked source modification.\n")
            self.export_context(root / "context", root / "export")
            source = self.build_source(root / "export")
            self.assertEqual(source.get("revision"), expected)
            self.assertTrue(source.get("modified"))

    def test_fixture_ignores_git_environment_redirects(self):
        """test_fixture_ignores_git_environment_redirects keeps all Git operations inside the fixture."""
        with tempfile.TemporaryDirectory(prefix="graphql-build-identity-environment-") as temporary:
            root = Path(temporary)
            expected = self.fixture(root / "context")
            outside = root / "outside"
            outside.mkdir()
            (outside / "sentinel").write_bytes(CANARY)
            poisoned = {"GIT_DIR": str(outside), "GIT_WORK_TREE": str(outside),
                        "GIT_COMMON_DIR": str(outside), "GIT_INDEX_FILE": str(outside / "index"),
                        "GIT_OBJECT_DIRECTORY": str(outside), "GIT_CONFIG_COUNT": "invalid"}
            self.assertEqual(run(["git", "rev-parse", "HEAD"], root / "context", env=poisoned).strip(), expected)
            self.assertEqual(list(outside.iterdir()), [outside / "sentinel"])
            self.assertEqual((outside / "sentinel").read_bytes(), CANARY)

    def test_linked_worktree_pointer_does_not_escape_context(self):
        """test_linked_worktree_pointer_does_not_escape_context preserves an honest unknown manual build."""
        with tempfile.TemporaryDirectory(prefix="graphql-build-identity-worktree-") as temporary:
            root = Path(temporary)
            self.fixture(root / "context")
            shutil.rmtree(root / "context/.git")
            # In /app an absolute host worktree pointer is unavailable. Keep it
            # unavailable here too, rather than accidentally reading host metadata.
            outside = root / "unavailable-outside-git"
            self.assertFalse(outside.exists())
            (root / "context/.git").write_text("gitdir: " + str(outside) + "\n")
            self.export_context(root / "context", root / "export")
            source = self.build_source(root / "export")
            self.assertNotIn("revision", source)
            self.assertTrue(source.get("go_version"))
            self.assertFalse(outside.exists())


if __name__ == "__main__":
    unittest.main()

