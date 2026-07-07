# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_lintian_binary() {
	if [ -z "$ppa" ]; then
		status skipped
		summary "no ppa argument — pass ppa=owner/name to lint prebuilt binaries"
		return 0
	fi
	have lintian || {
		status fail
		summary "lintian not installed"
		return 1
	}
	have pull-ppa-debs || {
		status fail
		summary "pull-ppa-debs not installed (ubuntu-dev-tools)"
		return 1
	}
	pkg=$(dpkg-parsechangelog -S Source 2>/dev/null) || {
		status fail
		summary "dpkg-parsechangelog failed"
		return 1
	}
	series=$(dpkg-parsechangelog -S Distribution 2>/dev/null)
	series=${series%%-*}
	art=$(mktemp -d)
	register_cleanup "$art"
	(cd "$art" && pull-ppa-debs --ppa "${ppa#ppa:}" "$pkg" "$series") ||
		{
			status fail
			summary "pull-ppa-debs failed for $pkg/$series from $ppa"
			return 1
		}
	debs=$(ls -1 -- "$art"/*.deb 2>/dev/null)
	if [ -z "$debs" ]; then
		status warn
		summary "no .deb pulled from $ppa for $pkg/$series (not built yet?)"
		return 0
	fi

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

	echo "=== lintian $profile -EviIL +pedantic ==="
	rc=0
	# shellcheck disable=SC2086 # intentional because we want smarter profile handling for lintian
	lintian $profile -EviIL +pedantic $debs || rc=$?

	if [ "$rc" -eq 1 ]; then
		status fail
		summary "lintian failed (exit $rc)"
		return "$rc"
	elif [ "$rc" -ge 2 ]; then
		status warn
		summary "lintian reported tags on binary packages"
		return 0
	fi
	status ok
	summary "binary lintian clean"
}
