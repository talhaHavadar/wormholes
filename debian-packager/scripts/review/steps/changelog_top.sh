# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_changelog_top() {
	[ -f debian/changelog ] || {
		status fail
		summary "debian/changelog missing"
		return 1
	}
	have dpkg-parsechangelog || {
		status fail
		summary "dpkg-parsechangelog not installed"
		return 1
	}
	dpkg-parsechangelog
	ver=$(dpkg-parsechangelog -S Version 2>/dev/null)
	dist=$(dpkg-parsechangelog -S Distribution 2>/dev/null)
	status ok
	summary "top entry: $ver -> $dist"
	hint "agent: verify each changelog bullet corresponds to a real git commit on the packaging branch"
}
