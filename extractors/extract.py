#!/usr/bin/env python3
"""Truth extractor — runs the truth-extractor prompt against a session artifact
via the claude CLI, parses the output, and compares to hand-written reference
truths in knowledge/truths/<scope>/.

Usage:
    ./extract.py --input <session.md> [--scope forge] [--model haiku] [--threshold 0.5]
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from datetime import date
from pathlib import Path

LOOM_ROOT = Path(__file__).resolve().parent.parent
PROMPT_PATH = LOOM_ROOT / "extractors" / "truth-extractor.md"
KNOWLEDGE_ROOT = LOOM_ROOT / "knowledge" / "truths"
EVAL_ROOT = LOOM_ROOT / "knowledge" / "truths-eval"
# Import the pre-processor (sibling module)
sys.path.insert(0, str(LOOM_ROOT / "extractors"))
from preprocess import preprocess as preprocess_jsonl
CLAUDE_BIN = "/opt/homebrew/bin/claude"
CODEX_BIN = "/opt/homebrew/bin/codex"
SENTINEL = "===END-OF-TRUTH==="
EXAMPLE_DELIMITER = "\n\n===REFERENCE-EXAMPLE===\n\n"

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


def load_reference_truths(scope: str) -> list[dict]:
    return load_reference_truths_from(KNOWLEDGE_ROOT / scope)


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
        "valid": has_verify and has_claim and bool(frontmatter.get("id")),
        "id": frontmatter.get("id", ""),
        "title": frontmatter.get("title", ""),
        "scope": frontmatter.get("scope", ""),
        "type": frontmatter.get("type", ""),
        "status": frontmatter.get("status", ""),
        "claim": sections.get("claim", ""),
        "verify": sections.get("how_to_verify", ""),
        "why": sections.get("why_it_matters", ""),
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


def build_prompt(template: str, refs: list[dict], input_text: str, today: str, input_format: str = "summary") -> str:
    ref_block = EXAMPLE_DELIMITER.join(r["raw"] for r in refs)
    guidance = INPUT_GUIDANCE_RAW if input_format == "raw" else INPUT_GUIDANCE_SUMMARY
    return (
        template
        .replace("{INPUT_GUIDANCE}", guidance)
        .replace("{REFERENCE_EXAMPLES}", ref_block)
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


def parse_output(text: str) -> list[dict]:
    text = text.strip()
    if text == "NO_TRUTHS":
        return []
    # Tolerate model preambles, trailing commentary, or markdown code fences
    # around the output. Extract each `---\n...\n---\n<body>` block individually
    # via regex, regardless of the sentinel.
    truth_pattern = re.compile(
        r"(?:^|\n)---\n(.*?)\n---\n(.*?)(?=\n---\n|\n===END-OF-TRUTH===|\Z)",
        re.DOTALL,
    )
    results = []
    for fm_match in truth_pattern.finditer(text):
        fm_text = fm_match.group(1)
        body = fm_match.group(2)
        # Strip trailing sentinel if present
        body = re.sub(r"\n?===END-OF-TRUTH===\s*$", "", body)
        chunk = f"---\n{fm_text}\n---\n{body}"
        results.append(parse_truth(chunk))
    return results


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
    p.add_argument("--input", required=True, help="path to session artifact (markdown or jsonl)")
    p.add_argument("--input-format", default="auto", choices=["auto", "summary", "raw"],
                    help="input format: summary (markdown), raw (jsonl), or auto (detect from extension)")
    p.add_argument("--scope", default="forge", help="scope directory under knowledge/truths")
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
    p.add_argument("--benchmark", action="store_true",
                    help="score against eval set (truths-eval/) instead of training set (truths/). "
                         "Training refs are still shown as few-shot examples — eval refs are never shown.")
    args = p.parse_args()

    # Apply preset overrides (only if user didn't also set the individual flags)
    if args.preset:
        preset = PRESETS[args.preset]
        for k, v in preset.items():
            setattr(args, k, v)

    input_path = Path(args.input).expanduser()
    if not input_path.exists():
        sys.exit(f"input not found: {input_path}")

    if not PROMPT_PATH.exists():
        sys.exit(f"prompt not found: {PROMPT_PATH}")

    template = PROMPT_PATH.read_text()

    # Training refs: always loaded, injected as few-shot examples in the prompt.
    training_refs = load_reference_truths(args.scope)
    if not training_refs:
        print(f"warning: no training truths in {KNOWLEDGE_ROOT/args.scope}", file=sys.stderr)

    # Scoring refs: what we score candidates against.
    # --benchmark: score against eval set (never shown to model), filtered to
    # truths sourced from the same session as the input artifact.
    # default: score against training set (same as examples — legacy mode).
    if args.benchmark:
        eval_dir = EVAL_ROOT / args.scope
        all_eval_refs = load_reference_truths_from(eval_dir)
        if not all_eval_refs:
            sys.exit(f"no eval truths in {eval_dir} — cannot benchmark without eval set")

        # Extract session ID from input to filter eval refs.
        # Summary: frontmatter session_id field. Raw: filename is the session ID.
        input_session_id = _extract_session_id(input_path)
        if input_session_id:
            scoring_refs = [r for r in all_eval_refs
                           if any(input_session_id.startswith(sid) or sid.startswith(input_session_id)
                                  for sid in r.get("source_sessions", []))]
            if not scoring_refs:
                print(f"warning: no eval truths match session {input_session_id} — scoring against all {len(all_eval_refs)} eval refs", file=sys.stderr)
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

    # Pre-process raw jsonl into a conversation thread
    if input_format == "raw":
        print(f"[extract] pre-processing raw jsonl...", file=sys.stderr)
        input_text = preprocess_jsonl(str(input_path))
        print(f"[extract] preprocessed: {len(input_text):,} chars", file=sys.stderr)
    else:
        input_text = input_path.read_text()

    today = date.today().isoformat()
    prompt = build_prompt(template, refs, input_text, today, input_format)

    if args.dry_run:
        print(prompt)
        return

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

    candidates = parse_output(output)
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
