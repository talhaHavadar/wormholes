# prelude.sh — shared shell stage library for the debian-packager tools.
#
# Go prepends this to each tool body and runs the combined script as a single,
# self-contained command on the builder. Nothing is assumed to survive between
# tool runs: every tool composes the stages it needs here, in one invocation.
#
# Temp dirs we create are removed on success and kept on failure (so a broken
# build can be inspected); the next run sweeps any left behind. A tool body
# marks the run successful with `_RESULT=ok` once it has done its job — so the
# final command must NOT be exec'd, or the EXIT trap won't run.
#
# Output markers the Go side parses:
#   ISPKG_WORKSPACE=<dir>   the git clone workspace
#   ISPKG_ERROR: <msg>      a prerequisite failed; relayed to the user as an error
#   ISPKG_WARNING: <msg>    a non-fatal caveat; relayed as a warning
#   ISPKG_STEP_*            step blocks (see the helpers below); review wraps
#                           every checklist step in them, and the build tools
#                           (build-binary.sh, build-source.sh) each emit one
#                           standalone block for their build-log analysis
# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
set -eu

_CLEANUP=
_RESULT=fail

emit_error() { echo "ISPKG_ERROR: $*" >&2; }
emit_warning() { echo "ISPKG_WARNING: $*" >&2; }

# register_cleanup <dir> — mark a temp dir for removal on a clean exit.
register_cleanup() { _CLEANUP="$_CLEANUP $1"; }

# on_exit removes the registered temp dirs on success, or keeps them and points
# the user at them on failure.
on_exit() {
	_st=$?
	if [ -n "$_CLEANUP" ]; then
		if [ "$_RESULT" = ok ]; then
			# shellcheck disable=SC2086  # word-split the dir list
			rm -rf -- $_CLEANUP 2>/dev/null || true
		else
			emit_warning "kept for debugging (exit $_st):$_CLEANUP"
		fi
	fi
	exit "$_st"
}
trap on_exit EXIT

# acquire_source <kind> <repo-or-path> <ref> <depth>
#   git:   clone into a fresh temp workspace (with a sibling build-area for
#          sbuild), then cd into the package dir. Workspaces live under a
#          fixed root (${TMPDIR:-/tmp}) so every run sweeps the same place
#          regardless of the runner's starting cwd. Echoes ISPKG_WORKSPACE=<dir>.
#   local: the runner already set cwd to the source tree (and contained mounts
#          it), so there is nothing to do.
acquire_source() {
	if [ "$1" = git ]; then
		prefix=interstellar-build-
		root=${TMPDIR:-/tmp}
		# Sweep only STALE leftovers (kept-on-failure workspaces from earlier
		# runs): age-gated so a concurrent call's live workspace on the same
		# builder survives. 24h outlasts any sane build; note the top-level
		# dir mtime stays at creation time (sbuild writes inside build-area/),
		# so a build longer than this could still be reaped mid-run.
		find "$root" -maxdepth 1 -name "${prefix}*" -mmin +1440 \
			-exec rm -rf -- {} + 2>/dev/null || true
		d=$(cd "$(mktemp -d "$root/${prefix}XXXXXX")" && pwd)
		register_cleanup "$d"
		echo "ISPKG_WORKSPACE=$d"
		# Assemble the clone argv in a subshell so reusing the positional list does
		# not clobber the tool args the caller still holds in "$@".
		(
			repo=$2
			ref=$3
			depth=$4
			set -- clone
			[ -n "$ref" ] && set -- "$@" --branch "$ref"
			[ "${depth:-0}" -gt 0 ] 2>/dev/null && set -- "$@" --depth "$depth"
			exec git "$@" -- "$repo" "$d/pkg"
		) || {
			emit_error "git clone failed for $2${3:+ (ref $3)}"
			exit 1
		}
		mkdir -p "$d/build-area"
		cd "$d/pkg"
	fi
	# Anchor the source tree for later stages. Multi-step tools (review) use
	# this to defensively cd back before each step, guarding against
	# subprocesses that leak cwd (uscan/debuild have been observed to).
	# shellcheck disable=SC2034  # read by the review fragments (run_step*)
	ISPKG_SRCDIR=$PWD
}

