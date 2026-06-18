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
set -eu

_CLEANUP=
_RESULT=fail

emit_error()   { echo "ISPKG_ERROR: $*" >&2; }
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
#          sbuild), then cd into the package dir. Stale workspaces are removed
#          first so each run starts clean. Echoes ISPKG_WORKSPACE=<dir>.
#   local: the runner already set cwd to the source tree (and contained mounts
#          it), so there is nothing to do.
acquire_source() {
	[ "$1" = git ] || return 0
	prefix=interstellar-build-
	rm -rf -- "$prefix"* 2>/dev/null || true
	d=$(cd "$(mktemp -d "${prefix}XXXXXX")" && pwd)
	register_cleanup "$d"
	echo "ISPKG_WORKSPACE=$d"
	# Assemble the clone argv in a subshell so reusing the positional list does
	# not clobber the tool args the caller still holds in "$@".
	( repo=$2; ref=$3; depth=$4
	  set -- clone
	  [ -n "$ref" ] && set -- "$@" --branch "$ref"
	  [ "${depth:-0}" -gt 0 ] 2>/dev/null && set -- "$@" --depth "$depth"
	  exec git "$@" -- "$repo" "$d/pkg" )
	mkdir -p "$d/build-area"
	cd "$d/pkg"
}

# fetch_orig — make the upstream tarball available for a non-native package.
# uscan deposits it in ../ (where dpkg-source and sbuild look). Native packages
# need none. A failure is relayed clearly instead of surfacing later as a
# confusing dpkg-source error.
fetch_orig() {
	{ [ -f debian/source/format ] && grep -q '(native)' debian/source/format; } && return 0
	uscan --download-current-version && return 0
	emit_error "orig tarball fetch failed (uscan --download-current-version)"
	return 1
}

# require_tool <command> <package> — fail with an actionable message when a
# helper this stage needs is not installed on the builder.
require_tool() {
	command -v "$1" >/dev/null 2>&1 && return 0
	emit_error "$1 not found on the builder — install $2 there to use this"
	return 1
}
