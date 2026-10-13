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

/** matchingDebugID requires the emitted debugId line directive to match its paired source map. */
function matchingDebugID(source, expected) {
  // The official plugin emits this standalone footer after the generated JavaScript.
  const directive = /(?:^|\n)[ \t]*\/\/#\s*debugId=([0-9a-f-]+)[ \t]*(?:\r?\n)?$/i.exec(source);
  return directive?.[1] === expected;
}

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
      const sourceMap = JSON.parse(await readFile(mapPath, 'utf8'));
      let debugID;
      if (/\.(?:js|mjs|cjs)$/.test(scriptPath)) {
        debugID = sourceMap.debug_id;
        if (typeof debugID !== 'string' || !debugIDPattern.test(debugID)
          || !matchingDebugID(await readFile(scriptPath, 'utf8'), debugID)) throw new Error(failureMessage);
      }
      pairs.push({ scriptPath, mapPath, debugID });
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
        await chmod(target, 0o600);
      }
      manifest.push({ script, map, ...(pair.debugID ? { debug_id: pair.debugID } : {}) });
    }
    await writeFile(join(artifact, 'manifest.json'), JSON.stringify({ format: 1, files: manifest }, null, 2) + '\n', { mode: 0o600 });
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
