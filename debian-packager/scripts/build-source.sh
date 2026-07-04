# build-source.sh — build a Debian source package (.dsc/.changes) for upload.
# Source-prep args: <kind> <repo|path> <ref> <depth>. No tool args.
# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
acquire_source "$1" "$2" "$3" "$4"
shift 4
fetch_orig

# Same console discipline as build-binary.sh: capture the build output to a
# scratch file and ship only a bounded tail. A source build is small next to
# an sbuild log, but a messy tree can still spray hundreds of dpkg-source
# lines — and the capture doubles as the log the warning analysis reads,
# since there is no sbuild .build file here.
console=$(mktemp "${TMPDIR:-/tmp}/source-build-console.XXXXXX")
register_cleanup "$console"
rc=0
dpkg-buildpackage -S -us -uc -d >"$console" 2>&1 || rc=$?

echo "=== dpkg-buildpackage -S console tail (exit $rc) ==="
tail -n 250 "$console"

# a failed build exits here: on_exit keeps the workspace and console capture
# for debugging, and the tail above carries the diagnostics.
[ "$rc" -eq 0 ] || exit "$rc"

# deterministic artifact listing from the .changes Files section — dpkg-
# buildpackage -S writes <pkg>_<ver>_source.changes to the parent directory.
# shellcheck disable=SC2012  # ls -t for newest-by-mtime; artifact names are tame
changes=$(ls -1t ../*_source.changes 2>/dev/null | head -n 1)
if [ -n "$changes" ]; then
	echo "=== artifacts ($changes) ==="
	awk '/^Files:/{f=1;next} f&&/^ /{print $NF;next} f{f=0}' "$changes"
fi

# analyze the captured console for warnings with the shared analyzer (for a
# -S build that is mostly the O bucket: dpkg-source/genchanges/buildpackage).
# The whole block runs with set +e — an analyzer hiccup must never fail a
# build that just succeeded, so the step only ever reports ok/warn and the
# block always ends cleanly.
echo "ISPKG_STEP_BEGIN: build_warnings"
set +e
echo "=== build log: dpkg-buildpackage -S console ==="
build_warnings_report "$console"
totals=$(build_warnings_totals "$console")
if [ "$totals" = "$ISPKG_WARNINGS_NONE" ]; then
	status ok
	summary "no build warnings in the source build"
else
	status warn
	summary "build warnings: $totals in the source build"
	hint "agent: dpkg-source warnings usually mean the debian diff carries changes it should not (upstream file edits, modes, deletions) -- move them into debian/patches or clean the tree"
fi
set -e
echo "ISPKG_STEP_END: build_warnings exit=0"
_RESULT=ok
