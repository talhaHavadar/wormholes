# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
