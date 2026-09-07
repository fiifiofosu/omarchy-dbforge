// Package engines is the catalogue of database engines DBForge knows how to
// run. Each engine encodes the details that differ between images: where the
// data lives inside the container, which port it listens on, and -- most
// importantly -- how it behaves on first boot versus subsequent boots.
package engines

import (
	"fmt"
	"sort"
	"strings"
)

// Engine describes how to run one database engine.
type Engine struct {
	// Name is the identifier users type: "postgres", "redis", ...
	Name string
	// Image is the registry reference, without a tag.
	Image string
	// ContainerPort is the port the engine listens on inside the container.
	ContainerPort int
	// DataPath is where the engine keeps its data inside the container. This
	// is the bind-mount target.
	DataPath string
	// DefaultPortBase is where port allocation starts searching for this
	// engine. It must lie inside ports.DefaultRange or the allocator falls
	// back to the bottom of the range and the value has no effect. The digits
	// echo the engine's conventional port (15432 for Postgres's 5432) so an
	// instance's port is guessable, without ever occupying the real one.
	DefaultPortBase int

	// FirstRunEnv returns the environment needed to initialise a *brand new*
	// data directory. Engines like Postgres and MySQL run an initdb step on
	// first boot only; passing these again on a populated directory is at best
	// ignored and at worst an error (spec 8).
	FirstRunEnv func(password string) map[string]string

	// NeedsPassword reports whether the engine requires a generated password.
	NeedsPassword bool

	// ConnString builds a ready-to-copy connection string (used by the TUI in
	// phase 3, and by `dbctl list -o json` today).
	ConnString func(port int, password string) string
}

var catalogue = map[string]Engine{
	"postgres": {
		Name:            "postgres",
		Image:           "docker.io/library/postgres",
		ContainerPort:   5432,
		DataPath:        "/var/lib/postgresql/data",
		DefaultPortBase: 15432,
		NeedsPassword:   true,
		FirstRunEnv: func(pw string) map[string]string {
			// Deliberately no custom PGDATA. The entrypoint chowns $PGDATA to
			// the postgres user but not its parent, so pointing PGDATA at a
			// subdirectory of the mount leaves the mount itself root-owned and
			// mode 0700 -- and the postgres user then cannot traverse into it.
			// Mounting directly at the default PGDATA lets the chown land on
			// the mount itself. Verified against postgres:16.
			return map[string]string{"POSTGRES_PASSWORD": pw}
		},
		ConnString: func(port int, pw string) string {
			return fmt.Sprintf("postgresql://postgres:%s@127.0.0.1:%d/postgres", pw, port)
		},
	},
	"mysql": {
		Name:            "mysql",
		Image:           "docker.io/library/mysql",
		ContainerPort:   3306,
		DataPath:        "/var/lib/mysql",
		DefaultPortBase: 15306,
		NeedsPassword:   true,
		FirstRunEnv: func(pw string) map[string]string {
			return map[string]string{"MYSQL_ROOT_PASSWORD": pw}
		},
		ConnString: func(port int, pw string) string {
			return fmt.Sprintf("mysql://root:%s@127.0.0.1:%d/", pw, port)
		},
	},
	"mariadb": {
		Name:            "mariadb",
		Image:           "docker.io/library/mariadb",
		ContainerPort:   3306,
		DataPath:        "/var/lib/mysql",
		DefaultPortBase: 15316,
		NeedsPassword:   true,
		FirstRunEnv: func(pw string) map[string]string {
			return map[string]string{"MARIADB_ROOT_PASSWORD": pw}
		},
		ConnString: func(port int, pw string) string {
			return fmt.Sprintf("mysql://root:%s@127.0.0.1:%d/", pw, port)
		},
	},
	"redis": {
		Name:            "redis",
		Image:           "docker.io/library/redis",
		ContainerPort:   6379,
		DataPath:        "/data",
		DefaultPortBase: 15379,
		NeedsPassword:   false,
		FirstRunEnv:     func(string) map[string]string { return map[string]string{} },
		ConnString: func(port int, _ string) string {
			return fmt.Sprintf("redis://127.0.0.1:%d", port)
		},
	},
}

// Get looks up an engine by name.
func Get(name string) (Engine, error) {
	e, ok := catalogue[strings.ToLower(name)]
	if !ok {
		return Engine{}, fmt.Errorf("unknown engine %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	return e, nil
}

// Names lists the known engine names, sorted for stable output.
func Names() []string {
	out := make([]string, 0, len(catalogue))
	for n := range catalogue {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ParseRef splits an "engine:version" reference, e.g. "postgres:16".
func ParseRef(ref string) (Engine, string, error) {
	name, version, ok := strings.Cut(ref, ":")
	if !ok || version == "" {
		return Engine{}, "", fmt.Errorf("reference %q must be engine:version, e.g. postgres:16", ref)
	}
	e, err := Get(name)
	if err != nil {
		return Engine{}, "", err
	}
	return e, version, nil
}
