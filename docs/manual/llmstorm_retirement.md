# LLM storm research retirement

The `GeneralAddLLMStormTask(prompt, api_key)` GraphQL mutation retains its
schema signature for existing clients, but rejects every request with:

```text
llm-storm research has been retired; new tasks are unavailable
```

It returns no task ID and does not authenticate against the task worker, create
a Redis task, or enqueue work. Empty inputs and canceled request contexts produce
the same retirement error. Clients should remove the option to start new
LLM storm research.

`GeneralGetLLMStormTaskResult(task_id)` keeps its existing authentication and
stored-result lookup. The result model, task decoder and stored Redis keys are
unchanged. This change does not delete research records, results, volumes or
history, and does not extend the existing storage retention period.

HTML crawler tasks, OneAPI key validation, locks and other GraphQL/MCP features
retain their existing behavior. Internal task storage helpers remain available
for historical data compatibility; the public research mutation no longer calls
the enqueue helper.

Deploy this API change and remove all other live enqueue callers before stopping
the dedicated LLM storm worker. Keep its deployment configuration and persisted
data recoverable according to the deployment retirement plan. A rollback of this
API change restores the mutation's previous behavior and therefore also requires
a working worker before clients can submit new research.

## Verification

`TestGeneralAddLLMStormTaskRetired` exercises normal and empty inputs with active
and canceled contexts while authentication and task-store dependencies are
unavailable. The stable error and empty ID prove that this entry point does not
need those dependencies. Existing `TestNewGeneralLLMStormTask` verifies historical
result conversion, including article, references, timestamps and status.

`TestLLMStormGraphQLRetirement` uses the executable generated schema, a
synthetic signed worker token and in-memory Redis. It verifies that the mutation
leaves pre-existing pending work intact, historical results remain readable,
historical reads keep their authentication, and ordinary crawler enqueue/dequeue
still work. Running this test against the previous resolver reproduces a new
pending research task and fails the retirement assertion.

General controller and GraphQL acceptance:

```sh
go test -race -count=1 -timeout 2m ./internal/web/general/controller
go test -race -count=1 -timeout 2m ./internal/web -run TestLLMStormGraphQLRetirement
```

The generated GraphQL schema is unchanged. The only module additions are the
miniredis test fixture and its gopher-lua dependency; existing dependency versions
are unchanged.

## References

- [gqlgen resolver errors](https://gqlgen.com/reference/errors/) describes how
  resolver errors are returned in GraphQL responses.
- [Go testing package](https://pkg.go.dev/testing) documents subtests and cleanup.

- [Miniredis](https://github.com/alicebob/miniredis) provides the in-memory
  Redis protocol fixture without an external Redis service.
