#!/usr/bin/env python3
"""Prepare only the public shallow HEAD objects needed for native Go VCS stamping.

The real checkout's Git directory never belongs in a Docker context.  A local
depth-one transfer into a new repository avoids copying dangling objects, old
commits, checkout authentication, hooks, worktree pointers and alternate stores.
"""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

DIRECTORY = ".docker-build-git"


def git(root, *args):
    """git ignores caller environment redirects and global configuration."""
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
               GIT_CONFIG_NOSYSTEM="1", GIT_TERMINAL_PROMPT="0", GIT_ALLOW_PROTOCOL="file")
    result = subprocess.run(
        ["git", "-c", "core.hooksPath=/dev/null", *args], cwd=root, env=env,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=120)
    if result.returncode:
        raise RuntimeError("Public build Git metadata preparation failed")
    return result.stdout.strip()


def prepare(root):
    """prepare leaves working bytes intact and refuses any existing output."""
    root = root.resolve(strict=True)
    target = root / DIRECTORY
    if target.exists() or target.is_symlink():
        raise RuntimeError("Reserved build metadata path already exists; nothing overwritten")
    revision = git(root, "rev-parse", "--verify", "HEAD")
    if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", revision):
        raise RuntimeError("Invalid source revision")
    object_format = git(root, "rev-parse", "--show-object-format")
    if object_format != "sha1":
        raise RuntimeError("Only this project's SHA-1 Git format is supported")
    # file transport deliberately uses upload-pack: a plain local clone can
    # ignore --depth and hardlink the entire unrelated source object store.
    with tempfile.TemporaryDirectory(prefix="graphql-public-build-git-") as temporary:
        clone = Path(temporary)
        git(clone, "init", "--quiet", "--template=", "--object-format=" + object_format)
        git(clone, "fetch", "--quiet", "--depth=1", "--no-tags",
            "--", root.as_uri(), revision)
        git(clone, "update-ref", "--no-deref", "HEAD", revision)
        git(clone, "read-tree", "HEAD")
        if git(clone, "rev-parse", "HEAD") != revision:
            raise RuntimeError("Prepared revision does not match source")
        shallow = clone / ".git/shallow"
        if not shallow.is_file() or shallow.read_text().strip() != revision:
            raise RuntimeError("Prepared metadata is not a single shallow revision")
        reachable = set(git(clone, "rev-list", "--objects", "--no-object-names", "HEAD").splitlines())
        stored = set(git(clone, "cat-file", "--batch-all-objects",
                         "--batch-check=%(objectname)").splitlines())
        if stored != reachable:
            raise RuntimeError("Prepared object store contains unrelated objects")
        metadata = clone / ".git"
        for path in metadata.rglob("*"):
            if path.is_symlink() or not (path.is_dir() or path.is_file()):
                raise RuntimeError("Unexpected prepared metadata entry")
        if any((metadata / name).exists() for name in (
                "objects/info/alternates", "objects/info/http-alternates",
                "info/grafts", "commondir", "config.worktree")):
            raise RuntimeError("Prepared metadata is not self-contained")
        if git(root, "rev-parse", "--verify", "HEAD") != revision:
            raise RuntimeError("Source revision changed during preparation")
        # mkdir exclusively claims the output, including a concurrent creator.
        # A failed copy stays visible as an incomplete preparation; never reuse
        # or recursively delete a caller's reserved path.
        target.mkdir(mode=0o700)
        (target / "refs").mkdir()  # Git requires this directory even for detached HEAD.
        for name in ("HEAD", "index", "shallow"):
            shutil.copyfile(metadata / name, target / name)
        shutil.copytree(metadata / "objects", target / "objects")
    return {"revision": revision, "object_count": len(stored), "directory": DIRECTORY}


def main():
    """main emits public identity only; no checkout configuration is read out."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    print(json.dumps(prepare(args.root)))


if __name__ == "__main__":
    main()
