#!/usr/bin/env python3
"""Truth extractor — runs the truth-extractor prompt against a session artifact
via the claude CLI, parses the output, and compares to hand-written reference
truths in knowledge/truths/<scope>/.

Usage:
    ./extract.py --input <session.md> [--scope forge|auto] [--project-path .]
                 [--model haiku] [--threshold 0.5]

`--scope auto` resolves the scope from --project-path via the repo's
`.loom-project` marker (see resolve_project.py).
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from datetime import date, datetime
from pathlib import Path

LOOM_ROOT = Path(__file__).resolve().parent.parent
SUMMARIZER_PATH = LOOM_ROOT / "extractors" / "summarizer.md"

# Knowledge store lives outside the loom repo so it survives reinstalls and
# accumulates across projects. Eval fixtures stay in-repo as test data.
KNOWLEDGE_ROOT = Path(os.environ.get(
    "LOOM_KNOWLEDGE_ROOT",
    str(Path.home() / ".loom" / "knowledge"),
)).expanduser()

# Import the pre-processor (sibling module)
sys.path.insert(0, str(LOOM_ROOT / "extractors"))
from knowledge_git import record_knowledge_commit, sanitize_record
from preprocess import preprocess as preprocess_jsonl
from resolve_project import describe as describe_project, resolve_project
CLAUDE_BIN = "/opt/homebrew/bin/claude"
CODEX_BIN = "/opt/homebrew/bin/codex"
EXAMPLE_DELIMITER = "\n\n===REFERENCE-EXAMPLE===\n\n"

# Per-type configuration: prompt, directories, sentinel.
# `candidates` mirrors the validated tree's type-first layout
# (_candidates/truths/<scope>/, _candidates/decisions/<scope>/) so the same
# scope hierarchy applies on both sides of the human-review gate.
TYPE_CONFIG = {
    "truth": {
        "prompt": LOOM_ROOT / "extractors" / "truth-extractor.md",
        "training": KNOWLEDGE_ROOT / "truths",
        "candidates": KNOWLEDGE_ROOT / "_candidates" / "truths",
        "eval": LOOM_ROOT / "knowledge" / "truths-eval",
        "sentinel": "===END-OF-TRUTH===",
    },
    "decision": {
        "prompt": LOOM_ROOT / "extractors" / "decision-extractor.md",
        "training": KNOWLEDGE_ROOT / "decisions",
        "candidates": KNOWLEDGE_ROOT / "_candidates" / "decisions",
        "eval": LOOM_ROOT / "knowledge" / "decisions-eval",
        "sentinel": "===END-OF-DECISION===",
    },
}

STOPWORDS = set("a an and are as at be but by for from has have if in is it of on or that the this to was were will with not no which when where who why how into over under across between".split())

INPUT_GUIDANCE_SUMMARY = """You will receive a session artifact. Most often it is a markdown summary with a frontmatter block (project, session_id, date) and sections like `### Overview`, `### Decisions`, `### Problems`, `### Discoveries`. Prioritize the `Discoveries` section — it contains verified claims. Read `Problems` and `Decisions` for context but be careful: not everything stated there is a truth.

Also look at `### Outcome` for the session's own self-assessment of what landed vs what didn't. A "partially_achieved" outcome often means the session surfaced *discoveries* more than *work* — exactly what you want for truth extraction."""

INPUT_GUIDANCE_RAW = """You will receive a **pre-processed conversation transcript** from a Claude Code session. The format uses labeled blocks:

- **ASSISTANT:** — Claude's visible analysis, recommendations, and discoveries. This is your primary extraction source. Look for architectural claims, root cause analyses, mechanism descriptions, and corrections ("I had that wrong", "this means...").
- **USER:** — Human input. Short messages are decisions/corrections ("no", "Let's do B", "that's wrong"). Longer blocks may be skill prompts or injected context — skim those for structure but don't extract truths from boilerplate.
- **TOOL:** — One-line summaries of tool calls (e.g., `TOOL: Grep("pattern" in path)`). These show what was investigated but rarely contain truths directly.
- **RESULT:** — Tool output, truncated. Occasionally contains the specific evidence that proves a truth (grep counts, error messages, file contents).
- **ERROR:** — Tool failures. These often trigger the root-cause analysis in the next ASSISTANT block — pay attention to the analysis, not just the error.

Key patterns to watch for in raw transcripts:

1. **Corrections/reversals**: "I had that wrong", "actually it's X not Y", "Option A would have broken..." — the correction itself is often the truth.
2. **Root cause chains**: multiple errors → investigation → single root cause. The root cause mechanism is the truth.
3. **User pushback**: when the user says "no" or redirects, the new direction often reveals a constraint or architectural fact.
4. **Evidence-backed claims**: when an ASSISTANT block cites specific file paths, line numbers, grep counts, or commit shas, and draws a conclusion — that's a high-confidence truth candidate.
5. **Architecture statements**: "X is designed as Y", "agents are stateless text processors", "the dist bundle is what runs, not the source" — direct claims about how systems work.

