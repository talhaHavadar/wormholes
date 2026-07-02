#!/usr/bin/env bash
# review.sh — run the Debian package review checklist.
# Source-prep args: <kind> <repo|path> <ref> <depth>. Tool args: [ppa].
#
# Each step is implemented as step_<name>() and invoked through run_step,
# which wraps it in ISPKG_STEP_BEGIN/END markers. From inside a step,
# helpers `status`, `summary`, `hint` emit:
#   ISPKG_STEP_STATUS:  <ok|warn|fail|skipped>
#   ISPKG_STEP_SUMMARY: <one-line takeaway shown to the agent>
#   ISPKG_STEP_HINT:    <follow-up suggestion for the agent>
# A step that doesn't set STATUS gets ok on exit 0, fail otherwise.
#
# Per-step failures DO NOT abort the loop — run_step toggles set +e around
# the call. A step's own status (or its exit code) is what the report shows.
acquire_source "$1" "$2" "$3" "$4"
shift 4
ppa=${1:-}

# Merge stderr into stdout so build/lint output appears inside the step block
# it came from. The Go parser concatenates the captured stdout and stderr
# buffers (stdout first, then stderr), so anything emitted to stderr inside a
# step would otherwise land after all ISPKG_STEP_END markers and be dropped
# from the per-step LogTail. With this in place a failing debuild/uscan/
# snapshot dumps its error right inside its step's block.
exec 2>&1

# ────────────────────────────────────────────────────────────────────────
# ENABLED_STEPS — edit to disable steps in the built container. A name
# listed here MUST have a matching step_<name>() defined below; the
# runner refuses to start (exit 2) if any name has no function, so
# drift is caught immediately.
#
# The agent CANNOT override this list — there is no per-call skip input.
# Runtime override on the builder (no rebuild needed):
#   REVIEW_DISABLED_STEPS="name1,name2"
# ────────────────────────────────────────────────────────────────────────
ENABLED_STEPS="
  watch
  lintian_source
  lintian_binary
  ppa_build_warnings
  copyright_licensecheck
  copyright_lrc
  copyright_holders
  patch_headers
  control_wrap_sort
  control_standards_version
  control_libs_section
  upstream_metadata
  symbols
  symbols_check_level
  changelog_top
  changelog_self_refs
  hardening_flags
  autopkgtest_present
  news_debian
  signed_tags
  dh_python
"

# helpers usable inside step functions
status() { echo "ISPKG_STEP_STATUS: $1"; }
summary() { echo "ISPKG_STEP_SUMMARY: $*"; }
hint() { echo "ISPKG_STEP_HINT: $*"; }
have() { command -v "$1" >/dev/null 2>&1; }

# fetch_orig_quiet is review's own non-fatal version of fetch_orig: it
# never emits ISPKG_ERROR (which would abort the whole tool), so a failed
# orig fetch fails only the step that needed it. Same source selection as
# the prelude's fetch_orig (see is_snapshot_package for the detection).
#
# "Quiet" means non-fatal, NOT silent: the tool's own output (uscan/snapshot)
# is left visible so a failure surfaces inside the step's LogTail with the
# real error message, not just a generic summary.
fetch_orig_quiet() {
    { [ -f debian/source/format ] && grep -q '(native)' debian/source/format; } && return 0
    if is_snapshot_package; then
        have snapshot || {
            echo "snapshot tool not installed on builder PATH"
            return 1
        }
        snapshot orig
        return $?
    fi
    uscan --download-current-version
}

