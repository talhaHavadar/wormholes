# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_symbols_check_level() {
	[ -f debian/rules ] || {
		status skipped
		summary "no debian/rules"
		return 0
	}
	if grep -qE '^[[:space:]]*export[[:space:]]+DPKG_GENSYMBOLS_CHECK_LEVEL[[:space:]]*=[[:space:]]*4' debian/rules ||
		grep -qE 'DPKG_GENSYMBOLS_CHECK_LEVEL[[:space:]]*[:?]?=[[:space:]]*4' debian/rules; then
		status ok
		summary "DPKG_GENSYMBOLS_CHECK_LEVEL=4 set in debian/rules"
	else
		status warn
		summary "DPKG_GENSYMBOLS_CHECK_LEVEL=4 not set — strict symbols checking disabled"
	fi
}
