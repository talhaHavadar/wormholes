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
# orig fetch fails only the step that needed it.
fetch_orig_quiet() {
    { [ -f debian/source/format ] && grep -q '(native)' debian/source/format; } && return 0
    uscan --download-current-version >/dev/null 2>&1
}

# run_step <name> — wrap a step function call in begin/end markers.
run_step() {
    name=$1
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
    fetch_orig_quiet || {
        status fail
        summary "orig tarball fetch failed (uscan --download-current-version)"
        return 1
    }
    if have debuild; then
        debuild --no-conf -S -d 1>&2 || {
            status fail
            summary "debuild -S -d failed"
            return 1
        }
    else
        dpkg-source -b . 1>&2 || {
            status fail
            summary "dpkg-source -b failed"
            return 1
        }
    fi
    cd ..
    dsc=$(ls -1 -- *.dsc 2>/dev/null | head -n1)
    [ -n "$dsc" ] || {
        status fail
        summary "no .dsc produced"
        return 1
    }
    rc=0
    lintian -EviL +pedantic "$dsc" || rc=$?
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
    lintian -EvIL +pedantic $debs || rc=$?
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
