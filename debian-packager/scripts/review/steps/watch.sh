# shellcheck shell=sh   # fragment: assembled and run via `sh -c` (see source.go)
step_watch() {
	# Snapshot-based packages: uscan can't track a monorepo-subdir source.
	# Regenerating the orig with `snapshot orig` IS the upstream health
	# check — success proves upstream is reachable and the orig is
	# reproducible; failure means tracking is broken. uscan's "is there a
	# newer release" question does not apply.
	if is_snapshot_package; then
		have snapshot || {
			status fail
			summary "snapshot-based package detected but 'snapshot' tool not installed on builder"
			return 1
		}
		if [ -f debian/snapshot.conf ]; then
			echo "=== debian/snapshot.conf ==="
			cat debian/snapshot.conf
			echo
		else
			echo "=== snapshot detected via $([ -n "${UPSTREAM_URL:-}" ] && echo 'UPSTREAM_URL env' || echo 'changelog version pattern (~git…)') ==="
			echo
		fi
		echo "=== snapshot orig ==="
		if snapshot orig; then
			status ok
			summary "snapshot orig succeeded — upstream is reachable and orig is reproducible"
			hint "agent: snapshot-based packages have no 'newer release' concept; the maintainer rolls new snapshots manually with 'snapshot create -u <ver>'"
			return 0
		fi
		status fail
		summary "snapshot orig failed — upstream tracking is broken"
		return 1
	fi
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
	rc=0
	uscan --watchfile debian/watch --verbose --report --no-download || rc=$?
	if [ "$rc" -ne 0 ]; then
		status fail
		summary "uscan --report failed (exit $rc) — watch file broken or upstream unreachable (see output above)"
		return "$rc"
	fi
	status ok
	summary "uscan watch report generated (see output above)"
}