# is_snapshot_package reports whether the package uses the `snapshot` tool
# rather than uscan-driven upstream tracking. Any one of these signals counts:
#   * debian/snapshot.conf exists       (explicit, default config location)
#   * UPSTREAM_URL is set in the env    (explicit, env-driven config)
#   * the top changelog version carries snapshot's encoded git stamp,
#     ~git<date>.<sha> or +git<date>.<sha>  (the version pattern snapshot
#     itself writes; reliable even when both configs are absent)
# A non-default snapshot config path (-c <path>) is not auto-detected — the
# user must point review at it explicitly (symlink it to debian/snapshot.conf
# or export UPSTREAM_URL).
is_snapshot_package() {
	[ -f debian/snapshot.conf ] && return 0
	[ -n "${UPSTREAM_URL:-}" ] && return 0
	[ -f debian/changelog ] || return 1
	ver=$(dpkg-parsechangelog -S Version 2>/dev/null) || return 1
	case "$ver" in
	*~git[0-9]* | *+git[0-9]*) return 0 ;;
	esac
	return 1
}

# fetch_orig — make the upstream tarball available for a non-native package.
# The orig lands in ../ (where dpkg-source and sbuild look).
#
# Source selection:
#   native package         -> no orig needed
#   snapshot package       -> use `snapshot orig` (uscan can't synthesise
#                             monorepo-subdir or multi-component origs).
#                             Requires `snapshot` on the builder PATH.
#                             See is_snapshot_package for the detection.
#   otherwise              -> `gbp export-orig` first (regenerate the orig from
#                             the pristine-tar/upstream branch, no network),
#                             falling back to uscan --download-current-version
#                             when gbp is absent or has nothing to export.
#
# A failure is relayed clearly via ISPKG_ERROR instead of surfacing later as a
# confusing dpkg-source error.
fetch_orig() {
	{ [ -f debian/source/format ] && grep -q '(native)' debian/source/format; } && return 0
	if is_snapshot_package; then
		if ! command -v snapshot >/dev/null 2>&1; then
			emit_error "snapshot-based package detected but the 'snapshot' tool is not on the builder PATH"
			return 1
		fi
		snapshot orig && return 0
		emit_error "snapshot orig failed (could not regenerate orig from upstream/pristine-tar)"
		return 1
	fi
	# Prefer git-buildpackage's orig export for git-maintained packages: it
	# rebuilds the orig from the pristine-tar/upstream branch with no network
	# round-trip. Only attempt it when gbp is installed, and fall back to uscan
	# when it has nothing to export (no pristine-tar data, no upstream branch).
	if command -v gbp >/dev/null 2>&1; then
		gbp export-orig && return 0
		emit_warning "gbp export-orig produced no orig; falling back to uscan"
	fi
	uscan --download-current-version && return 0
	emit_error "orig tarball fetch failed (gbp export-orig and uscan --download-current-version)"
	return 1
}

# require_tool <command> <package> — fail with an actionable message when a
# helper this stage needs is not installed on the builder.
require_tool() {
	command -v "$1" >/dev/null 2>&1 && return 0
	emit_error "$1 not found on the builder — install $2 there to use this"
	return 1
}

# ── step markers ────────────────────────────────────────────────────────
# From inside an ISPKG_STEP_BEGIN/END block, these emit:
#   ISPKG_STEP_STATUS:  <ok|warn|fail|skipped>
#   ISPKG_STEP_SUMMARY: <one-line takeaway shown to the agent>
#   ISPKG_STEP_HINT:    <follow-up suggestion for the agent>
# A step that doesn't set STATUS gets ok on exit 0, fail otherwise.
status() { echo "ISPKG_STEP_STATUS: $1"; }
summary() { echo "ISPKG_STEP_SUMMARY: $*"; }
hint() { echo "ISPKG_STEP_HINT: $*"; }
have() { command -v "$1" >/dev/null 2>&1; }

