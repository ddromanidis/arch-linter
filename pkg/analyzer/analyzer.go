package analyzer

import (
	"context"
	"go/token"

	"github.com/ddromanidis/arch-linter/internal/linter"
	"github.com/ddromanidis/arch-linter/internal/pipeline"
	"golang.org/x/tools/go/analysis"
)

var ConfigPath string

// Analyzer exports the logic to golangci-lint
var Analyzer = &analysis.Analyzer{
	Name: "archlinter",
	Doc:  "enforces architectural rules defined in arch.yaml",
	Run:  run,
}

func init() {
	// Register flags for the linter
	Analyzer.Flags.StringVar(&ConfigPath, "config", "arch.yaml", "path to architecture config file")
}

func run(pass *analysis.Pass) (interface{}, error) {
	// 1. Define the Adapter Step
	// This replaces the "ParseFiles" step since golangci-lint already parsed the code.
	ingestStep := func(ctx context.Context, s linter.State) (linter.State, error) {
		// Load Config
		cfg, err := linter.ParseConfig(ConfigPath)
		if err != nil {
			return s, err
		}

		// Auto-detect Module Name from go.mod if not set
		if cfg.RootMod == "" {
			// We try to read go.mod from the root of the analysis execution
			// In golangci-lint, this usually runs from project root.
			if modName, _ := linter.ReadGoMod("."); modName != "" {
				cfg.RootMod = modName
			}
		}

		var pkgs []linter.Package

		// Iterate over files provided by golangci-lint
		for _, file := range pass.Files {
			// Get absolute path of the file
			fullPath := pass.Fset.Position(file.Pos()).Filename

			// Resolve Module Name
			// Note: We might need to make path relative if your config uses relative paths
			// but ResolveModuleName handles absolute paths if Config logic supports it.
			// Ideally, we pass the relative path here if possible.
			modName := cfg.ResolveModuleName(fullPath)

			// Only process files that belong to a defined module
			if modName != "" {
				pkgs = append(pkgs, linter.Package{
					FilePath:   fullPath,
					ModuleName: modName,
					File:       file, // Use the AST provided by the runner
					Imports:    linter.ExtractImports(file),
				})
			}
		}

		s = s.WithConfig(cfg)
		return s.WithPackages(pkgs), nil
	}

	// 2. Initialize State
	// We pass the FileSet from golangci-lint so positions are reported correctly
	initialState := linter.State{
		Fset: pass.Fset,
	}

	// 3. Run Pipeline
	finalState, err := pipeline.Run[linter.State](
		context.Background(),
		initialState,
		ingestStep,
		linter.ValidateImports,
		linter.AnalyzeExports,
	)

	if err != nil {
		return nil, err
	}

	// 4. Report Violations
	for _, v := range finalState.Violations {
		pass.Report(analysis.Diagnostic{
			Pos:     token.NoPos,
			Message: v.Message,
		})
	}

	return nil, nil
}
