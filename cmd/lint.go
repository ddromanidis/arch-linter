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
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ddromanidis/arch-linter/internal/linter"
	"github.com/ddromanidis/arch-linter/internal/logger"
	"github.com/ddromanidis/arch-linter/internal/pipeline"

	"github.com/spf13/cobra"
)

var (
	configPath string
	rootPath   string
)

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Run the architectural analysis",
	Run: func(cmd *cobra.Command, args []string) {
		l := logger.NewLogger()

		start := time.Now()
		l.Log("Starting analysis...")

		initialState := linter.State{
			RootPath: rootPath,
		}

		finalState, err := pipeline.Run[linter.State](
			context.Background(),
			initialState,
			linter.LoadConfigStep(configPath),
			linter.ParseFiles,
			linter.ValidateImports,
			linter.AnalyzeExports,
		)

		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

		if len(finalState.Violations) > 0 {
			for _, v := range finalState.Violations {
				l.Log(
					"found violataion",
					logger.KV("module", v.Module),
					logger.KV("file", v.File),
					logger.KV("message", v.Message),
				)
			}
			os.Exit(1)
		}

		l.Log("success", logger.KV("finished_in", time.Since(start).Seconds()))
	},
}

func init() {
	rootCmd.AddCommand(lintCmd)
	lintCmd.Flags().StringVarP(&configPath, "config", "c", "arch.yaml", "Path to config")
	lintCmd.Flags().StringVarP(&rootPath, "path", "p", ".", "Root path to scan")
}