Unlike summaries, raw transcripts do NOT have curated `### Discoveries` sections. You must find the signal in the conversation flow. Expect more noise — but also richer evidence and corrections that summaries sometimes miss."""


def load_reference_truths_from(scope_dir: Path) -> list[dict]:
    if not scope_dir.exists():
        return []
    truths = []
    for path in sorted(scope_dir.glob("*.md")):
        if path.name.startswith("_"):
            continue
        truths.append(parse_truth(path.read_text(), source=str(path)))
    return truths


def parse_truth(text: str, source: str = "") -> dict:
    text = text.strip()
    fm_match = re.match(r"^---\n(.*?)\n---\n(.*)$", text, re.DOTALL)
    if not fm_match:
        return {"source": source, "valid": False, "error": "no frontmatter", "raw": text}
    fm_text, body = fm_match.groups()

    frontmatter = {}
    for line in fm_text.splitlines():
        if ":" in line and not line.startswith(" ") and not line.startswith("-") and not line.startswith("\t"):
            key, _, value = line.partition(":")
            frontmatter[key.strip()] = value.strip()

    sections = {}
    current = None
    buf = []
    for line in body.splitlines():
        if line.startswith("## "):
            if current is not None:
                sections[current] = "\n".join(buf).strip()
            current = line[3:].strip().lower().replace(" ", "_")
            buf = []
        else:
            buf.append(line)
    if current is not None:
        sections[current] = "\n".join(buf).strip()

    evidence_paths = re.findall(r"path:\s*(\S+)", fm_text)
    evidence_commits = re.findall(r"commit:\s*(\S+)", fm_text)
    # Extract source session IDs (may be multiple in the sources: block)
    source_sessions = re.findall(r"session:\s*(\S+)", fm_text)

    has_verify = bool(sections.get("how_to_verify"))
    has_claim = bool(sections.get("claim"))
    has_choice = bool(sections.get("choice"))
    has_rationale = bool(sections.get("rationale"))

    # Valid if it has id + either truth sections (claim+verify) or decision sections (choice+rationale)
    is_truth = has_claim and has_verify
    is_decision = has_choice and has_rationale
    has_content = is_truth or is_decision

    # Validation: hard requirements vs warnings
    warnings = []
    if not frontmatter.get("scope"):
        warnings.append("missing scope")
    if not frontmatter.get("type"):
        warnings.append("missing type")
    if not frontmatter.get("status"):
        warnings.append("missing status")
    if not evidence_paths and not evidence_commits:
        warnings.append("no evidence (no path: or commit: entries)")
    if not source_sessions:
        warnings.append("no source session")
    if not frontmatter.get("verified_at"):
        warnings.append("missing verified_at")

    evidence_count = len(evidence_paths) + len(evidence_commits)

    return {
        "source": source,
        "valid": has_content and bool(frontmatter.get("id")),
        "id": frontmatter.get("id", ""),
        "title": frontmatter.get("title", ""),
        "scope": frontmatter.get("scope", ""),
        "type": frontmatter.get("type", ""),
        "status": frontmatter.get("status", ""),
        "claim": sections.get("claim", "") or sections.get("choice", ""),
        "verify": sections.get("how_to_verify", ""),
        "why": sections.get("why_it_matters", ""),
        "choice": sections.get("choice", ""),
        "alternatives": sections.get("alternatives", ""),
        "rationale": sections.get("rationale", ""),
        "principle": sections.get("principle", ""),
        "evidence_paths": evidence_paths,
        "evidence_commits": evidence_commits,
        "evidence_count": evidence_count,
        "source_sessions": source_sessions,
        "warnings": warnings,
        "raw": text,
    }


def _extract_session_id(input_path: Path) -> str:
    """Extract a session ID from the input file.

    For summaries: parse session_id from YAML frontmatter.
    For raw jsonl: the filename (minus extension) IS the session ID.
    """
    name = input_path.stem  # e.g. "c7dbbcca-forge-apr07" or "c7dbbcca-cceb-452b-aafd-993eb791e230"
    if str(input_path).endswith(".jsonl"):
        return name  # full UUID is the session ID

    # Summary: try frontmatter first
    try:
        for line in input_path.read_text().splitlines()[:20]:
            if line.startswith("session_id:"):
                return line.split(":", 1)[1].strip()
    except Exception:
        pass

    # Fallback: first segment of filename (before first dash-separated label)
    # e.g. "c7dbbcca-forge-apr07" → "c7dbbcca"
    parts = name.split("-")
    if parts:
        return parts[0]
    return ""


def build_prompt(template: str, refs: list[dict], input_text: str, today: str, input_format: str = "summary", session_id: str = "") -> str:
    ref_block = EXAMPLE_DELIMITER.join(r["raw"] for r in refs)
    guidance = INPUT_GUIDANCE_RAW if input_format == "raw" else INPUT_GUIDANCE_SUMMARY
    session_value = session_id or "<session uuid from input frontmatter>"
    return (
        template
        .replace("{INPUT_GUIDANCE}", guidance)
        .replace("{REFERENCE_EXAMPLES}", ref_block)
        .replace("{SESSION_ID}", session_value)
        .replace("{INPUT}", input_text)
        .replace("{TODAY}", today)
    )


def call_claude(prompt: str, model: str) -> str:
    proc = subprocess.run(
        [CLAUDE_BIN, "-p", "--model", model],
        input=prompt,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        sys.stderr.write(f"claude CLI failed (exit {proc.returncode}):\n{proc.stderr}\n")
        sys.exit(2)
    return proc.stdout


def call_codex(prompt: str, model: str, reasoning: str = "medium") -> str:
    """Call codex exec non-interactively. Returns the agent's final message."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".txt", delete=False) as tmp:
        out_path = tmp.name
    try:
        cmd = [
            CODEX_BIN, "exec",
            "--skip-git-repo-check",
            "--sandbox", "read-only",
            "--ephemeral",
            "-o", out_path,
            "-c", f"model_reasoning_effort={reasoning}",
        ]
        if model and model != "default":
            cmd.extend(["-m", model])
        proc = subprocess.run(
            cmd,
            input=prompt,
            capture_output=True,
            text=True,
            check=False,
            timeout=900,
        )
        if proc.returncode != 0:
            sys.stderr.write(f"codex exec failed (exit {proc.returncode}):\n{proc.stderr[-2000:]}\n")
            sys.exit(2)
        try:
            with open(out_path) as f:
                return f.read()
        except FileNotFoundError:
            sys.stderr.write(f"codex exec produced no output file at {out_path}\nstdout tail:\n{proc.stdout[-2000:]}\n")
            sys.exit(2)
    finally:
        try:
            os.unlink(out_path)
        except FileNotFoundError:
            pass


def call_llm(prompt: str, provider: str, model: str, reasoning: str = "medium") -> str:
    if provider == "codex":
        return call_codex(prompt, model, reasoning)
    return call_claude(prompt, model)


def parse_output(text: str, sentinel: str = "===END-OF-TRUTH===") -> list[dict]:
    text = text.strip()
    if text in ("NO_TRUTHS", "NO_DECISIONS"):
        return []
    # Tolerate model preambles, trailing commentary, or markdown code fences
    # around the output. Extract each `---\n...\n---\n<body>` block individually
    # via regex, regardless of the sentinel.
    escaped_sentinel = re.escape(sentinel)
    truth_pattern = re.compile(
        rf"(?:^|\n)---\n(.*?)\n---\n(.*?)(?=\n---\n|\n{escaped_sentinel}|\Z)",
        re.DOTALL,
    )
    results = []
    for fm_match in truth_pattern.finditer(text):
        fm_text = fm_match.group(1)
        body = fm_match.group(2)
        # Strip trailing sentinel if present
        body = re.sub(rf"\n?{escaped_sentinel}\s*$", "", body)
        chunk = f"---\n{fm_text}\n---\n{body}"
        results.append(parse_truth(chunk))
    return results


