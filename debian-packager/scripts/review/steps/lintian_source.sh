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

	profile=""
	if have dpkg-parsechangelog; then
		dist="$(dpkg-parsechangelog -SDistribution)"
		series=${dist%%-*}
		profile=""
		if have ubuntu-distro-info; then
			if ubuntu-distro-info --series="$series" >/dev/null 2>&1; then
				profile="--profile ubuntu"
			elif debian-distro-info --series="$series" >/dev/null 2>&1; then
				profile="--profile debian"
			fi
		else
			hint "missing ubuntu-distro-info defaulting to empty profile for lintian call"
		fi
	else
		hint "missing dpkg-parsechangelog defaulting to empty profile for lintian call"
	fi

	echo "=== lintian $profile -EviIL +pedantic ==="
	rc=0
	# shellcheck disable=SC2086 # intentional because we want smarter profile handling for lintian
	lintian $profile -EviIL +pedantic || rc=$?

	# man lintian
	# EXIT STATUS
	#  0   Normal operation.
	#  1   Lintian run-time error. An error message is sent to stderr.
	#  2   Detected a condition specified via the --fail-on option. This can be used to trigger a non-zero exit value in case of policy violations.
	if [ "$rc" -eq 1 ]; then
		status fail
		summary "lintian failed (exit $rc)"
		return "$rc"
	elif [ "$rc" -ge 2 ]; then
		status warn
		summary "lintian reported tags — every E must be resolved, every W needs a fix or documented justification"
		return 0
	fi
	status ok
	summary "lintian source clean"
}
