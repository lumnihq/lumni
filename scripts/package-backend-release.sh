#!/bin/sh
# Backend-only release packaging; does not build, read, or copy a frontend.
set -eu
RELEASE_GO=${RELEASE_GO:-go}
RELEASE_PYTHON=${RELEASE_PYTHON:-python3}
RELEASE_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$RELEASE_ROOT"
RELEASE_OUT_DIR=${RELEASE_OUT_DIR:-"$RELEASE_ROOT/dist/backend-release"}
case "$RELEASE_OUT_DIR" in /*) ;; *) RELEASE_OUT_DIR="$RELEASE_ROOT/$RELEASE_OUT_DIR";; esac
RELEASE_TARGETS=${RELEASE_TARGETS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"}
RELEASE_VERSION=$("$RELEASE_PYTHON" - <<'PY'
import json,re
from pathlib import Path
version=json.loads(Path('sdk/typescript/package.json').read_text())['version']
python_version=re.search(r'^version\s*=\s*"([^"]+)"',Path('sdk/python/pyproject.toml').read_text(),re.M).group(1)
if version!=python_version or not re.fullmatch(r'\d+\.\d+\.\d+(?:[-.][0-9A-Za-z.-]+)?',version):
    raise SystemExit('Python and TypeScript SDK versions must agree and be valid release versions')
print(version)
PY
)
mkdir -p "$RELEASE_OUT_DIR"
for target in $RELEASE_TARGETS; do
  case "$target" in linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;; *) echo "Unsupported release target: $target" >&2; exit 1;; esac
  system=${target%/*}; architecture=${target#*/}
  name="lumni-${RELEASE_VERSION}-${system}-${architecture}"
  destination="$RELEASE_OUT_DIR/$name"
  mkdir -p "$destination"
  echo "Building backend binaries for $target"
  for command in lumni gateway lumnid worker; do
    CGO_ENABLED=0 GOOS="$system" GOARCH="$architecture" "$RELEASE_GO" build -trimpath -ldflags='-s -w' -o "$destination/$command" "./cmd/$command"
  done
  cp LICENSE "$destination/"
  cp docs/onboarding.md "$destination/README.md"
  tar -czf "$RELEASE_OUT_DIR/$name.tar.gz" -C "$RELEASE_OUT_DIR" "$name"
done
(cd sdk/typescript && npm run build && npm pack --pack-destination "$RELEASE_OUT_DIR")
"$RELEASE_PYTHON" -m build --wheel --outdir "$RELEASE_OUT_DIR" sdk/python
"$RELEASE_PYTHON" - "$RELEASE_OUT_DIR" <<'PY'
import hashlib,sys
from pathlib import Path
root=Path(sys.argv[1])
artifacts=sorted(p for p in root.iterdir() if p.name.endswith(('.tar.gz','.tgz','.whl')))
with (root/'SHA256SUMS').open('w') as out:
    for artifact in artifacts:
        out.write(f'{hashlib.sha256(artifact.read_bytes()).hexdigest()}  {artifact.name}\n')
PY
echo "Prepared backend release artifacts in $RELEASE_OUT_DIR"
