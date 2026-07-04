# step_ppa_build_warnings — fetch every arch's build log for the latest
# published source of this package in $ppa, dedup warnings across archs, and
# report categorized counts. PPA-gated like step_lintian_binary: with no
# $ppa, this step is skipped. See launchpad-api behavior: build_log_url is
# served as raw application/gzip so `curl --compressed` does NOT decompress —
# real `gunzip` is required.
# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_ppa_build_warnings() {
	if [ -z "$ppa" ]; then
		status skipped
		summary "no ppa argument — pass ppa=owner/name to fetch launchpad build logs"
		return 0
	fi
	have curl || {
		status fail
		summary "curl not installed"
		return 1
	}
	have gunzip || {
		status fail
		summary "gunzip not installed (launchpad build logs are gzipped)"
		return 1
	}
	have python3 || {
		status fail
		summary "python3 not installed (needed to parse launchpad JSON)"
		return 1
	}
	pkg=$(dpkg-parsechangelog -S Source 2>/dev/null) || {
		status fail
		summary "dpkg-parsechangelog failed"
		return 1
	}
	series=$(dpkg-parsechangelog -S Distribution 2>/dev/null)
	[ -n "$series" ] || {
		status fail
		summary "changelog Distribution empty"
		return 1
	}

	p=${ppa#ppa:}
	owner=${p%/*}
	archive=${p#*/}
	lp="https://api.launchpad.net/1.0"
	tmp=$(mktemp -d)
	register_cleanup "$tmp"

	# 1. latest source publication for pkg in series
	curl -sfG --retry 3 --retry-delay 2 \
		"$lp/~$owner/+archive/ubuntu/$archive" \
		--data-urlencode "ws.op=getPublishedSources" \
		--data-urlencode "source_name=$pkg" \
		--data-urlencode "exact_match=true" \
		--data-urlencode "order_by_date=true" \
		--data-urlencode "distro_series=$lp/ubuntu/$series" \
		-o "$tmp/pub.json" || {
		status warn
		summary "launchpad API unreachable or PPA not found ($owner/$archive)"
		return 0
	}

	pub_link=$(python3 -c '
import json,sys
d=json.load(open(sys.argv[1]))
es=d.get("entries") or []
print(es[0]["self_link"] if es else "")
' "$tmp/pub.json")
	[ -n "$pub_link" ] || {
		status warn
		summary "no source publication of $pkg in $ppa for $series"
		return 0
	}

	# 2. all builds for that publication
	curl -sfG --retry 3 --retry-delay 2 "$pub_link" \
		--data-urlencode "ws.op=getBuilds" \
		-o "$tmp/builds.json" || {
		status warn
		summary "could not list builds for $pkg publication"
		return 0
	}

	# 3. every build with a log in a final state (Successfully built / Failed
	#    to build). Emit "url arch state" per line.
	python3 -c '
import json,sys
d=json.load(open(sys.argv[1]))
for e in d.get("entries") or []:
    if not e.get("build_log_url"): continue
    if e.get("buildstate") not in ("Successfully built","Failed to build"): continue
    print(e["build_log_url"], e.get("arch_tag","?"), e["buildstate"].replace(" ","_"))
' "$tmp/builds.json" >"$tmp/builds.list"

	n_builds=$(wc -l <"$tmp/builds.list")
	[ "$n_builds" -gt 0 ] || {
		status skipped
		summary "no completed builds with logs yet for $pkg in $ppa/$series"
		return 0
	}

	# 4. fetch each log (max 6, 10 MB compressed → 100 MB decompressed cap).
	#    Pipe curl → gunzip → head -c because LP serves .txt.gz as raw
	#    gzipped bytes with Content-Type: application/gzip, so
	#    curl --compressed is a no-op.
	i=0
	fetched=""
	while IFS=' ' read -r url arch state; do
		i=$((i + 1))
		[ "$i" -gt 6 ] && break
		if curl -sfL --retry 3 --retry-delay 2 --max-filesize 10485760 "$url" 2>/dev/null |
			gunzip -c 2>/dev/null |
			head -c 104857600 >"$tmp/log.$i" && [ -s "$tmp/log.$i" ]; then
			fetched="$fetched $arch:$state"
		fi
	done <"$tmp/builds.list"

	# shellcheck disable=SC2144
	ls "$tmp"/log.* >/dev/null 2>&1 || {
		status warn
		summary "all $n_builds build logs failed to fetch"
		return 0
	}

	echo "=== source ==="
	echo "package: $pkg"
	echo "series:  $series"
	echo "fetched:$fetched"
	echo

	# 5. categorize with the shared analyzer (see ISPKG_WARNINGS_AWK in
	#    prelude.sh); duplicate matches across the per-arch logs collapse
	#    because awk array keys persist across all input files.
	build_warnings_report "$tmp"/log.*
	totals=$(build_warnings_totals "$tmp"/log.*)

	if [ "$totals" = "$ISPKG_WARNINGS_NONE" ]; then
		status ok
		summary "no build warnings across archs ($fetched)"
		return 0
	fi
	status warn
	summary "build warnings: $totals across archs ($fetched)"
	hint "agent: for undefined substvars, verify debian/rules populates them (substvars files or dh_gencontrol -V); for useless shlibdeps, propose -Wl,--as-needed in debian/rules; plugin-symbol refs are usually expected on plugin architectures (LLVM etc.) -- sanity-check against known plugin ABI"
}
