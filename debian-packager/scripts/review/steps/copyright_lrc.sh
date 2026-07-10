# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_copyright_lrc() {
	if ! have lrc; then
		status skipped
		summary "lrc not installed (apt install licenserecon)"
		return 0
	fi
	rc=0
	timeout 30m lrc || rc=$?
	if [ "$rc" -eq 0 ]; then
		status ok
	elif [ "$rc" -eq 124 ]; then
		status warn
		summary "lrc timed out after 30m"
	else
		status warn
		summary "lrc reported discrepancies (exit $rc)"
	fi
}
