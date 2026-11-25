# golangci-lint configuration notes

Facts about `.golangci.lint.yml` that are not obvious from the file itself.
Measured with golangci-lint v2.15.0, the version CI pins in
`.github/workflows/project-quality.yml`.

## Deprecations

- `gomodguard` is deprecated since v2.12.0 and replaced by `gomodguard_v2`.
  The config enables `gomodguard_v2` without settings, so it blocks nothing
  until allow/block lists are added.
- `stylecheck` no longer exists as a separate linter in v2 (it is part of
  `staticcheck`). Do not reference it in `exclusions.rules`.

## `exclusions.warn-unused`

`warn-unused: true` makes every exclusion path or rule that matched zero
issues print a `WARN`. Treat those warnings as a prompt to delete the entry,
not as noise. The template-era paths and rules were removed for this reason.

## revive runs no rules

In golangci-lint, configuring any `settings.revive.rules` entry replaces
revive's default rule set; only the listed rules run. The config lists a
single disabled rule (`indent-error-flow`), so revive reports nothing.

Removing the `rules` block (revive defaults, `tests: false`) reported 294
issues: 188 `exported` (missing doc comments on exported symbols, which
`AGENTS.md` requires), 49 `unused-parameter`, 27 `var-naming`,
27 `package-comments`, 2 `blank-imports`, 1 `error-return`. Turning revive on
needs those fixed or explicitly excluded first.

## Test files are not linted

`run.tests: false` skips `_test.go` files, so the test-only linters
(`paralleltest`, `testpackage`, `thelper`, `tparallel`, `testableexamples`)
never report. Switching to `tests: true` with the current linter set reported
about 3,700 issues, led by `paralleltest` (1,912), `goconst` (886),
`testpackage` (387) and `noctx` (216).

## gosec G404

G404 (weak random generator) is excluded in `settings.gosec.excludes`. Without
it, 5 findings appear; every `math/rand` use is jitter, seeded deterministic
resampling, or search-provider rotation.
