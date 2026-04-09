#!/usr/bin/env bash
set -u

LOOM_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RESULTS_DIR="$LOOM_ROOT/extractors/results-raw"
EXTRACT="$LOOM_ROOT/extractors/extract.py"
JSONL="/Users/smacbeth/.claude/projects/-Users-smacbeth-code-forge/88f1615b-69cc-4330-9c8b-3affd4494229.jsonl"
mkdir -p "$RESULTS_DIR"

CONFIGS=(
  "haiku|claude|haiku|-"
  "sonnet|claude|sonnet|-"
  "opus|claude|opus|-"
  "gpt5-low|codex|gpt-5|low"
  "gpt54-low|codex|gpt-5.4|low"
  "gpt54-med|codex|gpt-5.4|medium"
  "gpt54-high|codex|gpt-5.4|high"
)

JUDGE_ARGS=(--judge llm --judge-provider claude --judge-model haiku)

echo "=== Raw extraction matrix: 7 configs × 1 session (apr08 jsonl) ==="
echo

total_start=$(date +%s)
for cfg_entry in "${CONFIGS[@]}"; do
  IFS='|' read -r cfg_label provider model reasoning <<< "$cfg_entry"
  out_json="$RESULTS_DIR/apr08-raw__${cfg_label}.json"
  log_file="$RESULTS_DIR/apr08-raw__${cfg_label}.log"

  if [[ -f "$out_json" ]]; then
    echo "  [skip] $cfg_label (already have $out_json)"
    continue
  fi

  echo -n "  [run ] $cfg_label ... "

  cmd=(python3 "$EXTRACT"
    --input "$JSONL"
    --input-format raw
    --scope forge
    --provider "$provider"
    --model "$model"
    "${JUDGE_ARGS[@]}"
    --json-out "$out_json")

  if [[ "$reasoning" != "-" ]]; then
    cmd+=(--reasoning "$reasoning")
  fi

  start=$(date +%s)
  if "${cmd[@]}" > "$log_file" 2>&1; then
    elapsed=$(( $(date +%s) - start ))
    echo "PASS in ${elapsed}s"
  else
    elapsed=$(( $(date +%s) - start ))
    if [[ -f "$out_json" ]]; then
      echo "FAIL (verdict) in ${elapsed}s"
    else
      echo "ERROR in ${elapsed}s — see $log_file"
    fi
  fi
done

total_elapsed=$(( $(date +%s) - total_start ))
echo
echo "Done in ${total_elapsed}s. Results in $RESULTS_DIR"
