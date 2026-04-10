#!/usr/bin/env python3
"""Generate knowledge/index.md from all truth and decision files.

Scans knowledge/truths/<scope>/ and knowledge/decisions/<scope>/,
reads frontmatter from each file, and produces a navigable index
with one-line summaries and relative markdown links.

Usage:
    ./build-wiki.py              # regenerate knowledge/index.md
    ./build-wiki.py --dry-run    # print to stdout instead of writing
"""

import argparse
import re
import sys
from collections import defaultdict
from pathlib import Path

LOOM_ROOT = Path(__file__).resolve().parent.parent
KNOWLEDGE_DIR = LOOM_ROOT / "knowledge"

# Artifact type directories (training only — eval is not part of the wiki)
ARTIFACT_TYPES = {
    "truths": {"label": "Truths", "description": "Reusable, evidence-backed facts about how systems behave."},
    "decisions": {"label": "Decisions", "description": "Deliberate choices with alternatives and rationale."},
}


def parse_frontmatter(path: Path) -> dict:
    """Extract frontmatter fields from a markdown file."""
    text = path.read_text()
    fm_match = re.match(r"^---\n(.*?)\n---", text, re.DOTALL)
    if not fm_match:
        return {}
    fm = {}
    for line in fm_match.group(1).splitlines():
        if ":" in line and not line.startswith(" ") and not line.startswith("-") and not line.startswith("\t"):
            key, _, value = line.partition(":")
            fm[key.strip()] = value.strip()
    return fm


def collect_artifacts() -> dict:
    """Collect all artifacts organized by type → scope → list of (path, frontmatter)."""
    result = {}
    for type_dir, type_info in ARTIFACT_TYPES.items():
        base = KNOWLEDGE_DIR / type_dir
        if not base.exists():
            continue
        scopes = defaultdict(list)
        for scope_dir in sorted(base.iterdir()):
            if not scope_dir.is_dir():
                continue
            scope = scope_dir.name
            if scope == "universal":
                continue  # listed separately
            for md_file in sorted(scope_dir.glob("*.md")):
                if md_file.name.startswith("_"):
                    continue
                fm = parse_frontmatter(md_file)
                if not fm.get("id"):
                    continue
                scopes[scope].append({
                    "path": md_file,
                    "rel_path": md_file.relative_to(KNOWLEDGE_DIR),
                    "id": fm.get("id", ""),
                    "title": fm.get("title", md_file.stem),
                    "scope": scope,
                    "tag": fm.get("tag", ""),
                    "status": fm.get("status", ""),
                })
        # Check for universal
        universal_dir = base / "universal"
        if universal_dir.exists():
            for md_file in sorted(universal_dir.glob("*.md")):
                if md_file.name.startswith("_"):
                    continue
                fm = parse_frontmatter(md_file)
                if fm.get("id"):
                    scopes["universal"].append({
                        "path": md_file,
                        "rel_path": md_file.relative_to(KNOWLEDGE_DIR),
                        "id": fm.get("id", ""),
                        "title": fm.get("title", md_file.stem),
                        "scope": "universal",
                        "tag": fm.get("tag", ""),
                        "status": fm.get("status", ""),
                    })
        result[type_dir] = {"info": type_info, "scopes": dict(scopes)}
    return result


def render_index(artifacts: dict) -> str:
    """Render the index markdown."""
    lines = [
        "# Knowledge Base",
        "",
        "Auto-generated index of all truths and decisions. Do not edit manually — regenerate with `extractors/build-wiki.py`.",
        "",
    ]

    # Summary counts
    total = 0
    count_parts = []
    for type_dir, data in artifacts.items():
        n = sum(len(items) for items in data["scopes"].values())
        total += n
        count_parts.append(f"{n} {data['info']['label'].lower()}")
    lines.append(f"**{total} artifacts:** {', '.join(count_parts)}.")
    lines.append("")

    # Table of contents
    lines.append("## Contents")
    lines.append("")
    for type_dir, data in artifacts.items():
        label = data["info"]["label"]
        anchor = label.lower().replace(" ", "-")
        lines.append(f"- [{label}](#{anchor})")
        for scope in sorted(data["scopes"].keys()):
            scope_anchor = f"{anchor}--{scope}"
            n = len(data["scopes"][scope])
            lines.append(f"  - [{scope}](#{scope_anchor}) ({n})")
    lines.append("")

    # Sections per type
    for type_dir, data in artifacts.items():
        label = data["info"]["label"]
        desc = data["info"]["description"]
        lines.append(f"## {label}")
        lines.append("")
        lines.append(f"_{desc}_")
        lines.append("")

        for scope in sorted(data["scopes"].keys()):
            items = data["scopes"][scope]
            scope_label = scope.capitalize() if scope != "universal" else "Universal"
            lines.append(f"### {label} — {scope_label}")
            lines.append("")

            for item in items:
                rel = item["rel_path"]
                title = item["title"]
                tag = f" `{item['tag']}`" if item.get("tag") else ""
                lines.append(f"- [{title}]({rel}){tag}")

            lines.append("")

    # Footer
    lines.append("---")
    lines.append("")
    lines.append("*Generated by `extractors/build-wiki.py`*")
    lines.append("")
    return "\n".join(lines)


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--dry-run", action="store_true", help="print to stdout instead of writing")
    args = p.parse_args()

    artifacts = collect_artifacts()
    index_md = render_index(artifacts)

    if args.dry_run:
        print(index_md)
    else:
        out = KNOWLEDGE_DIR / "index.md"
        out.write_text(index_md)
        print(f"wrote {out} ({len(index_md)} chars)")


if __name__ == "__main__":
    main()
