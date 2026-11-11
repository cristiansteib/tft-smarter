#!/bin/sh
# Release assets beside the binaries, run by GoReleaser with the tag being released:
#   build/firmware.hex  the firmware, built for the Mega 2560 (tft-monitor flashes it)
#   build/compat.json   which firmware protocols this release's tft-monitor drives, and firmwareSince: the oldest
#                       release whose firmware comes from the same source, so a board on it is not flashed again
set -eu
tag="$1"
mkdir -p build
${PIO:-pio} run -s
cp .pio/build/megaatmega2560/firmware.hex build/firmware.hex
since="$tag"
for t in $(git tag --merged HEAD --sort=-v:refname -l 'v*'); do
  [ "$t" = "$tag" ] && continue
  git diff --quiet "$t" HEAD -- src platformio.ini || break
  since="$t"
done
go run ./cmd/tft-monitor -firmware-since "$since" compat > build/compat.json
cat build/compat.json
