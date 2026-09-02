#!/usr/bin/env python3
"""Strip credential-shaped strings out of transcript-derived text.

The extraction path reads raw session transcripts whose tool results can carry
secrets an agent never meant to publish — an `env` dump, a token echoed by a
curl, a key pasted into a shell. Everything that leaves the receiver pair or
enters a model context goes through here first; see
docs/transcript-trust-and-redaction.md for the policy this implements and the
enforcement points that call it.

Redaction is keyed to *shapes*, not entropy: a secret whose form is not in
PATTERNS passes through. Adding a shape means adding a row here and a case to
test_redact.py.

A pattern writes a private sentinel — `\\x00REDACTED:<kind>\\x01` — which one
final pass converts to the public `[REDACTED:<kind>]`. The guards that keep an
already-replaced value from being re-marked test the sentinel, so they cannot be
fooled by a transcript that writes the public marker itself. Both sentinel
characters are stripped from incoming text unless they already form a sentinel
carrying *this run's* nonce, and a literal `[REDACTED:` in the input is defanged
to `[REDACTED\\:` so it cannot pass for this module's output. The nonce is what
makes "this run wrote it" checkable rather than assumed: raw jsonl can spell the
control characters as escapes, so without it a transcript could forge a marker
and borrow its authority. It is drawn once per process and lives only in memory
— nothing configurable, nothing seedable, nothing on disk.

`redact_to_sentinels` and `reveal` are the same work in two halves, for a caller
that redacts in stages over text that flows into one output — preprocess()
redacts each tool result before truncating it, then the assembled thread — so
the earlier stage's markers are still sentinels when the later one runs.

Deliberately imports nothing from the rest of the package, so any writer that
moves transcript-derived text can call it.
"""
from __future__ import annotations

import re
import secrets
import sys
from collections import Counter
from collections.abc import Mapping

# The private form a pattern writes, and the public form `reveal` converts it
# to. The characters are C0 controls and the nonce is unpredictable and
# per-process, so a sentinel is text only this run's patterns can have written:
# `_sanitize` removes every sentinel character that is not part of one.
SENTINEL_OPEN = "\x00"
SENTINEL_CLOSE = "\x01"
_NONCE = secrets.token_hex(8)
_SENTINEL_BODY = _NONCE + ":REDACTED:"
_SENTINEL_RE = re.compile(SENTINEL_OPEN + _SENTINEL_BODY + r"([a-z-]+)" + SENTINEL_CLOSE)
# A value that is exactly one sentinel: what an earlier pattern already
# replaced, which a later one leaves under that pattern's kind rather than
# re-marking as its own.
_SENTINEL_VALUE = SENTINEL_OPEN + _SENTINEL_BODY + r"[a-z-]+" + SENTINEL_CLOSE
# A marker split by a caller's own truncation, which `reveal` could not convert.
_PARTIAL_SENTINEL_RE = re.compile(SENTINEL_OPEN + "[^" + SENTINEL_CLOSE + "]*$")
# Incoming text: a sentinel bearing this run's nonce is kept, every other
# sentinel character is dropped, a literal public marker is defanged.
_SANITIZE_RE = re.compile(_SENTINEL_VALUE + r"|[" + SENTINEL_OPEN + SENTINEL_CLOSE + r"]"
                          + r"|\[REDACTED:")

# A secret's name in an assignment. Every keyword but AUTH matches as a free
# substring; AUTH is anchored to a name component (start of the name or after
# `_`, end of the name or before `_`) because as a substring it swallows
# ordinary prose — `Authentication: middleware-level checks`.
_KEYWORDS = "SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|APIKEY|PRIVATE_KEY|ACCESS_KEY|CREDENTIAL"
_SECRET_NAME = (
    r"(?:[A-Za-z0-9_]*(?:" + _KEYWORDS + r")[A-Za-z0-9_]*"
    r"|(?:[A-Za-z0-9_]+_)?AUTH(?:_[A-Za-z0-9_]+)?)"
)
# The same name, but shaped like an environment variable. Case-sensitive
# despite the pattern's IGNORECASE: an all-caps name is itself evidence that a
# `name: value` line is an assignment rather than English.
_ENV_NAME = (
    r"(?-i:(?:[A-Z0-9_]*(?:" + _KEYWORDS + r")[A-Z0-9_]*"
    r"|(?:[A-Z0-9_]+_)?AUTH(?:_[A-Z0-9_]+)?))"
)

