package linter

import (
	"go/ast"
	"go/token"
)

// State is an immutable value object passed through the pipeline.
type State struct {
	Config     Config
	RootPath   string
	Packages   []Package
	Violations []Violation

	Fset *token.FileSet
}

type Package struct {
	FilePath   string
	ModuleName string
	Imports    map[string]string

	File *ast.File
}

type Violation struct {
	Module  string
	File    string
	Message string
}

func (s State) WithConfig(c Config) State {
	s.Config = c
	return s
}

func (s State) WithPackages(pkgs []Package) State {
	s.Packages = pkgs
	return s
}

func (s State) AddViolations(vs ...Violation) State {
	newSlice := make([]Violation, 0, len(s.Violations)+len(vs))
	newSlice = append(newSlice, s.Violations...)
	newSlice = append(newSlice, vs...)
	s.Violations = newSlice
	return s
}