def inject_frontmatter(raw: str, fields: dict) -> str:
    """Append fields to a candidate's `---` frontmatter block.

    The truth/decision parser is last-write-wins, so appended keys override
    any earlier value (e.g., a model-emitted `status: validated` becomes
    `status: candidate` once we re-write).
    """
    m = re.match(r"^---\n(.*?)\n---\n(.*)$", raw, re.DOTALL)
    if not m:
        return raw
    fm, body = m.group(1), m.group(2)
    extra = "\n".join(f"{k}: {v}" for k, v in fields.items())
    return f"---\n{fm}\n{extra}\n---\n{body}"


def override_source_sessions(raw: str, session_id: str) -> str:
    """Force every `session:` key in the frontmatter to the authoritative id.

    Model-emitted session ids are unreliable (hallucinated slugs instead of the
    real UUID), so the id derived from the input file is authoritative. Only the
    `sources:` block carries `session:` keys in practice; the body is never
    touched. If the model omitted the sources block entirely, append a minimal
    one so the candidate still cites its source.
    """
    if not session_id:
        return raw
    m = re.match(r"^---\n(.*?)\n---\n(.*)$", raw, re.DOTALL)
    if not m:
        return raw
    fm, body = m.group(1), m.group(2)
    # Anchored to line start so `session:` inside prose values (e.g. an
    # evidence note) is never rewritten.
    key_pattern = re.compile(r"^(\s*-?\s*session:[ \t]*)\S+", re.MULTILINE)
    if key_pattern.search(fm):
        fm = key_pattern.sub(lambda m: m.group(1) + session_id, fm)
    else:
        fm = f"{fm}\nsources:\n  - session: {session_id}"
    return f"---\n{fm}\n---\n{body}"


# A candidate id becomes a filename, and it comes from model output steered by
# a transcript this process did not author — one that reaches the extractor
# unattended from any machine holding a receiver token. Bound it to a single
# conservative path segment so no `../` in an id can place a write outside the
# candidates directory.
# `\Z`, not `$`: `$` would also match before a trailing newline.
CANDIDATE_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,120}\Z")

# Git's commit confirmation line, e.g. "[main 2bbeb99] [loom/x-1a2b] Subject".
# Mirrors commitLineRe in internal/summaries/commits.go — keep the two in step.
# Requiring the line establishes that the transcript *recorded* a commit, which
# a bare scan for `[id]`-shaped tokens does not: a transcript is thick with
# prose mentions and quoted file contents. It says nothing about the client
# being honest — a transcript is fully client-authored and reaches us on a
# receiver token, so a citation inherits the transcript's trust level.
COMMIT_LINE_RE = re.compile(r"^\[(.+) ([0-9a-f]{7,40})\] (.+)$")

# The ticket marker is the `[...]` prefix of the commit subject.
COMMIT_MARKER_RE = re.compile(r"^\[([^\[\]]+)\]")

# A ticket id derived this way reaches frontmatter from a transcript this
# process did not author — the same threat model as CANDIDATE_ID_RE. Require a
# namespaced `<project>/<slug>` on a conservative charset (no whitespace, colon,
# newline or bracket), which also stops git's own `[main 2bbeb99]` bracket from
# ever reading as a marker.
# `\Z`, not `$`: `$` would also match before a trailing newline.
TICKET_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,60}/[A-Za-z0-9][A-Za-z0-9._-]{0,60}\Z")

# Any frontmatter line carrying a `ticket:` key, list entry or not. YAML also
# accepts a quoted key and whitespace before the colon, so `- "ticket" : x` has
# to read as the same key.
TICKET_ENTRY_RE = re.compile(r"^[ \t]*-?[ \t]*[\"']?ticket[\"']?[ \t]*:")

# Every derived id appends a line to every candidate the run emits, and a
# client can author as many distinct commit confirmation lines as it likes.
# Bound the collection the way the rest of this path bounds transcript-derived
# values (CANDIDATE_ID_RE's length, preprocess.py's result truncation).
MAX_SOURCE_TICKETS = 32


def _ticket_ids_from_jsonl(path: Path) -> list[str]:
    """Scan a raw session jsonl for ticket ids in git commit confirmations."""
    ids: list[str] = []
    seen: set[str] = set()
    rejected: set[str] = set()
    bash_tool_use_ids: set[str] = set()
    # Session jsonl is UTF-8 by construction; name it rather than inheriting
    # the locale's encoding, so `replace` only ever absorbs genuine corruption.
    with open(path, encoding="utf-8", errors="replace") as f:
        for raw in f:
            if not raw.strip():
                continue
            try:
                record = json.loads(raw)
            except json.JSONDecodeError:
                continue
            if not isinstance(record, dict):
                continue
            rtype = record.get("type", "")
            if rtype not in ("assistant", "user"):
                continue
            message = record.get("message")
            content = message.get("content") if isinstance(message, dict) else None
            if not isinstance(content, list):
                continue
            for block in content:
                if not isinstance(block, dict):
                    continue
                btype = block.get("type", "")
                if rtype == "assistant" and btype == "tool_use":
                    if block.get("name") == "Bash" and isinstance(block.get("id"), str):
                        bash_tool_use_ids.add(block["id"])
                    continue
                # Only Bash results carry git output; a commit-shaped line in
                # any other tool's result is quoted text, not a commit.
                if rtype != "user" or btype != "tool_result":
                    continue
                if block.get("tool_use_id") not in bash_tool_use_ids:
                    continue
                text = block.get("content", "")
                # Same part shapes preprocess.py's _process_user normalizes:
                # text blocks and bare strings.
                if isinstance(text, list):
                    parts = []
                    for part in text:
                        if isinstance(part, dict) and part.get("type") == "text":
                            parts.append(part.get("text", ""))
                        elif isinstance(part, str):
                            parts.append(part)
                    text = "\n".join(parts)
                if not isinstance(text, str):
                    continue
                for line in text.split("\n"):
                    commit = COMMIT_LINE_RE.match(line.strip())
                    if not commit:
                        continue
                    marker = COMMIT_MARKER_RE.match(commit.group(3))
                    if not marker:
                        continue
                    ticket_id = marker.group(1)
                    if not TICKET_ID_RE.match(ticket_id):
                        # Say so rather than dropping it silently: otherwise a
                        # session whose markers were all rejected logs exactly
                        # like one that committed nothing, and the transcript
                        # is the only evidence on an unattended path.
                        if ticket_id not in rejected:
                            rejected.add(ticket_id)
                            print(f"[extract] warning: ignoring malformed ticket "
                                  f"marker {ticket_id[:80]!r}", file=sys.stderr)
                        continue
                    if ticket_id in seen:
                        continue
                    seen.add(ticket_id)
                    ids.append(ticket_id)
                    if len(ids) >= MAX_SOURCE_TICKETS:
                        print(f"[extract] warning: {path} hit the "
                              f"{MAX_SOURCE_TICKETS}-ticket cap — later ids ignored",
                              file=sys.stderr)
                        return ids
    return ids


