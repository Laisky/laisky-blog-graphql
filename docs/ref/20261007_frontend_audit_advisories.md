# Frontend Audit Advisories (2026-10-07)

`project-quality / frontend-quality` runs `pnpm audit` and fails on any finding.
Three advisories published in September–October 2026 hit the Tailwind CSS 3
build toolchain (`tailwindcss-animate > tailwindcss@3.4.19`). Sources: the GitHub
Advisory Database entries linked below, read through `gh api /advisories/<id>`.

| Advisory | Package | Fixed in | Resolution |
| --- | --- | --- | --- |
| [GHSA-68fv-2mgg-jv7q](https://github.com/advisories/GHSA-68fv-2mgg-jv7q) (high) | `source-map-js` 1.0.0–1.2.1 | 1.2.2 | `pnpm.overrides` → `^1.2.2` |
| [GHSA-rj75-hqrm-r3gf](https://github.com/advisories/GHSA-rj75-hqrm-r3gf) (moderate) | `postcss-selector-parser` < 7.1.6 | 7.1.6 | `pnpm.overrides` → `^7.1.6` (replaces the older `^6.1.3` override) |
| [GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) (high) | `braces` ≤ 3.0.3 | none published | `pnpm.auditConfig.ignoreGhsas` |

Notes:

- Tailwind 3 declares `postcss-selector-parser@^6`. Forcing 7.1.6 was verified by
  building the SPA before and after: every emitted CSS asset is byte-identical.
- `braces` is reached only through Tailwind's build-time content scanning
  (`micromatch`, `chokidar`), whose patterns come from our own
  `tailwind.config.cjs`, never from users. The stack-exhaustion DoS needs
  attacker-controlled brace patterns, so it is not reachable. Remove the ignore
  as soon as a patched `braces` is published or Tailwind 3 is retired.
- Use pnpm 10 (the CI version) to regenerate `pnpm-lock.yaml`; pnpm 12 rewrites
  the lockfile layout.
