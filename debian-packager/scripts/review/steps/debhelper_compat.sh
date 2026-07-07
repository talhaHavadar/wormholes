# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)

# _extract_int <key> — read stdin, print the integer value of "key": N in
# dh_assistant's JSON. Used by step_debhelper_compat below.
_extract_int() {
	awk -v k="\"$1\"" '
		{
			re = k "[[:space:]]*:[[:space:]]*[0-9]+"
			if (match($0, re)) {
				m = substr($0, RSTART, RLENGTH)
				gsub(/[^0-9]/, "", m)
				print m
				exit
			}
		}
	'
}

step_debhelper_compat() {
	have dh_assistant || {
		status skipped
		summary "dh_assistant not on builder PATH"
		return 0
	}

	# `dh_assistant supported-compat-levels` prints JSON. No jq on the builder,
	# and json_pp has no query syntax, so we use awk: match `"KEY": <int>` as a
	# single region, then strip non-digits from just that region. Works whether
	# dh_assistant pretty-prints or emits compact one-liner JSON, and cleanly
	# ignores keys whose value is `null`.
	supported=$(dh_assistant supported-compat-levels 2>/dev/null) || {
		status skipped
		summary "dh_assistant supported-compat-levels failed on the builder"
		return 0
	}
	highest=$(printf '%s\n' "$supported" | _extract_int HIGHEST_STABLE_COMPAT_LEVEL)
	lowest_ok=$(printf '%s\n' "$supported" | _extract_int LOWEST_NON_DEPRECATED_COMPAT_LEVEL)
	if [ -z "$highest" ]; then
		status fail
		summary "cannot parse HIGHEST_STABLE_COMPAT_LEVEL from dh_assistant"
		return 1
	fi

	# `dh_assistant active-compat-level` reads debian/control and debian/compat,
	# so it must run from the source tree (framework.sh's run_step pins cwd).
	# The regex in _extract_int requires the colon-adjacent value to be an
	# integer, which naturally rules out the sibling `-source` key (whose value
	# is a string) and any `null` entry.
	active_json=$(dh_assistant active-compat-level 2>/dev/null) || active_json=
	active=$(printf '%s\n' "$active_json" | _extract_int declared-compat-level)
	if [ -z "$active" ] || [ "$active" = 0 ]; then
		active=$(printf '%s\n' "$active_json" | _extract_int effective-compat-level)
	fi
	# Fallbacks for older debhelpers whose dh_assistant lacks active-compat-level,
	# or packages that declare compat only in debian/compat.
	if [ -z "$active" ] && [ -f debian/compat ]; then
		active=$(tr -d ' \t\n' <debian/compat 2>/dev/null)
	fi
	if [ -z "$active" ] && [ -f debian/control ]; then
		active=$(awk -F'[()[:space:]=]+' '/debhelper-compat/{
			for (i=1;i<=NF;i++) if ($i ~ /^[0-9]+$/) { print $i; exit }
		}' debian/control)
	fi
	if [ -z "$active" ]; then
		status skipped
		summary "no debhelper-compat declared (dh_assistant, debian/compat, debian/control)"
		return 0
	fi

	echo "active compat level:                $active"
	echo "highest stable compat level:        $highest"
	[ -n "$lowest_ok" ] && echo "lowest non-deprecated compat level:  $lowest_ok"

	# Deprecated compat is the strictest case — worth a hard fail so it lands
	# in the report's failed_steps digest.
	if [ -n "$lowest_ok" ] && [ "$active" -lt "$lowest_ok" ] 2>/dev/null; then
		status fail
		summary "compat $active is deprecated (lowest non-deprecated: $lowest_ok, highest stable: $highest)"
		hint "agent: run 'dh_assistant compat-upgrade-checklist --target-compat $highest' inside the source tree for a filtered per-item upgrade checklist"
		return 1
	fi

	if [ "$active" -eq "$highest" ] 2>/dev/null; then
		status ok
		summary "compat $active matches highest stable level"
		return 0
	fi

	if [ "$active" -gt "$highest" ] 2>/dev/null; then
		status warn
		summary "compat $active is above highest stable ($highest) — experimental level"
		hint "agent: confirm the experimental compat level is intentional; behaviour may still change"
		return 0
	fi

	# active < highest — the common upgrade opportunity.
	status warn
	summary "compat $active is behind highest stable ($highest)"
	hint "agent: run 'dh_assistant compat-upgrade-checklist --target-compat $highest' inside the source tree for a filtered per-item upgrade checklist"
	return 0
}
