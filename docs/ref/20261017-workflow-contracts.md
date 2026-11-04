# Workflow contract scope

The release tests check the current jobs and value flow rather than a historical
workflow digest. Comments, display names, mapping/declaration order, indentation,
literal scalar chomping and optional expression delimiters do not define the
release policy. The tests accept those equivalent edits and reject changes to
publishing events, skip-output binding, build dependencies, public-only browser
arguments, deployment concurrency and the selected SSH step.

The actual deployment shell still runs with disposable Docker/Compose commands.
Its failure, image verification, scoped recreation and rollback assertions remain.
These tests do not contact a host or registry and do not prove production rollout
acceptance. They are specific workflow contracts, not a full GitHub expression
interpreter or schema validator.

Install the test-only parser before running the two suites in a Python environment:

```sh
python3 -m pip install -r .scripts/requirements-deployment-tests.txt
python3 -B .scripts/test_deploy_workflow.py
python3 -B .scripts/test_browser_sentry_build_args.py
```

The contract workflow provisions Python and installs the pinned parser. Application
and Docker runtime dependencies do not change. The loader uses PyYAML's BaseLoader
so GitHub's `on` key stays a string; tests compare scalar booleans explicitly.

Primary references checked 2026-10-17: [PyYAML 6.0.3 package and supported Python
versions](https://pypi.org/project/PyYAML/6.0.3/) and [the official setup-python v6
release](https://github.com/actions/setup-python/releases/tag/v6.0.0).

Publishing filters retain branch-only releases; an explicit ignore-all tag filter
is equivalent and accepted, while filters admitting tag publication are rejected.
See the [official branch/tag and path filter semantics](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax).

## Application readiness and rollback

The deployment now probes the verified GraphQL container's address on the existing
`vps_default` network, port 17800, using the same credential-free
`/.well-known/mcp` metadata protocol accepted by the earlier release checks.
It requires complete, sessionless `server/discover` and `tools/list` results for
protocol `2026-07-28`, including a nonempty server display name and the existing
`web_fetch`/`web_search` catalog entries. It does not invoke tools or test their
backends. The unconditional `/health` response alone cannot establish MCP startup.

Each candidate or restored-image check has a 60-second hard wall-time budget,
at most 31 attempts, five-second request timeouts, two-second retry intervals
and a one-MiB response cap. Redirects, environment proxies and session responses
are rejected. The selected container ID, image and running state are checked
again after probing. The host needs Python 3; this is checked before image/tag
changes. GNU `timeout` is also checked before changes; the pull is bounded to
300 seconds. The pinned SSH action's explicit 15-minute command budget covers
the pull plus candidate/rollback Compose waits (120 seconds each) and readiness
checks (60 seconds each), with margin. The configured bridge/address/port remain the established deployment
contract; this change does not discover or alter production configuration.

A failed readiness check enters the existing image rollback path. Rollback is
reported as restored only after the previous image passes the same readiness and
identity checks. The original deployment exit status remains a failure regardless
of rollback outcome; there is still no automatic rollback without a previous
container. Retained tags/manual recovery and mutable `latest` selection are
unchanged. Disposable transport fixtures validate these paths, including stalled
bodies and a healthy old endpoint that cannot satisfy the new-container check;
they do not establish acceptance of a newly deployed production image.

The SSH timeout input is supported by the pinned
[ssh-action v1.0.3 metadata](https://github.com/appleboy/ssh-action/blob/v1.0.3/action.yml);
its default ten-minute command budget is explicitly extended for the bounded
candidate and rollback checks.
