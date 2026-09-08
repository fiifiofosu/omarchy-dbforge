// Package engines is the catalogue of database engines DBForge knows how to
// run. Each engine encodes the details that differ between images: where the
// data lives inside the container, which port it listens on, and -- most
// importantly -- how it behaves on first boot versus subsequent boots.
package engines

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Host is where every instance is reachable. Instances bind loopback only and
// nothing configures that away, so the host is a property of DBForge rather
// than of any one engine -- but callers still need it as a value to show and
// to build connection strings from.
const Host = "127.0.0.1"

// Engine describes how to run one database engine.
type Engine struct {
	// Name is the identifier users type: "postgres", "redis", ...
	Name string
	// Image is the registry reference, without a tag.
	Image string
	// ContainerPort is the port the engine listens on inside the container.
	ContainerPort int
	// DataPath is where the engine keeps its data inside the container. This
	// is the bind-mount target. Engines whose layout changed between major
	// versions set DataPathForVersion instead; use DataPathFor to read either.
	DataPath string
	// DataPathForVersion, when set, overrides DataPath for a given version
	// tag. Only Postgres needs it so far (see the 18 layout change below).
	DataPathForVersion func(version string) string
	// HostDirModeForVersion, when set, overrides the 0o700 default mode of
	// the host data directory. Only an engine that reaches its data as an
	// unprivileged user *below* the mount point needs this -- see Postgres 18.
	HostDirModeForVersion func(version string) os.FileMode
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

	// Database is the database a fresh instance connects to by default: the
	// path component of the connection string. Empty means the engine has no
	// such concept -- MySQL and MariaDB start with no user database, and a
	// client picks one after connecting -- which is a real answer, not a
	// missing one, so callers show it as "-" rather than inventing a name.
	Database string

	// StopTimeoutSecs is how long to let the engine shut down before podman
	// resorts to SIGKILL. Too short and a database is killed mid-write, which
	// means crash recovery on the next start -- observed with Postgres, where
	// the 10s default killed it during initdb and produced exit 137 on an
	// ordinary `dbctl stop`.
	StopTimeoutSecs uint

	// ConnString builds a ready-to-copy connection string (used by the TUI in
	// phase 3, and by `dbctl list -o json` today).
	ConnString func(port int, password string) string
}

