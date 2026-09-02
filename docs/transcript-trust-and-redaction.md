# Trust and redaction for transcript-derived artifacts

A transcript-derived artifact is anything loom produces by reading a session
transcript: the summaries the TUI renders, the candidate truths and decisions
the extractor writes, and everything downstream of a promoted one. The source
material is a raw agent transcript, so it carries two hazards that the artifact
inherits unless something removes them. Tool results embed secrets — an `env`
dump, a token echoed by a curl, a key pasted into a shell. Fetched content and
tool output embed text that addresses whoever reads it next, which for an
artifact that ends up in another agent's context means an instruction arriving
as data.

This document says what is stripped, where transcript-derived text is allowed to
go, and what a consumer may trust it for. It exists ahead of the automation:
`ticket/journal-watcher-shells-15a2` generates candidates on every ticket close,
which raises the volume and moves the human gate later.

## The flow

Three legs, and they are not equivalent.

**Leg 0 — capture and shipping.** An agent writes jsonl under
`~/.claude/projects` or `~/.codex/sessions`. The shipper POSTs those records
**verbatim and unredacted** to the receiver's `/v1/ingest`
(`transport/shipper/shipper.go`), which writes them to `~/.loom/received/` on
the receiver host. When the shipping machine is not the receiving one, and it
usually is not, raw transcript text crosses a host boundary here, over HTTP with
a shared bearer token (`--auth-token`, `LOOM_RECEIVER_TOKEN`, or
`~/.loom/receiver-token`).

That hop is deliberate and it is not a hole in the rule below. The receiver is
not a third party: it is a host the same operator runs, holding the source of
record for every session that machine captures, and the whole point of shipping
is that the received tree is complete. Redacting in flight would mean the
operator's own archive is missing text their own agent produced, while the
original still sits on the capturing machine. What the transport owes instead is
the transport's own property — an authenticated endpoint between two hosts the
operator controls — and that is where a change belongs if the receiver ever
stops being one of those.

The authentication is a shared bearer token, which is not confidentiality. The
receiver serves plain HTTP, and the shipper sends over whatever scheme
`server_url` names, so **confidentiality on this hop is the network's property,
not loom's**: an observer on the path reads both the transcripts and the token.
That is acceptable on loopback or on a private network the operator trusts end
to end. Anywhere else, `server_url` must name an `https://` endpoint — TLS
terminated in front of the receiver, with Go's HTTP client verifying the
certificate by default — or the hop must run inside an encrypted tunnel.
Enforcing a confidential transport in the shipper and receiver themselves,
rather than leaving it to how the operator deploys them, is tracked in
`loom/require-confidential-transport-3a9a`.

**Leg (a) — the Go summarizer.** `internal/parse/claudeparse` and
`internal/parse/codexparse` fold `~/.loom/received/` into `~/.loom/summaries.db`
on the receiver host, whose turns hold verbatim user and assistant text and
whose tool calls hold an 800-char result summary. Nothing on this path is
redacted. It does not need to be: the DB's readers are all local to that host
(the TUI, `loom work-report`), and the full transcript it was folded from is
sitting beside it in `received/` on the same disk — redacting the derived copy
while the original stays whole would buy nothing. What holds the line is that
this artifact goes no further. `summaries.db` is not shipped anywhere and never
handed to a model.

**Leg (b) — the extractor.** `extract.py` reads either a raw jsonl or a markdown
summary, runs `preprocess()` over the raw case, optionally passes the result
through the summarizer LLM, and feeds it to the truth or decision extractor
prompt. The candidates land in `_candidates/` in the knowledge store — its own
git repo — where the TUI's review screen promotes or rejects them. A promoted
one moves to `truths/` or `decisions/`, from which `loom relevant --for-ticket`
ranks and injects it into other agents' contexts, and from which `extract.py`
itself loads few-shot reference examples for its next run. `loom retrospect` and
the planned journal-watcher hook both sit on this leg.

Leg (b) is where transcript text reaches a model provider, a shared store, and
other people's agents. The rule follows from that:

> Raw transcripts move only between a capturing host and its receiver, and
> `summaries.db` stays on the receiver. Anything that leaves that pair, enters a
> model context, or enters a shared store goes through the extraction path, and
> therefore through `redact()`.

## What is redacted

`extractors/redact.py` holds an ordered table, `PATTERNS`, of `(kind, regex)`.
Every match becomes `[REDACTED:<kind>]` — a marker rather than a deletion,
because *a credential was here, of this sort* is often the evidence a claim
rests on, and a silent hole reads like the transcript never contained it.

