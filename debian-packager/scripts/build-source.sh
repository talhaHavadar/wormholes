# build-source.sh — build a Debian source package (.dsc/.changes) for upload.
# Source-prep args: <kind> <repo|path> <ref> <depth>. No tool args.
acquire_source "$1" "$2" "$3" "$4"
shift 4
fetch_orig
dpkg-buildpackage -S -us -uc -d
_RESULT=ok