# ── build-log warning analysis ──────────────────────────────────────────
# Shared by review's ppa_build_warnings step (launchpad logs, all archs),
# build-binary.sh (the local sbuild .build log), and build-source.sh (the
# dpkg-buildpackage -S console capture). awk's associative-array keys
# persist across all input files, so cross-file duplicate matches collapse
# for free.
#
# Categories:
#   S — dpkg-gencontrol undefined substvars       (dedup key: var+pkg)
#   P — dpkg-shlibdeps unresolvable plugin refs   (dedup key: symbol)
#   U — dpkg-shlibdeps "useless dependency"       (dedup key: path+lib)
#   O — other dpkg tool warnings                  (dedup key: full line)
#       (dpkg-source/dpkg-genchanges/dpkg-buildpackage only — the tools
#       with dedicated buckets above stay scoped to those patterns)
#   D — dh_* warnings                             (dedup key: full line)
#   C — CMake Warning headers                     (dedup key: full line)
#
# No single quotes anywhere in the program — required, since it's
# single-quoted in shell.
# shellcheck disable=SC2016  # $0 etc. are awk's, expansion is not wanted
ISPKG_WARNINGS_AWK='
BEGIN {
    s_n = 0; p_n = 0; u_n = 0; o_n = 0; d_n = 0; c_n = 0
    limit = 100
}

# 1. dpkg-gencontrol undefined substvar
/dpkg-gencontrol: warning: .* substitution variable .* used, but is not defined/ {
    match($0, /package [^:]+:/); pkg = substr($0, RSTART + 8, RLENGTH - 9)
    match($0, /\$\{[^}]+\}/);    var = substr($0, RSTART, RLENGTH)
    k = var SUBSEP pkg
    if (!(k in subst)) {
        subst[k] = 1; s_n++
        varpkgs[var] = varpkgs[var] " " pkg
    }
    next
}

# 2. shlibdeps unresolvable plugin symbol
/dpkg-shlibdeps: warning:.*unresolvable reference to symbol .*: it is probably a plugin/ {
    match($0, /symbol [^ :]+/); sym = substr($0, RSTART + 7, RLENGTH - 7)
    plug[sym]++; p_n++
    next
}

# 3. shlibdeps useless dep
/dpkg-shlibdeps: warning: package could avoid a useless dependency if .* was not linked against / {
    if (match($0, /if [^ ]+ was not linked against [^ ]+/)) {
        frag = substr($0, RSTART, RLENGTH)
        n = split(frag, a, " ")
        # a[1]=if a[2]=<path> a[3]=was a[4]=not a[5]=linked a[6]=against a[7]=<lib>
        path = a[2]; lib = a[7]
        k = path SUBSEP lib
        if (!(k in use)) { use[k] = 1; u_n++ }
    }
    next
}

# 4. other dpkg tool warnings — the source-build side (unrepresentable diff
# changes, mode changes, missing fields). Deliberately NOT a catch-all for
# every dpkg-*: gencontrol and shlibdeps have dedicated buckets above, and
# their uncategorized variants stay unreported as before.
/^dpkg-(source|genchanges|buildpackage): warning:/ {
    if (!($0 in dpkgw)) { dpkgw[$0] = 1; o_n++ }
    next
}

# 5. dh_* warning
/^dh_[a-z_]+: warning:/ {
    if (!($0 in dhw)) { dhw[$0] = 1; d_n++ }
    next
}

