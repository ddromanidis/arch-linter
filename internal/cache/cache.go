// Package cache skips re-analysing packages that have not changed.
//
// The cost of a run is almost entirely go/packages type-checking: 1.78s for a 161-package
// module, of which the analysis itself is a few milliseconds. Type information is what the
// export rule needs and there is no way around loading it — but there is a way around
// loading it *again* for a package whose answer cannot have changed.
//
// The correctness question is what a package's findings actually depend on, and it is not
// only its own source. `func F() other.T` is a leak or not depending on what other.T is, so
// changing a dependency can change this package's answer without touching a byte of it.
// Fingerprints are therefore computed over the import graph: a package's fingerprint covers
// its own files and, recursively, the fingerprints of everything it imports from this
// module. Change anything a package transitively depends on and its entry is invalidated.
//
// A stale cache would be the worst possible bug in this tool — a green run that proves
// nothing — so every input is in the key, including the tool's own version, and --no-cache
// exists for when you do not want to trust any of it.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ddromanidis/arch-linter/internal/report"
)

// Entry is one package's cached result.
type Entry struct {
	Fingerprint string           `json:"fingerprint"`
	Findings    []report.Finding `json:"findings"`
}

// File is the on-disk cache for one module and configuration.
type File struct {
	// Version of the tool that wrote it. A different build may analyse differently, so a
	// mismatch discards everything rather than trusting results it did not produce.
	Version string `json:"version"`
	// Key covers the configuration: both YAML files, the build tags, the flags that change
	// what is analysed. A different key is a different cache.
	Key      string           `json:"key"`
	Packages map[string]Entry `json:"packages"`
}

// Cache is a loaded File plus the results accumulated during this run.
type Cache struct {
	path    string
	version string
	key     string

	mu     sync.Mutex
	loaded map[string]Entry
	fresh  map[string]Entry

	hits, misses int
}

// Open loads the cache for a module, or returns an empty one.
//
// Any problem reading it — missing, corrupt, written by another version — is silently an
// empty cache rather than an error. A cache is an optimisation, and failing a build
// because an optimisation is unreadable would be absurd.
func Open(dir, module, version, key string) *Cache {
	c := &Cache{
		path:    filepath.Join(dir, fileName(module)),
		version: version,
		key:     key,
		loaded:  map[string]Entry{},
		fresh:   map[string]Entry{},
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return c
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return c
	}
	if f.Version != version || f.Key != key {
		return c
	}
	if f.Packages != nil {
		c.loaded = f.Packages
	}
	return c
}

func fileName(module string) string {
	sum := sha256.Sum256([]byte(module))
	return hex.EncodeToString(sum[:8]) + ".json"
}

// Dir is where caches live: the user cache directory, so nothing is written into the
// repository and no .gitignore entry is needed.
func Dir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "archlint")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Lookup returns the cached findings for a package if its fingerprint still matches.
func (c *Cache) Lookup(pkg, fingerprint string) ([]report.Finding, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.loaded[pkg]
	if !ok || e.Fingerprint != fingerprint {
		c.misses++
		return nil, false
	}
	c.hits++
	// Carried forward, or a run that hits everything would write an empty cache.
	c.fresh[pkg] = e
	return e.Findings, true
}

// Store records a package's findings under its fingerprint.
func (c *Cache) Store(pkg, fingerprint string, findings []report.Finding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fresh[pkg] = Entry{Fingerprint: fingerprint, Findings: findings}
}

// Stats reports how much was reused.
func (c *Cache) Stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// Save writes the cache, dropping anything not seen this run so it cannot grow without
// bound as packages are renamed and deleted.
func (c *Cache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := File{Version: c.version, Key: c.key, Packages: c.fresh}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	// Written atomically. A run interrupted midway through writing would otherwise leave
	// a truncated file that the next run reads as valid JSON if it is unlucky.
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Package is the input to fingerprinting: what a package is made of and what it imports.
type Package struct {
	ImportPath string
	Files      []string
	// Imports lists only packages within the module. Everything outside it is pinned by
	// go.mod, whose hash is part of the configuration key, so a dependency cannot change
	// underneath a fingerprint without the whole cache being invalidated anyway.
	Imports []string
}

// Fingerprints computes a fingerprint per package over the import graph.
//
// A package's fingerprint covers its own file contents and the fingerprints of everything
// it imports from this module, transitively. That is what makes the cache sound: changing
// a type in a dependency changes the dependent's fingerprint, even though none of its own
// files were touched.
//
// Cycles cannot occur — Go forbids import cycles between packages — but a malformed graph
// is handled rather than trusted, since hanging is a worse failure than a cache miss.
func Fingerprints(pkgs []Package) map[string]string {
	byPath := make(map[string]Package, len(pkgs))
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}

	// File hashes first, in parallel: this is the only part that touches the disk.
	fileHashes := hashFiles(pkgs)

	out := make(map[string]string, len(pkgs))
	inProgress := map[string]bool{}

	var fingerprint func(path string) string
	fingerprint = func(path string) string {
		if fp, done := out[path]; done {
			return fp
		}
		p, ok := byPath[path]
		if !ok {
			// Outside the module. Pinned by go.mod, which is in the configuration key.
			return ""
		}
		if inProgress[path] {
			// Only reachable if the graph is malformed. Contribute nothing rather than
			// recurse forever.
			return ""
		}
		inProgress[path] = true

		h := sha256.New()
		fmt.Fprintf(h, "pkg\x00%s\x00", path)
		for _, f := range sortedCopy(p.Files) {
			fmt.Fprintf(h, "file\x00%s\x00%s\x00", f, fileHashes[f])
		}
		for _, dep := range sortedCopy(p.Imports) {
			if fp := fingerprint(dep); fp != "" {
				fmt.Fprintf(h, "dep\x00%s\x00%s\x00", dep, fp)
			}
		}

		delete(inProgress, path)
		fp := hex.EncodeToString(h.Sum(nil))
		out[path] = fp
		return fp
	}

	for _, p := range pkgs {
		fingerprint(p.ImportPath)
	}
	return out
}

// hashFiles reads and hashes every file once, concurrently.
func hashFiles(pkgs []Package) map[string]string {
	seen := map[string]bool{}
	var paths []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			if !seen[f] {
				seen[f] = true
				paths = append(paths, f)
			}
		}
	}

	out := make(map[string]string, len(paths))
	var mu sync.Mutex
	var wg sync.WaitGroup
	// Bounded, so a very large module does not open thousands of files at once.
	sem := make(chan struct{}, 32)

	for _, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sum := "missing"
			if data, err := os.ReadFile(path); err == nil {
				h := sha256.Sum256(data)
				sum = hex.EncodeToString(h[:])
			}
			mu.Lock()
			out[path] = sum
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// Key builds the configuration half of the cache key from everything that changes what a
// run would report.
func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%s\x00", p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
