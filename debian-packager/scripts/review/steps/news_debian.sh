# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_news_debian() {
	if [ -f debian/NEWS ]; then
		status ok
		summary "debian/NEWS present"
	else
		status skipped
		summary "no debian/NEWS (only needed for significant user-visible changes)"
	fi
}
