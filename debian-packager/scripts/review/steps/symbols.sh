# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
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
