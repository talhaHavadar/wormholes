# lint.sh — lint a package with lintian.
# Source-prep args: <kind> <repo|path> <ref> <depth>. Tool args: [ppa].
#   ppa set   → pull the prebuilt source + binary artifacts for this package
#               from that Launchpad PPA (via ubuntu-dev-tools) and lint them.
#   ppa unset → build the source package locally and lint it (source only;
#               warns that there are no binaries to lint).
acquire_source "$1" "$2" "$3" "$4"
shift 4
ppa=${1:-}

pkg=$(dpkg-parsechangelog -S Source)
series=$(dpkg-parsechangelog -S Distribution)

if [ -n "$ppa" ]; then
	require_tool pull-ppa-source ubuntu-dev-tools
	require_tool pull-ppa-debs ubuntu-dev-tools
	ppa=${ppa#ppa:}
	art=$(mktemp -d)
	register_cleanup "$art"
	cd "$art"
	# --ppa takes <owner>/<name>; release is the changelog distribution.
	pull-ppa-source --ppa "$ppa" "$pkg" "$series" \
		|| { emit_error "pull-ppa-source failed for $pkg ($series) from ppa $ppa"; exit 1; }
	pull-ppa-debs --ppa "$ppa" "$pkg" "$series" \
		|| emit_warning "no binary packages pulled from ppa $ppa (built yet?); linting source only"
	patterns='*.dsc *.deb *.ddeb'
else
	fetch_orig
	dpkg-buildpackage -S -us -uc -d || { emit_error "source build failed before lint"; exit 1; }
	cd ..
	emit_warning "no binary packages to lint; linted the source package only (pass a ppa to lint built binaries)"
	patterns='*.dsc'
fi

# Lint whichever artifacts actually landed (glob-safe: skip non-matches).
files=
for pat in $patterns; do
	for f in $pat; do
		[ -e "$f" ] && files="$files $f"
	done
done
[ -n "$files" ] || { emit_error "no artifacts found to lint"; exit 1; }

rc=0
# shellcheck disable=SC2086  # intentional word-split of the collected paths
lintian --info --pedantic --no-tag-display-limit $files || rc=$?
[ "$rc" -lt 2 ] && _RESULT=ok   # lintian 0/1 = ran fine (tags are findings); >=2 = it failed
exit "$rc"