# run_step <name> — wrap a step function call in begin/end markers.
#
# Pins cwd back to the source tree first: some upstream subprocesses
# (uscan on multi-component watches, debuild-as-root) have been observed
# to leak cwd upward, and every step_* below assumes cwd = source tree.
# Emitting a warning marker when drift is detected preserves the signal
# for whoever wants to fix the root cause upstream.
run_step() {
    name=$1
    if [ -n "${ISPKG_SRCDIR:-}" ] && [ "$PWD" != "$ISPKG_SRCDIR" ]; then
        emit_warning "cwd drifted before step $name: was $PWD, resetting to $ISPKG_SRCDIR"
        cd "$ISPKG_SRCDIR" || {
            emit_error "cannot cd back to source dir $ISPKG_SRCDIR"
            return 1
        }
    fi
    echo "ISPKG_STEP_BEGIN: $name"
    rc=0
    set +e
    "step_$name"
    rc=$?
    set -e
    echo "ISPKG_STEP_END: $name exit=$rc"
}

# ── step implementations (cwd = source tree) ───────────────────────────

step_watch() {
    # Snapshot-based packages: uscan can't track a monorepo-subdir source.
    # Regenerating the orig with `snapshot orig` IS the upstream health
    # check — success proves upstream is reachable and the orig is
    # reproducible; failure means tracking is broken. uscan's "is there a
    # newer release" question does not apply.
    if is_snapshot_package; then
        have snapshot || {
            status fail
            summary "snapshot-based package detected but 'snapshot' tool not installed on builder"
            return 1
        }
        if [ -f debian/snapshot.conf ]; then
            echo "=== debian/snapshot.conf ==="
            cat debian/snapshot.conf
            echo
        else
            echo "=== snapshot detected via $([ -n "${UPSTREAM_URL:-}" ] && echo 'UPSTREAM_URL env' || echo 'changelog version pattern (~git…)') ==="
            echo
        fi
        echo "=== snapshot orig ==="
        if snapshot orig; then
            status ok
            summary "snapshot orig succeeded — upstream is reachable and orig is reproducible"
            hint "agent: snapshot-based packages have no 'newer release' concept; the maintainer rolls new snapshots manually with 'snapshot create -u <ver>'"
            return 0
        fi
        status fail
        summary "snapshot orig failed — upstream tracking is broken"
        return 1
    fi
    [ -f debian/watch ] || {
        status fail
        summary "debian/watch missing"
        return 1
    }
    have uscan || {
        status fail
        summary "uscan not installed (devscripts)"
        return 1
    }
    uscan --watchfile debian/watch --verbose --report --no-download
}

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
    rc=0
    # shellcheck disable=SC2086
    lintian -EviIL +pedantic $debs || rc=$?
    if [ "$rc" -ge 2 ]; then
        status fail
        summary "lintian failed (exit $rc)"
        return "$rc"
    elif [ "$rc" -eq 1 ]; then
        status warn
        summary "lintian reported tags on binary packages"
        return 0
    fi
    status ok
    summary "binary lintian clean"
}

# step_ppa_build_warnings — fetch every arch's build log for the latest
# published source of this package in $ppa, dedup warnings across archs, and
# report categorized counts. PPA-gated like step_lintian_binary: with no
# $ppa, this step is skipped. See launchpad-api behavior: build_log_url is
# served as raw application/gzip so `curl --compressed` does NOT decompress —
# real `gunzip` is required.
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
' "$tmp/builds.json" > "$tmp/builds.list"

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

    # 5. categorize. awk's associative-array keys persist across all input
    #    files, so per-arch duplicate matches collapse for free.
    #
    # Categories:
    #   S — dpkg-gencontrol undefined substvars       (dedup key: var+pkg)
    #   P — dpkg-shlibdeps unresolvable plugin refs   (dedup key: symbol)
    #   U — dpkg-shlibdeps "useless dependency"       (dedup key: path+lib)
    #   D — dh_* warnings                             (dedup key: full line)
    #   C — CMake Warning headers                     (dedup key: full line)
    #
    # No single quotes anywhere in awk_prog — required, since it's
    # single-quoted in shell.
    awk_prog='
BEGIN {
    s_n = 0; p_n = 0; u_n = 0; d_n = 0; c_n = 0
    limit = 100
}

