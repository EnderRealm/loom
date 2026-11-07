.PHONY: dev test-extractors test-extractors-yaml

# dev builds the loom binary locally for running unreleased changes.
# Separate from the installed/daemon binary, which the updater keeps on
# the latest GitHub Release.
dev:
	go build -o ./loom ./cmd/loom

# test-extractors runs the stdlib unittest suite offline. make is in
# verify_allow, so `verify: make test-extractors` runs without listing
# python3 there.
test-extractors:
	python3 -m unittest discover -s extractors -p "test_*.py"

# test-extractors-yaml runs the strict-YAML test, which the offline suite
# skips without PyYAML. `uv run --with` fetches PyYAML from the network,
# so a ticket opts into that fetch by naming this target; uv stays out of
# verify_allow.
test-extractors-yaml:
	cd extractors && uv run --with pyyaml python3 -m unittest test_extract_frontmatter.EmitCandidatesFrontmatterTest.test_real_extractor_output_parses_under_a_strict_yaml_loader
