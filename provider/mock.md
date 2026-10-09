# Mock model

Sol accepts the canonical GoAI mock directly from
`github.com/airlockrun/goai/testutil`:

```go
model, err := testutil.NewMockModel(testutil.MockConfig{
    ID: "test-model",
    Default: &testutil.MockResponse{Text: "offline answer"},
})
if err != nil {
    return err
}
var selected stream.Model = model
```

Pass `model` to `sol.RunnerOptions.Model` or a GoAI `stream.Input.Model` without
conversion. Construction requires an explicit ID. Without a matching rule,
response sequence, or default, streaming returns `testutil.ErrMockNoMatch`.

The shared engine supports ordered exact last-user/predicate rules, default
text/tool responses, explicit event scripts, fallback response sequences,
concurrency-safe request snapshots/reset, and context/abort cancellation.

`model.Configure(testutil.MockConfig{ID: model.ID(), Default: &testutil.MockResponse{Text: "new answer"}})`
atomically changes behavior while preserving ID and history. In-flight streams
retain their captured configuration. Configure restarts response sequences.

Use `Default: &testutil.MockResponse{Events: script}` for a single script,
`Default: &testutil.MockResponse{Events: []stream.Event{}}` for an empty stream,
and `Responses: []testutil.MockResponse{{Events: first}, {Events: second}}` for
a sequence. `MockConfig.Stream` accepts a context-aware streaming hook and takes
precedence over rules and scripts. Its request is isolated, and its context honors
both call cancellation and `AbortSignal`. Inspect copied `model.Requests()` values,
clear history with `model.ResetRequests()`, and change behavior with `Configure`.

See GoAI's [`testutil/mock.md`](https://github.com/airlockrun/goai/blob/main/testutil/mock.md)
for matching, copying, sequence, and cancellation semantics. The GoAI dependency
must include `testutil.NewMockModel`; workspace builds use the local GoAI source.