# 1. dpkg-gencontrol undefined substvar
/dpkg-gencontrol: warning: .* substitution variable .* used, but is not defined/ {
    match($0, /package [^:]+:/); pkg = substr($0, RSTART + 8, RLENGTH - 9)
    match($0, /\$\{[^}]+\}/);    var = substr($0, RSTART, RLENGTH)
    k = var SUBSEP pkg
    if (!(k in subst)) {
        subst[k] = 1; s_n++
        varpkgs[var] = varpkgs[var] " " pkg
    }
    next
}

# 2. shlibdeps unresolvable plugin symbol
/dpkg-shlibdeps: warning:.*unresolvable reference to symbol .*: it is probably a plugin/ {
    match($0, /symbol [^ :]+/); sym = substr($0, RSTART + 7, RLENGTH - 7)
    plug[sym]++; p_n++
    next
}

# 3. shlibdeps useless dep
/dpkg-shlibdeps: warning: package could avoid a useless dependency if .* was not linked against / {
    if (match($0, /if [^ ]+ was not linked against [^ ]+/)) {
        frag = substr($0, RSTART, RLENGTH)
        n = split(frag, a, " ")
        # a[1]=if a[2]=<path> a[3]=was a[4]=not a[5]=linked a[6]=against a[7]=<lib>
        path = a[2]; lib = a[7]
        k = path SUBSEP lib
        if (!(k in use)) { use[k] = 1; u_n++ }
    }
    next
}

# 4. dh_* warning
/^dh_[a-z_]+: warning:/ {
    if (!($0 in dhw)) { dhw[$0] = 1; d_n++ }
    next
}

# 5. CMake warning block: header + following indented message lines.
# getline consumes lines from the main input stream, so any warning that
# begins immediately after (no blank line separating) would be lost — CMake
# in practice always emits a trailing blank, so this is acceptable. Un-
# indented lines like "Call Stack (most recent call first):" terminate the
# block; the raw log still carries the stack if a reviewer needs it.
/^CMake Warning at / {
    header = $0
    block = $0
    added = 0
    while (added < 8 && (getline line) > 0) {
        if (line !~ /^[[:space:]]/ && line != "") break
        block = block "\n" line
        added++
        if (line ~ /^[[:space:]]*$/ && added > 1) break
    }
    if (!(header in cmw)) { cmw[header] = block; c_n++ }
    next
}

