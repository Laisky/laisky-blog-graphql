#!/usr/bin/env bash
# Verify the vendored package by rebuilding original Inspector sources.
set -euo pipefail
vendor="$(cd -- "$(dirname -- "$0")" && pwd -P)"
commit=2997156791354497acb66091c79a2409d7f14429
source_sha256=ca73a3d7fb6d702cc596cfc46efd51ee72536212a6ad29d3871a47f3f2d3a6fc
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
cd "$work"
curl --fail --silent --show-error --location "https://codeload.github.com/modelcontextprotocol/inspector/tar.gz/$commit" -o source.tgz
printf '%s  %s\n' "$source_sha256" source.tgz | sha256sum --check
mkdir source
tar -xzf source.tgz -C source --strip-components=1
cd source
cp -- "$vendor/package-lock.json" package-lock.json
node --input-type=module - "$vendor/pins.json" <<'JS'
import fs from 'node:fs';
const pins = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
for (const folder of ['.', 'client', 'server', 'cli']) {
  const file = folder + '/package.json';
  const manifest = JSON.parse(fs.readFileSync(file, 'utf8'));
  for (const key of ['dependencies', 'devDependencies']) {
    for (const [name, version] of Object.entries(pins)) {
      if (manifest[key] && Object.hasOwn(manifest[key], name)) manifest[key][name] = version;
    }
  }
  if (folder === '.') manifest.overrides = { ...manifest.overrides, ...pins };
  fs.writeFileSync(file, JSON.stringify(manifest, null, 2) + '\n');
}
JS
npm ci --ignore-scripts --no-fund --no-audit
npm audit --omit=dev --workspace=client
npm run build --workspace=client --ignore-scripts
npm pack --workspace=client --ignore-scripts --silent
cmp -- modelcontextprotocol-inspector-client-1.0.6.tgz "$vendor/inspector-client-1.0.6.tgz"
