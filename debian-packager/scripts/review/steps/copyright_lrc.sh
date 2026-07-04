# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_copyright_lrc() {
	if ! have lrc; then
		status skipped
		summary "lrc not installed (apt install licenserecon)"
		return 0
	fi
	rc=0
	lrc || rc=$?
	if [ "$rc" -eq 0 ]; then
		status ok
	else
		status warn
		summary "lrc reported discrepancies (exit $rc)"
	fi
}
