package main

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

// combine joins stdout and stderr for parsing; both can carry build output.
func combine(res *wormhole.ExecResult) string {
	if res == nil {
		return ""
	}
	return string(res.Stdout) + "\n" + string(res.Stderr)
}

// artifactRE matches Debian artifact paths/filenames in build output. The
// character class keeps `/` so relative paths (e.g. ../build-area/foo.deb)
// survive, but stops at quotes and whitespace.
var artifactRE = regexp.MustCompile(`[A-Za-z0-9._~+/-]+\.(?:changes|buildinfo|dsc|deb|ddeb|udeb)\b`)

// findArtifacts collects unique Debian artifact references from build output.
func findArtifacts(out string) []string {
	seen := map[string]bool{}
	var res []string
	for _, m := range artifactRE.FindAllString(out, -1) {
		if !seen[m] {
			seen[m] = true
			res = append(res, m)
		}
	}
	sort.Strings(res)
	return res
}

func pickExt(artifacts []string, ext string) string {
	for _, a := range artifacts {
		if strings.HasSuffix(a, ext) {
			return a
		}
	}
	return ""
}

type lintianSummary struct {
	Errors       int      `json:"errors"`
	Warnings     int      `json:"warnings"`
	Info         int      `json:"info"`
	Pedantic     int      `json:"pedantic"`
	Experimental int      `json:"experimental,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

// lintianLineRE matches a lintian tag line, e.g. "W: pkg: tag-name extra".
var lintianLineRE = regexp.MustCompile(`(?m)^([EWIPX]):\s+\S+:\s+(\S+)`)

// parseLintian summarizes lintian tag lines from output. Returns nil when none
// are present (e.g. a clean package or non-lintian output).
func parseLintian(out string) *lintianSummary {
	matches := lintianLineRE.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return nil
	}
	s := &lintianSummary{}
	for _, m := range matches {
		switch m[1] {
		case "E":
			s.Errors++
		case "W":
			s.Warnings++
		case "I":
			s.Info++
		case "P":
			s.Pedantic++
		case "X":
			s.Experimental++
		}
		s.Tags = append(s.Tags, m[1]+": "+m[2])
	}
	return s
}

type watchResult struct {
	Package         string `json:"package,omitempty"`
	CurrentVersion  string `json:"current_version,omitempty"`
	UpstreamVersion string `json:"upstream_version,omitempty"`
	NewerAvailable  bool   `json:"newer_available"`
	Status          string `json:"status,omitempty"`
	UpstreamURL     string `json:"upstream_url,omitempty"`
	Warnings        string `json:"warnings,omitempty"`
	Errors          string `json:"errors,omitempty"`
}

type dehs struct {
	Package         string `xml:"package"`
	DebianUversion  string `xml:"debian-uversion"`
	UpstreamVersion string `xml:"upstream-version"`
	UpstreamURL     string `xml:"upstream-url"`
	Status          string `xml:"status"`
	Warnings        string `xml:"warnings"`
	Errors          string `xml:"errors"`
}

// parseWatch extracts the DEHS report uscan --dehs prints (it may be embedded
// among other log lines).
func parseWatch(out string) (watchResult, error) {
	start := strings.Index(out, "<dehs>")
	end := strings.Index(out, "</dehs>")
	if start < 0 || end < 0 {
		return watchResult{}, fmt.Errorf("no DEHS output from uscan: %s", strings.TrimSpace(tail(out, 20)))
	}
	var d dehs
	if err := xml.Unmarshal([]byte(out[start:end+len("</dehs>")]), &d); err != nil {
		return watchResult{}, fmt.Errorf("parsing uscan DEHS: %w", err)
	}
	return watchResult{
		Package:         d.Package,
		CurrentVersion:  d.DebianUversion,
		UpstreamVersion: d.UpstreamVersion,
		UpstreamURL:     d.UpstreamURL,
		Status:          strings.TrimSpace(d.Status),
		Warnings:        strings.TrimSpace(d.Warnings),
		Errors:          strings.TrimSpace(d.Errors),
		NewerAvailable:  d.UpstreamVersion != "" && d.UpstreamVersion != d.DebianUversion,
	}, nil
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
