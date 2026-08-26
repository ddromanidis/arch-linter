package analyzer

import (
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// The directive that silences one diagnostic:
//
//	//arch-lint:ignore exports the migration tool genuinely needs the handle
//
// Written without a space after the slashes, following //go: and //nolint:, though a space
// is tolerated because people type it.
const directive = "arch-lint:ignore"

// waiver is one such comment.
type waiver struct {
	rule   string
	reason string
	pos    token.Pos
	used   bool
}

// waivers indexes directives by file and line.
//
// Keyed on position rather than on what they suppress because that is how they are written
// and read: a waiver sits next to the thing it excuses, and its scope is that line. A
// waiver that could be written anywhere and match by content would drift away from its
// subject and outlive it.
type waivers struct {
	byLine map[string]map[int]*waiver
	all    []*waiver
	// malformed are directives with no reason. Collected rather than ignored: an
	// unexplained waiver is the beginning of a rule nobody remembers agreeing to.
	malformed []*waiver
}

func collectWaivers(pass *analysis.Pass) *waivers {
	w := &waivers{byLine: map[string]map[int]*waiver{}}
	for _, f := range pass.Files {
		for _, group := range f.Comments {
			for _, c := range group.List {
				text, ok := parseDirective(c.Text)
				if !ok {
					continue
				}
				pos := pass.Fset.Position(c.Pos())
				rule, reason := splitRule(text)
				item := &waiver{rule: rule, reason: reason, pos: c.Pos()}
				if reason == "" || rule == "" {
					w.malformed = append(w.malformed, item)
					continue
				}
				if w.byLine[pos.Filename] == nil {
					w.byLine[pos.Filename] = map[int]*waiver{}
				}
				w.byLine[pos.Filename][pos.Line] = item
				w.all = append(w.all, item)
			}
		}
	}
	return w
}

// parseDirective returns the text after the directive.
func parseDirective(comment string) (string, bool) {
	body := strings.TrimPrefix(comment, "//")
	if body == comment {
		// A /* */ comment. Directives are line comments by convention, and accepting block
		// comments would mean deciding what a directive spanning six lines applies to.
		return "", false
	}
	body = strings.TrimLeft(body, " \t")
	rest, ok := strings.CutPrefix(body, directive)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func splitRule(text string) (rule, reason string) {
	rule, reason, _ = strings.Cut(text, " ")
	return strings.TrimSpace(rule), strings.TrimSpace(reason)
}

// allow reports whether a diagnostic is waived, and marks the waiver used.
//
// A directive counts on the diagnostic's own line — for an import, where it reads as a
// trailing comment — or on the line directly above it, which is where a doc comment sits
// and therefore where a waiver for a multi-line declaration has to go.
func (w *waivers) allow(pass *analysis.Pass, pos token.Pos, rule string) bool {
	p := pass.Fset.Position(pos)
	lines, ok := w.byLine[p.Filename]
	if !ok {
		return false
	}
	for _, line := range []int{p.Line, p.Line - 1} {
		if item, ok := lines[line]; ok && item.rule == rule {
			item.used = true
			return true
		}
	}
	return false
}

// checker carries what both rules need, so neither has to take five arguments.
type checker struct {
	pass      *analysis.Pass
	rules     *Rules
	component string
	waivers   *waivers
	// target is set immediately before each report call, naming the package at fault.
	target string
	found  []Violation
}

// report emits a diagnostic unless a waiver excuses it.
//
// Diagnostics are how go vet and golangci-lint hear about a violation; they carry only a
// message, so anything that wants structure has to reconstruct it by parsing English. The
// same violation is therefore also recorded here and handed back as the analyzer's result,
// which is what the CLI reads. A baseline keyed on parsed message text would break the
// first time a message was reworded.
func (c *checker) report(d analysis.Diagnostic) {
	if c.waivers.allow(c.pass, d.Pos, d.Category) {
		return
	}
	c.pass.Report(d)
	c.found = append(c.found, Violation{
		Rule:      d.Category,
		Component: c.component,
		Target:    c.target,
		Message:   d.Message,
		Pos:       d.Pos,
	})
	c.target = ""
}

// reportWaiverProblems flags directives that are malformed or that excused nothing.
//
// Both are rot. An unexplained waiver is a rule nobody remembers agreeing to, and one that
// suppresses nothing is a lie about the code that will be believed by the next person to
// read it — usually left behind by the very fix that made it unnecessary.
func (c *checker) reportWaiverProblems() {
	if c.rules.Config.Severity.Waivers == "off" {
		return
	}
	for _, item := range c.waivers.malformed {
		c.pass.Report(analysis.Diagnostic{
			Category: RuleWaivers,
			Pos:      item.pos,
			Message: "//" + directive + " needs a rule and a reason, as in " +
				"`//" + directive + " exports the migration tool needs the handle`",
		})
	}
	for _, item := range c.waivers.all {
		if item.used {
			continue
		}
		c.pass.Report(analysis.Diagnostic{
			Category: RuleWaivers,
			Pos:      item.pos,
			Message:  "this " + item.rule + " waiver suppressed nothing and can go",
		})
	}
}
