#!/usr/bin/env bash
# Opt-in load check: Go and Python 3; fresh databases, no default go-test load.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/spanbox-load.XXXXXX")
trap 'rm -rf "$work"' EXIT
go build -o "$work/spanbox" ./cmd/spanbox
# Keep the measurements even when an assertion fails.
set +e
TMPDIR="$work" go run ./scripts/load -work "$work" -binary "$work/spanbox" "$@" 2>&1 | tee "$work/measurements.md"
status=${PIPESTATUS[0]}
set -e
# Preserve the pre-implementation failure analysis and change justification.
python3 - "$work/measurements.md" <<'PY'
from pathlib import Path
import sys
result = Path('docs/superpowers/specs/2026-09-30-search-p95-during-purge-result.md')
header = result.read_text().split('## Measurements\n', 1)[0]
result.write_text(header + '## Measurements\n\n```text\n' + Path(sys.argv[1]).read_text() + '\n```\n')
PY
exit "$status"