END {
    pu = 0; for (s in plug) pu++
    if (mode == "totals") {
        printf "S=%d P=%dx%d U=%d D=%d C=%d\n", s_n, p_n, pu, u_n, d_n, c_n
        exit 0
    }

    printf "=== undefined substvars (%d) ===\n", s_n
    if (s_n == 0) print "(none)"
    else {
        i = 0
        for (v in varpkgs) {
            if (i++ >= limit) { printf "[... %d more omitted]\n", s_n - limit; break }
            n = split(varpkgs[v], a, " "); seen = ""; out = ""
            for (j = 1; j <= n; j++) {
                if (a[j] == "") continue
                if (index(seen, " " a[j] " ") == 0) {
                    seen = seen " " a[j] " "
                    out = out (out == "" ? "" : ", ") a[j]
                }
            }
            printf "%-24s used by: %s\n", v, out
        }
    }
    print ""

    printf "=== shlibdeps: unresolvable plugin refs (%d total, %d unique symbol%s) ===\n", \
        p_n, pu, (pu == 1 ? "" : "s")
    if (p_n == 0) print "(none)"
    else {
        i = 0
        for (s in plug) {
            if (i++ >= limit) { printf "[... %d more omitted]\n", pu - limit; break }
            printf "%-40s x %d (plugin architecture -- usually expected)\n", s, plug[s]
        }
    }
    print ""

    printf "=== shlibdeps: useless dependencies (%d unique) ===\n", u_n
    if (u_n == 0) print "(none)"
    else {
        i = 0
        for (k in use) {
            if (i++ >= limit) { printf "[... %d more omitted]\n", u_n - limit; break }
            split(k, p, SUBSEP)
            printf "%-40s -> %s\n", p[1], p[2]
        }
    }
    print ""

    printf "=== dh_ warnings (%d unique) ===\n", d_n
    if (d_n == 0) print "(none)"
    else {
        i = 0
        for (m in dhw) {
            if (i++ >= limit) { print "[... truncated]"; break }
            print m
        }
    }
    print ""

    printf "=== CMake warnings (%d unique) ===\n", c_n
    if (c_n == 0) print "(none)"
    else {
        i = 0
        for (h in cmw) {
            if (i++ >= limit) { print "[... truncated]"; break }
            print cmw[h]
            print ""
        }
    }
}
'
    echo "=== source ==="
    echo "package: $pkg"
    echo "series:  $series"
    echo "fetched:$fetched"
    echo

    printf '%s' "$awk_prog" | awk -f - "$tmp"/log.*
    totals=$(printf '%s' "$awk_prog" | awk -f - -v mode=totals "$tmp"/log.*)

    if echo "$totals" | grep -qE '^S=0 P=0x0 U=0 D=0 C=0$'; then
        status ok
        summary "no build warnings across archs ($fetched)"
        return 0
    fi
    status warn
    summary "build warnings: $totals across archs ($fetched)"
    hint "agent: for undefined substvars, verify debian/rules populates them (substvars files or dh_gencontrol -V); for useless shlibdeps, propose -Wl,--as-needed in debian/rules; plugin-symbol refs are usually expected on plugin architectures (LLVM etc.) -- sanity-check against known plugin ABI"
}

step_copyright_licensecheck() {
    have licensecheck || {
        status fail
        summary "licensecheck not installed (devscripts)"
        return 1
    }
    licensecheck --check '.*' --recursive --deb-machine --lines 0 .
    status ok
    summary "see machine-readable license report above"
    hint "agent: cross-check every detected license against debian/copyright; flag missing or incompatible entries"
}

step_copyright_lrc() {
    if ! have lrc; then
        status skipped
        summary "lrc not installed (apt install licenserecon)"
        return 0
    fi
    rc=0
    lrc || rc=$?
    if [ "$rc" -eq 0 ]; then
        status ok
    else
        status warn
        summary "lrc reported discrepancies (exit $rc)"
    fi
}

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

step_patch_headers() {
    [ -d debian/patches ] || {
        status skipped
        summary "no debian/patches directory"
        return 0
    }
    patches=$(find debian/patches -maxdepth 2 -type f \( -name '*.patch' -o -name '*.diff' \) 2>/dev/null)
    if [ -z "$patches" ]; then
        status skipped
        summary "no patches under debian/patches"
        return 0
    fi
    bad=0
    for p in $patches; do
        m=
        grep -qE '^(Description|Subject):' "$p" || m="$m Description"
        grep -qE '^(Origin|Author|From):' "$p" || m="$m Origin/Author"
        grep -qE '^Last-Update:' "$p" || m="$m Last-Update"
        if [ -n "$m" ]; then
            echo "$p: missing$m"
            bad=$((bad + 1))
        fi
    done
    if [ "$bad" -gt 0 ]; then
        status warn
        summary "$bad patch(es) missing DEP-3 fields"
    else
        status ok
        summary "all patches carry DEP-3 headers"
    fi
}

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
    (cd "$tmp" && wrap-and-sort -ast) >/dev/null 2>&1 || true
    if diff -ruN debian "$tmp/debian"; then
        status ok
        summary "debian/ already matches wrap-and-sort -ast"
    else
        status warn
        summary "wrap-and-sort -ast would change debian/ (diff above)"
    fi
}

