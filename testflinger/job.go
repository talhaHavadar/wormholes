package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// prepareJob builds the Testflinger job to submit. The optional base YAML (a
// job file) is the fallback; the config's direct fields are overlaid on top
// with priority. It also ensures a reserve_data block with ssh keys and a
// timeout, then validates the minimum a reserve job needs.
func prepareJob(base []byte, cfg config) ([]byte, error) {
	var doc map[string]any
	if len(base) > 0 {
		if err := yaml.Unmarshal(base, &doc); err != nil {
			return nil, fmt.Errorf("parsing job yaml: %w", err)
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}

	// job_queue: the config value overrides the job file's.
	if cfg.JobQueue != "" {
		doc["job_queue"] = cfg.JobQueue
	}
	if _, ok := doc["job_queue"]; !ok {
		return nil, fmt.Errorf("job_queue is required (set job_queue in config or the job file)")
	}

	// provision_data: merge the config block over the job file's, key by key,
	// so config wins while job-file-only keys survive as fallback.
	if len(cfg.ProvisionData) > 0 {
		pd, _ := doc["provision_data"].(map[string]any)
		if pd == nil {
			pd = map[string]any{}
		}
		for k, v := range cfg.ProvisionData {
			pd[k] = v
		}
		doc["provision_data"] = pd
	}

	rd, _ := doc["reserve_data"].(map[string]any)
	if rd == nil {
		rd = map[string]any{}
	}
	if len(cfg.SSHKeys) > 0 {
		keys := make([]any, len(cfg.SSHKeys))
		for i, k := range cfg.SSHKeys {
			keys[i] = k
		}
		rd["ssh_keys"] = keys
	}
	if _, ok := rd["ssh_keys"]; !ok {
		return nil, fmt.Errorf("reserve_data.ssh_keys is required (set ssh_keys in config or the job file)")
	}
	if cfg.ReserveTimeoutSecs > 0 {
		rd["timeout"] = cfg.ReserveTimeoutSecs
	}
	doc["reserve_data"] = rd

	return yaml.Marshal(doc)
}

var jobIDRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// parseJobID extracts the job UUID from `testflinger submit` output (which
// prints e.g. "job_id: <uuid>").
func parseJobID(out string) (string, error) {
	if m := jobIDRE.FindString(out); m != "" {
		return m, nil
	}
	return "", fmt.Errorf("no job id (uuid) in submit output: %s", strings.TrimSpace(out))
}

type sshTarget struct {
	User string
	Host string
}

// sshLineRE matches an "ssh [opts] user@host" command line; bareRE matches a
// bare user@ip token. Both capture (user, host).
var (
	sshLineRE = regexp.MustCompile(`ssh\s+(?:-\S+\s+|\S+=\S+\s+)*([A-Za-z][\w.-]*)@([A-Za-z0-9][\w.-]*)`)
	bareRE    = regexp.MustCompile(`\b([a-z][\w.-]*)@((?:\d{1,3}\.){3}\d{1,3})\b`)
)

// parseSSHInfo finds the reserved machine's SSH target in poll output. A custom
// regex (capturing user, host) overrides the defaults; otherwise an explicit
// ssh command line is preferred, then a bare user@ip. The last match wins, so
// later output (the actual reservation line) is taken over earlier echoes.
func parseSSHInfo(out string, custom *regexp.Regexp) (sshTarget, bool) {
	if custom != nil {
		if m := lastSubmatch(custom, out); m != nil && len(m) >= 3 {
			return sshTarget{User: m[1], Host: m[2]}, true
		}
		return sshTarget{}, false
	}
	if m := lastSubmatch(sshLineRE, out); m != nil {
		return sshTarget{User: m[1], Host: m[2]}, true
	}
	if m := lastSubmatch(bareRE, out); m != nil {
		return sshTarget{User: m[1], Host: m[2]}, true
	}
	return sshTarget{}, false
}

func lastSubmatch(re *regexp.Regexp, s string) []string {
	all := re.FindAllStringSubmatch(s, -1)
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

// remoteShellCommand renders a single shell command string to run argv on the
// reserved machine via ssh, restoring the working directory and environment
// (ssh carries neither). Everything is shell-quoted.
func remoteShellCommand(dir string, env map[string]string, argv []string) string {
	var parts []string
	for _, k := range sortedKeys(env) {
		parts = append(parts, k+"="+shellQuote(env[k]))
	}
	for _, a := range argv {
		parts = append(parts, shellQuote(a))
	}
	cmd := strings.Join(parts, " ")
	if dir != "" {
		cmd = "cd " + shellQuote(dir) + " && " + cmd
	}
	return cmd
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// shellQuote single-quotes s for safe inclusion in a shell command string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
