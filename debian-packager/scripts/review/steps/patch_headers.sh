# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_patch_headers() {
	[ -d debian/patches ] || {
		status skipped
		summary "no debian/patches directory"
		return 0
	}
	patches=$(find debian/patches -maxdepth 2 -type f \( -name '*.patch' -o -name '*.diff' \) 2>/dev/null)
	if [ -z "$patches" ]; then
		status skipped
		summary "no patches under debian/patches"
		return 0
	fi
	bad=0
	for p in $patches; do
		m=
		grep -qE '^(Description|Subject):' "$p" || m="$m Description"
		grep -qE '^(Origin|Author|From):' "$p" || m="$m Origin/Author"
		grep -qE '^Last-Update:' "$p" || m="$m Last-Update"
		if [ -n "$m" ]; then
			echo "$p: missing$m"
			bad=$((bad + 1))
		fi
	done
	if [ "$bad" -gt 0 ]; then
		status warn
		summary "$bad patch(es) missing DEP-3 fields"
	else
		status ok
		summary "all patches carry DEP-3 headers"
	fi
}