# 6. CMake warning block: header + following indented message lines.
# getline consumes lines from the main input stream, so any warning that
# begins immediately after (no blank line separating) would be lost — CMake
# in practice always emits a trailing blank, so this is acceptable. Un-
# indented lines like "Call Stack (most recent call first):" terminate the
# block; the raw log still carries the stack if a reviewer needs it.
/^CMake Warning at / {
    header = $0
    block = $0
    added = 0
    while (added < 8 && (getline line) > 0) {
        if (line !~ /^[[:space:]]/ && line != "") break
        block = block "\n" line
        added++
        if (line ~ /^[[:space:]]*$/ && added > 1) break
    }
    if (!(header in cmw)) { cmw[header] = block; c_n++ }
    next
}

END {
    pu = 0; for (s in plug) pu++
    if (mode == "totals") {
        printf "S=%d P=%dx%d U=%d O=%d D=%d C=%d\n", s_n, p_n, pu, u_n, o_n, d_n, c_n
        exit 0
    }

    printf "=== undefined substvars (%d) ===\n", s_n
    if (s_n == 0) print "(none)"
    else {
        i = 0
        for (v in varpkgs) {
            if (i++ >= limit) { printf "[... %d more omitted]\n", s_n - limit; break }
            n = split(varpkgs[v], a, " "); seen = ""; out = ""
            for (j = 1; j <= n; j++) {
                if (a[j] == "") continue
                if (index(seen, " " a[j] " ") == 0) {
                    seen = seen " " a[j] " "
                    out = out (out == "" ? "" : ", ") a[j]
                }
            }
            printf "%-24s used by: %s\n", v, out
        }
    }
    print ""

    printf "=== shlibdeps: unresolvable plugin refs (%d total, %d unique symbol%s) ===\n", \
        p_n, pu, (pu == 1 ? "" : "s")
    if (p_n == 0) print "(none)"
    else {
        i = 0
        for (s in plug) {
            if (i++ >= limit) { printf "[... %d more omitted]\n", pu - limit; break }
            printf "%-40s x %d (plugin architecture -- usually expected)\n", s, plug[s]
        }
    }
    print ""

    printf "=== shlibdeps: useless dependencies (%d unique) ===\n", u_n
    if (u_n == 0) print "(none)"
    else {
        i = 0
        for (k in use) {
            if (i++ >= limit) { printf "[... %d more omitted]\n", u_n - limit; break }
            split(k, p, SUBSEP)
            printf "%-40s -> %s\n", p[1], p[2]
        }
    }
    print ""

    printf "=== other dpkg warnings (%d unique) ===\n", o_n
    if (o_n == 0) print "(none)"
    else {
        i = 0
        for (m in dpkgw) {
            if (i++ >= limit) { print "[... truncated]"; break }
            print m
        }
    }
    print ""

    printf "=== dh_ warnings (%d unique) ===\n", d_n
    if (d_n == 0) print "(none)"
    else {
        i = 0
        for (m in dhw) {
            if (i++ >= limit) { print "[... truncated]"; break }
            print m
        }
    }
    print ""

    printf "=== CMake warnings (%d unique) ===\n", c_n
    if (c_n == 0) print "(none)"
    else {
        i = 0
        for (h in cmw) {
            if (i++ >= limit) { print "[... truncated]"; break }
            print cmw[h]
            print ""
        }
    }
}
'

# build_warnings_report <log>... — categorized, deduped warning report.
build_warnings_report() {
	printf '%s' "$ISPKG_WARNINGS_AWK" | awk -f - "$@"
}

# build_warnings_totals <log>... — the one-line machine summary
# (S=<substvars> P=<total>x<unique plugin refs> U=<useless deps>
# O=<other dpkg> D=<dh> C=<cmake>).
build_warnings_totals() {
	printf '%s' "$ISPKG_WARNINGS_AWK" | awk -f - -v mode=totals "$@"
}

# ISPKG_WARNINGS_NONE — the totals line a clean log produces; callers compare
# build_warnings_totals output against this. Keep in sync with the totals
# printf in the awk END block above.
# shellcheck disable=SC2034  # read by the tool-body fragments
ISPKG_WARNINGS_NONE='S=0 P=0x0 U=0 O=0 D=0 C=0'
