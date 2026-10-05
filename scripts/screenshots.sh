#!/bin/sh
# Regenerates the README screenshots in docs/screenshots.
#
# TestScreenshots in cmd/reclaim runs scan, here and the select checklist
# against a fixture machine it builds in a temp directory, never against this
# machine, and writes their colored output to cmd/reclaim/testdata/screenshots.
# freeze then renders each file as an SVG. freeze runs through go run, so
# nothing is installed and go.mod does not change.
#
# Usage: scripts/screenshots.sh
set -eu

cd "$(dirname "$0")/.."

# Character widths must not depend on the locale of the machine.
RUNEWIDTH_EASTASIAN=0 RECLAIM_UPDATE_SCREENSHOTS=1 go test -count=1 -run '^TestScreenshots$' ./cmd/reclaim/

mkdir -p docs/screenshots
for name in scan here select; do
	go run github.com/charmbracelet/freeze@latest \
		"cmd/reclaim/testdata/screenshots/$name.ansi" \
		--output "docs/screenshots/$name.svg" \
		--window \
		--border.radius 8 \
		--padding 20,24,20,24 \
		--font.size 14 \
		--line-height 1.3
done

# The fixture lives in a temp directory and its paths are shown relative to
# its home, so no real path may appear in a screenshot.
if grep -l -e '/Users/' -e '/home/' -e '/var/folders/' docs/screenshots/*.svg; then
	echo "screenshots.sh: a screenshot shows a real path" >&2
	exit 1
fi
echo "Rendered docs/screenshots/scan.svg, here.svg and select.svg."