# Ordered: the first pattern to claim a span names it, so a more specific shape
# must precede the general one it is a subset of (anthropic-key before
# openai-key). Each regex may name a `pre` group (kept verbatim before the
# marker) and a `post` group (kept after); everything else it matches is
# replaced by `[REDACTED:<kind>]`.
PATTERNS = [
    # PEM block, BEGIN through the matching END. A block whose END line never
    # arrives — preprocess() truncates a non-error tool result at 500 chars, so
    # a `cat` of a key is cut mid-block by construction — is redacted through
    # its own base64 body and no further: a `.*` fallback under DOTALL would
    # swallow the rest of the transcript behind one marker.
    ("private-key", re.compile(
        r"-----BEGIN [A-Z ]*PRIVATE KEY-----"
        # The gap to the END line may not cross another BEGIN: a truncated block
        # followed later by a whole one would otherwise match from the first
        # BEGIN to the second block's END and replace the transcript between
        # them. The no-END branch takes only body-width base64 lines, so an
        # ordinary word on the line after a truncated block is not part of it.
        r"(?:(?:(?!-----BEGIN [A-Z ]*PRIVATE KEY-----)[\s\S])*?"
        r"-----END [A-Z ]*PRIVATE KEY-----"
        r"|[^\r\n]*(?:\r?\n[A-Za-z0-9+/=]{16,})*)")),
    ("anthropic-key", re.compile(r"\bsk-ant-[A-Za-z0-9_-]{20,}")),
    ("openai-key", re.compile(r"\bsk-[A-Za-z0-9_-]{20,}")),
    ("github-token", re.compile(
        r"\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})")),
    ("aws-access-key", re.compile(r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b")),
    ("slack-token", re.compile(r"\bxox[abprs]-[A-Za-z0-9-]{10,}")),
    ("google-api-key", re.compile(r"\bAIza[0-9A-Za-z_-]{35}")),
    ("jwt", re.compile(
        r"\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}")),
    # The scheme stays: which header carried a credential is evidence, the
    # credential is not. Two rows under one kind, because the header and the
    # prose are different evidence. After `Authorization:` anything 8+ long is a
    # credential — nobody writes a sentence there — so composition is not
    # consulted and a letters-only token still goes.
    ("bearer", re.compile(
        r"(?P<pre>Authorization[\"']?\s*[:=]\s*[\"']?Bearer )"
        r"[A-Za-z0-9._~+/=-]{8,}(?<![.-])",
        re.IGNORECASE)),
    # A bare `Bearer` in prose needs the composition signal: a real credential
    # is random, an English word is not. `.` and `-` are not signals — they end
    # a sentence and hyphenate a word — so `Bearer authentication.` and
    # `Bearer auth-token` survive.
    ("bearer", re.compile(
        r"(?P<pre>Bearer )(?=[A-Za-z0-9._~+/=-]*[0-9_~+/=])[A-Za-z0-9._~+/=-]{8,}(?<![.-])",
        re.IGNORECASE)),
    # ://user:password@host — only the password goes. The guard keeps a password
    # a pattern above already replaced (`x-access-token:ghp_...`) under that
    # pattern's kind instead of re-marking it as this one.
    ("url-credential", re.compile(
        r"(?P<pre>://[^\s:/@]+:)(?!" + _SENTINEL_VALUE + r"@)[^\s/@]+(?=@)")),
    # An assignment whose name reads like a secret's: what catches `env` dumps
    # and `export FOO_TOKEN=...` in bash output. The name and operator are kept
    # so the reader sees which variable held it. A quoted value runs to its
    # closing quote and may contain spaces, or to end of line when the quote
    # never arrives — preprocess() truncates a result mid-value by construction,
    # and a credential's prefix in the clear is still a credential. An unquoted
    # value is any run of non-whitespace, with no length floor: `TOKEN=abc123`
    # is a secret. The lookaheads exempt a value that names a secret rather than
    # being one — a variable reference (`$FOO`, `${FOO}`), a `<placeholder>`, a
    # `_`-joined uppercase identifier — and a value that is *exactly* what a
    # pattern above already replaced. Exactly, so a value carrying a replaced
    # secret and a second one beside it is redacted whole rather than left with
    # the tail in the clear. The identifier exemption takes no digits and
    # demands an underscore: `LOOM_RECEIVER_TOKEN` names a secret, `ABCD1234EFGH`
    # and `ABCDEFGHIJ` are secrets that happen to be uppercase.
    ("env-secret", re.compile(
        r"(?P<pre>\b(?:"
        # An env-var-shaped name is an assignment whichever operator follows.
        r"" + _ENV_NAME + r"[\"']?\s*[=:]\s*"
        # A lowercase name needs corroboration, or the pattern reads English:
        # either the assignment operator, or a quoted value.
        r"|" + _SECRET_NAME + r"[\"']?\s*=\s*"
        r"|" + _SECRET_NAME + r"[\"']?\s*[=:]\s*(?=[\"'])"
        r")(?P<q>[\"']?))"
        r"(?![$<])(?!" + _SENTINEL_VALUE + r"(?:[\s\"']|$))"
        r"(?!(?-i:[A-Z]+(?:_[A-Z]+)+)(?:[\s\"']|$))"
        r"(?:(?<=[\"'])[^\"'\r\n]+|[^\s\"']+)(?P<post>(?P=q)|(?=\r?\n|$))",
        re.IGNORECASE)),
]


def redact(text: str) -> str:
    """Replace every credential-shaped span with `[REDACTED:<kind>]`.

    The marker rather than deletion: a downstream reader — human or model —
    still sees that a secret was present and what sort, which is often the
    evidence the claim rests on.
    """
    return redact_with_report(text)[0]


def redact_with_report(text: str) -> tuple[str, Counter, int]:
    """redact(), plus a span count per kind and the characters they replaced.

    A redaction is invisible in its own output: one marker stands for one
    character or for forty thousand. The span count says how often a pattern
    fired; the character count is what separates a pattern that ate a token
    from one that ate a transcript.
    """
    text, counts, chars = redact_to_sentinels(text)
    return reveal(text), counts, chars


def redact_to_sentinels(text: str) -> tuple[str, Counter, int]:
    """redact_with_report(), stopping at the private sentinel.

    For a caller that redacts in stages over text that ends up in one output:
    the sentinel is what tells a later stage's guards which markers this module
    wrote, and it is unspellable in transcript text. Convert with `reveal` once,
    when the last stage has run.
    """
    counts = Counter()
    chars = 0
    text = _SANITIZE_RE.sub(_sanitize, text)

    for kind, pattern in PATTERNS:
        def replace(match: re.Match, kind: str = kind) -> str:
            nonlocal chars
            groups = match.groupdict()
            pre = groups.get("pre") or ""
            post = groups.get("post") or ""
            chars += len(match.group(0)) - len(pre) - len(post)
            return f"{pre}{SENTINEL_OPEN}{_SENTINEL_BODY}{kind}{SENTINEL_CLOSE}{post}"

        text, n = pattern.subn(replace, text)
        if n:
            counts[kind] += n
    return text, counts, chars


def reveal(text: str) -> str:
    """Convert every sentinel to its public `[REDACTED:<kind>]` marker."""
    return _SENTINEL_RE.sub(lambda m: f"[REDACTED:{m.group(1)}]", text)


def drop_partial_marker(text: str) -> str:
    """Drop a marker the caller's own truncation cut in half.

    A caller that slices redacted text can split a sentinel, which `reveal`
    would then leave as raw control characters in its output.
    """
    return _PARTIAL_SENTINEL_RE.sub("", text)


def _sanitize(match: re.Match) -> str:
    """One incoming occurrence of something that could pass for this module's
    own output."""
    text = match.group(0)
    if text.startswith(SENTINEL_OPEN) and len(text) > 1:
        # A whole sentinel carrying this run's nonce: an earlier stage wrote it.
        return text
    if text in (SENTINEL_OPEN, SENTINEL_CLOSE):
        # A sentinel character that is not part of one of this run's sentinels,
        # so the form stays this run's alone.
        return ""
    # A literal public marker the input supplied: defanged visibly, so a reader
    # sees that the text said it rather than that this module did.
    return "[REDACTED\\:"


def report(counts: Mapping[str, int], chars: int) -> str:
    """One line for a caller's stderr: `3 span(s), 1204 chars: env-secret×2`."""
    kinds = ", ".join(f"{kind}×{n}" for kind, n in sorted(counts.items()))
    return f"{sum(counts.values())} span(s), {chars} chars: {kinds}"


def log_redaction(label: str, counts: Mapping[str, int], chars: int) -> None:
    """Print a call site's one-line note, or nothing when it replaced nothing.

    Every enforcement point logs the same way and to the same place, so the
    sweep's log carries `[redact] <label>: …` per stage; `internal/extract`
    forwards those lines into extractor.log on a successful run.
    """
    if counts:
        print(f"[redact] {label}: {report(counts, chars)}", file=sys.stderr)
