// @vitest-environment node
import { readFileSync, readdirSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

import tailwindcss from '@tailwindcss/postcss';
import postcss from 'postcss';
import { beforeAll, describe, expect, it } from 'vitest';

// Behaviors that Tailwind 4 changed and src/index.css deliberately restores to Tailwind 3. The
// assertions run on the CSS that Tailwind actually compiles from this repository, not on source text.

const webRoot = resolve(__dirname, '..');
const entry = resolve(webRoot, 'src/index.css');

let compiled = '';

beforeAll(async () => {
  const result = await postcss([tailwindcss({ base: webRoot, optimize: { minify: true } })]).process(readFileSync(entry, 'utf8'), { from: entry });
  compiled = result.css;
}, 60_000);

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((item) => {
    const path = join(directory, item.name);
    if (item.isDirectory()) {
      return sourceFiles(path);
    }
    return /\.tsx?$/.test(item.name) && !/\.test\.tsx?$/.test(item.name) ? [path] : [];
  });
}

const sources = sourceFiles(resolve(webRoot, 'src')).map((path) => ({ path: relative(webRoot, path), text: readFileSync(path, 'utf8') }));

describe('Tailwind 3 parity in compiled CSS', () => {
  it('gives enabled buttons a pointer cursor', () => {
    expect(compiled).toContain('button:not(:disabled),[role=button]:not(:disabled){cursor:pointer}');
  });

  it('applies hover variants without the hover media query', () => {
    expect(compiled).toMatch(/\.hover\\:bg-accent:hover[,{]/);
    expect(compiled).toContain(':is(:where(.group):hover .group-hover\\:opacity-100){opacity:1}');
    expect(compiled).not.toMatch(/@media\s*\(hover:\s*hover\)/);
  });

  it('keeps the class-based dark selector and its specificity', () => {
    expect(compiled).toMatch(/\.dark\\:[\w\\/-]+:is\(\.dark \*\)\{/);
    expect(compiled).not.toContain(':where(.dark');
  });

  it('colors pseudo-element borders with the theme border color', () => {
    expect(compiled).toContain('*,:after,:before,::backdrop{border-color:hsl(var(--border))}');
    expect(compiled).toContain('::file-selector-button{border-color:hsl(var(--border))}');
  });
});

describe('utilities whose Tailwind 4 output changed', () => {
  it('does not offset dialog entry animations by the centering translate', () => {
    // v4 centers with the `translate` property, which the animate plugin's `transform` keyframes add
    // to instead of replacing, so v3's slide-from-left-1/2 / top-[48%] start half a dialog away.
    const offenders = sources.filter(({ text }) => /slide-(?:in-from|out-to)-(?:left-1\/2|top-\[48%\])/.test(text)).map(({ path }) => path);
    expect(offenders).toEqual([]);
  });

  it('transitions the scale property wherever a press scales an element', () => {
    // v4 `scale-*` sets the `scale` property, so a transition list naming only `transform` snaps.
    const offenders = sources.flatMap(({ path, text }) =>
      Array.from(text.matchAll(/(?:className=|cn\(|')[^\n]*\bactive:scale-[\d]+[^\n]*/g))
        .map((match) => match[0])
        .filter((line) => /transition-\[[^\]]*\]/.test(line) && !/transition-\[[^\]]*\bscale\b[^\]]*\]/.test(line))
        .map(() => path)
    );
    expect(offenders).toEqual([]);
  });
});