def extract_ticket_ids(input_path: Path) -> list[str]:
    """Ticket ids cited by the commits an input session landed, first seen first.

    Only raw jsonl carries commit data, so the caller gates on the resolved
    input format rather than this function re-deriving it. Read from the jsonl
    rather than preprocess.py's output because that truncates non-error tool
    results to 500 chars and git hooks print preamble, so a commit confirmation
    can fall outside that window.

    Never raises: extraction of knowledge must not be lost because a citation
    could not be derived, so an unreadable file or an unexpected record shape
    degrades to no ticket ids.
    """
    try:
        return _ticket_ids_from_jsonl(input_path)
    except Exception as e:
        print(f"[extract] warning: could not derive ticket ids from {input_path}: {e}",
              file=sys.stderr)
        return []


def inject_source_tickets(raw: str, ticket_ids: list[str]) -> str:
    """Add one `ticket:` entry to the frontmatter `sources:` block per id.

    The ids are derived from the session's own git commit confirmation lines,
    so — like `override_source_sessions` — they are authoritative: any
    model-emitted `ticket:` *list entry* is dropped, including when the
    derivation yielded nothing. That is the common case, not the rare one (a
    session that landed no commits), and it is exactly where an unvalidated
    model-emitted id would otherwise survive into the store. The strip covers
    the block-sequence spellings YAML accepts for the key (bare, quoted,
    space before the colon) — not a flow mapping like `- {ticket: x}`, which
    neither template invites and which no reader parses today.

    This keeps the *field* trustworthy; it is not a trust boundary on the
    artifact's citations. `knowledge.Rank` (internal/knowledge/relevant.go)
    tests citation by plain substring over the whole body, so a model set on a
    `--for-ticket` hit can simply name the id in its claim prose, key or no
    key. What actually bounds that is promotion: `Rank` skips anything not
    `status: validated`, so a forged citation has to clear a human first.

    Entries land at the end of the `sources:` block rather than the end of the
    frontmatter, because `sources:` is followed by top-level keys in both
    extractor templates (`related:` then `verified_at:` for truths,
    `recorded_at:` for decisions).
    """
    m = re.match(r"^---\n(.*?)\n---\n(.*)$", raw, re.DOTALL)
    if not m:
        return raw
    fm, body = m.group(1), m.group(2)
    # Line-anchored, so `ticket:` inside a prose value (e.g. an evidence note)
    # is never dropped.
    lines = [ln for ln in fm.split("\n") if not TICKET_ENTRY_RE.match(ln)]
    if ticket_ids:
        entries = [f"  - ticket: {t}" for t in ticket_ids]
        # The last `sources:`, not the first: `override_source_sessions`
        # appends its own block whenever the model's frontmatter carries no
        # `session:` key (including under a model-emitted `sources: []`), and
        # the block holding the session id is the one these entries belong in.
        sources_at = next((i for i in range(len(lines) - 1, -1, -1)
                           if lines[i].startswith("sources:")), None)
        if sources_at is None:
            lines.append("sources:")
            lines.extend(entries)
        else:
            # Insert at the end of the block's items: the frontmatter carries
            # top-level keys after `sources:`, and no top-level key can begin
            # with `-`, so a flush-left sequence item still reads as one.
            end = sources_at + 1
            while end < len(lines) and lines[end][:1] in (" ", "\t", "-"):
                end += 1
            lines[end:end] = entries
    fm = "\n".join(lines)
    return f"---\n{fm}\n---\n{body}"


def emit_candidates(candidates: list[dict], base_dir: Path, scope: str,
                    provider: str, model: str, reasoning: str | None,
                    session_id: str, ticket_ids: list[str]) -> list[Path]:
    """Write each valid candidate to base_dir/scope/<id>--<timestamp>.md.

    Adds `status: candidate`, `extracted_at`, `extracted_by` to frontmatter.
    Filename suffix is the wall-clock timestamp at run start, so a re-run on
    the same session produces a sibling rather than overwriting. Candidates
    whose id isn't a safe filename are skipped with a warning.
    """
    out_dir = base_dir / scope
    out_dir.mkdir(parents=True, exist_ok=True)
    now = datetime.now()
    timestamp_slug = now.strftime("%Y%m%d-%H%M%S")
    extracted_by = f"{provider}:{model}"
    if reasoning:
        extracted_by += f":{reasoning}"
    extracted_at = now.replace(microsecond=0).isoformat()

    resolved_out = out_dir.resolve()
    written = []
    for c in candidates:
        cid = c.get("id") or ""
        if not CANDIDATE_ID_RE.match(cid):
            print(f"  warn: skipping candidate with unusable id {cid!r}", file=sys.stderr)
            continue
        path = out_dir / f"{cid}--{timestamp_slug}.md"
        # Belt and braces: the write must land in out_dir even if the pattern
        # above is ever loosened.
        if path.resolve().parent != resolved_out:
            print(f"  warn: skipping candidate {cid!r}: {path} escapes {out_dir}", file=sys.stderr)
            continue
        raw = override_source_sessions(c["raw"], session_id)
        raw = inject_source_tickets(raw, ticket_ids)
        body = inject_frontmatter(raw, {
            "status": "candidate",
            "extracted_at": extracted_at,
            "extracted_by": extracted_by,
        })
        path.write_text(body)
        written.append(path)
    return written


