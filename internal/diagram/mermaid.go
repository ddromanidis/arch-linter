package diagram

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ddromanidis/arch-linter/internal/linter"
)

// Optional: Mermaid Diagram Generator Step
func GenerateDiagramMermaid(ctx context.Context, s *linter.State) (*linter.State, error) {
	// Simple Mermaid Graph generation based on Config
	var builder strings.Builder
	builder.WriteString("graph TD;\n")

	for _, m := range s.Config.Modules {
		for impName := range m.Imports {
			// domain --> infrastructure
			builder.WriteString(fmt.Sprintf("  %s --> %s;\n", m.Name, impName))
		}
	}

	os.WriteFile("arch.mmd", []byte(builder.String()), 0644)
	return s, nil
}
