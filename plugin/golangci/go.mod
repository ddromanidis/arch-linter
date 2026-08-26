module github.com/ddromanidis/arch-linter/plugin/golangci

go 1.25.0

require (
	github.com/ddromanidis/arch-linter v0.0.0
	github.com/golangci/plugin-module-register v0.1.2
	golang.org/x/tools v0.49.0
)

require gopkg.in/yaml.v3 v3.0.1 // indirect

// The plugin ships alongside the linter it wraps, so it always builds against this
// checkout rather than a published version that may be older than the analyzer it needs.
replace github.com/ddromanidis/arch-linter => ../..
