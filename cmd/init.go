/*
Copyright © 2025 Dmitry Romanidis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Template for the default configuration
const defaultArchConfig = `version: 1

# Optional: Define your module name manually if go.mod is not present
# module: "github.com/my/project"

global:
  # Rules applied to ALL modules
  imports:
    allow:
      - "fmt"
      - "errors"
      - "os"
      - "context"
      - "time"
      - "strings"
    deny:
      - "reflect" # Example: Ban reflection project-wide

  exports:
    allow:
      - "time"    # Allowed to return time.Time globally
      - "context" # Allowed to accept context.Context globally

modules:
  # Example 1: Domain Layer (Recursive)
  # Matches "internal/domain" AND "internal/domain/user", "internal/domain/order" etc.
  - name: domain
    path: "internal/domain/..."
    description: "Core business logic"
    imports: [] # Pure domain, no dependencies
    exports: []

  # Example 2: Application Layer (Recursive)
  - name: application
    path: "internal/application/..."
    imports: 
      - "domain"
    exports: 
      - "domain" # Can return domain entities

  # Example 3: Infrastructure (Exact Match)
  # Matches ONLY files in "internal/infrastructure", not subdirectories
  - name: infrastructure
    path: "internal/infrastructure"
    imports:
      - "domain"
      - "application"
      - "database/sql"
    exports: ["domain"]

  # Example 4: API Layer
  - name: api
    path: "internal/api/..."
    imports:
      - "application"
      - "domain"
      - "github.com/gin-gonic/gin"
    exports: []
`

var forceInit bool

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a default arch.yaml configuration",
	Long:  `Creates a new arch.yaml file in the current directory with default settings and examples.`,
	Run: func(cmd *cobra.Command, args []string) {
		filePath := "arch.yaml"

		// Check if file exists
		if _, err := os.Stat(filePath); err == nil && !forceInit {
			fmt.Printf("Error: %s already exists. Use --force to overwrite.\n", filePath)
			os.Exit(1)
		}

		// Write file
		err := os.WriteFile(filePath, []byte(defaultArchConfig), 0644)
		if err != nil {
			fmt.Printf("Error writing file: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Successfully created arch.yaml")
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVarP(&forceInit, "force", "f", false, "Overwrite existing config file")

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// initCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// initCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
