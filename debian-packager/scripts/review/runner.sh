# review/runner.sh — execute the enabled review steps across the lanes.
# Assembled last (after framework.sh and every steps/<name>.sh), so all
# step functions are defined by the time this runs.

# fail fast if any enabled step has no matching function — catches drift
# between the ENABLED_STEPS list and the steps/<name>.sh definitions.
# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
missing=
for s in $ENABLED_STEPS; do
	type "step_$s" >/dev/null 2>&1 || missing="$missing $s"
done
if [ -n "$missing" ]; then
	emit_error "review: enabled steps with no step_<name>() function:$missing"
	exit 2
fi

# subtract REVIEW_DISABLED_STEPS (comma-separated) from the enabled list
disabled=" $(printf '%s' "${REVIEW_DISABLED_STEPS:-}" | tr ',' ' ') "
final=
for s in $ENABLED_STEPS; do
	case "$disabled" in
	*" $s "*) ;;
	*) final="$final $s" ;;
	esac
done

# ── lane scheduling ────────────────────────────────────────────────────
# Steps split across three lanes to overlap the two slow, I/O-bound classes
# with the source build:
#
#   NETWORK_LANE   isolated + network-bound (own mktemp; the only tree file
#                  they read is debian/changelog). Safe for the whole review,
#                  including alongside debuild — this is where a PPA review's
#                  download time hides under the build.
#   COPYRIGHT_LANE read-only scans of the source tree, run concurrently with
#                  each other. Because they read the tree, they are joined
#                  BEFORE the build lane, which mutates it (debuild -S runs
#                  dh clean).
#   BUILD_LANE     watch + lintian_source: fetch orig, build, lint. Mutates
#                  the tree and ../ and stays strictly serial.
#
# Every other enabled step is a cheap grep/awk check; those run inline and
# serial, before the build, since they read the tree too.
NETWORK_LANE="lintian_binary ppa_build_warnings"
COPYRIGHT_LANE="copyright_licensecheck copyright_lrc copyright_holders"
BUILD_LANE="watch lintian_source"

ISPKG_PAR_ROOT=$(mktemp -d)
register_cleanup "$ISPKG_PAR_ROOT"

# 1. launch the background lanes (active members only); both run while the
#    inline checks and the build proceed.
network_pids=
network_bufs=
copyright_pids=
copyright_bufs=
for s in $final; do
	in_list "$s" "$NETWORK_LANE" || continue
	buf="$ISPKG_PAR_ROOT/buf.$s"
	run_step_buffered "$s" "$buf" &
	network_pids="$network_pids $!"
	network_bufs="$network_bufs $buf"
done
for s in $final; do
	in_list "$s" "$COPYRIGHT_LANE" || continue
	buf="$ISPKG_PAR_ROOT/buf.$s"
	run_step_buffered "$s" "$buf" &
	copyright_pids="$copyright_pids $!"
	copyright_bufs="$copyright_bufs $buf"
done

# 2. cheap read-only checks, inline and serial (must precede the build).
for s in $final; do
	in_list "$s" "$NETWORK_LANE $COPYRIGHT_LANE $BUILD_LANE" && continue
	run_step "$s"
done

# 3. join the tree-reading copyright lane before the build touches the tree,
#    then emit its blocks.
# shellcheck disable=SC2086  # intentional word-split of the pid/buffer lists
par_wait $copyright_pids
# shellcheck disable=SC2086
par_flush $copyright_bufs

# 4. build lane, inline and serial (mutates the tree + ../).
for s in $BUILD_LANE; do
	in_list "$s" "$final" || continue
	run_step "$s"
done

# 5. the network lane overlapped everything above; join and emit it last.
# shellcheck disable=SC2086  # intentional word-split of the pid/buffer lists
par_wait $network_pids
# shellcheck disable=SC2086
par_flush $network_bufs

# the runner completed; per-step pass/fail is conveyed in the step markers.
_RESULT=ok
