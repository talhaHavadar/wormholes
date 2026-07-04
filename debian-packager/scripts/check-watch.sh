# check-watch.sh — report whether debian/watch has a newer upstream release.
# Source-prep args: <kind> <repo|path> <ref> <depth>. No tool args.
# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
acquire_source "$1" "$2" "$3" "$4"
shift 4
rc=0
uscan --report --dehs || rc=$?
_RESULT=ok # a watch check leaves only scratch; always clean up
exit "$rc"
