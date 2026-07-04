# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_signed_tags() {
	[ -f debian/watch ] || {
		status skipped
		summary "no debian/watch"
		return 0
	}
	if grep -qE 'pgpsigurlmangle|pgpmode' debian/watch; then
		status ok
		summary "debian/watch enforces upstream signature verification"
	else
		status warn
		summary "debian/watch does not verify upstream signatures (pgpsigurlmangle/pgpmode)"
		hint "agent: if upstream signs release tags or tarballs, add pgpmode=auto or pgpsigurlmangle to debian/watch"
	fi
}
