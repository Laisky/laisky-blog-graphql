"""Keep every Go toolchain pin aligned with the root go.mod.

The root ``go.mod`` ``go`` directive (or its ``toolchain`` directive, when
present) is the single source of truth for the Go release. Hand-written pins
drifted from it more than once (docs said 1.27.0 and 1.27.2 while go.mod,
CI and the Docker builder had moved to 1.27.3), so this test fails whenever:

* a ``golang:<version>`` builder image in a tracked Dockerfile differs;
* an ``actions/setup-go`` step pins a different ``go-version`` (a
  ``go-version-file: go.mod`` step is always aligned, and a ``major.minor``
  pin must name the same minor release);
* ``docs/ref/go_toolchain.md`` does not state the current version.

Run directly (``python3 .scripts/test_go_toolchain.py``); ``make lint`` and the
Linter workflow run it too.
"""
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

GO_DIRECTIVE = re.compile(r"^go\s+(\d+\.\d+(?:\.\d+)?)\s*$", re.MULTILINE)
TOOLCHAIN_DIRECTIVE = re.compile(r"^toolchain\s+go(\d+\.\d+(?:\.\d+)?)\s*$", re.MULTILINE)
DOCKER_GOLANG = re.compile(r"^\s*FROM\s+(?:--\S+\s+)*golang:(\d+\.\d+(?:\.\d+)?)", re.MULTILINE | re.IGNORECASE)
SETUP_GO = re.compile(r"uses:\s*actions/setup-go@")
GO_VERSION = re.compile(r"^\s*go-version:\s*['\"]?([^'\"\s#]+)['\"]?")
GO_VERSION_FILE = re.compile(r"^\s*go-version-file:\s*['\"]?([^'\"\s#]+)['\"]?")


def module_go_version(go_mod_text):
    """Return the Go release the root module selects (toolchain wins over go)."""
    toolchain = TOOLCHAIN_DIRECTIVE.search(go_mod_text)
    if toolchain:
        return toolchain.group(1)
    directive = GO_DIRECTIVE.search(go_mod_text)
    if not directive:
        raise RuntimeError("go.mod has no go directive")
    return directive.group(1)


def setup_go_pins(workflow_text):
    """Yield (line_number, kind, value) for each setup-go step's version input."""
    lines = workflow_text.splitlines()
    for index, line in enumerate(lines):
        if not SETUP_GO.search(line):
            continue
        indent = len(line) - len(line.lstrip(" -"))
        found = False
        for offset in range(index + 1, min(index + 12, len(lines))):
            candidate = lines[offset]
            stripped = candidate.strip()
            if stripped.startswith("- ") or (stripped and len(candidate) - len(candidate.lstrip()) <= indent - 2):
                break
            version = GO_VERSION.match(candidate)
            version_file = GO_VERSION_FILE.match(candidate)
            if version:
                yield offset + 1, "go-version", version.group(1)
                found = True
            elif version_file:
                yield offset + 1, "go-version-file", version_file.group(1)
                found = True
        if not found:
            yield index + 1, "missing", ""


def drift(root):
    """Return human-readable drift findings for the tree at root."""
    want = module_go_version((root / "go.mod").read_text())
    want_minor = ".".join(want.split(".")[:2])
    problems = []
    tracked = subprocess.run(["git", "ls-files"], cwd=root, check=True, capture_output=True, text=True).stdout.split()
    for name in tracked:
        path = root / name
        base = path.name.lower()
        if base == "dockerfile" or base.startswith("dockerfile.") or base.endswith(".dockerfile"):
            for match in DOCKER_GOLANG.finditer(path.read_text()):
                if match.group(1) != want:
                    problems.append(f"{name}: golang:{match.group(1)} differs from go.mod {want}")
        if name.startswith(".github/workflows/") and name.endswith((".yml", ".yaml")):
            for line, kind, value in setup_go_pins(path.read_text()):
                if kind == "missing":
                    problems.append(f"{name}:{line}: setup-go step has no go-version or go-version-file")
                elif kind == "go-version-file" and value != "go.mod":
                    problems.append(f"{name}:{line}: go-version-file {value} is not the root go.mod")
                elif kind == "go-version" and value not in (want, want_minor):
                    problems.append(f"{name}:{line}: go-version {value} differs from go.mod {want}")
    doc = root / "docs/ref/go_toolchain.md"
    if doc.exists() and not re.search(r"(?<!\d)(?<!\d\.)" + re.escape(want) + r"(?!\.?\d)", doc.read_text()):
        problems.append(f"docs/ref/go_toolchain.md does not state the go.mod version {want}")
    return problems


