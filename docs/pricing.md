# Model pricing coverage

`internal/pricing/rates.json` is the only source of every dollar figure
`cost-report` and `run-report` emit. Transcripts carry tokens, never cost. A
model, date or usage shape the table cannot price is reported as a null cost
with a named reason — never a default rate, never zero.

Every figure is an estimate at the vendor's published API list rates, as read
on each entry's `checked` date. It is not what a ChatGPT, Codex or Cursor
subscription invoiced: Codex on a ChatGPT plan and Cursor's included usage
pools are metered against plan allowances, not billed per token, and the
API-key path is the one these rates describe. Read the numbers as "what this
usage would have cost at list price".

## Provenance

The table's top-level `source` and `checked` are Anthropic's pricing page and
apply to every entry that carries no `source`/`checked` of its own. Each
OpenAI and Cursor entry carries both, plus a free-text `note` recording the
basis of its `effective` date. `Parse` validates a per-entry `checked` date
and defaults it to the table's when absent.

| Entry | Source | Checked | Effective | Basis |
|---|---|---|---|---|
| `gpt-6-astra` | developers.openai.com/api/docs/pricing | 2026-09-20 | 2026-09-03 | API release date, OpenAI changelog |
| `gpt-5.6-sol` | developers.openai.com/api/docs/pricing | 2026-09-20 | 2026-08-21 | documented price cut to $4/$20, promotional at least through 2026-11-21 |
| `gpt-5.4` | developers.openai.com/api/docs/pricing | 2026-09-20 | 2026-03-05 | dated snapshot `gpt-5.4-2026-03-05` on the model page |
| `gpt-5.3-codex` | developers.openai.com/api/docs/pricing | 2026-09-20 | 2026-02-24 | Responses API release date, OpenAI changelog |
| `gpt-5.2-codex` | developers.openai.com/api/docs/models/gpt-5.2-codex | 2026-09-20 | 2026-01-14 | Responses API release date; deprecated and absent from the pricing page, rate read off its model page |
| `composer-2.5-fast` | cursor.com/docs/models | 2026-09-20 | 2026-01-01 | table floor date; Cursor publishes no availability date |
| `cursor-grok-4.5-high` | cursor.com/docs/models | 2026-09-20 | 2026-01-01 | table floor date; Cursor publishes no availability date |
| `gpt-5.6-sol-high`, `gpt-5.6-sol-xhigh` | cursor.com/docs/models | 2026-09-20 | 2026-08-21 | Cursor bills the model's API price, which changed on that date |

## Codex (`codex-cli`)

Rollouts record `input_tokens`, `output_tokens` and `cached_input_tokens`,
the last a subset of the first (OpenAI's cached-input semantics), and no
cache writes. Both reports label the bucket `cache_read_included_in_input`
and price billable input as `input − cache_read` at the list rate plus the
cache read at the cached-input rate. A cache read larger than its input is a
broken record and unprices the unit with `cache read exceeds input`.

Recorded identities and their coverage:

| Recorded model | Priced | Input / cached input / output ($ per 1M) |
|---|---|---|
| `gpt-6-astra` | yes | 10 / 1 / 50 |
| `gpt-5.6-sol` | from 2026-08-21 | 4 / 0.4 / 20 |
| `gpt-5.4` | yes | 2.5 / 0.25 / 15 |
| `gpt-5.3-codex` | yes | 1.75 / 0.175 / 14 |
| `gpt-5.2-codex` | yes | 1.75 / 0.175 / 14 |
| `codex-auto-review` | no | the literal Codex records as the model of its auto-review feature; not a vendor model identity, so there is no rate to cite |

What the OpenAI entries deliberately do not model:

- **Cache writes.** OpenAI lists a cache-write rate for `gpt-6-astra`
  ($12.50) and `gpt-5.6-sol` ($5) and none for the rest; the page states a
  cache write is a category of input token billed at 1.25× the list rate, not
  an additive fee. The table records that figure in both `cache_write` fields
  (there is no TTL split), but rollouts never record a cache-write count, so
  written tokens price as plain input. That slightly under-estimates a
  request that used explicit cache breakpoints.
- **Long context.** Prompts over 272K input tokens are billed at 2× input and
  cache rates and 1.5× output for the whole request. Turn-level counters
  cannot tell which requests crossed the line, so every token prices at the
  short-context rate.
- **Service tiers.** Batch, Flex and Fast mode are priced differently;
  rollouts record no service tier, so the entries carry no fast rates and a
  Codex turn is always priced at Standard.
- **`gpt-5.6-sol` before 2026-08-21.** The family shipped 2026-07-09 at a
  higher rate — the changelog describes the cut as 20% off input and 33% off
  output — whose cached-input figure the checked pages no longer state. Usage
  in that window reports `unpriced model "gpt-5.6-sol" at <date>` rather than
  being priced at either rate.
- **The `gpt-5.6` alias.** It routes to `gpt-5.6-sol`, but a transcript that
  recorded the alias rather than the resolved model is not mapped: the table
  prices recorded identities, not aliases.

## Cursor (`cursor-cli`)

The journals Cursor CLI writes today record context-window occupancy
(`used_tokens` / `max_tokens`) and no billing counters — see
[Cursor session summaries](cursor-session-records.md). Every Cursor session
folds with `usage_known = 0`, so its tokens and cost are null whatever its
model, and occupancy is never counted as tokens. The entries below therefore
price nothing today; they exist so that a journal that does record counters
prices without a table change.

Cursor's models page states a per-model rate for its own models and bills
third-party models "at the model's API price". It prices Fast mode as a
separate row and offers reasoning effort as a per-request choice with no
separate price.

| Recorded model | Mapped to | Input / cache read / output ($ per 1M) | Why |
|---|---|---|---|
| `composer-2.5-fast` | Cursor "Composer 2.5 (Fast)" | 3 / 0.5 / 15 | the pool's model id `composer-2.5` in Fast mode, priced as its own row |
| `cursor-grok-4.5-high` | Cursor "Grok 4.5" | 2 / 0.5 / 6 | the page names the pool's models "Cursor Grok 4.6, Grok 4.5, and Composer 2.5"; `-high` is an effort level, which the page does not price separately |
| `gpt-5.6-sol-high`, `gpt-5.6-sol-xhigh` | Cursor "GPT-5.6 Sol" | 4 / 0.4 / 20 | third-party model at OpenAI's API price; `-high`/`-xhigh` are OpenAI reasoning effort levels, priced no differently by either vendor |
| `claude-fable-5-1-thinking-max` | — | — | Cursor prices "Claude Fable 5.1" at Anthropic's rate but documents Max Mode only for legacy request-based plans, at the API rate plus 20%; whether this identity carries that surcharge is not settled, so it stays unmapped rather than guessed |

Cursor's page lists no cache-write rate for its own models, so those entries
carry 0. Cursor bucket semantics are `unknown`: a session that recorded
counters prices only while it carries no cache read or write, and otherwise
reports `cache accounting semantics unknown for cursor-cli`, because whether a
Cursor cache read sits inside its input is undocumented.

## Claude (`claude-code`)

Unchanged: Anthropic's list rates, input exclusive of the cache buckets, each
bucket at its own rate, fast mode where offered. See the table's top-level
`note`.

## Keeping the table current

When a vendor page changes, add an entry with the new `effective` date and its
own `checked` date rather than editing the old one, so a report over an old
window keeps pricing history at the rate that held then. The test
`TestDefaultTableCoversRecordedCodexAndCursorModels` pins every mapped
identity, its figures and its date boundary; update it with the table.
