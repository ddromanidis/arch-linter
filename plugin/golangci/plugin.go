// Package golangci exposes arch-lint as a golangci-lint module plugin.
//
// A nested module, deliberately. golangci-lint's plugin registry drags in golangci-lint's
// own dependency graph, and anybody importing github.com/ddromanidis/arch-linter/analyzer
// to embed the linter in something else should not inherit that. Keeping it out here means
// the main module depends on nothing but x/tools and a YAML parser.
//
// Note the adoption cost, which is real and should not be glossed over: module plugins
// require users to build a custom golangci-lint binary via .custom-gcl.yml. The older .so
// plugin path avoided that and is effectively dead. For anyone unwilling to maintain a
// custom build, `arch-lint` as a standalone step in CI is the simpler answer and loses
// nothing but a shared invocation.
package golangci

import (
	"github.com/ddromanidis/arch-linter/analyzer"
	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"
)

func init() {
	register.Plugin("archlint", New)
}

// Settings is the block under linters-settings.custom.archlint.settings in
// .golangci.yml. Both are optional: with neither, the analyzer walks up from each package
// to find arch.yaml, which is what a single-module repository wants.
type Settings struct {
	// Arch is the path to arch.yaml.
	Arch string `json:"arch"`
	// Config is the path to arch.config.yaml.
	Config string `json:"config"`
}

type plugin struct {
	settings Settings
}

// New builds the plugin from its settings block.
func New(raw any) (register.LinterPlugin, error) {
	s, err := register.DecodeSettings[Settings](raw)
	if err != nil {
		return nil, err
	}
	return &plugin{settings: s}, nil
}

func (p *plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	// An explicitly configured architecture is loaded once, here, so a bad path is an
	// error at startup rather than the same error repeated for every package.
	if p.settings.Arch != "" {
		rules, err := analyzer.Load(p.settings.Arch, p.settings.Config)
		if err != nil {
			return nil, err
		}
		return []*analysis.Analyzer{analyzer.New(rules)}, nil
	}
	return []*analysis.Analyzer{analyzer.Analyzer}, nil
}

// GetLoadMode asks for full type information, which the export rule cannot do without:
// deciding whether a signature leaks a package means resolving what its types actually
// are, and syntax alone cannot answer that.
func (p *plugin) GetLoadMode() string { return register.LoadModeTypesInfo }
