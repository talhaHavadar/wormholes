# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_control_wrap_sort() {
	have wrap-and-sort || {
		status fail
		summary "wrap-and-sort not installed (devscripts)"
		return 1
	}
	[ -d debian ] || {
		status fail
		summary "no debian/ directory"
		return 1
	}
	tmp=$(mktemp -d)
	register_cleanup "$tmp"
	cp -a debian "$tmp/"
	(cd "$tmp" && wrap-and-sort) >/dev/null 2>&1 || true
	if diff -ruN debian "$tmp/debian"; then
		status ok
		summary "debian/ already matches wrap-and-sort"
	else
		status warn
		summary "wrap-and-sort would change debian/ (diff above)"
	fi
}
