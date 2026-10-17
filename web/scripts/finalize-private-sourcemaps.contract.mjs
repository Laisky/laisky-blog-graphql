import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { copyFile, mkdtemp, mkdir, readFile, readdir, rm, stat, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { finalizePrivateSourceMaps, hasSourceMapDirective } from './finalize-private-sourcemaps.mjs';

const debugID = '12345678-1234-4123-8123-123456789abc';
const webDirectory = resolve(dirname(fileURLToPath(import.meta.url)), '..');

/** fixture provides synthetic private maps in a disposable checkout and separate scratch directory. */
async function fixture(context, withMaps = true) {
  const root = await mkdtemp(join(tmpdir(), 'mcp-map-test-'));
  context.after(() => rm(root, { recursive: true, force: true }));
  const checkout = join(root, 'checkout');
  const dist = join(checkout, 'web/dist');
  const scratch = join(root, 'scratch');
  await mkdir(join(dist, 'assets'), { recursive: true });
  await mkdir(scratch);
  const scriptPath = join(dist, 'assets/index-fixture.js');
  const script = `console.log("sourceMappingURL=literal");\n//# debugId=${debugID}\n`;
  await writeFile(scriptPath, script);
  const mapPath = `${scriptPath}.map`;
  const map = { version: 3, sources: ['fixture.ts'], sourcesContent: ['synthetic private source'], mappings: '', debug_id: debugID };
  if (withMaps) await writeFile(mapPath, JSON.stringify(map));
  return { checkout, dist, scratch, scriptPath, script, mapPath, map };
}

test('opt-in quarantine preserves exact Debug ID pairs outside checkout and removes served maps', async (context) => {
  const data = await fixture(context);
  const artifact = await finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch });
  assert.ok(relative(data.checkout, artifact).startsWith(`..${sep}`));
  assert.ok(artifact.startsWith(data.scratch + sep));
  assert.equal(await readFile(join(artifact, 'assets/index-fixture.js'), 'utf8'), data.script);
  assert.deepEqual(JSON.parse(await readFile(join(artifact, 'assets/index-fixture.js.map'), 'utf8')), data.map);
  assert.equal((await stat(artifact)).mode & 0o777, 0o700);
  assert.equal((await stat(join(artifact, 'assets/index-fixture.js.map'))).mode & 0o777, 0o600);
  assert.deepEqual(JSON.parse(await readFile(join(artifact, 'manifest.json'), 'utf8')), {
    format: 1, repaired_count: 0, files: [{ script: 'assets/index-fixture.js', map: 'assets/index-fixture.js.map', debug_id: debugID,
      source_map_sha256: createHash('sha256').update(JSON.stringify(data.map)).digest('hex'),
      prepared_map_sha256: createHash('sha256').update(JSON.stringify(data.map)).digest('hex'), repaired: false }],
  });
  assert.deepEqual(await readdir(join(data.dist, 'assets')), ['index-fixture.js']);
  assert.equal(await readFile(data.scriptPath, 'utf8'), data.script);
});

test('default build creates no private artifacts and rejects unexpected public maps', async (context) => {
  const plain = await fixture(context, false);
  assert.equal(await finalizePrivateSourceMaps({ distDir: plain.dist, checkoutDir: plain.checkout, scratchRoot: plain.scratch }), undefined);
  assert.deepEqual(await readdir(plain.scratch), []);
  await writeFile(plain.mapPath, JSON.stringify(plain.map));
  await assert.rejects(finalizePrivateSourceMaps({ distDir: plain.dist, checkoutDir: plain.checkout, scratchRoot: plain.scratch }),
    { message: 'Private source map quarantine failed.' });
});

test('rejects scratch roots inside dist or checkout before copying and cleans served maps', async (context) => {
  const data = await fixture(context);
  for (const scratchRoot of [data.dist, data.checkout, join(data.dist, 'assets')]) {
    await writeFile(data.mapPath, JSON.stringify(data.map));
    const before = (await readdir(scratchRoot)).filter((name) => !name.endsWith('.map'));
    await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot }),
      { message: 'Private source map quarantine failed.' });
    assert.deepEqual(await readdir(scratchRoot), before);
    await assert.rejects(readFile(data.mapPath, 'utf8'), { code: 'ENOENT' });
  }
});

test('permits ordinary symlink scratch ancestry after resolving its real location', async (context) => {
  const data = await fixture(context);
  const alias = join(data.checkout, 'temporary-alias');
  await symlink(data.scratch, alias, 'dir');
  const artifact = await finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: alias });
  assert.ok(artifact.startsWith(data.scratch + sep));
  assert.deepEqual(await readdir(join(data.dist, 'assets')), ['index-fixture.js']);
});

