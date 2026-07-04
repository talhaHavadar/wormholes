# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_lintian_source() {
	have lintian || {
		status fail
		summary "lintian not installed"
		return 1
	}
	echo "=== fetch orig ==="
	fetch_orig_quiet || {
		status fail
		if is_snapshot_package; then
			summary "snapshot orig failed (see fetch output above)"
		else
			summary "uscan --download-current-version failed (see fetch output above)"
		fi
		return 1
	}
	if have debuild; then
		echo "=== debuild --no-conf -S -d -uc -us ==="
		debuild --no-conf -S -d -uc -us || {
			status fail
			summary "debuild -S -d failed (see source-build output above)"
			return 1
		}
	else
		echo "=== dpkg-source -b . ==="
		dpkg-source -b . || {
			status fail
			summary "dpkg-source -b failed (see source-build output above)"
			return 1
		}
	fi
	# Bare `lintian` reads debian/changelog from cwd and auto-locates the
	# matching .changes in ../, ../build-area, or /var/cache/pbuilder/result.
	# It is anchored to this package's exact name+version, so a sibling
	# package's stale .dsc in the same parent (~/projects/debian/*) can't
	# ever be picked by mistake. Staying in cwd also keeps every step after
	# this one on the source tree, as they assume.
	rc=0
	lintian -EviIL +pedantic || rc=$?
	# lintian: 0 clean, 1 had tags, >=2 internal error
	if [ "$rc" -ge 2 ]; then
		status fail
		summary "lintian failed (exit $rc)"
		return "$rc"
	elif [ "$rc" -eq 1 ]; then
		status warn
		summary "lintian reported tags — every E must be resolved, every W needs a fix or documented justification"
		return 0
	fi
	status ok
	summary "lintian source clean"
}
