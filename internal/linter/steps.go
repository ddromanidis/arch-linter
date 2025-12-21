package linter

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Helper: Extract imports from AST
func ExtractImports(f *ast.File) map[string]string {
	imports := make(map[string]string)
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, "\"")
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			parts := strings.Split(path, "/")
			name = parts[len(parts)-1]
		}
		imports[name] = path
	}
	return imports
}

// Step 1: Load Configuration
func LoadConfigStep(configPath string) func(context.Context, State) (State, error) {
	return func(ctx context.Context, s State) (State, error) {
		// 1. Load arch.yaml
		cfg, err := ParseConfig(configPath)
		if err != nil {
			return s, err
		}

		// 2. Auto-detect Go Module if not manually defined
		if cfg.RootMod == "" {
			// Try reading go.mod from the RootPath defined in CLI
			modName, _ := ReadGoMod(s.RootPath)
			if modName != "" {
				cfg.RootMod = modName
			}
		}

		// 3. Update State
		return s.WithConfig(cfg), nil
	}
}

// Step 2: Parse Files (Concurrent & Filtered)
func ParseFiles(ctx context.Context, s State) (State, error) {
	fset := token.NewFileSet()
	var mu sync.Mutex
	var collected []Package

	g, ctx := errgroup.WithContext(ctx)

	err := filepath.Walk(s.RootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// --- FIX STARTS HERE ---

		// 1. Calculate path relative to the scanned root
		// If s.RootPath is "./example" and path is "example/internal/foo.go",
		// relPath becomes "internal/foo.go"
		relPath, err := filepath.Rel(s.RootPath, path)
		if err != nil {
			return nil
		}

		// 2. Normalize separators (Windows fixes)
		relPath = filepath.ToSlash(relPath)

		// 3. Resolve Module using the RELATIVE path
		modName := s.Config.ResolveModuleName(relPath)
		if modName == "" {
			return nil // Correctly skips files not in arch.yaml
		}

		// --- FIX ENDS HERE ---

		g.Go(func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Note: We parse the ORIGINAL path so error messages point to the correct file location
			astFile, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return nil
			}

			pkg := Package{
				FilePath:   path,    // Store the original path for CLI output
				ModuleName: modName, // Mapped correctly now
				File:       astFile,
				Imports:    ExtractImports(astFile),
			}

			mu.Lock()
			collected = append(collected, pkg)
			mu.Unlock()
			return nil
		})
		return nil
	})

	// ... rest of the function remains the same ...
	if err != nil {
		return s, err
	}
	if err := g.Wait(); err != nil {
		return s, err
	}

	newState := s.WithPackages(collected)
	newState.Fset = fset
	return newState, nil
}

func ValidateImports(ctx context.Context, s State) (State, error) {
	var violations []Violation

	for _, pkg := range s.Packages {
		currentMod := s.Config.Modules[pkg.ModuleName]

		for alias, impPath := range pkg.Imports {
			// Resolve target module name
			targetName := s.Config.ResolveModuleByImport(impPath)
			if targetName == "" {
				targetName = impPath
			}

			if currentMod.Name == targetName {
				continue
			}

			// --- GLOBAL CHECKS (IMPORTS) ---

			// 1. Global Deny
			if s.Config.Global.Imports.DenySet[impPath] {
				violations = append(violations, Violation{
					Module: currentMod.Name,
					File:   pkg.FilePath,
					Message: fmt.Sprintf(
						"Global Deny: Importing '%s' is banned project-wide",
						impPath,
					),
				})
				continue
			}

			// 2. Global Allow
			if s.Config.Global.Imports.AllowSet[impPath] {
				continue
			}

			// --- MODULE CHECK ---
			allowed := currentMod.Imports[targetName]
			if !allowed {
				allowed = currentMod.Imports[impPath]
			}

			if !allowed {
				violations = append(violations, Violation{
					Module:  currentMod.Name,
					File:    pkg.FilePath,
					Message: fmt.Sprintf("Illegal import '%s' (alias: %s)", impPath, alias),
				})
			}
		}
	}
	return s.AddViolations(violations...), nil
}

