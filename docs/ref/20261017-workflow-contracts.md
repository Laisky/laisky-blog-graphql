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