var catalogue = map[string]Engine{
	"postgres": {
		Name:          "postgres",
		Image:         "docker.io/library/postgres",
		ContainerPort: 5432,
		DataPath:      "/var/lib/postgresql/data",
		// Postgres 18 changed the layout: the image now stores data in a
		// major-version-specific subdirectory ($PGDATA is
		// /var/lib/postgresql/<major>/docker) so that `pg_upgrade --link` can
		// run without crossing a mount boundary, and its entrypoint aborts if
		// it finds a mount at the old /var/lib/postgresql/data. From 18 on the
		// mount therefore goes one level up, at /var/lib/postgresql.
		// See https://github.com/docker-library/postgres/pull/1259.
		DataPathForVersion: func(version string) string {
			if postgresLegacyLayout(version) {
				return "/var/lib/postgresql/data"
			}
			return "/var/lib/postgresql"
		},
		// 0o700 is right when the mount *is* $PGDATA: the entrypoint chowns
		// it to the postgres user. From 18 the mount is its parent, which
		// stays owned by root, and the entrypoint re-execs itself as the
		// postgres user and walks down into $PGDATA -- through a directory it
		// cannot traverse at 0o700, which fails as
		// "mkdir: cannot create directory '/var/lib/postgresql'".
		// 0o711 grants exactly the traversal needed and no listing. The
		// database itself keeps initdb's own 0o700 one level down.
		HostDirModeForVersion: func(version string) os.FileMode {
			if postgresLegacyLayout(version) {
				return 0o700
			}
			return 0o711
		},
		DefaultPortBase: 15432,
		NeedsPassword:   true,
		// Generous: a checkpoint on a large database takes real time, and
		// initdb on first boot is slower still.
		StopTimeoutSecs: 60,
		// initdb always creates a database named after the superuser.
		Database: "postgres",
		FirstRunEnv: func(pw string) map[string]string {
			// Deliberately no custom PGDATA. The entrypoint chowns $PGDATA to
			// the postgres user but not its parent, so pointing PGDATA at a
			// subdirectory of the mount leaves the mount itself root-owned and
			// mode 0700 -- and the postgres user then cannot traverse into it.
			// Mounting directly at the default PGDATA lets the chown land on
			// the mount itself. Verified against postgres:16. From 18 the
			// mount is the parent directory instead and the entrypoint,
			// running as root, creates and chowns the version subdirectory
			// itself -- so still no custom PGDATA.
			return map[string]string{"POSTGRES_PASSWORD": pw}
		},
		ConnString: func(port int, pw string) string {
			return fmt.Sprintf("postgresql://postgres:%s@%s:%d/postgres", pw, Host, port)
		},
	},
	"mysql": {
		Name:            "mysql",
		Image:           "docker.io/library/mysql",
		ContainerPort:   3306,
		DataPath:        "/var/lib/mysql",
		DefaultPortBase: 15306,
		NeedsPassword:   true,
		StopTimeoutSecs: 60,
		FirstRunEnv: func(pw string) map[string]string {
			return map[string]string{"MYSQL_ROOT_PASSWORD": pw}
		},
		ConnString: func(port int, pw string) string {
			return fmt.Sprintf("mysql://root:%s@%s:%d/", pw, Host, port)
		},
	},
	"mariadb": {
		Name:            "mariadb",
		Image:           "docker.io/library/mariadb",
		ContainerPort:   3306,
		DataPath:        "/var/lib/mysql",
		DefaultPortBase: 15316,
		NeedsPassword:   true,
		StopTimeoutSecs: 60,
		FirstRunEnv: func(pw string) map[string]string {
			return map[string]string{"MARIADB_ROOT_PASSWORD": pw}
		},
		ConnString: func(port int, pw string) string {
			return fmt.Sprintf("mysql://root:%s@%s:%d/", pw, Host, port)
		},
	},
	"redis": {
		Name:            "redis",
		Image:           "docker.io/library/redis",
		ContainerPort:   6379,
		DataPath:        "/data",
		DefaultPortBase: 15379,
		NeedsPassword:   false,
		// Redis persists on its own schedule and shuts down quickly.
		StopTimeoutSecs: 15,
		// Redis has no named databases, only numbered ones, and a client that
		// selects nothing is on 0.
		Database:    "0",
		FirstRunEnv: func(string) map[string]string { return map[string]string{} },
		ConnString: func(port int, _ string) string {
			return fmt.Sprintf("redis://%s:%d", Host, port)
		},
	},
}

// postgresLegacyLayout reports whether a Postgres tag predates the 18 layout
// change. Unknown tags ("latest", "alpine", "bookworm") are treated as new:
// those tags track the newest release.
func postgresLegacyLayout(version string) bool {
	major, ok := majorVersion(version)
	return ok && major < 18
}

// HostDirModeFor is the mode the host data directory is created with.
func (e Engine) HostDirModeFor(version string) os.FileMode {
	if e.HostDirModeForVersion != nil {
		return e.HostDirModeForVersion(version)
	}
	// The data directory holds the database and nothing else needs to read it.
	return 0o700
}

// DataPathFor is the bind-mount target for one version of the engine.
func (e Engine) DataPathFor(version string) string {
	if e.DataPathForVersion != nil {
		return e.DataPathForVersion(version)
	}
	return e.DataPath
}

// majorVersion pulls the leading major version out of an image tag: "16",
// "16.3", "16-alpine" and "16.3-bookworm" all yield 16. Tags that do not start
// with digits ("latest", "alpine") report false.
func majorVersion(tag string) (int, bool) {
	end := 0
	for end < len(tag) && tag[end] >= '0' && tag[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(tag[:end])
	if err != nil {
		return 0, false
	}
	return n, true
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