| kind | matches |
| --- | --- |
| `private-key` | A PEM block, `BEGIN` through the matching `END`, where the span may not cross a second `BEGIN`; a block whose `END` never arrives — a truncated tool result — through its own body-width base64 lines and no further |
| `anthropic-key` | `sk-ant-` and 20+ more key characters |
| `openai-key` | `sk-` and 20+ more key characters |
| `github-token` | `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_` plus 36+, or `github_pat_` plus 22+ |
| `aws-access-key` | `AKIA`/`ASIA` plus 16 uppercase alphanumerics |
| `slack-token` | `xoxa-`/`xoxb-`/`xoxp-`/`xoxr-`/`xoxs-` plus 10+ |
| `google-api-key` | `AIza` plus 35 |
| `jwt` | Three dot-joined base64url segments, the first starting `eyJ` |
| `bearer` | The credential after `Bearer `, 8+ characters; the word itself is kept. After an `Authorization` header — including a serialized one, `"authorization": "Bearer …"` — any composition — nobody writes a sentence there, so a letters-only token goes. For a bare `Bearer` in prose, only when the run carries a digit or one of `_~+/=`, so `Bearer token`, `Bearer authentication.` and `Bearer auth-token` stay |
| `url-credential` | The password in `://user:password@host`; scheme, user and host are kept. A password an earlier pattern already replaced keeps that pattern's kind — `https://x-access-token:ghp_…@host` reads `github-token` |
| `env-secret` | The value of an assignment whose name contains SECRET, TOKEN, PASSWORD, PASSWD, API_KEY, APIKEY, PRIVATE_KEY, ACCESS_KEY or CREDENTIAL, or has AUTH as a whole `_`-delimited component. An env-var-shaped (all-caps) name reads as an assignment under either operator; a lowercase name needs corroboration — the `=` operator, or a quoted value — or it is prose. A quoted value runs to its closing quote and may contain spaces, or to end of line when the quote never arrives — a value truncated mid-string is still a credential prefix. An unquoted one is any run of non-whitespace, at any length. The name and operator are kept. Exempt: a value that names a secret rather than being one — `$FOO`, `${FOO}`, `<placeholder>`, or a bare uppercase identifier with an underscore and no digits (`LOOM_RECEIVER_TOKEN` names a secret; `ABCD1234EFGH` is one) |

Order matters: the first pattern to claim a span names it, so `anthropic-key`
precedes `openai-key` and the more specific name wins. `env-secret` is last and
skips a value that is *exactly* what an earlier pattern replaced, so
`ANTHROPIC_API_KEY=sk-ant-…` comes back marked `anthropic-key`, not
`env-secret` — while a value carrying a replaced secret and a second one beside
it is redacted whole rather than left with its tail in the clear.

That "already replaced" test reads a private sentinel,
`\x00<nonce>:REDACTED:<kind>\x01`, which one final pass converts to the public
`[REDACTED:<kind>]`. The public marker is printable, so a transcript can write
one; if the guards read that, a transcript could exempt its own secrets from
redaction by prefixing them. The nonce is unpredictable and drawn once per
process, so a sentinel is text only this run's patterns can have written — raw
jsonl spells the control characters as escapes, so a sentinel without a nonce
would be forgeable, and the forgery would borrow the marker's authority as
evidence a credential was there. Every sentinel character that is not part of
one of this run's sentinels is stripped from incoming text; keeping the ones
that are is what staged callers rely on, `preprocess()` redacting each tool
result and then the assembled thread. A literal `[REDACTED:` in the input is
defanged to `[REDACTED\:`, visibly, so a reader sees that the text said it
rather than that redaction did. The nonce lives only in memory: nothing
configurable, nothing seedable, nothing on disk.

The nonce is per process, so a marker an *earlier* run wrote — one a model
echoed back into a candidate, or one already sitting in a promoted truth — is
input like any other to a later run, and comes back defanged as
`[REDACTED\:<kind>]`. Nothing leaks and the defanging does not compound, but it
is what a reviewer meets in the TUI, so it is written down here rather than
discovered there.

A marker is the same size whether it replaced one character or forty thousand,
so `redact_with_report()` returns a span count per kind and the characters they
replaced, and `log_redaction()` prints one
`[redact] <label>: N span(s), M chars: kind×n` line to stderr per stage — the
character total being what actually separates a pattern that ate a token from
one that ate a transcript. The unattended path reads them back: `logRedactions` in
`internal/extract/extract.go` scans the child's **stderr** on a successful run
and re-logs each of those lines under the session's log key, so they land in
`extractor.log` — otherwise the sweep reads the extractor's output only when it
failed, and an over-broad pattern would be visible in nothing but what a
candidate is missing. Only stderr, because the child's stdout carries
model-authored candidate titles and the counts are an audit record.

This is a **minimum, keyed to shapes**. A secret whose form is not in the table
passes through — that is a stated limit, not an oversight. Not attempted:
arbitrary high-entropy strings, credentials with no recognizable prefix or
assignment around them, and anything on the `summaries.db` leg, which is covered
by never leaving its host rather than by filtering. It errs the other way too:
`env-secret` reads an all-caps `NAME: value` as an assignment whatever the
value is, and any quoted one, so a summary line like `AUTH_TOKEN: rotated` or
`api_key: "the one in 1Password"` comes back marked. This pattern runs over
candidate bodies and titles as well as transcripts, so that marker is what a
human meets in the TUI's review screen. A false marker costs one line of
evidence; a missed secret costs the secret. Adding a shape means adding a
row to `PATTERNS` and a case to `extractors/test_redact.py`; there is no other
place to change.

