import { createHash } from 'node:crypto';
import { chmod, copyFile, mkdir, mkdtemp, readFile, readdir, realpath, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const webDirectory = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const failureMessage = 'Private source map quarantine failed.';
const debugIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** isWithin checks containment without confusing sibling path prefixes. */
function isWithin(parent, child) {
  const path = relative(parent, child);
  return path === '' || (!isAbsolute(path) && path !== '..' && !path.startsWith(`..${sep}`));
}

/** collectFiles rejects symlink entries instead of copying sources beyond the build directory. */
async function collectFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isSymbolicLink()) throw new Error(failureMessage);
    if (entry.isDirectory()) files.push(...await collectFiles(path));
    else if (entry.isFile()) files.push(path);
  }
  return files;
}

/** directiveComments yields real comment text while skipping quoted JavaScript values. */
function* directiveComments(source) {
  for (let index = 0; index < source.length; index += 1) {
    const quote = source[index];
    if (quote === '"' || quote === "'" || quote === '`') {
      index += 1;
      while (index < source.length && source[index] !== quote) {
        if (source[index] === '\\') index += 1;
        index += 1;
      }
    } else if (source.startsWith('//', index)) {
      const end = source.indexOf('\n', index + 2);
      yield { kind: 'line', text: source.slice(index + 2, end < 0 ? source.length : end).trim() };
      index = end < 0 ? source.length : end;
    } else if (source.startsWith('/*', index)) {
      const end = source.indexOf('*/', index + 2);
      yield { kind: 'block', text: source.slice(index + 2, end < 0 ? source.length : end).trim() };
      index = end < 0 ? source.length : end + 1;
    }
  }
}