step_control_standards_version() {
    [ -f debian/control ] || {
        status fail
        summary "debian/control missing"
        return 1
    }
    sv=$(awk -F': *' '/^Standards-Version:/{print $2; exit}' debian/control)
    if [ -z "$sv" ]; then
        status fail
        summary "Standards-Version field missing from debian/control"
        return 1
    fi
    echo "Standards-Version: $sv"
    status ok
    summary "Standards-Version=$sv"
    hint "agent: compare $sv against the current Debian Policy version and recommend a bump if behind"
}

step_control_libs_section() {
    [ -f debian/control ] || {
        status fail
        summary "debian/control missing"
        return 1
    }
    bad=$(awk '
        function check() {
            if (pkg ~ /^lib/ && pkg !~ /-dev$/ && pkg !~ /-doc$/ \
                && sect != "libs" && sect != "oldlibs" && sect != "")
                print pkg ": Section=" sect " (expected libs)"
        }
        /^Package:/ { check(); pkg=$2; sect="" }
        /^Section:/ { sect=$2 }
        END         { check() }
    ' debian/control)
    if [ -n "$bad" ]; then
        echo "$bad"
        status warn
        summary "library binary package(s) with non-libs Section"
    else
        status ok
        summary "library packages use Section: libs"
    fi
}

step_upstream_metadata() {
    f=debian/upstream/metadata
    if [ ! -f "$f" ]; then
        status warn
        summary "$f missing — add upstream metadata (Bug-Database, Repository, etc.)"
        return 0
    fi
    if have python3; then
        if out=$(python3 -c "import sys, yaml; yaml.safe_load(open('$f'))" 2>&1); then
            cat "$f"
            status ok
            summary "$f parses as YAML"
        else
            printf '%s\n' "$out"
            status fail
            summary "$f is not valid YAML"
        fi
    else
        cat "$f"
        status ok
        summary "$f present (YAML validation skipped — no python3)"
    fi
}

step_symbols() {
    sos=$(grep -hE '\.so(\.|$)' debian/*.install 2>/dev/null | awk '{print $1}' | sort -u || true)
    if [ -z "$sos" ]; then
        status skipped
        summary "no shared libraries shipped via debian/*.install"
        return 0
    fi
    syms=$(ls -1 -- debian/*.symbols debian/*.symbols.* 2>/dev/null || true)
    echo "shared libraries shipped:"
    echo "$sos"
    echo
    echo "symbols files present:"
    if [ -n "$syms" ]; then echo "$syms"; else echo "(none)"; fi
    if [ -z "$syms" ]; then
        status warn
        summary "shared libraries shipped but no debian/*.symbols file"
        hint "agent: generate a .symbols file (dpkg-gensymbols / pkgkde-symbolshelper); C++ symbols use the (c++) tag format"
    else
        status ok
        summary "symbols files present"
    fi
}

step_symbols_check_level() {
    [ -f debian/rules ] || {
        status skipped
        summary "no debian/rules"
        return 0
    }
    if grep -qE '^[[:space:]]*export[[:space:]]+DPKG_GENSYMBOLS_CHECK_LEVEL[[:space:]]*=[[:space:]]*4' debian/rules ||
        grep -qE 'DPKG_GENSYMBOLS_CHECK_LEVEL[[:space:]]*[:?]?=[[:space:]]*4' debian/rules; then
        status ok
        summary "DPKG_GENSYMBOLS_CHECK_LEVEL=4 set in debian/rules"
    else
        status warn
        summary "DPKG_GENSYMBOLS_CHECK_LEVEL=4 not set — strict symbols checking disabled"
    fi
}

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

step_hardening_flags() {
    [ -f debian/rules ] || {
        status skipped
        summary "no debian/rules"
        return 0
    }
    if grep -qE 'DEB_BUILD_MAINT_OPTIONS.*hardening=\+all' debian/rules; then
        status ok
        summary "hardening=+all enabled explicitly"
        return 0
    fi
    if grep -qE '^[[:space:]]*dh[[:space:]]+\$@' debian/rules; then
        compat=
        [ -f debian/compat ] && compat=$(cat debian/compat 2>/dev/null)
        [ -z "$compat" ] && compat=$(awk -F'[()]' '/debhelper-compat/{print $2; exit}' debian/control 2>/dev/null)
        if [ -n "$compat" ] && [ "$compat" -ge 9 ] 2>/dev/null; then
            status ok
            summary "dh sequencer with compat $compat enables hardening defaults"
        else
            status warn
            summary "dh sequencer used but compat $compat — set DEB_BUILD_MAINT_OPTIONS=hardening=+all explicitly"
        fi
    else
        status warn
        summary "no DEB_BUILD_MAINT_OPTIONS hardening=+all and no dh sequencer — confirm hardening flags"
    fi
}

step_autopkgtest_present() {
    if [ -f debian/tests/control ]; then
        cat debian/tests/control
        lines=$(wc -l <debian/tests/control)
        status ok
        summary "debian/tests/control exists ($lines lines)"
        hint "agent: read debian/tests/control and the test scripts under debian/tests/ — verify the tests make sense, exercise representative functionality, and are correctly wired (Tests:, Test-Command:, Depends:, Restrictions:)"
    else
        status warn
        summary "no debian/tests/control — autopkgtest missing (mandatory for new packages)"
        hint "agent: propose a minimal debian/tests/control exercising the package's main entry points"
    fi
}

step_news_debian() {
    if [ -f debian/NEWS ]; then
        status ok
        summary "debian/NEWS present"
    else
        status skipped
        summary "no debian/NEWS (only needed for significant user-visible changes)"
    fi
}

step_signed_tags() {
    [ -f debian/watch ] || {
        status skipped
        summary "no debian/watch"
        return 0
    }
    if grep -qE 'pgpsigurlmangle|pgpmode' debian/watch; then
        status ok
        summary "debian/watch enforces upstream signature verification"
    else
        status warn
        summary "debian/watch does not verify upstream signatures (pgpsigurlmangle/pgpmode)"
        hint "agent: if upstream signs release tags or tarballs, add pgpmode=auto or pgpsigurlmangle to debian/watch"
    fi
}

step_dh_python() {
    is_python=0
    grep -qiE 'python' debian/control 2>/dev/null && is_python=1
    [ -f setup.py ] && is_python=1
    [ -f pyproject.toml ] && is_python=1
    ls debian/*.pybuild 2>/dev/null >/dev/null && is_python=1
    if [ "$is_python" -eq 0 ]; then
        status skipped
        summary "not a Python package"
        return 0
    fi
    if grep -qE 'dh[[:space:]]+\$@.*--with[[:space:]]+[^[:space:]]*python' debian/rules 2>/dev/null ||
        grep -qE 'dh-python' debian/control debian/rules 2>/dev/null; then
        status ok
        summary "dh-python is used"
    else
        status warn
        summary "Python package without dh-python in debian/rules"
        hint "agent: recommend dh-python (dh \$@ --with python3) for Python packaging"
    fi
}

# ── runner ────────────────────────────────────────────────────────────

# fail fast if any enabled step has no matching function — catches drift
# between the ENABLED_STEPS list and the step_*() definitions above.
missing=
for s in $ENABLED_STEPS; do
    type "step_$s" >/dev/null 2>&1 || missing="$missing $s"
done
if [ -n "$missing" ]; then
    emit_error "review.sh: enabled steps with no step_<name>() function:$missing"
    exit 2
fi

# subtract REVIEW_DISABLED_STEPS (comma-separated) from the enabled list
disabled=" $(printf '%s' "${REVIEW_DISABLED_STEPS:-}" | tr ',' ' ') "
final=
for s in $ENABLED_STEPS; do
    case "$disabled" in
    *" $s "*) ;;
    *) final="$final $s" ;;
    esac
done

for s in $final; do
    run_step "$s"
done

# the runner completed; per-step pass/fail is conveyed in the step markers.
_RESULT=ok
