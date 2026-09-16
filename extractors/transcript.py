"""Load supported journals through their existing parsers, before redaction."""
import json
import re
import subprocess
import sys
from pathlib import Path

from knowledge_store import _loom_bin


def load_transcript(path: Path) -> dict:
    records = []
    with path.open(encoding="utf-8", errors="replace") as stream:
        for line in stream:
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(record, dict):
                records.append(record)
    if not any(r.get("format") == "cursor-store-v1" for r in records):
        return {"session_id": path.stem, "source_runtime": "claude-code", "records": records}

    # The Go parser owns journal replay and reference traversal. Never truncate
    # its result here: preprocessing redacts full values before its own cuts.
    proc = subprocess.run([_loom_bin(), "extract", "cursor-input", str(path.resolve())],
                          capture_output=True, encoding="utf-8", timeout=120)
    if proc.returncode:
        raise ValueError(f"Cursor preprocessing failed: {proc.stderr.strip()}")
    result = json.loads(proc.stdout)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", result["session_id"]):
        raise ValueError("Cursor preprocessing returned an unsafe session id")
    result["source_runtime"] = "cursor-cli"
    for diagnostic in result.get("diagnostics") or []:
        print(f"[preprocess] Cursor diagnostic: {json.dumps(diagnostic, ensure_ascii=True)}", file=sys.stderr)
    return result
