# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
