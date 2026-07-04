# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_copyright_licensecheck() {
	have licensecheck || {
		status fail
		summary "licensecheck not installed (devscripts)"
		return 1
	}
	# Full per-file report first, compact histogram LAST: the Go side budgets
	# an ok step's log tail to a few KB (tail-preserving), so on big trees the
	# per-file lines get trimmed while the histogram always survives.
	rep=$(mktemp)
	rc=0
	licensecheck --check '.*' --recursive --deb-machine --lines 0 . >"$rep" || rc=$?
	cat "$rep"
	echo
	echo "=== license histogram (files x license) ==="
	awk -F'\t' 'NF >= 2 { c[$2]++ } END { for (l in c) printf "%6d  %s\n", c[l], l }' "$rep" | sort -rn
	rm -f "$rep"
	if [ "$rc" -ne 0 ]; then
		status warn
		summary "licensecheck exited $rc — histogram above may be partial"
		return 0
	fi
	status ok
	summary "license histogram at the end of the log (per-file lines above it may be trimmed)"
	hint "agent: cross-check every detected license against debian/copyright; flag missing or incompatible entries"
}
