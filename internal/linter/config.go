package linter

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version int               `yaml:"version"`
	RootMod string            `yaml:"module"`
	Global  GlobalConfig      `yaml:"global"`
	Modules map[string]Module `yaml:"-"`
}

type GlobalConfig struct {
	Imports RuleSet `yaml:"imports"`
	Exports RuleSet `yaml:"exports"`
}

type RuleSet struct {
	Allow    []string        `yaml:"allow"`
	Deny     []string        `yaml:"deny"`
	AllowSet map[string]bool `yaml:"-"`
	DenySet  map[string]bool `yaml:"-"`
}

type Module struct {
	Name       string          `yaml:"name"`
	Path       string          `yaml:"path"`
	Recursive  bool            `yaml:"-"`
	Imports    map[string]bool `yaml:"-"`
	Exports    map[string]bool `yaml:"-"`
	RawImports []string        `yaml:"imports"`
	RawExports []string        `yaml:"exports"`
}

// ParseConfig loads the YAML file and builds the configuration object
func ParseConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var raw struct {
		Version int          `yaml:"version"`
		RootMod string       `yaml:"module"`
		Global  GlobalConfig `yaml:"global"`
		Modules []Module     `yaml:"modules"`
	}

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}

	cfg := Config{
		Version: raw.Version,
		RootMod: raw.RootMod,
		Global:  raw.Global,
		Modules: make(map[string]Module),
	}

	initRuleSet := func(rs *RuleSet) {
		rs.AllowSet = make(map[string]bool)
		for _, item := range rs.Allow {
			rs.AllowSet[item] = true
		}
		rs.DenySet = make(map[string]bool)
		for _, item := range rs.Deny {
			rs.DenySet[item] = true
		}
	}
	initRuleSet(&cfg.Global.Imports)
	initRuleSet(&cfg.Global.Exports)

	for _, m := range raw.Modules {
		// Detect Recursion based on "/..." suffix
		rawPath := filepath.ToSlash(m.Path)
		if strings.HasSuffix(rawPath, "/...") {
			m.Recursive = true
			m.Path = strings.TrimSuffix(rawPath, "/...")
		} else {
			m.Recursive = false
			m.Path = strings.TrimSuffix(rawPath, "/")
		}

		// Convert RawImports Slice -> Imports Map
		m.Imports = make(map[string]bool)
		for _, imp := range m.RawImports {
			m.Imports[imp] = true
		}

		// Convert RawExports Slice -> Exports Map
		m.Exports = make(map[string]bool)
		for _, exp := range m.RawExports {
			m.Exports[exp] = true
		}

		cfg.Modules[m.Name] = m
	}

	return cfg, nil
}

func (c Config) ResolveModuleByImport(importPath string) string {
	importPath = filepath.ToSlash(importPath)
	if c.RootMod != "" && strings.HasPrefix(importPath, c.RootMod) {
		importPath = strings.TrimPrefix(importPath, c.RootMod)
		importPath = strings.TrimPrefix(importPath, "/") // Remove leading slash
	}

	var bestMatch string
	longestLen := 0

	for name, mod := range c.Modules {
		cleanPath := mod.Path

		isSuffix := strings.HasSuffix(importPath, cleanPath)
		isSegment := strings.Contains(importPath, "/"+cleanPath+"/")
		isExact := importPath == cleanPath

		isChild := strings.HasPrefix(importPath, cleanPath+"/")

		if isSuffix || isSegment || isExact || isChild {
			if len(cleanPath) > longestLen {
				longestLen = len(cleanPath)
				bestMatch = name
			}
		}
	}
	return bestMatch
}

func (c Config) ResolveModuleName(filePath string) string {
	filePath = filepath.ToSlash(filePath)
	fileDir := filepath.Dir(filePath) // Get the directory of the file

	var bestMatch string
	longestLen := 0

	for name, mod := range c.Modules {
		match := false

		if mod.Recursive {
			// Recursive: Does fileDir start with mod.Path?
			// Check for exact match OR prefix match with a slash to avoid partials (e.g. "internals" vs "internal")
			if fileDir == mod.Path || strings.HasPrefix(fileDir, mod.Path+"/") {
				match = true
			}
		} else {
			// Exact: The file must be DIRECTLY in this directory
			if fileDir == mod.Path {
				match = true
			}
		}

		if match {
			if len(mod.Path) > longestLen {
				longestLen = len(mod.Path)
				bestMatch = name
			}
		}
	}
	return bestMatch
}

// ReadGoMod attempts to find and parse the module name from go.mod
func ReadGoMod(dir string) (string, error) {
	// Look for go.mod in the provided directory
	path := filepath.Join(dir, "go.mod")

	// If it doesn't exist, maybe we are in a subdir?
	// For simplicity, we assume the linter runs from project root.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Simple parse: Look for "module <name>"
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			// Extract everything after "module "
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}

	return "", nil
}
