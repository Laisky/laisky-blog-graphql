# Deployment false success, 2026-10-08

## Confirmed cause

At baseline `fcc856f51f318cf8beeea884556755820350d399`, CI run
[37670431288](https://github.com/Laisky/laisky-blog-graphql/actions/runs/37670431288),
[deploy job 112962019619](https://github.com/Laisky/laisky-blog-graphql/actions/runs/37670431288/job/112962019619)
reported success despite a Compose error at 2026-10-07 18:58:21 UTC:
`services.derper.volumes contains unsupported option: 'create_host_path'`.
The subsequent `docker ps` listed the GraphQL container created four days earlier.

Two independent defects combined: B1's legacy `docker-compose` v1.28.4 could not
parse its current Compose configuration, and the remote script ran the next
command after failure. The final successful `docker ps` became the SSH exit status.
The pinned action's local shell options do not enable remote fail-fast behavior.
No SSO implementation change is needed for this deployment defect.

Read-only B1 checks on 2026-10-08 confirmed Compose plugin 2.39.4 is already
installed, `docker compose ... config --quiet` succeeds, and the exact rollout
flags are supported. GraphQL's service block contains no interpolated environment
variables; configuration warnings concern other services. GraphQL runs the manually
deployed image `sha256:6b68f9e836d00f54ecca8d14621bea7857e5d3cb7881ebccdc7035f7b2c88201`.
No production failure injection or configuration mutation was used to diagnose it.

## Behavioral RED/GREEN

The unchanged workflow payload was executed in a disposable directory with Docker
and Compose commands replaced by local executables. Only the VPS path was changed.
The Compose mock returned 17 with the reported parser error; the payload returned
**0** and ran `docker ps`. This reproduces false success independently of production.

Before implementation, the first 12 retained contracts in
`.scripts/test_deploy_workflow.py` all failed against that payload. After the fix,
those same contracts passed. Review additions expanded coverage to 20 test methods
(26 isolated scenario executions), all passing. Run:

```sh
python3 .scripts/test_deploy_workflow.py
```

Coverage includes successful recreation, missing v2, invalid configuration, image
snapshot and pull failures, container-query/inspection failures, empty/multiple
container IDs, wrong image, stopped container, original failure propagation after
successful/failed rollback, rollback verification, failed diagnostics, first
installation without a rollback image, and retention of the saved rollback tag.
A review-discovered rerun tag collision was separately reproduced RED before
adding the workflow attempt number; reruns now preserve distinct recovery tags.
The tests execute the actual workflow payload and reject unknown mock commands.
A PR-only `deployment-contract` job keeps these checks in CI.

The extracted Bash payload also passes `bash -n`, `shellcheck -s bash`, and the
repository diff whitespace check. Mock tests establish shell/control-flow behavior;
they do not exercise a real production restart, application health, or a real
remote Docker failure.

## Recovery and operating limits

The SSH payload explicitly invokes Bash with strict options and uses Compose v2.
Configuration validates before pulling or changing image tags. The running
container's image is saved as `ppcelery/laisky-blog-graphql:rollback-ci-<run-id>-<attempt>`.
Recreation targets only `graphql`, leaves dependencies/orphans alone, waits up to
120 seconds, disables an extra pull, and checks the actual running image ID.

If recreation or verification fails, recovery restores the saved image to the
local `latest` tag, recreates only GraphQL without pulling, and verifies the prior
image. Successful recovery still exits with the original deployment failure;
failed recovery reports failure and retains the image for manual intervention.
There is no automatic rollback on first installation without a prior container.
Image snapshots are retained; this patch does not prune them or overwrite the
existing operator rollback tag.

All workflow refs targeting B1 share one deployment concurrency group, with
cancellation disabled, so CI jobs cannot interrupt or overlap recovery. Operators
must avoid overlapping manual deployments. The existing mutable registry `latest`
policy remains: verification proves the pulled image is running, not that a
particular source revision owns the mutable registry tag. B1 has no configured
container health check, so running-image verification does not establish SSO,
MongoDB, or end-to-end application correctness. Image rollback does not roll back
configuration, mounted data, migrations, or external side effects.

Merging a code/workflow change triggers the existing image build and B1 deployment.
It can briefly interrupt GraphQL during forced recreation. No host package,
secret, security setting, or application configuration change is part of this fix.
