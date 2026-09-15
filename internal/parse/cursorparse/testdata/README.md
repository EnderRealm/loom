# Cursor journal fixtures

`parent.jsonl` and `child.jsonl` derive from the real Cursor CLI
2026.09.10-fd3934a sessions recorded in
`docs/cursor-cli-capture-evidence.json`. The fixtures retain the observed
protobuf graph, JSON model-message shapes, tool-call IDs, tool results,
timestamps, context counts, model name and parent relationship.

Host policy messages, encrypted model selectors, encryption keys and opaque
reasoning signatures were removed. User prompts were replaced with fixture
prompts. References were recomputed from the changed blob bytes.

The parent adds a second invocation, an errored shell call, and three long
review responses. The first turn's model messages are in a summary archive,
exercising recovery after compaction. These additions are test cases, not
claims about the original capture. The optional tests named `Shipped` read
the untouched received journals on the capture host.