class TestRepositoryAlignment(unittest.TestCase):
    def test_repository_pins_match_go_mod(self):
        self.assertEqual([], drift(ROOT))


class TestNegativeControls(unittest.TestCase):
    """Prove the checker reports each kind of drift it claims to catch."""

    def tree(self, go_mod, dockerfile, workflow, doc):
        directory = Path(tempfile.mkdtemp())
        subprocess.run(["git", "init", "-q"], cwd=directory, check=True)
        (directory / ".github/workflows").mkdir(parents=True)
        (directory / "docs/ref").mkdir(parents=True)
        (directory / "go.mod").write_text(go_mod)
        (directory / "Dockerfile").write_text(dockerfile)
        (directory / ".github/workflows/ci.yml").write_text(workflow)
        (directory / "docs/ref/go_toolchain.md").write_text(doc)
        subprocess.run(["git", "add", "-A"], cwd=directory, check=True)
        return directory

    GO_MOD = "module example.com/m\n\ngo 1.27.3\n"
    DOCKERFILE = "FROM golang:1.27.3-bookworm AS gobuild\nFROM debian:bookworm\n"
    WORKFLOW = (
        "jobs:\n  a:\n    steps:\n      - uses: actions/setup-go@v6\n        with:\n"
        "          go-version: '1.27.3'\n          cache: true\n"
        "      - uses: actions/setup-go@v5\n        with:\n          go-version-file: go.mod\n"
        "      - uses: actions/setup-go@v6\n        with:\n          go-version: 1.27\n"
    )
    DOC = "The repository targets Go 1.27.3.\n"

    def test_aligned_tree_is_clean(self):
        self.assertEqual([], drift(self.tree(self.GO_MOD, self.DOCKERFILE, self.WORKFLOW, self.DOC)))

    def test_toolchain_directive_wins(self):
        go_mod = self.GO_MOD + "\ntoolchain go1.27.4\n"
        problems = drift(self.tree(go_mod, self.DOCKERFILE, self.WORKFLOW, self.DOC))
        self.assertTrue(any("Dockerfile" in p for p in problems), problems)

    def test_each_drift_kind_is_reported(self):
        cases = {
            "Dockerfile": (self.DOCKERFILE.replace("1.27.3", "1.27.0"), self.WORKFLOW, self.DOC),
            "go-version 1.27.2": (self.DOCKERFILE, self.WORKFLOW.replace("'1.27.3'", "'1.27.2'"), self.DOC),
            "go-version 1.26": (self.DOCKERFILE, self.WORKFLOW.replace("go-version: 1.27", "go-version: 1.26"), self.DOC),
            "go-version-file": (self.DOCKERFILE, self.WORKFLOW.replace("go-version-file: go.mod", "go-version-file: tools/go.mod"), self.DOC),
            "no go-version": (self.DOCKERFILE, self.WORKFLOW.replace("          go-version: '1.27.3'\n", ""), self.DOC),
            "go_toolchain.md": (self.DOCKERFILE, self.WORKFLOW, "The repository targets Go 1.27.0.\n"),
        }
        for needle, (dockerfile, workflow, doc) in cases.items():
            with self.subTest(needle=needle):
                problems = drift(self.tree(self.GO_MOD, dockerfile, workflow, doc))
                self.assertTrue(any(needle in p for p in problems), problems)

    def test_longer_version_is_not_a_match(self):
        problems = drift(self.tree(self.GO_MOD, self.DOCKERFILE, self.WORKFLOW, "Targets Go 1.27.31.\n"))
        self.assertTrue(any("go_toolchain.md" in p for p in problems), problems)


if __name__ == "__main__":
    unittest.main()
