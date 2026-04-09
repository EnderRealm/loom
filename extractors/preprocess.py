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

Usage:
    ./preprocess.py <session.jsonl> [--max-result-chars 500]
"""

import argparse
import json
import sys
from pathlib import Path

DEFAULT_MAX_RESULT_CHARS = 500


def preprocess(jsonl_path: str, max_result_chars: int = DEFAULT_MAX_RESULT_CHARS) -> str:
    """Read a session jsonl and return a filtered conversation thread as text."""
    path = Path(jsonl_path)
    if not path.exists():
        raise FileNotFoundError(f"not found: {path}")

    lines = path.read_text().splitlines()
    blocks = []

    for line_no, raw in enumerate(lines, 1):
        if not raw.strip():
            continue
        try:
            record = json.loads(raw)
        except json.JSONDecodeError:
            continue

        rtype = record.get("type", "")

        if rtype == "assistant":
            blocks.extend(_process_assistant(record))
        elif rtype == "user":
            blocks.extend(_process_user(record, max_result_chars))
        # Discard: system, file-history-snapshot, permission-mode, attachment, summary, result

    return "\n".join(blocks)


def _process_assistant(record: dict) -> list[str]:
    """Extract text and tool-use summaries from an assistant record."""
    out = []
    content = record.get("message", {}).get("content", [])
    if not isinstance(content, list):
        return out

    for block in content:
        btype = block.get("type", "")

        if btype == "text":
            text = block.get("text", "").strip()
            if text:
                out.append(f"ASSISTANT:\n{text}\n")

        elif btype == "tool_use":
            name = block.get("name", "?")
            inp = block.get("input", {})
            summary = _summarize_tool_input(name, inp)
            out.append(f"TOOL: {name}({summary})")

        # Skip thinking (empty/redacted) and other block types

    return out


def _process_user(record: dict, max_result_chars: int) -> list[str]:
    """Extract human input and tool results from a user record."""
    out = []
    content = record.get("message", {}).get("content")

    if content is None:
        content = record.get("content", "")

    # String content: direct human input or slash command
    if isinstance(content, str):
        text = content.strip()
        if not text:
            return out
        # Filter out pure XML command wrappers with no human text
        if text.startswith("<local-command-caveat>") or text.startswith("<command-name>"):
            # Extract any human-readable parts
            human_text = _extract_human_from_command(text)
            if human_text:
                out.append(f"USER: {human_text}\n")
        else:
            out.append(f"USER: {text}\n")
        return out

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

                if is_error:
                    out.append(f"ERROR:\n{result_content}\n")
                else:
                    if len(result_content) > max_result_chars:
                        truncated = result_content[:max_result_chars]
                        out.append(f"RESULT: ({len(result_content)} chars, truncated)\n{truncated}...\n")
                    else:
                        out.append(f"RESULT:\n{result_content}\n")

    return out


def _summarize_tool_input(name: str, inp: dict) -> str:
    """Produce a short summary of tool input args."""
    if name in ("Bash",):
        cmd = inp.get("command", "")
        if len(cmd) > 120:
            cmd = cmd[:120] + "..."
        return cmd
    elif name in ("Read",):
        return inp.get("file_path", "?")
    elif name in ("Grep",):
        pattern = inp.get("pattern", "?")
        path = inp.get("path", ".")
        return f'"{pattern}" in {path}'
    elif name in ("Glob",):
        return inp.get("pattern", "?")
    elif name in ("Edit",):
        return inp.get("file_path", "?")
    elif name in ("Write",):
        return inp.get("file_path", "?")
    elif name in ("Agent",):
        desc = inp.get("description", "?")
        return desc
    elif name.startswith("mcp__"):
        # MCP tool: show the most useful params
        short_name = name.split("__")[-1]
        key_params = {k: v for k, v in inp.items() if v and k not in ("sessionId",)}
        if key_params:
            pairs = ", ".join(f"{k}={_short_val(v)}" for k, v in list(key_params.items())[:4])
            return f"{short_name}: {pairs}"
        return short_name
    else:
        # Generic: show first 2 key=value pairs
        if not inp:
            return ""
        pairs = ", ".join(f"{k}={_short_val(v)}" for k, v in list(inp.items())[:2])
        return pairs


def _short_val(v) -> str:
    """Truncate a value for display."""
    s = str(v)
    if len(s) > 80:
        return s[:80] + "..."
    return s


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
