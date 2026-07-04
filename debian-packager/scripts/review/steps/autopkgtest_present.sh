# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