def append_extract_log(extract_type: str, scope: str, session_id: str, count: int) -> Path | None:
    """Append one entry to ~/.loom/knowledge/log.md per extraction run, and hand
    back the file it appended to so the run's commit names that same file rather
    than deciding for itself where the entry went.

    Skips silently if log.md doesn't exist — the file is bootstrapped at store
    init, not by the extractor — and returns None, so nothing is committed for
    an entry that was never written.
    """
    log_path = KNOWLEDGE_ROOT / "log.md"
    if not log_path.exists():
        return None
    today = date.today().isoformat()
    with open(log_path, "a") as f:
        f.write(f"\n## [{today}] {run_label(extract_type, scope, session_id, count)}\n")
    return log_path


def short_session(session_id: str) -> str:
    """The id truncation run_label interpolates, kept out of the f-string so the
    fallback for a session id we could not derive stays legible."""
    return session_id[:8] if session_id else "?"


def run_label(extract_type: str, scope: str, session_id: str, count: int) -> str:
    """One run's label, verbatim in both the log.md entry (after its date) and
    the knowledge-store commit subject — they name the same unit, so an edit
    here moves both rather than desyncing them."""
    return f"extract {short_session(session_id)} | {scope} | {count} {extract_type} candidate(s)"


def keywords(text: str) -> set[str]:
    words = re.findall(r"[a-z][a-z0-9_-]{2,}", text.lower())
    return {w for w in words if w not in STOPWORDS}


def similarity(a: dict, b: dict) -> float:
    # Exact id match short-circuits — this is the same truth.
    if a.get("id") and a["id"] == b.get("id"):
        return 1.0
    score = 0.0
    ta, tb = keywords(a["title"]), keywords(b["title"])
    if ta and tb:
        score += 0.4 * len(ta & tb) / max(len(ta | tb), 1)
    ca, cb = keywords(a["claim"]), keywords(b["claim"])
    if ca and cb:
        score += 0.4 * len(ca & cb) / max(len(ca | cb), 1)
    pa, pb = set(a["evidence_paths"]), set(b["evidence_paths"])
    if pa and pb and (pa & pb):
        score += 0.2
    return min(score, 1.0)


def compare_keyword(candidates: list[dict], refs: list[dict]) -> dict:
    # Build all (ref_idx, cand_idx, score) triples, sort by score desc, then
    # greedy-assign. This is closer to optimal bipartite matching than the
    # naive per-ref greedy, which is order-dependent.
    valid_cands = [(i, c) for i, c in enumerate(candidates) if c.get("valid")]
    triples = []
    for ri, ref in enumerate(refs):
        for ci, cand in valid_cands:
            triples.append((similarity(ref, cand), ri, ci))
    triples.sort(key=lambda t: -t[0])

    ref_match = {}
    used_refs = set()
    used_cands = set()
    for score, ri, ci in triples:
        if ri in used_refs or ci in used_cands:
            continue
        ref_match[ri] = (ci, score)
        used_refs.add(ri)
        used_cands.add(ci)

    results = []
    for ri, ref in enumerate(refs):
        ci, score = ref_match.get(ri, (None, 0.0))
        results.append({
            "ref_id": ref["id"],
            "ref_title": ref["title"],
            "matched_idx": ci,
            "matched_id": candidates[ci]["id"] if ci is not None else None,
            "score": score,
        })
    mean = sum(r["score"] for r in results) / len(results) if results else 0.0
    extras = [i for i, c in enumerate(candidates) if c.get("valid") and i not in used_cands]
    return {"results": results, "mean": mean, "extras": extras}


JUDGE_PROMPT = """You are judging whether a truth extractor recovered a set of reference truths from an input session artifact. Your job: for each reference truth, identify which candidate (if any) describes the SAME underlying fact — even if wording, framing, or emphasis differs.

Two claims are the same truth if they describe the same architectural mechanism, root cause, or operational rule. Two claims are DIFFERENT if they describe different mechanisms, even if they share keywords.

Examples of equivalent framings (should MATCH):
- "Review agents have no tk access; skills must inline ticket content" == "Review skills must inline ticket content into agent prompts, because agents can't fetch it themselves"
- "Pipeline stages depend on (type, risk)" == "Using-forge SKILL.md is wrong about task and chore stages; source pipeline definition is authoritative"

Examples of different framings (should NOT match):
- "Stale dist bundle undeploys src/tk fixes" vs "Spec skill bypasses review gate" — unrelated mechanisms
- "MCP coercion failures cluster" vs "Review agents are stateless" — different subsystems

---

Reference truths:

{REFS}

Candidate truths extracted by the model:

{CANDS}

---

For each reference, output the letter of the best-matching candidate (if any), plus a score:
- 1.0 = same underlying fact, any framing
- 0.7 = strongly related, same general area, partially overlapping claim
- 0.3 = tangentially related
- 0.0 = unrelated or no match

Each candidate may match at most one reference (pick the best pairing).

Output ONLY a JSON object on a single line, no preamble:
{{"1": ["A", 1.0], "2": [null, 0.0], "3": ["C", 0.7], ...}}

Use null when no candidate matches the reference at all.
"""