test('quarantine and pair validation failures abort and remove collected public maps', async (context) => {
  const data = await fixture(context);
  const badScratch = join(data.scratch, 'not-a-directory');
  await writeFile(badScratch, 'fixture');
  await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: badScratch }),
    { message: 'Private source map quarantine failed.' });
  await assert.rejects(readFile(data.mapPath, 'utf8'), { code: 'ENOENT' });
  await writeFile(data.mapPath, JSON.stringify({ ...data.map, debug_id: '87654321-1234-4123-8123-123456789abc' }));
  await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch }),
    { message: 'Private source map quarantine failed.' });
  assert.deepEqual(await readdir(data.scratch), ['not-a-directory']);
  await assert.rejects(readFile(data.mapPath, 'utf8'), { code: 'ENOENT' });
  assert.equal(await readFile(data.scriptPath, 'utf8'), data.script);
});

test('detects actual directive comments without rejecting ordinary string literals', () => {
  assert.equal(hasSourceMapDirective('console.log("//# sourceMappingURL=literal.map");'), false);
  assert.equal(hasSourceMapDirective("const text = '/*# sourceMappingURL=literal.map */';"), false);
  assert.equal(hasSourceMapDirective('const text = `\n//# sourceMappingURL=literal.map\n`;'), false);
  assert.equal(hasSourceMapDirective('// Regular comment mentions sourceMappingURL=literal.map'), false);
  assert.equal(hasSourceMapDirective('console.log(1);\n//# sourceMappingURL=private.map'), true);
  assert.equal(hasSourceMapDirective('console.log(1);/*# sourceMappingURL=data:application/json;base64,synthetic */'), true);
});

test('served inline map directives fail closed even without public map files', async (context) => {
  const data = await fixture(context, false);
  await writeFile(data.scriptPath, 'console.log(1);\n//# sourceMappingURL=data:application/json;base64,synthetic');
  await assert.rejects(finalizePrivateSourceMaps({ distDir: data.dist, checkoutDir: data.checkout }),
    { message: 'Private source map quarantine failed.' });
});

test('configuration keeps maps opt-in, disables uploads and gates release mutations', async () => {
  const config = await readFile(join(webDirectory, 'vite.config.ts'), 'utf8');
  assert.ok(config.includes("process.env.SENTRY_BUILD_PRIVATE_SOURCEMAPS === 'true'"));
  assert.ok(config.includes("sourcemaps: { disable: 'disable-upload' }"));
  assert.ok(config.includes('telemetry: false'));
  assert.ok(config.includes('release: { inject: false, create: false, finalize: false }'));
  assert.ok(config.includes("sourcemap: privateSourceMaps ? 'hidden' : false"));
  assert.ok(config.includes('plugins: [react(),'));
  const pkg = JSON.parse(await readFile(join(webDirectory, 'package.json'), 'utf8'));
  assert.equal(pkg.devDependencies['@sentry/vite-plugin'], '5.4.1');
  assert.equal(pkg.scripts.build, 'tsc -b && vite build');
  assert.ok(config.includes('privateSourceMapQuarantine],'));
  assert.ok(config.includes("apply: 'build'"));
  assert.ok(config.includes("order: 'post'"));
  assert.ok(config.includes('sequential: true'));
  assert.ok(config.includes('await finalizePrivateSourceMaps({ enabled: privateSourceMaps, distDir: outputDirectory })'));
});

test('a UUID string occurrence does not substitute for its emitted Debug ID directive', async (context) => {
  const data = await fixture(context);
  await writeFile(data.scriptPath, `console.log("${debugID}");\n`);
  await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch }),
    { message: 'Private source map quarantine failed.' });
  await assert.rejects(readFile(data.mapPath, 'utf8'), { code: 'ENOENT' });
  assert.equal(await readFile(data.scriptPath, 'utf8'), `console.log("${debugID}");\n`);
  assert.deepEqual(await readdir(data.scratch), []);
});

test('copy failure removes only collected maps and preserves JavaScript and unrelated files', async (context) => {
  const data = await fixture(context);
  const unrelated = join(data.dist, 'unrelated.txt');
  await writeFile(unrelated, 'fixture must survive');
  let copies = 0;
  await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout,
    scratchRoot: data.scratch, copyFileFn: async () => { copies += 1; throw new Error('synthetic copy failure'); } }),
  { message: 'Private source map quarantine failed.' });
  assert.equal(copies, 1);
  await assert.rejects(readFile(data.mapPath, 'utf8'), { code: 'ENOENT' });
  assert.equal(await readFile(data.scriptPath, 'utf8'), data.script);
  assert.equal(await readFile(unrelated, 'utf8'), 'fixture must survive');
});

