# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_changelog_self_refs() {
	[ -f debian/changelog ] || {
		status fail
		summary "debian/changelog missing"
		return 1
	}
	if grep -nE '^[[:space:]]+\*.*(d/changelog|debian/changelog)' debian/changelog; then
		status warn
		summary "changelog entries reference debian/changelog itself — remove these lines"
	else
		status ok
		summary "no self-referential changelog entries"
	fi
}