def compare_llm(candidates: list[dict], refs: list[dict], provider: str, model: str, reasoning: str = "medium") -> dict:
    """Use an LLM to judge semantic equivalence between candidates and references."""
    valid = [c for c in candidates if c.get("valid")]
    if not valid or not refs:
        return {"results": [], "mean": 0.0, "extras": []}

    def fmt_truth(idx, label, t):
        lines = [
            f"{label}. id: {t['id']}",
            f"   title: {t['title']}",
            f"   claim: {t['claim'][:400]}",
        ]
        ev_paths = t.get("evidence_paths", [])
        ev_commits = t.get("evidence_commits", [])
        if ev_paths or ev_commits:
            ev_parts = [f"path:{p}" for p in ev_paths[:3]] + [f"commit:{c}" for c in ev_commits[:2]]
            lines.append(f"   evidence: {', '.join(ev_parts)}")
        else:
            lines.append(f"   evidence: NONE")
        return "\n".join(lines)

    refs_block = "\n\n".join(fmt_truth(i, str(i + 1), r) for i, r in enumerate(refs))
    cands_block = "\n\n".join(
        fmt_truth(i, chr(ord("A") + i), c) for i, c in enumerate(valid)
    )
    prompt = JUDGE_PROMPT.replace("{REFS}", refs_block).replace("{CANDS}", cands_block)

    raw = call_llm(prompt, provider, model, reasoning)
    # Extract JSON object from response (tolerate leading/trailing whitespace/text)
    match = re.search(r"\{[^{}]*\}", raw)
    if not match:
        sys.stderr.write(f"[judge] failed to parse JSON from response:\n{raw}\n")
        return compare_keyword(candidates, refs)
    try:
        parsed = json.loads(match.group(0))
    except json.JSONDecodeError as e:
        sys.stderr.write(f"[judge] json parse error: {e}\nraw: {match.group(0)}\n")
        return compare_keyword(candidates, refs)

    # Map valid-candidate index to its original index in the full candidates list
    valid_to_original = [i for i, c in enumerate(candidates) if c.get("valid")]

    results = []
    used_cands = set()
    for i, ref in enumerate(refs):
        key = str(i + 1)
        entry = parsed.get(key, [None, 0.0])
        if not isinstance(entry, list) or len(entry) != 2:
            entry = [None, 0.0]
        label, score = entry
        matched_idx = None
        matched_id = None
        if label and isinstance(label, str):
            cand_pos = ord(label.upper()) - ord("A")
            if 0 <= cand_pos < len(valid):
                matched_idx = valid_to_original[cand_pos]
                matched_id = candidates[matched_idx]["id"]
                used_cands.add(matched_idx)
        results.append({
            "ref_id": ref["id"],
            "ref_title": ref["title"],
            "matched_idx": matched_idx,
            "matched_id": matched_id,
            "score": float(score) if isinstance(score, (int, float)) else 0.0,
        })

    mean = sum(r["score"] for r in results) / len(results) if results else 0.0
    extras = [i for i, c in enumerate(candidates) if c.get("valid") and i not in used_cands]
    return {"results": results, "mean": mean, "extras": extras}


PRESETS = {
    "fast": {"provider": "codex", "model": "gpt-5", "reasoning": "low"},
    "deep": {"provider": "claude", "model": "sonnet", "reasoning": "medium"},
}


