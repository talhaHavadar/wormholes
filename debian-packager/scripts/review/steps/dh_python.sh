# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
