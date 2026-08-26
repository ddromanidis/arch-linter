package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Severity decides what a violation costs.
type Severity string

const (
	// Error fails the run.
	Error Severity = "error"
	// Warning is reported and ignored by the exit code — the setting that lets a team turn
	// a rule on before they have finished obeying it.
	Warning Severity = "warning"
	// Off disables the rule entirely.
	Off Severity = "off"
)

func (s Severity) valid() bool {
	switch s {
	case Error, Warning, Off:
		return true
	}
	return false
}

// Config is the parsed arch.config.yaml: how the tool behaves, as opposed to what the
// architecture is.
type Config struct {
	Version  int        `yaml:"version"`
	Output   Output     `yaml:"output"`
	Severity Severities `yaml:"severity"`
	// Exclude drops packages from analysis entirely, by import path pattern.
	Exclude []string `yaml:"exclude"`
	// IncludeTests analyses _test.go files. Off by default: tests routinely and correctly
	// reach across boundaries their production code may not, and a linter that fails on
	// that is a linter people switch off.
	IncludeTests bool   `yaml:"include-tests"`
	Baseline     string `yaml:"baseline"`
}

type Output struct {
	Format string `yaml:"format"`
	Color  string `yaml:"color"`
}

type Severities struct {
	Imports Severity `yaml:"imports"`
	Exports Severity `yaml:"exports"`
	// Waivers reports directives that are malformed or that suppress nothing. A warning by
	// default: stale waivers are worth knowing about but are not themselves an
	// architecture violation, and failing a build over one would be perverse.
	Waivers Severity `yaml:"waivers"`
	// Cycles reports dependency loops between components. A property of arch.yaml rather
	// than of any package, so it is checked once per run.
	Cycles Severity `yaml:"cycles"`
	// Coverage reports a declared component whose path pattern matches no package. An
	// error by default, because it is always a mistake: a misspelled path produces a
	// component with rules that can never fire, which is indistinguishable from a
	// component that is enforced and clean.
	Coverage Severity `yaml:"coverage"`
	// Unclassified reports packages inside the module that no component claims. Off by
	// default — switching it on for a repository mid-adoption would report every corner
	// nobody has got to yet, which is the behaviour that makes people uninstall a linter.
	// Worth turning on once the architecture is fully described, to keep it that way.
	Unclassified Severity `yaml:"unclassified"`
}

// Default is the configuration used when no arch.config.yaml exists, which should be the
// common case — the file exists to change something, not to state the obvious.
func Default() *Config {
	return &Config{
		Version: 1,
		Output:  Output{Format: "text", Color: "auto"},
		Severity: Severities{
			Imports:      Error,
			Exports:      Error,
			Waivers:      Warning,
			Cycles:       Error,
			Coverage:     Error,
			Unclassified: Off,
		},
		Baseline: "",
	}
}

// ParseConfig reads an arch.config.yaml. A missing file is not an error: it means the
// defaults, which is a legitimate and expected way to run.
func ParseConfig(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	return parseConfig(data, file)
}

// parseConfig decodes over the defaults, so a file that sets one field keeps the rest.
func parseConfig(data []byte, file string) (*Config, error) {
	c := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := c.validate(file); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate(file string) error {
	if c.Version != 1 {
		return fmt.Errorf("%s: version must be 1, got %d", file, c.Version)
	}
	switch c.Output.Format {
	case "text", "json", "github", "sarif":
	default:
		return fmt.Errorf("%s: unknown output format %q", file, c.Output.Format)
	}
	switch c.Output.Color {
	case "auto", "always", "never":
	default:
		return fmt.Errorf("%s: color must be auto, always or never, got %q", file, c.Output.Color)
	}
	for name, s := range map[string]Severity{
		"imports":      c.Severity.Imports,
		"exports":      c.Severity.Exports,
		"waivers":      c.Severity.Waivers,
		"cycles":       c.Severity.Cycles,
		"coverage":     c.Severity.Coverage,
		"unclassified": c.Severity.Unclassified,
	} {
		if !s.valid() {
			return fmt.Errorf("%s: severity.%s must be error, warning or off, got %q",
				file, name, s)
		}
	}
	return nil
}
