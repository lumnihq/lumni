#!/bin/sh
# Gather what the package ships: the console's built pages (for `demo`), the
# onboarding command (for `init`) and the sample agents (for `try`). Run from
# anywhere; needs Bun for the console build.
set -eu
here=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
repo=$(CDPATH= cd -- "$here/../.." && pwd)
(cd "$repo/console" && bun scripts/build.ts >/dev/null)
rm -rf "$here/assets"
mkdir -p "$here/assets/console"
cp -R "$repo/console/dist/." "$here/assets/console/"
cp "$repo/console/scripts/lumni-init.mjs" "$here/assets/lumni-init.mjs"
# lumniverse try: the sample agents (they use @lumniverse/sdk, a dependency).
mkdir -p "$here/assets/fleet/scenarios"
cp "$repo/examples/agent-fleet/run.mjs" "$repo/examples/agent-fleet/apps.mjs" "$repo/examples/agent-fleet/lumni.mjs" "$here/assets/fleet/"
cp "$repo/examples/agent-fleet/scenarios/"*.mjs "$here/assets/fleet/scenarios/"
echo "Assembled packages/lumni/assets"