def main():
    p = argparse.ArgumentParser(
        description=__doc__.splitlines()[0],
        epilog="Presets: --preset fast (gpt-5 low, default) | --preset deep (sonnet)",
    )
    p.add_argument("--extract-type", default="truth", choices=list(TYPE_CONFIG),
                    help="what to extract: truth or decision (default: truth)")
    p.add_argument("--input", required=True, help="path to session artifact (markdown or jsonl)")
    p.add_argument("--input-format", default="auto", choices=["auto", "summary", "raw"],
                    help="input format: summary (markdown), raw (jsonl), or auto (detect from extension)")
    p.add_argument("--scope", default="forge",
                    help="scope directory under knowledge/truths, or 'auto' to resolve it "
                         "from --project-path via the repo's .loom-project marker")
    p.add_argument("--project-path", default=".", help="path --scope auto resolves from (default: cwd)")
    p.add_argument("--preset", choices=list(PRESETS), help="shortcut: fast (gpt-5 low) or deep (sonnet)")
    p.add_argument("--provider", default="codex", choices=["claude", "codex"], help="LLM provider (default: codex)")
    p.add_argument("--model", default="gpt-5", help="model alias/id (default: gpt-5)")
    p.add_argument("--reasoning", default="low", choices=["low", "medium", "high", "xhigh"], help="codex reasoning effort (default: low)")
    p.add_argument("--threshold", type=float, default=0.5, help="pass threshold for mean score")
    p.add_argument("--dry-run", action="store_true", help="print the full prompt and exit")
    p.add_argument("--show-output", action="store_true", help="print raw model output before parsing")
    p.add_argument("--judge", default="llm", choices=["keyword", "llm"], help="scoring method (default: llm)")
    p.add_argument("--judge-provider", default="claude", choices=["claude", "codex"], help="LLM provider for the judge")
    p.add_argument("--judge-model", default="haiku", help="model for --judge=llm")
    p.add_argument("--judge-reasoning", default="low", choices=["low", "medium", "high", "xhigh"], help="codex reasoning effort for the judge")
    p.add_argument("--json-out", default="", help="write a structured json result to this path")
    p.add_argument("--raw-out", default="", help="write raw model output to this path")
    p.add_argument("--summarize", action="store_true",
                    help="two-stage pipeline for raw input: preprocess → summarize → extract. "
                         "Adds one LLM call but improves extraction quality on raw transcripts.")
    p.add_argument("--summarize-model", default="", help="model for summarization step (defaults to same as --model)")
    p.add_argument("--summarize-provider", default="", help="provider for summarization step (defaults to same as --provider)")
    p.add_argument("--summarize-reasoning", default="", help="reasoning effort for summarization step (defaults to same as --reasoning)")
    p.add_argument("--benchmark", action="store_true",
                    help="score against eval set (truths-eval/) instead of training set (truths/). "
                         "Training refs are still shown as few-shot examples — eval refs are never shown.")
    p.add_argument("--no-emit-candidates", dest="emit_candidates", action="store_false",
                    help="don't write candidate files to ~/.loom/knowledge/_candidates/<type>s/<scope>/. "
                         "Useful for benchmark / debugging runs.")
    p.set_defaults(emit_candidates=True)
    args = p.parse_args()

    # Apply preset overrides (only if user didn't also set the individual flags)
    if args.preset:
        preset = PRESETS[args.preset]
        for k, v in preset.items():
            setattr(args, k, v)

    # --scope auto derives the scope from the project's .loom-project marker, so
    # everything downstream sees a resolved name.
    if args.scope == "auto":
        resolved = resolve_project(args.project_path)
        for warning in resolved.warnings:
            print(f"[extract] scope auto: {warning}", file=sys.stderr)
        if resolved.name is None:
            sys.exit(f"scope auto: cannot resolve a project name for {args.project_path} — pass an explicit --scope")
        print(f"[extract] scope auto: {describe_project(resolved)}", file=sys.stderr)
        args.scope = resolved.name

    input_path = Path(args.input).expanduser()
    if not input_path.exists():
        sys.exit(f"input not found: {input_path}")

    # Authoritative session id derived from the input file (filename for raw
    # jsonl, frontmatter for summaries). Model-emitted ids are overridden with
    # this value at emission.
    session_id = _extract_session_id(input_path)

    # Resolve type-specific config (prompt, directories, sentinel)
    tcfg = TYPE_CONFIG[args.extract_type]
    prompt_path = tcfg["prompt"]
    knowledge_root = tcfg["training"]
    eval_root = tcfg["eval"]
    sentinel = tcfg["sentinel"]

    if not prompt_path.exists():
        sys.exit(f"prompt not found: {prompt_path}")

    template = prompt_path.read_text()

    # Training refs: always loaded, injected as few-shot examples in the prompt.
    training_refs = load_reference_truths_from(knowledge_root / args.scope)
    if not training_refs:
        print(f"warning: no training {args.extract_type}s in {knowledge_root/args.scope}", file=sys.stderr)

    # Scoring refs: what we score candidates against.
    # --benchmark: score against eval set (never shown to model), filtered to
    # truths sourced from the same session as the input artifact.
    # default: score against training set (same as examples — legacy mode).
    if args.benchmark:
        eval_dir = eval_root / args.scope
        all_eval_refs = load_reference_truths_from(eval_dir)
        if not all_eval_refs:
            sys.exit(f"no eval truths in {eval_dir} — cannot benchmark without eval set")

        # Filter eval refs to those sourced from the input's session.
        if session_id:
            scoring_refs = [r for r in all_eval_refs
                           if any(session_id.startswith(sid) or sid.startswith(session_id)
                                  for sid in r.get("source_sessions", []))]
            if not scoring_refs:
                print(f"warning: no eval truths match session {session_id} — scoring against all {len(all_eval_refs)} eval refs", file=sys.stderr)
                scoring_refs = all_eval_refs
        else:
            print(f"warning: could not extract session ID from input — scoring against all {len(all_eval_refs)} eval refs", file=sys.stderr)
            scoring_refs = all_eval_refs

        print(f"[extract] BENCHMARK mode: {len(training_refs)} training examples, {len(scoring_refs)} eval targets (filtered from {len(all_eval_refs)} total)", file=sys.stderr)
    else:
        scoring_refs = training_refs

    # For prompt building, always use training refs as few-shot examples.
    refs = training_refs

    # Determine input format
    input_format = args.input_format
    if input_format == "auto":
        input_format = "raw" if str(input_path).endswith(".jsonl") else "summary"

    # Tickets the session's commits cited, derived from the raw jsonl. Emitted
    # into every candidate's sources block as a citation back to the intent.
    # Must be derived here: only raw input carries commit data, and the
    # --summarize block below reassigns input_format to "summary" once it has
    # replaced the transcript with its summary.
    ticket_ids: list[str] = []
    if input_format == "raw":
        ticket_ids = extract_ticket_ids(input_path)
        print(f"[extract] source tickets: {', '.join(ticket_ids) if ticket_ids else 'none found'}", file=sys.stderr)
    else:
        print("[extract] source tickets: n/a (summary input carries no commits)", file=sys.stderr)

    # Pre-process raw jsonl into a conversation thread
    if input_format == "raw":
        print(f"[extract] pre-processing raw jsonl...", file=sys.stderr)
        input_text = preprocess_jsonl(str(input_path))
        print(f"[extract] preprocessed: {len(input_text):,} chars", file=sys.stderr)
    else:
        input_text = input_path.read_text()

    # Two-stage pipeline: summarize the preprocessed transcript before extraction.
    # Only applies to raw input. Summary input is already compressed.
    if args.summarize and input_format == "raw":
        if not SUMMARIZER_PATH.exists():
            sys.exit(f"summarizer prompt not found: {SUMMARIZER_PATH}")
        sum_provider = args.summarize_provider or args.provider
        sum_model = args.summarize_model or args.model
        sum_reasoning = args.summarize_reasoning or args.reasoning
        sum_template = SUMMARIZER_PATH.read_text()
        sum_prompt = sum_template.replace("{INPUT}", input_text)
        print(f"[extract] summarizing with {sum_provider}:{sum_model} (reasoning={sum_reasoning})...", file=sys.stderr)
        sum_start = time.time()
        summary_text = call_llm(sum_prompt, sum_provider, sum_model, sum_reasoning)
        sum_secs = time.time() - sum_start
        print(f"[extract] summary: {len(summary_text):,} chars in {sum_secs:.1f}s", file=sys.stderr)

        # Save intermediate summary if raw-out path is set
        raw_path = args.raw_out
        if not raw_path and args.json_out:
            raw_path = args.json_out.replace(".json", ".raw.txt")
        if raw_path:
            sum_path = raw_path.replace(".raw.txt", ".summary.txt")
            Path(sum_path).write_text(summary_text)
            print(f"[extract] intermediate summary saved to {sum_path}", file=sys.stderr)

        # Feed the summary (not the raw transcript) to the truth extractor.
        # Switch input_format to "summary" so the extractor prompt uses summary guidance.
        input_text = summary_text
        input_format = "summary"
    elif args.summarize and input_format != "raw":
        print(f"[extract] --summarize ignored (input is already a summary)", file=sys.stderr)

    today = date.today().isoformat()
    prompt = build_prompt(template, refs, input_text, today, input_format, session_id)

    if args.dry_run:
        print(prompt)
        return

    print(f"[extract] type: {args.extract_type}", file=sys.stderr)
    print(f"[extract] input: {input_path} ({input_format})", file=sys.stderr)
    print(f"[extract] scope: {args.scope}", file=sys.stderr)
    print(f"[extract] provider: {args.provider}", file=sys.stderr)
    print(f"[extract] model: {args.model}", file=sys.stderr)
    if args.provider == "codex":
        print(f"[extract] reasoning: {args.reasoning}", file=sys.stderr)
    print(f"[extract] refs loaded: {len(refs)}", file=sys.stderr)
    print(f"[extract] prompt size: {len(prompt)} chars", file=sys.stderr)
    print(f"[extract] calling {args.provider} CLI...", file=sys.stderr)

    extract_start = time.time()
    output = call_llm(prompt, args.provider, args.model, args.reasoning)
    extract_secs = time.time() - extract_start

    print(f"[extract] response: {len(output)} chars in {extract_secs:.1f}s", file=sys.stderr)

    # Save raw output if requested (or auto-derive path from json-out)
    raw_path = args.raw_out
    if not raw_path and args.json_out:
        raw_path = args.json_out.replace(".json", ".raw.txt")
    if raw_path:
        Path(raw_path).write_text(output)
        print(f"[extract] raw output saved to {raw_path}", file=sys.stderr)

    if args.show_output:
        print("\n---RAW OUTPUT---", file=sys.stderr)
        print(output, file=sys.stderr)
        print("---END RAW OUTPUT---\n", file=sys.stderr)

    candidates = parse_output(output, sentinel)
    valid = [c for c in candidates if c.get("valid")]
    invalid = [c for c in candidates if not c.get("valid")]
    print(f"[extract] parsed {len(valid)} valid / {len(invalid)} invalid candidate(s)")

    warned = 0
    for c in valid:
        title = c["title"][:70] or "<no title>"
        ev = c.get("evidence_count", 0)
        ev_tag = f" [ev:{ev}]" if ev else " [NO-EVIDENCE]"
        print(f"  + {c['id'] or '<no id>':50} {title}{ev_tag}")
        if c.get("warnings"):
            warned += 1
            for w in c["warnings"]:
                print(f"    warn: {w}")
    for c in invalid:
        err = c.get("error", "missing required sections/fields")
        print(f"  ! <invalid>                                          {err}")
    if warned:
        print(f"  ({warned} candidate(s) with schema warnings)")

    # Persist candidates to the knowledge store unless explicitly disabled
    # or running in benchmark mode (a measurement run, not production).
    if args.emit_candidates and not args.benchmark and valid:
        reasoning = args.reasoning if args.provider == "codex" else None
        written = emit_candidates(valid, tcfg["candidates"], args.scope,
                                  args.provider, args.model, reasoning, session_id,
                                  ticket_ids)
        print(f"[extract] wrote {len(written)} candidate(s) → {tcfg['candidates']}/{args.scope}/", file=sys.stderr)
        log_path = append_extract_log(args.extract_type, args.scope,
                                      session_id, len(written))
        # One commit per run, covering the candidate files and the log.md
        # append together. Only when the run actually wrote candidates: a
        # zero-count log.md entry rides along with the next run's commit, the
        # same file-granular absorption internal/tui/candidate.go accepts.
        if written:
            commit_paths = list(written)
            if log_path:
                commit_paths.append(log_path)
            # Sanitized here, not just inside the commit, so the line printed
            # below is the subject git actually recorded.
            message = sanitize_record(run_label(args.extract_type, args.scope,
                                                session_id, len(written)))
            reason = record_knowledge_commit(KNOWLEDGE_ROOT, commit_paths, message)
            if reason:
                print(f"[extract] knowledge store not committed: {reason}", file=sys.stderr)
            else:
                print(f"[extract] committed to knowledge store: {message}", file=sys.stderr)

    if not scoring_refs:
        print("\n[extract] no scoring refs to compare against — skipping scoring")
        if args.json_out:
            Path(args.json_out).write_text(json.dumps({
                "input": str(input_path),
                "scope": args.scope,
                "provider": args.provider,
                "model": args.model,
                "reasoning": args.reasoning if args.provider == "codex" else None,
                "extract_secs": extract_secs,
                "candidates_valid": len(valid),
                "candidates_invalid": len(invalid),
                "references_loaded": 0,
                "verdict": "UNSCORED",
                "candidates": [{"id": c["id"], "title": c["title"], "scope": c.get("scope", ""), "claim": c.get("claim", ""), "verify": c.get("verify", ""), "evidence_count": c.get("evidence_count", 0), "warnings": c.get("warnings", [])} for c in valid],
            }, indent=2))
        return

    judge_start = time.time()
    if args.judge == "llm":
        print(f"[extract] judging with {args.judge_provider}:{args.judge_model}...", file=sys.stderr)
        report = compare_llm(valid, scoring_refs, args.judge_provider, args.judge_model, args.judge_reasoning)
    else:
        report = compare_keyword(valid, scoring_refs)
    judge_secs = time.time() - judge_start
    print()
    print(f"== Coverage vs reference ({args.judge} scoring) ==")
    for r in report["results"]:
        status = "HIT " if r["score"] >= args.threshold else "MISS"
        match = r["matched_id"] or "<no match>"
        print(f"  {status}  score={r['score']:.2f}  {r['ref_id']}")
        print(f"         matched: {match}")

    if report["extras"]:
        print(f"\n  Extra candidates not in reference: {len(report['extras'])}")
        for i in report["extras"]:
            print(f"    + {valid[i]['id']}")

    print()
    print(f"Mean score: {report['mean']:.2f}")
    print(f"Threshold:  {args.threshold}")
    verdict = "PASS" if report["mean"] >= args.threshold else "FAIL"
    print(f"Verdict:    {verdict}")
    print(f"Timing:     extract {extract_secs:.1f}s + judge {judge_secs:.1f}s")

    if args.json_out:
        Path(args.json_out).write_text(json.dumps({
            "input": str(input_path),
            "scope": args.scope,
            "provider": args.provider,
            "model": args.model,
            "reasoning": args.reasoning if args.provider == "codex" else None,
            "extract_secs": extract_secs,
            "judge_secs": judge_secs,
            "candidates_valid": len(valid),
            "candidates_invalid": len(invalid),
            "benchmark": args.benchmark,
            "training_refs": len(training_refs),
            "references_loaded": len(scoring_refs),
            "reference_hits": sum(1 for r in report["results"] if r["score"] >= args.threshold),
            "mean_score": report["mean"],
            "verdict": verdict,
            "references": report["results"],
            "extras": [valid[i]["id"] for i in report["extras"]],
            "candidates": [{"id": c["id"], "title": c["title"], "scope": c.get("scope", ""), "claim": c.get("claim", ""), "verify": c.get("verify", ""), "evidence_count": c.get("evidence_count", 0), "warnings": c.get("warnings", [])} for c in valid],
        }, indent=2))

    sys.exit(0 if verdict == "PASS" else 1)


if __name__ == "__main__":
    main()
