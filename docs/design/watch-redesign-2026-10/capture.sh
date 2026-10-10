#!/bin/sh
# Re-capture the five watch-redesign screenshots in colour (tick cl7).
#
# The first capture (epic ymf) fed the pipeline plain text and came out
# monochrome; these screenshots are captured from the styled output instead:
# the test below writes each scenario's frame with the terminal's own style
# set — the same renderer and fixtures the golden frames are pinned against —
# and freeze turns each into a PNG. No window chrome: the window controls
# would add their own red, yellow and green, and the screenshots must show
# the dashboard's accents, not the capture's (the committed-PNG guard,
# TestCommittedScreenshotsShowTheColourAccents, reads the whole image).
#
# Requires: go, and freeze (https://github.com/charmbracelet/freeze) on PATH.
# Usage: capture.sh [output-dir]    (default: this directory)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
out=${1:-"$here"}

command -v freeze >/dev/null 2>&1 || {
	echo "capture.sh: freeze is not on PATH (go install github.com/charmbracelet/freeze@latest)" >&2
	exit 2
}

repo=$(cd "$here/../../.." && pwd)
src=$(mktemp -d)
trap 'rm -rf "$src"' EXIT

if [ "$out" != "$here" ]; then
	mkdir -p "$out"
fi

(cd "$repo" && go test ./internal/cli -run TestDashboardScreenshotSources -count=1 -screenshot-dir="$src") >/dev/null

for scenario in fresh busy held landed failed; do
	freeze --language ansi \
		--font.size 16.8 --line-height 1.19 \
		--padding 10 --width 1230 --height 851 \
		--background "#1e1e2e" \
		"$src/watch-$scenario.ansi" -o "$out/watch-$scenario-120x40.png"
done

# The capture verifies itself when it captures in place: the guard reads the
# committed PNGs back, which is only the freshly written set when the output
# directory is the one the repository commits.
if [ "$out" = "$here" ]; then
	(cd "$repo" && go test ./internal/cli -run TestCommittedScreenshotsShowTheColourAccents -count=1)
fi

echo "captured the five screenshots in colour into $out"