test('repairs only the private map copy when both IDs are absent, retaining all original source-map fields', async (context) => {
  const data = await fixture(context);
  const map = { version: 3, sources: ['../src/private-fixture.ts'], sourcesContent: ['synthetic private source'],
    names: ['renderFixture'], mappings: 'AAAA,CAAC', file: 'index-fixture.js', sourceRoot: '../', extra: { retain: true } };
  const original = JSON.stringify(map, null, 2);
  await writeFile(data.mapPath, original);
  const script = `globalThis._sentryDebugIdIdentifier="sentry-dbid-${debugID}";\n`
    + `globalThis._sentryDebugIds["fixture-stack"]="${debugID}";\n//# debugId=${debugID}\n`;
  await writeFile(data.scriptPath, script);
  let originalObserved;
  const artifact = await finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch,
    copyFileFn: async (source, target) => {
      if (source === data.mapPath) originalObserved = await readFile(source, 'utf8');
      await copyFile(source, target);
    } });
  assert.equal(originalObserved, original, 'never rewrite the public source-map input');
  const preparedBytes = await readFile(join(artifact, 'assets/index-fixture.js.map'));
  assert.deepEqual(JSON.parse(preparedBytes.toString('utf8')), { ...map, debug_id: debugID, debugId: debugID });
  assert.equal(await readFile(data.scriptPath, 'utf8'), script);
  assert.equal(await readFile(join(artifact, 'assets/index-fixture.js'), 'utf8'), script);
  await assert.rejects(readFile(data.mapPath), { code: 'ENOENT' });
  const manifest = JSON.parse(await readFile(join(artifact, 'manifest.json'), 'utf8'));
  assert.equal(manifest.repaired_count, 1);
  assert.equal(manifest.files[0].repaired, true);
  assert.equal(manifest.files[0].source_map_sha256, createHash('sha256').update(original).digest('hex'));
  assert.equal(manifest.files[0].prepared_map_sha256, createHash('sha256').update(preparedBytes).digest('hex'));
  assert.notEqual(manifest.files[0].source_map_sha256, manifest.files[0].prepared_map_sha256);
});

for (const fields of [
  { debug_id: debugID, debugId: debugID },
  { debugId: debugID },
]) {
  test(`preserves existing valid ID fields without repair: ${Object.keys(fields).join(', ')}`, async (context) => {
    const data = await fixture(context);
    const { debug_id: _removed, ...map } = data.map;
    const original = JSON.stringify({ ...map, ...fields }, null, 2);
    await writeFile(data.mapPath, original);
    const artifact = await finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch });
    assert.equal(await readFile(join(artifact, 'assets/index-fixture.js.map'), 'utf8'), original);
    const manifest = JSON.parse(await readFile(join(artifact, 'manifest.json'), 'utf8'));
    assert.equal(manifest.repaired_count, 0);
    assert.equal(manifest.files[0].source_map_sha256, manifest.files[0].prepared_map_sha256);
  });
}

for (const [label, fields] of [
  ['invalid present underscore', { debug_id: 'invalid' }],
  ['null present underscore', { debug_id: null }],
  ['invalid present camel case', { debugId: 'invalid' }],
  ['mismatched underscore', { debug_id: '87654321-1234-4123-8123-123456789abc' }],
  ['inconsistent dual fields', { debug_id: debugID, debugId: '87654321-1234-4123-8123-123456789abc' }],
]) {
  test(`rejects ${label} and removes served maps`, async (context) => {
    const data = await fixture(context);
    const { debug_id: _removed, ...map } = data.map;
    await writeFile(data.mapPath, JSON.stringify({ ...map, ...fields }));
    await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch }),
      { message: 'Private source map quarantine failed.' });
    await assert.rejects(readFile(data.mapPath), { code: 'ENOENT' });
    assert.equal(await readFile(data.scriptPath, 'utf8'), data.script);
  });
}

for (const [label, script] of [
  ['only runtime ID', `globalThis._sentryDebugIdIdentifier="sentry-dbid-${debugID}";`],
  ['two official footers', `//# debugId=${debugID}\n//# debugId=${debugID}\n`],
  ['inconsistent runtime identifier', `globalThis._sentryDebugIdIdentifier="sentry-dbid-87654321-1234-4123-8123-123456789abc";\n//# debugId=${debugID}\n`],
  ['inconsistent runtime table', `globalThis._sentryDebugIds["fixture"]="87654321-1234-4123-8123-123456789abc";\n//# debugId=${debugID}\n`],
]) {
  test(`does not repair a missing-ID map with ${label}`, async (context) => {
    const data = await fixture(context);
    const { debug_id: _removed, ...map } = data.map;
    await writeFile(data.mapPath, JSON.stringify(map));
    await writeFile(data.scriptPath, script);
    await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch }),
      { message: 'Private source map quarantine failed.' });
    await assert.rejects(readFile(data.mapPath), { code: 'ENOENT' });
    assert.equal(await readFile(data.scriptPath, 'utf8'), script);
  });
}

for (const [label, fields] of [
  ['non-array sources', { sources: 'synthetic.ts', mappings: 'AAAA' }],
  ['non-string mappings', { sources: ['synthetic.ts'], mappings: ['AAAA'] }],
]) {
  test(`does not repair missing IDs with ${label}`, async (context) => {
    const data = await fixture(context);
    await writeFile(data.mapPath, JSON.stringify({ version: 3, ...fields }));
    await assert.rejects(finalizePrivateSourceMaps({ enabled: true, distDir: data.dist, checkoutDir: data.checkout, scratchRoot: data.scratch }),
      { message: 'Private source map quarantine failed.' });
    await assert.rejects(readFile(data.mapPath), { code: 'ENOENT' });
    assert.equal(await readFile(data.scriptPath, 'utf8'), data.script);
    assert.deepEqual(await readdir(data.scratch), []);
  });
}
