# build-binary.sh — build binary packages (.deb) with sbuild.
# Source-prep args: <kind> <repo|path> <ref> <depth>. Tool args: <distribution> [arch].
acquire_source "$1" "$2" "$3" "$4"
shift 4
fetch_orig
dist=$1
arch=${2:-}
set -- sbuild -d "$dist"
[ -n "$arch" ] && set -- "$@" --arch "$arch"
"$@" --no-clean-source
_RESULT=ok