// Step 4: Analyze Exports (Abstraction Leakage)
func AnalyzeExports(ctx context.Context, s State) (State, error) {
	var violations []Violation

	for _, pkg := range s.Packages {
		currentMod := s.Config.Modules[pkg.ModuleName]

		ast.Inspect(pkg.File, func(n ast.Node) bool {
			// Skip function bodies
			if _, isBody := n.(*ast.BlockStmt); isBody {
				return false
			}

			// 1. Check Function Signatures (Functions & Methods)
			if fn, ok := n.(*ast.FuncDecl); ok && ast.IsExported(fn.Name.Name) {
				// For functions, we always check params/results because the function itself is public
				violations = append(
					violations,
					checkFieldList(fn.Type.Params, pkg, currentMod, s, false)...)
				violations = append(
					violations,
					checkFieldList(fn.Type.Results, pkg, currentMod, s, false)...)
			}

			// 2. Check Struct Fields
			if ts, ok := n.(*ast.TypeSpec); ok && ast.IsExported(ts.Name.Name) {
				if st, ok := ts.Type.(*ast.StructType); ok {
					// Pass isStruct=true to filter out private fields
					violations = append(
						violations,
						checkFieldList(st.Fields, pkg, currentMod, s, true)...)
				}
			}
			return true
		})
	}

	return s.AddViolations(violations...), nil
}

// checkFieldList now takes an 'isStruct' flag
func checkFieldList(
	fields *ast.FieldList,
	pkg Package,
	mod Module,
	s State,
	isStruct bool,
) []Violation {
	var vs []Violation
	if fields == nil {
		return vs
	}

	for _, f := range fields.List {
		// 1. Filter Unexported Struct Fields
		if isStruct {
			isFieldExported := false
			if len(f.Names) > 0 {
				for _, name := range f.Names {
					if ast.IsExported(name.Name) {
						isFieldExported = true
						break
					}
				}
			} else {
				// Embedded fields are considered exported for architecture checks
				isFieldExported = true
			}
			if !isFieldExported {
				continue
			}
		}

		// 2. Resolve Type
		typeName := extractTypeName(f.Type)
		if typeName == "" {
			continue
		}

		parts := strings.Split(typeName, ".")
		if len(parts) < 2 {
			continue
		} // Local type

		alias := parts[0]
		realPath, ok := pkg.Imports[alias]
		if !ok {
			continue
		}

		// 3. Resolve Module
		targetName := s.Config.ResolveModuleByImport(realPath)
		if targetName == "" {
			targetName = realPath
		}

		// --- THE FIX IS HERE ---
		// If the type belongs to the SAME module we are currently checking,
		// it is NOT a leak. It is internal usage.
		if targetName == mod.Name {
			continue
		}
		// -----------------------

		// 4. Global Rules
		if s.Config.Global.Exports.DenySet[realPath] {
			vs = append(vs, Violation{
				Module:  mod.Name,
				File:    pkg.FilePath,
				Message: fmt.Sprintf("Global Deny: Exporting type from '%s' is banned", realPath),
			})
			continue
		}
		if s.Config.Global.Exports.AllowSet[realPath] {
			continue
		}

		// 5. Module Rules
		allowed := mod.Exports[targetName]
		if !allowed {
			allowed = mod.Exports[realPath]
		}

		if !allowed {
			vs = append(vs, Violation{
				Module: mod.Name,
				File:   pkg.FilePath,
				Message: fmt.Sprintf(
					"Leaking type from '%s' (alias: %s) in exported symbol",
					realPath,
					alias,
				),
			})
		}
	}
	return vs
}

func extractTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return extractTypeName(t.X)
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			return x.Name + "." + t.Sel.Name
		}
	case *ast.ArrayType:
		return extractTypeName(t.Elt)
	}
	return ""
}
