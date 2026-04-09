#!/usr/bin/env bash
# Run the truth extractor across a matrix of (session, model-config) pairs.
# Collects a JSON result per run under extractors/results/.

set -u

LOOM_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RESULTS_DIR="$LOOM_ROOT/extractors/results"
mkdir -p "$RESULTS_DIR"

# Test sessions: label|path|scope
SESSIONS=(
  "apr08-forge-bugs|/Users/smacbeth/code/forge-data/sessions/forge/2026-04-08-88f1615b-69cc-4330-9c8b-3affd4494229.md|forge"
  "mar14-forge-feature|/Users/smacbeth/code/forge-data/sessions/forge/2026-03-14-b9b4c0be-4180-40ec-b1e3-b0dc77b0669b.md|forge"
  "mar25-ticket-multistore|/Users/smacbeth/code/forge-data/sessions/ticket/2026-03-25-91d979db-8c94-4f38-999b-90b028c5b543.md|forge"
)

# Model configs: label|provider|model|reasoning
CONFIGS=(
  "haiku|claude|haiku|-"
  "sonnet|claude|sonnet|-"
  "opus|claude|opus|-"
  "gpt5-low|codex|gpt-5|low"
  "gpt54-low|codex|gpt-5.4|low"
  "gpt54-med|codex|gpt-5.4|medium"
  "gpt54-high|codex|gpt-5.4|high"
)

# Judge is fixed: claude haiku (LLM scoring) for consistency and cost.
JUDGE_ARGS=(--judge llm --judge-provider claude --judge-model haiku)

# Raw output is always saved alongside JSON for future re-parsing.

run_one() {
  local session_label="$1" session_path="$2" session_scope="$3"
  local cfg_label="$4" provider="$5" model="$6" reasoning="$7"

  local out_json="$RESULTS_DIR/${session_label}__${cfg_label}.json"
  local log_file="$RESULTS_DIR/${session_label}__${cfg_label}.log"

  if [[ -f "$out_json" ]]; then
    echo "  [skip] $session_label / $cfg_label (already have $out_json)"
    return 0
  fi

  echo "  [run ] $session_label / $cfg_label"

  local cmd=(python3 "$LOOM_ROOT/extractors/extract.py"
    --input "$session_path"
    --scope "$session_scope"
    --provider "$provider"
    --model "$model"
    "${JUDGE_ARGS[@]}"
    --json-out "$out_json")

  if [[ "$reasoning" != "-" ]]; then
    cmd+=(--reasoning "$reasoning")
  fi

  local start
  start=$(date +%s)
  if "${cmd[@]}" > "$log_file" 2>&1; then
    local elapsed=$(( $(date +%s) - start ))
    echo "         PASS in ${elapsed}s"
  else
    local elapsed=$(( $(date +%s) - start ))
    if [[ -f "$out_json" ]]; then
      echo "         FAIL (verdict) in ${elapsed}s"
    else
      echo "         ERROR in ${elapsed}s — see $log_file"
    fi
  fi
}

echo "=== Matrix: ${#SESSIONS[@]} sessions × ${#CONFIGS[@]} configs = $(( ${#SESSIONS[@]} * ${#CONFIGS[@]} )) runs ==="
echo

total_start=$(date +%s)
for session_entry in "${SESSIONS[@]}"; do
  IFS='|' read -r session_label session_path session_scope <<< "$session_entry"
  echo "── $session_label ──"
  for cfg_entry in "${CONFIGS[@]}"; do
    IFS='|' read -r cfg_label provider model reasoning <<< "$cfg_entry"
    run_one "$session_label" "$session_path" "$session_scope" "$cfg_label" "$provider" "$model" "$reasoning"
  done
  echo
done

total_elapsed=$(( $(date +%s) - total_start ))
echo "Matrix complete in ${total_elapsed}s"
echo "Results in $RESULTS_DIR"
