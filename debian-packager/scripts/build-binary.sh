# build-binary.sh — build binary packages (.deb) with sbuild.
# Source-prep args: <kind> <repo|path> <ref> <depth>. Tool args: <distribution> [arch].
# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
acquire_source "$1" "$2" "$3" "$4"
shift 4
fetch_orig
dist=$1
arch=${2:-}
set -- sbuild -d "$dist"
[ -n "$arch" ] && set -- "$@" --arch "$arch"

# sbuild streams the whole build log to its console AND writes it to a .build
# file. The exec link buffers everything in memory on both ends, so shipping
# the console copy of an LLVM-sized log is pure waste — capture it to a
# scratch file on the builder and emit only a bounded tail. The full log
# stays on the builder (the .build file; plus this capture, kept on failure).
console=$(mktemp "${TMPDIR:-/tmp}/sbuild-console.XXXXXX")
register_cleanup "$console"
rc=0
"$@" --no-clean-source >"$console" 2>&1 || rc=$?

echo "=== sbuild console tail (exit $rc; full log kept on builder) ==="
tail -n 250 "$console"

# a failed build exits here: on_exit keeps the workspace and console capture
# for debugging, and the tail above carries the diagnostics.
[ "$rc" -eq 0 ] || exit "$rc"

# locate the sbuild output dir (log + artifacts): the builder config puts
# them in ../build-area; stock sbuild drops them next to the source or in
# cwd. Newest .build wins — stale logs from earlier runs sort later; sbuild
# also leaves a <pkg>_<ver>_<arch>.build symlink to the timestamped log, and
# taking exactly one file keeps the warning totals single-counted.
logdir=
log=
for d in ../build-area .. .; do
	# shellcheck disable=SC2012  # ls -t for newest-by-mtime; artifact names are tame
	log=$(ls -1t "$d"/*.build 2>/dev/null | head -n 1)
	[ -n "$log" ] && {
		logdir=$d
		break
	}
done

# deterministic artifact listing from the .changes Files section — the
# bounded console tail can scroll dpkg-deb lines out on many-binary packages.
if [ -n "$logdir" ]; then
	# shellcheck disable=SC2012  # ls -t for newest-by-mtime; artifact names are tame
	changes=$(ls -1t "$logdir"/*.changes 2>/dev/null | head -n 1)
	if [ -n "$changes" ]; then
		echo "=== artifacts ($changes) ==="
		awk '/^Files:/{f=1;next} f&&/^ /{print $NF;next} f{f=0}' "$changes"
	fi
fi

# analyze the full build log for warnings with the shared analyzer (same
# categories as review's ppa_build_warnings step). The whole block runs with
# set +e — a missing log or an analyzer hiccup must never fail a build that
# just succeeded, so the step only ever reports ok/warn/skipped and the block
# always ends cleanly.
echo "ISPKG_STEP_BEGIN: build_warnings"
set +e
if [ -z "$log" ]; then
	status skipped
	summary "no .build log found (looked in ../build-area, .., .)"
else
	echo "=== build log: $log ==="
	build_warnings_report "$log"
	totals=$(build_warnings_totals "$log")
	if [ "$totals" = "$ISPKG_WARNINGS_NONE" ]; then
		status ok
		summary "no build warnings in ${log##*/}"
	else
		status warn
		summary "build warnings: $totals in ${log##*/}"
		hint "agent: for undefined substvars, verify debian/rules populates them (substvars files or dh_gencontrol -V); for useless shlibdeps, propose -Wl,--as-needed in debian/rules; plugin-symbol refs are usually expected on plugin architectures (LLVM etc.) -- sanity-check against known plugin ABI"
	fi
fi
set -e
echo "ISPKG_STEP_END: build_warnings exit=0"
_RESULT=ok
