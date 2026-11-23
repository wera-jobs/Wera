#!/usr/bin/env bash
# Runs the Go tests with the race detector and coverage, as CI does.
#
# Packages whose tests use the test database (they call into
# internal/testutil) share it, so they run one at a time. Every other
# package runs in parallel, alongside them. The two coverage profiles are
# merged into coverage.out.
#
# Usage: scripts/test.sh [extra go test flags]
set -euo pipefail
cd "$(dirname "$0")/.."

db=()
other=()
while IFS=' ' read -r pkg dir; do
	if grep -qsE 'testutil\.|WERA_TEST_DATABASE_URL' "$dir"/*_test.go; then
		db+=("$pkg")
	else
		other+=("$pkg")
	fi
done < <(go list -f '{{.ImportPath}} {{.Dir}}' ./...)

echo "database packages, one at a time: ${db[*]}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
flags=(-race -coverpkg=./internal/... "$@")

go test -p 1 "${flags[@]}" -coverprofile="$tmp/db.out" "${db[@]}" >"$tmp/db.log" 2>&1 &
dbpid=$!
status=0
go test "${flags[@]}" -coverprofile="$tmp/other.out" "${other[@]}" || status=1
wait "$dbpid" || status=1
cat "$tmp/db.log"

if [[ -s $tmp/db.out && -s $tmp/other.out ]]; then
	# Same mode line in both; cover merges repeated blocks.
	{ cat "$tmp/db.out"; tail -n +2 "$tmp/other.out"; } >coverage.out
fi
exit "$status"
