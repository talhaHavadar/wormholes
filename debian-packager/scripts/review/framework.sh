# review/framework.sh — the Debian package review checklist: args, config,
# and step infrastructure.
# Source-prep args: <kind> <repo|path> <ref> <depth>. Tool args: [ppa].
#
# The review tool body is assembled by Go (see assembleReviewBody) as:
#   framework.sh + steps/<name>.sh (sorted) + runner.sh
# Each step lives in its own file defining step_<name>() and is invoked
# through run_step, which wraps it in ISPKG_STEP_BEGIN/END markers. From
# inside a step, the prelude helpers `status`, `summary`, `hint` emit the
# STATUS/SUMMARY/HINT markers (see prelude.sh). A step that doesn't set
# STATUS gets ok on exit 0, fail otherwise.
#
# Per-step failures DO NOT abort the loop — run_step toggles set +e around
# the call. A step's own status (or its exit code) is what the report shows.
# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
acquire_source "$1" "$2" "$3" "$4"
shift 4
# shellcheck disable=SC2034  # read by the steps fragments (lintian_binary, ppa_build_warnings)
ppa=${1:-}

# Merge stderr into stdout so build/lint output appears inside the step block
# it came from. The Go parser concatenates the captured stdout and stderr
# buffers (stdout first, then stderr), so anything emitted to stderr inside a
# step would otherwise land after all ISPKG_STEP_END markers and be dropped
# from the per-step LogTail. With this in place a failing debuild/uscan/
# snapshot dumps its error right inside its step's block.
exec 2>&1

# ────────────────────────────────────────────────────────────────────────
# ENABLED_STEPS — edit to disable steps in the built container. A name
# listed here MUST have a matching steps/<name>.sh defining step_<name>();
# the runner refuses to start (exit 2) if any name has no function, and
# `go test` enforces the list ↔ steps/ file correspondence both ways, so
# drift is caught immediately. Adding a step = drop in steps/<name>.sh and
# list its name here (order here = report order for the inline steps).
#
# The agent CANNOT override this list — there is no per-call skip input.
# Runtime override on the builder (no rebuild needed):
#   REVIEW_DISABLED_STEPS="name1,name2"
# ────────────────────────────────────────────────────────────────────────
# shellcheck disable=SC2034  # read by runner.sh, assembled after the steps
ENABLED_STEPS="
  watch
  lintian_source
  lintian_binary
  ppa_build_warnings
  copyright_licensecheck
  copyright_lrc
  copyright_holders
  patch_headers
  control_wrap_sort
  control_standards_version
  control_libs_section
  upstream_metadata
  symbols
  symbols_check_level
  changelog_top
  changelog_self_refs
  hardening_flags
  autopkgtest_present
  news_debian
  signed_tags
  dh_python
"

# step helpers (status/summary/hint/have) come from prelude.sh

# fetch_orig_quiet is review's own non-fatal version of fetch_orig: it
# never emits ISPKG_ERROR (which would abort the whole tool), so a failed
# orig fetch fails only the step that needed it. Same source selection as
# the prelude's fetch_orig (see is_snapshot_package for the detection).
#
# "Quiet" means non-fatal, NOT silent: the tool's own output (uscan/snapshot)
# is left visible so a failure surfaces inside the step's LogTail with the
# real error message, not just a generic summary.
fetch_orig_quiet() {
	{ [ -f debian/source/format ] && grep -q '(native)' debian/source/format; } && return 0
	if is_snapshot_package; then
		have snapshot || {
			echo "snapshot tool not installed on builder PATH"
			return 1
		}
		snapshot orig
		return $?
	fi
	if command -v gbp >/dev/null 2>&1; then
		gbp export-orig && return 0
		emit_warning "gbp export-orig produced no orig; falling back to uscan"
	fi
	uscan --download-current-version
}

# run_step <name> — wrap a step function call in begin/end markers.
#
# Pins cwd back to the source tree first: some upstream subprocesses
# (uscan on multi-component watches, debuild-as-root) have been observed
# to leak cwd upward, and every step_* below assumes cwd = source tree.
# Emitting a warning marker when drift is detected preserves the signal
# for whoever wants to fix the root cause upstream.
run_step() {
	name=$1
	if [ -n "${ISPKG_SRCDIR:-}" ] && [ "$PWD" != "$ISPKG_SRCDIR" ]; then
		emit_warning "cwd drifted before step $name: was $PWD, resetting to $ISPKG_SRCDIR"
		cd "$ISPKG_SRCDIR" || {
			emit_error "cannot cd back to source dir $ISPKG_SRCDIR"
			return 1
		}
	fi
	echo "ISPKG_STEP_BEGIN: $name"
	rc=0
	set +e
	"step_$name"
	rc=$?
	set -e
	echo "ISPKG_STEP_END: $name exit=$rc"
}

# ── parallel lane infrastructure ───────────────────────────────────────
#
# Some steps run off the main line to overlap their latency with the source
# build. The Go parser (parseReviewSteps) rebuilds steps from one merged
# stream and treats a second ISPKG_STEP_BEGIN before the matching END as a
# crashed step, so concurrent steps must NOT write to the shared stdout.
# Each backgrounded step instead writes its whole BEGIN…END block to a
# private buffer file; the buffers are cat'd out — each already a complete
# block — once their producers have been waited on.

# run_step_buffered <name> <buffer-file> — like run_step, but the step's
# whole block is redirected into <buffer-file>. Runs in a subshell so its
# cwd changes and set -e toggling stay local, and points TMPDIR at the
# parallel scratch root so any mktemp the step does lands there:
# register_cleanup mutates a shell variable that never escapes a `&`
# subshell, so those temp dirs would otherwise leak past on_exit. Meant to
# be backgrounded with `&`.
run_step_buffered() {
	_name=$1
	_buf=$2
	(
		[ -n "${ISPKG_SRCDIR:-}" ] && cd "$ISPKG_SRCDIR" 2>/dev/null || true
		export TMPDIR="$ISPKG_PAR_ROOT"
		echo "ISPKG_STEP_BEGIN: $_name"
		_rc=0
		set +e
		"step_$_name"
		_rc=$?
		set -e
		echo "ISPKG_STEP_END: $_name exit=$_rc"
	) >"$_buf" 2>&1
}

# par_wait <pid...> — join background steps. A step encodes its own outcome
# in its buffered exit= marker, so a non-zero job status is not fatal here.
par_wait() {
	for _p in "$@"; do wait "$_p" 2>/dev/null || true; done
}

# par_flush <buffer-file...> — emit finished step blocks to stdout in order.
par_flush() {
	for _f in "$@"; do
		if [ -f "$_f" ]; then cat "$_f"; fi
	done
}

# in_list <name> <space-separated-list> — word-membership test.
in_list() {
	case " $2 " in
	*" $1 "*) return 0 ;;
	*) return 1 ;;
	esac
}
