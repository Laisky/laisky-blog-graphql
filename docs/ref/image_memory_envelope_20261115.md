# Image create memory envelope and Go soft memory limit (2026-11-15)

Sources verified on 2026-11-15 for the maintainer review of PR #94 ("the
decode budget is not a complete process-memory bound"). Cited by
`library/memlimit`, `internal/mcp/userrequests/image_memory.go` and
`cmd/user_requests_images.go`.

## Go runtime and containers

- Go 1.25 made `GOMAXPROCS` cgroup-aware; the Go 1.26 and 1.27 release notes
  do not mention the memory limit, so the soft limit is still unlimited by
  default — https://go.dev/doc/go1.26, https://go.dev/doc/go1.27.
- golang/go#75164 ("cgroup memory limit aware GOMEMLIMIT default", 90% of the
  minimum `memory.max` / `limit_in_bytes` along the hierarchy, GODEBUG
  `cgroupmemlimit`) is still an open proposal —
  https://github.com/golang/go/issues/75164.
- Go GC guide: "leave an additional 5-10% of headroom to account for memory
  sources the Go runtime is unaware of" — https://go.dev/doc/gc-guide. Hence
  `memlimit.DefaultSoftLimitRatio = 0.9`.
- cgroup v2 `memory.max` holds a byte count or the literal `max` (no limit) —
  https://github.com/torvalds/linux/blob/master/Documentation/admin-guide/cgroup-v2.rst.
  cgroup v1 reports "no limit" as a page-aligned value near MaxInt64.
- KimMachineGun/automemlimit (v1.0.1, 2026-10) is the common library: minimum
  limit walking up the cgroup hierarchy, ratio 0.9, leaves an explicit
  `GOMEMLIMIT` alone — https://pkg.go.dev/github.com/KimMachineGun/automemlimit/memlimit.
  `library/memlimit` implements the same rules without a new dependency.

## Measurement (TestImageCreateMemoryEnvelope)

Eight concurrent creates (four identities, the shipped caps: 8 per process,
2 per identity, 5 images, 20 MiB raw, 1.25 GiB decode budget), each with four
1536 x 1536 16-bit noise PNGs (the largest normalized output, ~18 MiB, held
until its upload ends) and one 8192 x 8192 image (the largest decode working
set). Objects were discarded by the fake store, so only server memory counts.
Peak RSS sampled every 2 ms from `/proc/self/status`, Go 1.27, linux/amd64:

| Go soft limit | Peak RSS above idle | Envelope | Ratio |
|---|---|---|---|
| none (GOGC=100), run 1 | 4637 MiB | 2307 MiB | 2.01 |
| none (GOGC=100), run 2 | 4869 MiB | 2307 MiB | 2.11 |
| baseline + envelope, run 1 | 2327 MiB | 2307 MiB | 1.01 |
| baseline + envelope, run 2 | 2329 MiB | 2307 MiB | 1.01 |

Envelope = decode budget 1280 MiB + input buffers 8 x (18 MiB form memory +
20 MiB raw attachment) = 304 MiB + prepared outputs 8 x 5 x 18.08 MiB =
723 MiB. Before the fix the retained PNG was the encoder's `bytes.Buffer`,
whose capacity reached 20 MiB for an 18 MiB image (up to 2x in general).

Conclusions applied in PR #94:

- The decode budget alone bounds about half of the create path; the envelope
  is computed and logged at start, and a defaulted decode budget is lowered so
  the envelope fits half the process memory limit (floor 256 MiB). An
  explicitly configured budget is kept; an envelope that still does not fit is
  an ERROR telling the operator to lower `max_concurrent_requests`.
- Without a soft limit the heap reached twice the live set, so `api` sets the
  Go soft limit to 90% of the container limit unless `GOMEMLIMIT` is in the
  environment (`GOMEMLIMIT=off` opts out).
