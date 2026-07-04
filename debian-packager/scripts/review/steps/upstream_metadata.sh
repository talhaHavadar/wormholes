# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
