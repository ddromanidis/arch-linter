// Command archlint-vet is archlint as a vet tool:
//
//	go vet -vettool=$(which archlint-vet) ./...
//
// A separate binary from archlint because the two answer to different masters. `go vet`
// expects a unitchecker — it drives the build itself, hands over one package at a time as
// a JSON export file, and owns the output format and the exit code. The archlint command
// wants none of that: it loads packages itself so it can offer sarif, a baseline, and an
// exit code that distinguishes an error from a warning. Trying to be both in one binary
// means one of them gets the worse deal.
//
// The rules are identical. Both are the same Analyzer, which is the entire reason for
// building the linter as one in the first place. Configuration is found by walking up from
// each package to the nearest arch.yaml, since `go vet` gives no opportunity to pass
// anything in; -archlint.arch overrides it.
package main

import (
	"github.com/ddromanidis/arch-linter/analyzer"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(analyzer.Analyzer)
}
