#!/usr/bin/env python3
"""Aggregate matrix run results into a summary table.

Reads all JSON files under extractors/results/ and prints:
- A table of (session × config) -> (hit_ratio, mean_score, verdict)
- A per-session view showing which references were hit by which configs
- Timing and cost-proxy summary
"""

import json
import sys
from collections import defaultdict
from pathlib import Path

LOOM_ROOT = Path(__file__).resolve().parent.parent
RESULTS_DIR = LOOM_ROOT / "extractors" / "results"


def load_results():
    results = []
    if not RESULTS_DIR.exists():
        return results
    for path in sorted(RESULTS_DIR.glob("*.json")):
        try:
            data = json.loads(path.read_text())
        except json.JSONDecodeError:
            print(f"WARN: could not parse {path}", file=sys.stderr)
            continue
        stem = path.stem
        if "__" not in stem:
            continue
        session_label, cfg_label = stem.split("__", 1)
        data["_session_label"] = session_label
        data["_cfg_label"] = cfg_label
        results.append(data)
    return results


def summary_table(results):
    sessions = sorted(set(r["_session_label"] for r in results))
    configs = sorted(set(r["_cfg_label"] for r in results), key=config_sort_key)
    by_key = {(r["_session_label"], r["_cfg_label"]): r for r in results}

    session_width = max((len(s) for s in sessions), default=10) + 2

    print("=" * (session_width + len(configs) * 15 + 4))
    print("MATRIX SUMMARY — hits/refs (mean score)")
    print("=" * (session_width + len(configs) * 15 + 4))
    header = f"{'session':<{session_width}}"
    for c in configs:
        header += f" {c:<14}"
    print(header)
    print("-" * (session_width + len(configs) * 15 + 4))
    for s in sessions:
        row = f"{s:<{session_width}}"
        for c in configs:
            r = by_key.get((s, c))
            if r is None:
                row += f" {'--':<14}"
            elif r.get("verdict") == "UNSCORED":
                n = r.get("candidates_valid", 0)
                row += f" {f'{n} cands':<14}"
            else:
                hits = r.get("reference_hits", 0)
                refs = r.get("references_loaded", 0)
                mean = r.get("mean_score", 0.0)
                verdict = r.get("verdict", "?")
                mark = "✓" if verdict == "PASS" else "✗"
                row += f" {mark}{hits}/{refs} ({mean:.2f})  "
        print(row)
    print()


def config_sort_key(label):
    order = {"haiku": 0, "sonnet": 1, "opus": 2, "gpt5-low": 3, "gpt54-low": 4, "gpt54-med": 5, "gpt54-high": 6}
    return order.get(label, 99)


def per_reference_view(results):
    # For each session, show which refs were hit by which configs.
    sessions = sorted(set(r["_session_label"] for r in results))
    configs = sorted(set(r["_cfg_label"] for r in results), key=config_sort_key)

    for session in sessions:
        session_runs = [r for r in results if r["_session_label"] == session]
        if not session_runs:
            continue
        first = session_runs[0]
        refs = first.get("references", [])
        if not refs:
            continue
        ref_ids = [r["ref_id"] for r in refs]

        print("=" * 80)
        print(f"Per-reference coverage — {session}")
        print("=" * 80)
        ref_col = max(len(r) for r in ref_ids) + 2
        header = f"{'reference':<{ref_col}}"
        for c in configs:
            header += f" {c:<12}"
        print(header)
        print("-" * (ref_col + len(configs) * 13))

        for ref_id in ref_ids:
            row = f"{ref_id:<{ref_col}}"
            for cfg in configs:
                run = next((r for r in session_runs if r["_cfg_label"] == cfg), None)
                if not run:
                    row += f" {'--':<12}"
                    continue
                entry = next((r for r in run.get("references", []) if r["ref_id"] == ref_id), None)
                if not entry:
                    row += f" {'?':<12}"
                else:
                    score = entry["score"]
                    if score >= 0.7:
                        row += f" {'HIT ' + f'{score:.1f}':<12}"
                    elif score >= 0.3:
                        row += f" {'~~ ' + f'{score:.1f}':<12}"
                    else:
                        row += f" {'--':<12}"
            print(row)
        print()


def extras_view(results):
    """Show which 'extras' each config emitted per session — candidate discoveries."""
    sessions = sorted(set(r["_session_label"] for r in results))

    print("=" * 80)
    print("EXTRAS — candidates emitted but not matching any reference")
    print("=" * 80)
    print("These are the extractor's discovery signal: truths in the input that")
    print("aren't in the current reference set. Some are legitimate, some noise.")
    print()

    for session in sessions:
        session_runs = [r for r in results if r["_session_label"] == session]
        print(f"── {session} ──")
        extras_freq = defaultdict(list)
        for run in session_runs:
            for extra in run.get("extras", []):
                extras_freq[extra].append(run["_cfg_label"])
        if not extras_freq:
            print("  (no extras)")
        else:
            # sort by frequency desc
            for extra, configs in sorted(extras_freq.items(), key=lambda x: -len(x[1])):
                n = len(configs)
                cfg_list = ", ".join(sorted(configs, key=config_sort_key))
                print(f"  [{n}x]  {extra}")
                print(f"         by: {cfg_list}")
        print()


def timing_view(results):
    print("=" * 80)
    print("TIMING & COST PROXY")
    print("=" * 80)
    by_cfg = defaultdict(list)
    for r in results:
        by_cfg[r["_cfg_label"]].append(r.get("extract_secs", 0))
    print(f"{'config':<14} {'mean_extract_s':>16} {'max_s':>10} {'n_runs':>8}")
    print("-" * 50)
    for cfg in sorted(by_cfg.keys(), key=config_sort_key):
        secs = by_cfg[cfg]
        if not secs:
            continue
        mean = sum(secs) / len(secs)
        mx = max(secs)
        print(f"{cfg:<14} {mean:>16.1f} {mx:>10.1f} {len(secs):>8}")
    print()


def main():
    results = load_results()
    if not results:
        sys.exit(f"no results found in {RESULTS_DIR}")

    print(f"Loaded {len(results)} runs from {RESULTS_DIR}\n")
    summary_table(results)
    per_reference_view(results)
    extras_view(results)
    timing_view(results)


if __name__ == "__main__":
    main()