/** hasSourceMapDirective examines actual directive comments rather than arbitrary string text. */
export function hasSourceMapDirective(source) {
  return Array.from(directiveComments(source)).some(({ text }) => /^[#@]\s*sourceMappingURL\s*=/.test(text));
}

/** officialDebugID validates one official footer and corroborates any injected runtime identifier. */
function officialDebugID(source) {
  const lines = [...source.matchAll(/^[ \t]*\/\/#\s*debugId=([^\r\n]*)$/gm)];
  const footer = /(?:^|\n)[ \t]*\/\/#\s*debugId=([0-9a-f-]+)[ \t]*(?:\r?\n)?$/i.exec(source);
  const debugID = footer?.[1];
  if (lines.length !== 1 || typeof debugID !== 'string' || !debugIDPattern.test(debugID)) throw new Error(failureMessage);
  const runtimeIdentifiers = [...source.matchAll(/_sentryDebugIdIdentifier\s*=\s*["']([^"']+)["']/g)];
  if (runtimeIdentifiers.some((match) => match[1] !== `sentry-dbid-${debugID}`)) throw new Error(failureMessage);
  const runtimeTable = [...source.matchAll(/_sentryDebugIds\[[^\]\r\n]+\]\s*=\s*["']([^"']+)["']/g)];
  if (runtimeTable.some((match) => match[1] !== debugID)) throw new Error(failureMessage);
  return debugID;
}

/** prepareMap preserves present IDs and repairs only a private flat V3 copy missing both ID fields. */
function prepareMap(sourceBytes, sourceMap, debugID) {
  if (!sourceMap || typeof sourceMap !== 'object' || Array.isArray(sourceMap)
    || sourceMap.version !== 3 || Object.hasOwn(sourceMap, 'sections')
    || !Array.isArray(sourceMap.sources) || typeof sourceMap.mappings !== 'string') throw new Error(failureMessage);
  const hasUnderscore = Object.hasOwn(sourceMap, 'debug_id');
  const hasCamelCase = Object.hasOwn(sourceMap, 'debugId');
  for (const key of ['debug_id', 'debugId']) {
    if (Object.hasOwn(sourceMap, key) && (typeof sourceMap[key] !== 'string'
      || !debugIDPattern.test(sourceMap[key]) || sourceMap[key] !== debugID)) throw new Error(failureMessage);
  }
  if (hasUnderscore || hasCamelCase) return { bytes: sourceBytes, repaired: false };
  return { bytes: Buffer.from(JSON.stringify({ ...sourceMap, debug_id: debugID, debugId: debugID }) + '\n'), repaired: true };
}

/** sha256 identifies exact original and privately prepared map bytes without exposing their source. */
function sha256(bytes) { return createHash('sha256').update(bytes).digest('hex'); }

/** finalizePrivateSourceMaps quarantines matching build artifacts before removing served maps. */
export async function finalizePrivateSourceMaps({ enabled = false, distDir = join(webDirectory, 'dist'),
  checkoutDir = resolve(webDirectory, '..'), scratchRoot = tmpdir(), copyFileFn = copyFile } = {}) {
  let collectedMaps = [];
  try {
    const [dist, checkout] = await Promise.all([realpath(distDir), realpath(checkoutDir)]);
    const files = await collectFiles(dist);
    const maps = files.filter((path) => path.endsWith('.map'));
    collectedMaps = maps;
    const scripts = files.filter((path) => /\.(?:js|mjs|cjs|css)$/.test(path));
    for (const script of scripts) {
      if (hasSourceMapDirective(await readFile(script, 'utf8'))) throw new Error(failureMessage);
    }
    if (!enabled) {
      if (maps.length > 0) throw new Error(failureMessage);
      return undefined;
    }
    const scratch = await realpath(scratchRoot);
    if (isWithin(checkout, scratch) || isWithin(dist, scratch)) throw new Error(failureMessage);
    if (maps.length === 0) throw new Error(failureMessage);
    const pairs = [];
    for (const mapPath of maps) {
      const scriptPath = mapPath.slice(0, -4);
      if (!files.includes(scriptPath)) throw new Error(failureMessage);
      const sourceBytes = await readFile(mapPath);
      const sourceMap = JSON.parse(sourceBytes.toString('utf8'));
      let debugID;
      let prepared = { bytes: sourceBytes, repaired: false };
      if (/\.(?:js|mjs|cjs)$/.test(scriptPath)) {
        debugID = officialDebugID(await readFile(scriptPath, 'utf8'));
        prepared = prepareMap(sourceBytes, sourceMap, debugID);
      }
      pairs.push({ scriptPath, mapPath, debugID, sourceBytes, prepared });
    }
    if (!pairs.some((pair) => pair.debugID)) throw new Error(failureMessage);
    const artifactDir = await mkdtemp(join(scratch, 'mcp-private-sourcemaps-'));
    await chmod(artifactDir, 0o700);
    const artifact = await realpath(artifactDir);
    if (isWithin(checkout, artifact) || isWithin(dist, artifact)) throw new Error(failureMessage);
    const manifest = [];
    for (const pair of pairs) {
      const script = relative(dist, pair.scriptPath);
      const map = relative(dist, pair.mapPath);
      for (const source of [pair.scriptPath, pair.mapPath]) {
        const target = join(artifact, relative(dist, source));
        await mkdir(dirname(target), { recursive: true, mode: 0o700 });
        await copyFileFn(source, target);
        if (source === pair.mapPath && pair.prepared.repaired) await writeFile(target, pair.prepared.bytes);
        await chmod(target, 0o600);
      }
      manifest.push({ script, map, ...(pair.debugID ? { debug_id: pair.debugID } : {}),
        source_map_sha256: sha256(pair.sourceBytes), prepared_map_sha256: sha256(pair.prepared.bytes), repaired: pair.prepared.repaired });
    }
    await writeFile(join(artifact, 'manifest.json'), JSON.stringify({ format: 1, repaired_count: pairs.filter((pair) => pair.prepared.repaired).length, files: manifest }, null, 2) + '\n', { mode: 0o600 });
    return artifact;
  } catch { throw new Error(failureMessage); }
  finally {
    const cleanup = await Promise.allSettled(collectedMaps.map((map) => rm(map, { force: true })));
    if (cleanup.some((result) => result.status === 'rejected')) throw new Error(failureMessage);
  }
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  try {
    const artifact = await finalizePrivateSourceMaps({ enabled: process.env.SENTRY_BUILD_PRIVATE_SOURCEMAPS === 'true' });
    if (artifact) console.log(`Private source maps retained at ${artifact}`);
  } catch {
    console.error(failureMessage);
    process.exitCode = 1;
  }
}
