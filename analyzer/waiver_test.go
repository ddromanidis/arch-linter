package analyzer

import "testing"

// A directive with no reason is not a waiver. Asserted here rather than in a testdata
// fixture because analysistest matches expectations by line, and a `// want` comment on the
// directive's own line would supply the very reason whose absence is under test.
func TestDirectiveParsing(t *testing.T) {
	for _, tc := range []struct {
		comment string
		ok      bool
		rule    string
		reason  string
	}{
		{"//arch-lint:ignore exports the installer needs the handle", true, "exports", "the installer needs the handle"},
		{"// arch-lint:ignore imports legacy", true, "imports", "legacy"}, // a space is tolerated
		{"//arch-lint:ignore exports", true, "exports", ""},               // parses, but no reason
		{"//arch-lint:ignore", true, "", ""},
		{"// just a comment", false, "", ""},
		{"//nolint:all", false, "", ""},
		{"/*arch-lint:ignore exports x*/", false, "", ""}, // block comments are not directives
	} {
		text, ok := parseDirective(tc.comment)
		if ok != tc.ok {
			t.Errorf("parseDirective(%q) ok = %v, want %v", tc.comment, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		rule, reason := splitRule(text)
		if rule != tc.rule || reason != tc.reason {
			t.Errorf("parseDirective(%q) = (%q, %q), want (%q, %q)",
				tc.comment, rule, reason, tc.rule, tc.reason)
		}
	}
}
