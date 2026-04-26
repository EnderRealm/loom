# knowledge/ — eval fixtures only

Durable extracted knowledge moved out of this repo on 2026-04-25 and now lives at `~/.loom/knowledge/` as its own git repo. Override the store path with `LOOM_KNOWLEDGE_ROOT`.

What remains here:

- `truths-eval/` — held-out reference truths used by `extract.py --benchmark`
- `decisions-eval/` — held-out reference decisions

These are test fixtures for the extraction pipeline. They never participate in the wiki.

See `~/.loom/knowledge/SCHEMA.md` for the contract and `extractors/extract.py` for the producer.