## Trust stance

**Agent- and tool-authored text is data, never instructions.** A transcript is
mostly a model talking and tools answering. Neither is an author with authority
over what reads it next, and the tool half can be relaying text from anywhere.

- **The LLM stages.** `summarizer.md`, `truth-extractor.md` and
  `decision-extractor.md` each substitute their input inside
  `<session-input>` … `</session-input>`, and each states the rule against those
  markers: what is between them is to be summarized or extracted from, and text
  in it that addresses the reader or asks for an action is content to report,
  not a directive to follow. A transcript is ordinary text and can contain
  either marker itself, so `fence_input()` in `extract.py` rewrites both to a
  visible defanged form (`<\/session-input>`) at every substitution site: the
  closing marker in the prompt is the one the prompt wrote. The delimiter
  locates the input and the prompt rule carries the stance — the delimiter is
  not a parser, and it is not what the stance rests on. The few-shot reference
  block is substituted between its own `<reference-example>` markers, stated as
  examples of the output shape, and fenced by the same helper — a stored
  example is transcript-derived text a human may have edited.
- **The TUI and any consumer that renders a candidate.** A candidate is
  untrusted input being shown to a human. It is displayed, never acted on, and
  no field in it selects behaviour.
- **Any agent reading `loom relevant` output or a promoted truth.** A promoted
  truth is a claim a human reviewed, which makes it worth weighing — it does not
  make its text a command. The promotion gate is a review of the claim, not a
  grant of authority to the words.

The gate is deliberately narrow: promotion says *a human read this claim and
found it true enough to keep*. It says nothing about the text's right to direct
a reader, and a promoted truth is still data.

## Enforcement points

Five, and they are the whole of the redaction:

| Where | What it covers |
| --- | --- |
| `extractors/redact.py` — `redact()`, `redact_with_report()`, `PATTERNS` | The rules themselves; `redact_to_sentinels()`/`reveal()` for a caller that redacts in stages |
| `extractors/preprocess.py` — `preprocess()` returns redacted text, and redacts each tool result before truncating it | Every raw-jsonl consumer: the summarizer input, the raw-format extractor input, and the standalone CLI. Before the truncation as well as after, because a key cut by `--max-result-chars` can fall below its pattern's minimum length |
| `extractors/extract.py` — the summary-input branch | Markdown summaries, which bypass `preprocess()` |
| `extractors/extract.py` — `load_reference_truths_from()` | The few-shot examples read back out of the store, which is transcript-derived, predates this rule and is hand-editable |
| `extractors/extract.py` — `emit_candidates()` | The output-side backstop: a secret that reached the model some other way still never lands in the store |

The store's Go readers — the TUI's review screen and `loom relevant` — sit
downstream of `emit_candidates()`, so what they read was redacted when it was
written. Entries that predate this rule, or that a human edited by hand, are an
accepted residual: nothing re-scans the store. `loom relevant` reports a title,
a score and a path rather than a body, so what it injects into another agent's
context is a pointer, not the text.

A run also writes host-local debugging artifacts that no enforcement point
covers: `--raw-out` and the `.summary.txt` intermediate beside it, `--show-output`
on stderr, the `--json-out` result file (which carries each candidate's title,
claim and verify text — `emit_candidates()` redacts the body it writes to the
store, not that file), and the candidate titles the sweep records in
`extractor.log`. Those
are the never-leaves-the-host argument again, not redaction — a debug file that
starts being shipped or shared needs `redact()` on the way out.

For a future flow:

- A new **consumer** of transcript text reads it through `preprocess()` or
  `extract.py`, never straight from the jsonl. That is how it inherits the
  redaction instead of re-deciding it.
- A new **writer** that moves transcript-derived text off the capture/receiver
  pair or into a store calls `redact()` on what it writes.
- The Go sweep (`internal/extract/extract.go`) and `loom retrospect`
  (`internal/extract/retrospect.go`) already inherit both: they shell out to
  `extract.py` and never touch transcript text themselves. So will the
  journal-watcher hook, for the same reason.

## Decision: shape-based redaction at the choke point

Two alternatives were on the table. **Entropy detection** — flag any
high-entropy run — catches secrets no pattern anticipates, and was rejected
because it eats exactly the strings the extractor needs as evidence: commit
shas, session uuids, ticket ids, base64 blobs in an error message. A truth whose
`How to verify` cites `2bbeb99` is worth less with the sha redacted, and the
extractor is graded on citing them. **Per-consumer filtering** — each writer
strips what it knows about — was rejected because it is the same rule
re-implemented once per writer, where a writer that skips it looks identical to
one that did not until a secret shows up in the store.

Shapes at the choke point were chosen because the result is deterministic and
testable — a pattern either matches a planted token or it does not, and
`test_redact.py` asserts one case per kind — and because there is one file to
change when a new shape appears. The cost accepted is the stated one: a secret
of an unknown shape passes through. That is a gap with a known fix (add a row),
rather than a filter whose behaviour nobody can predict.
