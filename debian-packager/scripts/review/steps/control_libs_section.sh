# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
