#!/usr/bin/env python3
"""Pre-process a Claude Code session JSONL into a filtered conversation thread
suitable for truth extraction.

Reads a raw session transcript (*.jsonl) and emits a text thread that preserves
truth-bearing content while discarding noise.

What's kept:
  - Assistant text blocks: verbatim (analysis, recommendations, discoveries)
  - User string messages: verbatim (human input, corrections, decisions)
  - User text blocks in arrays: verbatim (additional human context)
  - Tool results with is_error=true: verbatim (error signals)
  - Tool_use blocks: one-line summary (TOOL: name(key_args))
  - Tool results with is_error=false/null: truncated to --max-result-chars

What's discarded:
  - Thinking blocks (empty/redacted in persisted jsonl)
  - System records (metadata: hook summaries, turn durations)
  - File-history-snapshot records (git state)
  - Permission-mode records

What's redacted:
  - Credential-shaped strings anywhere in the thread, replaced with
    [REDACTED:<kind>] (see docs/transcript-trust-and-redaction.md)

Usage:
    ./preprocess.py <session.jsonl> [--max-result-chars 500]
"""

import argparse
import json
import sys
from collections import Counter
from pathlib import Path

from redact import (drop_partial_marker, log_redaction,
                    redact_to_sentinels, reveal)

DEFAULT_MAX_RESULT_CHARS = 500


def preprocess(jsonl_path: str, max_result_chars: int = DEFAULT_MAX_RESULT_CHARS) -> str:
    """Read a session jsonl and return a filtered conversation thread as text."""
    path = Path(jsonl_path)
    if not path.exists():
        raise FileNotFoundError(f"not found: {path}")

    lines = path.read_text().splitlines()
    blocks = []
    counts = Counter()
    chars = 0

    for line_no, raw in enumerate(lines, 1):
        if not raw.strip():
            continue
        try:
            record = json.loads(raw)
        except json.JSONDecodeError:
            continue

        rtype = record.get("type", "")

        if rtype == "assistant":
            lines_out, found, n = _process_assistant(record)
            blocks.extend(lines_out)
            counts.update(found)
            chars += n
        elif rtype == "user":
            lines_out, found, n = _process_user(record, max_result_chars)
            blocks.extend(lines_out)
            counts.update(found)
            chars += n
        # Discard: system, file-history-snapshot, permission-mode, attachment, summary, result

    # Policy enforcement point for every raw-jsonl consumer — the summarizer
    # input, the raw-format extractor input and this module's own CLI all leave
    # through here. See docs/transcript-trust-and-redaction.md.
    # The whole-thread pass is the backstop; tool results are redacted before
    # they are truncated, so a key cut by --max-result-chars cannot leave a
    # sub-minimum prefix behind. Both passes report into one line: a marker
    # stands for one character or for forty thousand, and the counts are what
    # make an over-broad pattern visible in the sweep's log.
    # Sentinels rather than markers between the two passes, so this one can tell
    # what the result pass replaced from what the transcript merely wrote.
    thread, found, n = redact_to_sentinels("\n".join(blocks))
    counts.update(found)
    log_redaction("transcript", counts, chars + n)
    return reveal(thread)


def _process_assistant(record: dict) -> tuple[list[str], Counter, int]:
    """Extract text and tool-use summaries from an assistant record.

    Also returns what redacting its tool-call arguments replaced: the summary
    below truncates them, and that has to happen after the redaction rather
    than in preprocess()'s whole-thread pass.
    """
    out = []
    counts = Counter()
    chars = 0
    content = record.get("message", {}).get("content", [])
    if not isinstance(content, list):
        return out, counts, chars

    for block in content:
        btype = block.get("type", "")

        if btype == "text":
            text = block.get("text", "").strip()
            if text:
                out.append(f"ASSISTANT:\n{text}\n")

        elif btype == "tool_use":
            name = block.get("name", "?")
            inp = block.get("input", {})
            summary, found, n = _summarize_tool_input(name, inp)
            counts.update(found)
            chars += n
            out.append(f"TOOL: {name}({summary})")

        # Skip thinking (empty/redacted) and other block types

    return out, counts, chars


def _process_user(record: dict, max_result_chars: int) -> tuple[list[str], Counter, int]:
    """Extract human input and tool results from a user record.

    Also returns what redacting its tool results replaced, since that has to
    happen before the truncation below rather than in preprocess()'s
    whole-thread pass.
    """
    out = []
    counts = Counter()
    chars = 0
    content = record.get("message", {}).get("content")

    if content is None:
        content = record.get("content", "")

    # String content: direct human input or slash command
    if isinstance(content, str):
        text = content.strip()
        if not text:
            return out, counts, chars
        # Filter out pure XML command wrappers with no human text
        if text.startswith("<local-command-caveat>") or text.startswith("<command-name>"):
            # Extract any human-readable parts
            human_text = _extract_human_from_command(text)
            if human_text:
                out.append(f"USER: {human_text}\n")
        else:
            out.append(f"USER: {text}\n")
        return out, counts, chars

    # Array content: mix of tool_result and text blocks
    if isinstance(content, list):
        for block in content:
            if not isinstance(block, dict):
                continue
            btype = block.get("type", "")

            if btype == "text":
                text = block.get("text", "").strip()
                if text and not text.startswith("<"):
                    out.append(f"USER: {text}\n")

            elif btype == "tool_result":
                is_error = block.get("is_error")
                result_content = block.get("content", "")

                # Normalize content to string
                if isinstance(result_content, list):
                    parts = []
                    for part in result_content:
                        if isinstance(part, dict) and part.get("type") == "text":
                            parts.append(part.get("text", ""))
                        elif isinstance(part, str):
                            parts.append(part)
                    result_content = "\n".join(parts)

                if not isinstance(result_content, str):
                    result_content = str(result_content)

                result_content = result_content.strip()
                if not result_content:
                    continue

                # The tool result's own size, before redaction rewrote it: the
                # label below describes what the tool printed.
                result_len = len(result_content)

                # Policy enforcement point, before the truncation: a credential
                # cut across --max-result-chars can fall below its pattern's
                # minimum length, and the prefix of a key is a key.
                result_content, found, n = redact_to_sentinels(result_content)
                counts.update(found)
                chars += n

                if is_error:
                    out.append(f"ERROR:\n{result_content}\n")
                else:
                    if len(result_content) > max_result_chars:
                        # The slice can land inside a marker this pass wrote.
                        truncated = drop_partial_marker(result_content[:max_result_chars])
                        out.append(f"RESULT: ({result_len} chars, truncated)\n{truncated}...\n")
                    else:
                        out.append(f"RESULT:\n{result_content}\n")

    return out, counts, chars


