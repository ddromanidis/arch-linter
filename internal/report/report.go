// Package report turns diagnostics into the output formats arch.config.yaml offers.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one violation, flattened out of the analysis framework so the reporters do
// not each have to know about token.FileSet.
type Finding struct {
	Rule      string `json:"rule"` // "imports" or "exports"
	Component string `json:"component"`
	// Target is the import path at fault — the stable key a baseline is built on.
	Target   string `json:"target"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// Sort orders findings the way a person reads them: by file, then position. Deterministic
// output matters more than it sounds — without it, two runs on unchanged code produce
// different diffs, and nobody can tell a real change from map iteration order.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return fs[i].Column < fs[j].Column
	})
}

// Write emits findings in the named format.
func Write(w io.Writer, format string, findings []Finding, root string, colour bool) error {
	Sort(findings)
	switch format {
	case "text":
		return writeText(w, findings, root, colour)
	case "json":
		return writeJSON(w, findings, root)
	case "github":
		return writeGitHub(w, findings, root)
	case "sarif":
		return writeSARIF(w, findings, root)
	}
	return fmt.Errorf("unknown format %q", format)
}

const (
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
	dim    = "\x1b[2m"
	bold   = "\x1b[1m"
	reset  = "\x1b[0m"
)

func writeText(w io.Writer, findings []Finding, root string, colour bool) error {
	paint := func(code, s string) string {
		if !colour {
			return s
		}
		return code + s + reset
	}

	var errors, warnings int
	for _, f := range findings {
		switch f.Severity {
		case "warning":
			warnings++
		default:
			errors++
		}
		tag := paint(red, "error")
		if f.Severity == "warning" {
			tag = paint(yellow, "warning")
		}
		loc := fmt.Sprintf("%s:%d:%d", rel(root, f.File), f.Line, f.Column)
		fmt.Fprintf(w, "%s  %s  %s  %s\n",
			paint(bold, loc), tag, f.Message, paint(dim, "["+f.Rule+"]"))
	}

	if len(findings) == 0 {
		fmt.Fprintln(w, paint(dim, "no violations"))
		return nil
	}
	fmt.Fprintf(w, "\n%d error(s), %d warning(s)\n", errors, warnings)
	return nil
}

func writeJSON(w io.Writer, findings []Finding, root string) error {
	// Relative paths, like every other format. A machine-readable report full of one
	// developer's home directory is not portable between the machine that produced it and
	// the one reading it.
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		f.File = rel(root, f.File)
		out = append(out, f)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Never encode nil as `null`: a consumer scripting against this should get an empty
	// list on a clean run, not a value it has to special-case.
	return enc.Encode(out)
}

// writeGitHub emits workflow commands, which make violations appear inline on the diff of
// a pull request rather than only in the job log nobody opens.
func writeGitHub(w io.Writer, findings []Finding, root string) error {
	for _, f := range findings {
		level := "error"
		if f.Severity == "warning" {
			level = "warning"
		}
		fmt.Fprintf(w, "::%s file=%s,line=%d,col=%d::%s\n",
			level, rel(root, f.File), f.Line, f.Column, escapeGitHub(f.Message))
	}
	return nil
}

func escapeGitHub(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	return r.Replace(s)
}

func writeSARIF(w io.Writer, findings []Finding, root string) error {
	type region struct {
		StartLine   int `json:"startLine"`
		StartColumn int `json:"startColumn"`
	}
	type artifact struct {
		URI string `json:"uri"`
	}
	type location struct {
		PhysicalLocation struct {
			ArtifactLocation artifact `json:"artifactLocation"`
			Region           region   `json:"region"`
		} `json:"physicalLocation"`
	}
	// Every field is tagged, including the nested one. An untagged `Text string` marshals
	// as "Text", and SARIF requires "text" — GitHub code scanning then ingests the
	// document with no message on any result, reports nothing, and goes green. Go's
	// unmarshaller is case-insensitive, so a round-trip test does not catch it either;
	// only asserting on the raw bytes does.
	type message struct {
		Text string `json:"text"`
	}
	type result struct {
		RuleID    string     `json:"ruleId"`
		Level     string     `json:"level"`
		Message   message    `json:"message"`
		Locations []location `json:"locations"`
	}

	results := make([]result, 0, len(findings))
	for _, f := range findings {
		var r result
		r.RuleID = "archlint/" + f.Rule
		r.Level = f.Severity
		if r.Level == "" {
			r.Level = "error"
		}
		r.Message.Text = f.Message
		var loc location
		loc.PhysicalLocation.ArtifactLocation.URI = rel(root, f.File)
		loc.PhysicalLocation.Region = region{StartLine: f.Line, StartColumn: f.Column}
		r.Locations = []location{loc}
		results = append(results, r)
	}

	doc := map[string]any{
		"version": "2.1.0",
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"runs": []any{map[string]any{
			"tool": map[string]any{"driver": map[string]any{
				"name":           "archlint",
				"informationUri": "https://github.com/ddromanidis/arch-linter",
			}},
			"results": results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// rel shortens absolute paths against the project root, because a wall of
// /Users/you/src/... prefixes is noise in every one of these formats.
//
// Always forward slashes, including on Windows. Two of these formats require it: a SARIF
// artifactLocation.uri is a URI, and a GitHub workflow command's file= is matched against
// paths git reports with forward slashes — a backslash in either is silently not a path,
// so the annotation simply never appears. Emitting the same separator everywhere also
// means output does not change shape by platform, which is what a golden test and a
// diffed CI log both depend on.
func rel(root, file string) string {
	if root == "" {
		return filepath.ToSlash(file)
	}
	if r, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(file)
}
