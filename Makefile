.PHONY: dev

# dev builds the loom binary locally for running unreleased changes.
# Separate from the installed/daemon binary, which the updater keeps on
# the latest GitHub Release.
dev:
	go build -o ./loom ./cmd/loom