def _summarize_tool_input(name: str, inp: dict) -> tuple[str, Counter, int]:
    """Produce a short summary of tool input args.

    Also returns what redacting them replaced. The branches that truncate
    redact first — see _short_val — so a credential cut mid-token cannot fall
    below its pattern's minimum length; the rest are covered by preprocess()'s
    whole-thread pass, which truncates nothing.
    """
    counts = Counter()
    chars = 0
    if name in ("Bash",):
        cmd, counts, chars = redact_to_sentinels(inp.get("command", ""))
        if len(cmd) > 120:
            cmd = drop_partial_marker(cmd[:120]) + "..."
        return cmd, counts, chars
    elif name in ("Read",):
        return inp.get("file_path", "?"), counts, chars
    elif name in ("Grep",):
        pattern = inp.get("pattern", "?")
        path = inp.get("path", ".")
        return f'"{pattern}" in {path}', counts, chars
    elif name in ("Glob",):
        return inp.get("pattern", "?"), counts, chars
    elif name in ("Edit",):
        return inp.get("file_path", "?"), counts, chars
    elif name in ("Write",):
        return inp.get("file_path", "?"), counts, chars
    elif name in ("Agent",):
        desc = inp.get("description", "?")
        return desc, counts, chars
    elif name.startswith("mcp__"):
        # MCP tool: show the most useful params
        short_name = name.split("__")[-1]
        key_params = {k: v for k, v in inp.items() if v and k not in ("sessionId",)}
        if key_params:
            pairs, counts, chars = _short_pairs(list(key_params.items())[:4])
            return f"{short_name}: {pairs}", counts, chars
        return short_name, counts, chars
    else:
        # Generic: show first 2 key=value pairs
        if not inp:
            return "", counts, chars
        pairs, counts, chars = _short_pairs(list(inp.items())[:2])
        return pairs, counts, chars


def _short_pairs(items: list) -> tuple[str, Counter, int]:
    """Render `k=v` pairs for a tool-call summary, folding together what
    redacting each value replaced."""
    counts = Counter()
    chars = 0
    pairs = []
    for k, v in items:
        val, found, n = _short_val(v)
        counts.update(found)
        chars += n
        pairs.append(f"{k}={val}")
    return ", ".join(pairs), counts, chars


def _short_val(v) -> tuple[str, Counter, int]:
    """Truncate a value for display, redacting it first: the prefix of a key
    left by an 80-char cut is a key, and no pattern would match it afterwards."""
    s, counts, chars = redact_to_sentinels(str(v))
    if len(s) > 80:
        s = drop_partial_marker(s[:80]) + "..."
    return s, counts, chars


def _extract_human_from_command(text: str) -> str:
    """Extract human-readable content from XML command wrappers."""
    import re
    # Look for command-name
    cmd_match = re.search(r"<command-name>(/\w+)</command-name>", text)
    # Look for stdout content
    stdout_match = re.search(r"<local-command-stdout>(.*?)</local-command-stdout>", text, re.DOTALL)
    parts = []
    if cmd_match:
        parts.append(cmd_match.group(1))
    if stdout_match:
        stdout = stdout_match.group(1).strip()
        if stdout and stdout != "(no content)":
            parts.append(f"→ {stdout}")
    return " ".join(parts) if parts else ""


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[1])
    p.add_argument("input", help="path to session JSONL file")
    p.add_argument("--max-result-chars", type=int, default=DEFAULT_MAX_RESULT_CHARS,
                    help=f"truncate non-error tool results to this many chars (default: {DEFAULT_MAX_RESULT_CHARS})")
    p.add_argument("-o", "--output", help="write to file instead of stdout")
    p.add_argument("--stats", action="store_true", help="print processing stats to stderr")
    args = p.parse_args()

    result = preprocess(args.input, args.max_result_chars)

    if args.stats:
        lines = result.count("\n")
        chars = len(result)
        original = Path(args.input).stat().st_size
        ratio = chars / original * 100 if original else 0
        print(f"[preprocess] {original:,} bytes → {chars:,} chars ({ratio:.0f}%)", file=sys.stderr)
        print(f"[preprocess] {lines} output lines", file=sys.stderr)

    if args.output:
        Path(args.output).write_text(result)
        if args.stats:
            print(f"[preprocess] written to {args.output}", file=sys.stderr)
    else:
        print(result)


if __name__ == "__main__":
    main()
