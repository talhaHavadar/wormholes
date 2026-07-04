# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
