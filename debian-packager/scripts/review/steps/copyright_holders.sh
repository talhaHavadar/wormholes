# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_copyright_holders() {
	[ -f debian/copyright ] || {
		status fail
		summary "debian/copyright missing"
		return 1
	}
	[ -f debian/changelog ] || {
		status fail
		summary "debian/changelog missing"
		return 1
	}
	echo "=== authors from debian/changelog ==="
	awk '/^ -- /{ sub(/^ -- /,""); sub(/  .*/,""); print }' debian/changelog | sort -u
	echo
	echo "=== copyright holders for debian/* paragraphs ==="
	awk '
        BEGIN { in_deb=0 }
        /^Files:/         { in_deb = ($0 ~ /debian\/\*/) ? 1 : 0; in_cp=0; next }
        in_deb && /^Copyright:/ { sub(/^Copyright:[[:space:]]*/,""); print; in_cp=1; next }
        in_deb && in_cp && /^[[:space:]]/ { sub(/^[[:space:]]+/,""); print; next }
        in_deb && /^[A-Za-z-]+:/ { in_cp=0 }
        /^$/              { in_deb=0; in_cp=0 }
    ' debian/copyright | sort -u
	status warn
	summary "compare the two lists above"
	hint "agent: every debian/changelog author must appear under a debian/* Copyright paragraph in debian/copyright; flag any missing"
}
