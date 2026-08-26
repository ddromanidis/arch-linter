// Package scaffold writes a starting arch.yaml.
//
// The presets are opinions, not templates to be obeyed. Their real job is to show the
// shape of the file and — more importantly — to demonstrate the one rule people have not
// seen before: infrastructure that may import its driver and may not hand it out. A blank
// file teaches neither.
package scaffold

import (
	"fmt"
	"os"
	"strings"
)

// Presets, by name.
var presets = map[string]string{
	"hexagonal": hexagonal,
	"layered":   layered,
	"ddd":       ddd,
	"minimal":   minimal,
}

// Names lists the available presets, for the error message and the help text.
func Names() []string { return []string{"minimal", "layered", "hexagonal", "ddd"} }

// Write creates path from a preset. It refuses to overwrite: an arch.yaml is a hand-edited
// design document, and clobbering one because somebody re-ran init in the wrong directory
// would be unforgivable.
func Write(path, preset, module string) error {
	body, ok := presets[preset]
	if !ok {
		return fmt.Errorf("unknown preset %q; try one of: %s",
			preset, strings.Join(Names(), ", "))
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if module != "" {
		body = strings.ReplaceAll(body, "# module: your/module/path",
			"module: "+module)
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

const preamble = `version: 1

# The Go module path. Optional — without it, archlint reads go.mod.
# module: your/module/path

# Components are named regions of the codebase. A trailing /... matches everything
# beneath, as in go tooling.
#
# Both rule lists are ALLOWLISTS: anything not named is forbidden. A denylist is only
# as good as your imagination on the day you wrote it, and the dependency that hurts is
# the one nobody thought to ban.
#
#   imports  what this component may depend on
#   exports  what may appear in its exported API — signatures, exported struct fields,
#            interface methods, type aliases
#
# The second is the one other Go architecture linters do not have, and the reason this
# tool exists. A repository may import its database driver and still have no business
# returning it.
`

const minimal = preamble + `
components:
  app:
    path: internal/app/...
    imports: []
    exports: []

defaults:
  imports: [fmt, errors, context]
  exports: [context, time]
`

const layered = preamble + `
components:
  model:
    path: internal/model/...
    description: "Types and rules. Depends on nothing."
    imports: []
    exports: []

  store:
    path: internal/store/...
    description: "Persistence. Owns the driver and does not hand it out."
    imports: [model, gorm.io/gorm]
    # Note what is absent: gorm. The driver may be used here and must not escape,
    # which is the rule no import-only linter can express.
    exports: [model]

  service:
    path: internal/service/...
    description: "Use cases."
    imports: [model, store]
    exports: [model]

  http:
    path: internal/http/...
    description: "Handlers. The outermost layer."
    imports: [model, service, net/http]
    exports: [model]

  cmd:
    path: cmd/...
    description: "Entry points. Everything may be reached from here."
    imports: [model, store, service, http]
    exports: []

defaults:
  imports: [fmt, errors, context, log/slog]
  exports: [context, time]
`

const hexagonal = preamble + `
components:
  domain:
    path: internal/domain/...
    description: "Entities and business rules. The centre depends on nothing."
    imports: []
    exports: []

  ports:
    path: internal/ports/...
    description: "Interfaces the domain needs the outside world to satisfy."
    imports: [domain]
    exports: [domain]

  adapters:
    path: internal/adapters/...
    description: "Implementations of the ports: databases, queues, HTTP clients."
    imports: [domain, ports, gorm.io/gorm, net/http]
    # The adapters own their libraries. Exporting gorm here would put the driver in
    # the hands of everything that depends on an adapter, which is precisely the
    # coupling hexagonal architecture exists to prevent.
    exports: [domain, ports]

  app:
    path: internal/app/...
    description: "Wires ports to adapters and drives the use cases."
    imports: [domain, ports]
    exports: [domain, ports]

  cmd:
    path: cmd/...
    description: "Composition root. The only place that may see an adapter."
    imports: [domain, ports, adapters, app]
    exports: []

defaults:
  imports: [fmt, errors, context, log/slog]
  exports: [context, time]
`

const ddd = preamble + `
components:
  shared:
    path: internal/shared/...
    description: "Shared kernel. Keep it small; everything depends on it."
    imports: []
    exports: []

  domain:
    path: internal/domain/...
    description: "Aggregates, entities, value objects, domain events."
    imports: [shared]
    exports: [shared]

  application:
    path: internal/application/...
    description: "Application services and command handlers."
    imports: [shared, domain]
    exports: [shared, domain]

  infrastructure:
    path: internal/infrastructure/...
    description: "Repository implementations, message buses, external services."
    imports: [shared, domain, application, gorm.io/gorm]
    # A repository returns aggregates, never rows. The absence of gorm here is what
    # makes that a rule rather than a convention.
    exports: [shared, domain, application]

  interfaces:
    path: internal/interfaces/...
    description: "HTTP, gRPC and CLI adapters."
    imports: [shared, domain, application, net/http]
    exports: [shared, domain, application]

  cmd:
    path: cmd/...
    description: "Composition root."
    imports: [shared, domain, application, infrastructure, interfaces]
    exports: []

defaults:
  imports: [fmt, errors, context, log/slog]
  exports: [context, time]
`
