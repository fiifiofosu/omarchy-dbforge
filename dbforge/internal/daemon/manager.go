// Package daemon owns all DBForge state. Every mutation goes through Manager,
// which is the single writer -- concurrent dbctl invocations, the TUI and the
// status widget all funnel through here (spec 5, 7 phase 2).
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fiifiofosu/dbforge/internal/engines"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/notify"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// ManagedLabel marks a container as ours. Reconciliation keys off this.
const ManagedLabel = "io.dbforge.managed"

// ScopeLabel isolates one DBForge installation from another on the same host.
const ScopeLabel = "io.dbforge.scope"

// DefaultScope is used when none is configured.
const DefaultScope = "default"

// Errors surfaced to the CLI with distinct exit behaviour.
var (
	ErrInstanceExists   = errors.New("instance already exists")
	ErrInstanceNotFound = errors.New("instance not found")
	ErrContainerMissing = errors.New("instance has no container")
)

// Config tunes a Manager.
type Config struct {
	DataRoot  string
	PortRange ports.Range
	Probe     ports.Prober
	Log       *slog.Logger
	// Scope isolates this daemon's containers from any other DBForge on the
	// same host. Empty means DefaultScope.
	Scope string
	// Notifier is told whenever instance state changes, so the status bar can
	// refresh without polling. May be nil.
	Notifier *notify.Notifier
}

// Manager is the single source of truth for instance state.
type Manager struct {
	rt     runtime.Runtime
	st     *store.Store
	cfg    Config
	log    *slog.Logger
	notify *notify.Notifier
	clock  func() time.Time

	// mu guards the instances map itself.
	mu        sync.Mutex
	instances map[string]*model.Instance
	// locks serialises mutations per instance id, so two dbctl calls racing
	// on the same instance queue rather than interleave (spec 7, phase 2).
	locks map[string]*sync.Mutex
}

// NewManager builds a Manager. Call Reconcile before serving.
func NewManager(rt runtime.Runtime, st *store.Store, cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.PortRange == (ports.Range{}) {
		cfg.PortRange = ports.DefaultRange
	}
	if cfg.Scope == "" {
		cfg.Scope = DefaultScope
	}
	return &Manager{
		rt: rt, st: st, cfg: cfg, log: cfg.Log, notify: cfg.Notifier,
		clock:     time.Now,
		instances: map[string]*model.Instance{},
		locks:     map[string]*sync.Mutex{},
	}
}

func (m *Manager) lockFor(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locks[id]; ok {
		return l
	}
	l := &sync.Mutex{}
	m.locks[id] = l
	return l
}

// ReconcileReport summarises what a reconciliation pass changed. It is
// returned so the daemon can log it and `dbctl doctor` can show it.
type ReconcileReport struct {
	Adopted []string // containers found in Podman but absent from config
	Missing []string // config entries whose container has vanished
	Pruned  []string // half-created entries with no container, cleaned up
	// Unclean lists instances whose container exited nonzero. For engines
	// like Postgres this means crash recovery runs on the next start, which
	// is worth telling the user about rather than hiding (spec 7, phase 2).
	Unclean []string
}

// Reconcile rebuilds state by treating Podman as ground truth and the TOML
// file as an index (spec 6). It runs at startup and before any read that must
// be accurate.
//
// Three kinds of drift are handled:
//
//   - A container carries our label but has no config entry. That happens if
//     the daemon crashed after Create but before Save. Adopt it.
//   - A config entry has no container, because the user ran `podman rm`
//     directly. Mark it missing; never silently recreate it.
//   - A config entry is still in "creating" with no container, meaning a
//     create died partway. Prune it so the id and port are reusable.
func (m *Manager) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport

	persisted, err := m.st.Load()
	if err != nil {
		return rep, fmt.Errorf("loading config: %w", err)
	}

	live, err := m.rt.ListManaged(ctx, ManagedLabel)
	if err != nil {
		return rep, fmt.Errorf("listing containers: %w", err)
	}
	byName := make(map[string]runtime.Container, len(live))
	for _, c := range live {
		// Ignore containers belonging to another DBForge installation. An
		// unlabelled container predates scoping, so treat it as ours.
		if sc := c.Labels[ScopeLabel]; sc != "" && sc != m.cfg.Scope {
			continue
		}
		byName[c.Name] = c
	}

	next := map[string]*model.Instance{}

	for i := range persisted {
		inst := persisted[i]
		c, found := byName[inst.ContainerName()]
		switch {
		case found:
			inst.ContainerID = c.ID
			inst.Status = statusFrom(c)
			inst.StartedAt = c.StartedAt
			if inst.Status != model.StatusRunning {
				inst.LastExitCode = c.ExitCode
				if c.ExitCode != 0 {
					rep.Unclean = append(rep.Unclean, inst.ID)
					m.log.Warn("instance exited uncleanly",
						"id", inst.ID, "exit_code", c.ExitCode,
						"hint", "the engine may run crash recovery on next start; see `dbctl logs "+inst.ID+"`")
				}
			} else {
				inst.LastExitCode = 0
			}
			inst = withDefaults(inst)
			next[inst.ID] = &inst
			delete(byName, inst.ContainerName())
		case inst.Status == model.StatusCreating:
			// Died mid-create: no container was ever made. Drop the entry so
			// the id and port become reusable.
			rep.Pruned = append(rep.Pruned, inst.ID)
			m.log.Warn("pruning half-created instance", "id", inst.ID)
		default:
			// Removed behind our back. Keep the entry -- the data directory
			// still exists and the user may want it back -- but surface drift.
			inst.Status = model.StatusMissing
			inst.ContainerID = ""
			inst = withDefaults(inst)
			next[inst.ID] = &inst
			rep.Missing = append(rep.Missing, inst.ID)
			m.log.Warn("instance container is missing", "id", inst.ID)
		}
	}

	// Anything still in byName is a labelled container we have no record of.
	for name, c := range byName {
		inst, err := instanceFromLabels(c)
		if err != nil {
			m.log.Warn("ignoring unrecognised dbforge container", "name", name, "error", err)
			continue
		}
		inst.Status = statusFrom(c)
		next[inst.ID] = &inst
		rep.Adopted = append(rep.Adopted, inst.ID)
		m.log.Warn("adopted untracked container", "id", inst.ID, "name", name)
	}

	sort.Strings(rep.Adopted)
	sort.Strings(rep.Missing)
	sort.Strings(rep.Pruned)
	sort.Strings(rep.Unclean)

	m.mu.Lock()
	m.instances = next
	m.mu.Unlock()

	drifted := len(rep.Adopted) > 0 || len(rep.Pruned) > 0 ||
		len(rep.Missing) > 0 || len(rep.Unclean) > 0

	// An upgrade over an older config must rewrite it even when nothing
	// drifted, or a config that happens to reconcile cleanly stays on the old
	// schema indefinitely -- and the next release's migration would then be
	// starting from a version it no longer expects.
	migrated := m.st.LoadedVersion() < store.SchemaVersion

	if drifted || migrated {
		// Reconciliation changed our view, so the index is stale. Force the
		// write: we have just read the world, and our view is authoritative.
		if err := m.save(true); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// withDefaults fills in fields that a config written by an older DBForge will
// not have. Without this, every pre-existing instance would read back with an
// empty restart policy and an empty desired state, and so would never be
// restored after a reboot.
func withDefaults(i model.Instance) model.Instance {
	if i.Scope == "" {
		i.Scope = DefaultScope
	}
	if !model.ValidRestartPolicy(i.Restart) {
		i.Restart = model.DefaultRestartPolicy
	}
	if i.Desired != model.DesiredRunning && i.Desired != model.DesiredStopped {
		// Infer intent from what we can observe: a running container was
		// clearly wanted running.
		if i.Status == model.StatusRunning {
			i.Desired = model.DesiredRunning
		} else {
			i.Desired = model.DesiredStopped
		}
	}
	return i
}

func statusFrom(c runtime.Container) model.Status {
	switch c.State {
	case runtime.StateRunning:
		return model.StatusRunning
	case runtime.StateFailed:
		return model.StatusError
	default:
		return model.StatusStopped
	}
}

// instanceFromLabels rebuilds an Instance purely from container labels, which
// is what makes Podman the source of truth rather than the TOML file.
func instanceFromLabels(c runtime.Container) (model.Instance, error) {
	id := c.Labels["io.dbforge.id"]
	if id == "" {
		return model.Instance{}, errors.New("container has no io.dbforge.id label")
	}
	port, _ := strconv.Atoi(c.Labels["io.dbforge.port"])
	if port == 0 {
		port = c.HostPort
	}
	return withDefaults(model.Instance{
		ID:          id,
		Engine:      c.Labels["io.dbforge.engine"],
		Version:     c.Labels["io.dbforge.version"],
		DataDir:     c.Labels["io.dbforge.data_dir"],
		Port:        port,
		ContainerID: c.ID,
		Restart:     model.RestartPolicy(c.Labels["io.dbforge.restart"]),
		Desired:     model.DesiredState(c.Labels["io.dbforge.desired"]),
		Scope:       c.Labels[ScopeLabel],
	}), nil
}

func (m *Manager) save(force bool) error {
	m.mu.Lock()
	out := make([]model.Instance, 0, len(m.instances))
	for _, i := range m.instances {
		out = append(out, *i)
	}
	m.mu.Unlock()

	err := m.st.Save(out, force)
	if err == nil {
		// Hooked here rather than at each call site: every state change ends
		// in a save, so this cannot be forgotten when a new action is added.
		m.notify.Changed()
	}
	if errors.Is(err, store.ErrChangedOnDisk) {
		// The user edited the file while we were running. Our in-memory view
		// was just reconciled against Podman, so it is the better truth --
		// but say so loudly rather than clobbering silently (spec 7, phase 2).
		m.log.Warn("config changed on disk; overwriting with reconciled state",
			"path", m.st.Path())
		err = m.st.Save(out, true)
		if err == nil {
			m.notify.Changed()
		}
		return err
	}
	return err
}

// List returns all instances, reconciled against Podman first so that drift
// shows up as `missing` rather than a stale `running`.
func (m *Manager) List(ctx context.Context) ([]model.Instance, error) {
	if _, err := m.Reconcile(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Instance, 0, len(m.instances))
	for _, i := range m.instances {
		out = append(out, *i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

// Get returns one instance.
func (m *Manager) Get(id string) (model.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.instances[id]
	if !ok {
		return model.Instance{}, fmt.Errorf("%w: %s", ErrInstanceNotFound, id)
	}
	return *i, nil
}

// takenPorts maps assigned ports to their owning instance.
func (m *Manager) takenPorts() map[int]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int]string, len(m.instances))
	for _, i := range m.instances {
		out[i.Port] = i.ID
	}
	return out
}

// CreateOptions are the knobs for Create.
type CreateOptions struct {
	Ref  string // engine:version, e.g. postgres:16
	ID   string
	Port int // 0 means allocate
	// MemoryLimitBytes and NanoCPUs default to the engine's sensible cap.
	MemoryLimitBytes int64
	NanoCPUs         int64
	Start            bool
	// Restart is the policy for automatic restarts. Empty means the default.
	Restart model.RestartPolicy
}

// Create makes a new instance. It is deliberately ordered so that a crash at
// any point leaves either a clean state or one the reconciler can repair.
func (m *Manager) Create(ctx context.Context, opt CreateOptions) (model.Instance, error) {
	eng, version, err := engines.ParseRef(opt.Ref)
	if err != nil {
		return model.Instance{}, err
	}
	if opt.ID == "" {
		opt.ID = fmt.Sprintf("%s%s", eng.Name, version)
	}
	if err := validateID(opt.ID); err != nil {
		return model.Instance{}, err
	}
	if opt.Restart == "" {
		opt.Restart = model.DefaultRestartPolicy
	}
	if !model.ValidRestartPolicy(opt.Restart) {
		return model.Instance{}, fmt.Errorf(
			"invalid restart policy %q (want one of: no, on-failure, always)", opt.Restart)
	}

	lock := m.lockFor(opt.ID)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	_, exists := m.instances[opt.ID]
	m.mu.Unlock()
	if exists {
		// Reject before touching Podman (spec 7, phase 1).
		return model.Instance{}, fmt.Errorf("%w: %s", ErrInstanceExists, opt.ID)
	}

	image := eng.Image + ":" + version

	// Validate the tag against the registry *before* creating anything, so a
	// typo like postgres:99 fails fast and leaves nothing behind.
	have, err := m.rt.ImageExists(ctx, image)
	if err != nil {
		return model.Instance{}, err
	}
	if !have {
		if err := m.rt.PullImage(ctx, image); err != nil {
			return model.Instance{}, fmt.Errorf("image %s is not available: %w", image, err)
		}
	}

	alloc := ports.New(m.cfg.Probe, m.takenPorts())
	var port int
	if opt.Port != 0 {
		if err := alloc.Pin(opt.Port, opt.ID); err != nil {
			return model.Instance{}, err
		}
		port = opt.Port
	} else {
		port, err = alloc.Allocate(eng.DefaultPortBase, m.cfg.PortRange, opt.ID)
		if err != nil {
			return model.Instance{}, err
		}
	}

	password := ""
	if eng.NeedsPassword {
		if password, err = generatePassword(); err != nil {
			return model.Instance{}, err
		}
	}

	dataDir := filepath.Join(m.cfg.DataRoot, eng.Name, version, opt.ID)
	inst := model.Instance{
		ID: opt.ID, Engine: eng.Name, Version: version, Image: image,
		Port: port, DataDir: dataDir, Env: eng.FirstRunEnv(password),
		CreatedAt: m.clock().UTC(), Status: model.StatusCreating,
		Scope:   m.cfg.Scope,
		Restart: opt.Restart,
		// The user asked for this instance; unless they said --no-start, they
		// want it running, and that intent must outlive a reboot.
		Desired: desiredFor(opt.Start),
	}

	// Record the intent before doing anything destructive or slow. If we die
	// after this point, Reconcile finds a "creating" entry with no container
	// and prunes it.
	m.mu.Lock()
	m.instances[inst.ID] = &inst
	m.mu.Unlock()
	if err := m.save(false); err != nil {
		m.rollback(inst.ID)
		return model.Instance{}, err
	}

	// 0o700: the data directory holds the database. Rootless Podman maps our
	// UID to root inside the container, so the engine sees it as its own.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		m.rollback(inst.ID)
		return model.Instance{}, fmt.Errorf("creating data directory %s: %w", dataDir, err)
	}

	spec := runtime.CreateSpec{
		Name: inst.ContainerName(), Image: image, Env: inst.Env,
		Labels: inst.Labels(), HostPort: port, ContainerPort: eng.ContainerPort,
		HostDataDir: dataDir, ContainerDataDir: eng.DataPath,
		MemoryLimitBytes: opt.MemoryLimitBytes, NanoCPUs: opt.NanoCPUs,
		TZ:              os.Getenv("TZ"),
		StopTimeoutSecs: eng.StopTimeoutSecs,
		// Deliberately "no", whatever the user's policy is.
		//
		// Podman's own restart policy is re-evaluated when the podman service
		// restarts, and it has no idea whether the user deliberately stopped
		// an instance -- so a container marked "always" comes back even after
		// `dbctl stop`, silently undoing an explicit decision. Restart policy
		// is the daemon's to enforce, because only the daemon knows desired
		// state. Two controllers with different information is one too many.
		RestartPolicy: "no",
	}
	cid, err := m.rt.Create(ctx, spec)
	if err != nil {
		m.rollback(inst.ID)
		return model.Instance{}, fmt.Errorf("creating container: %w", err)
	}

	m.mu.Lock()
	inst.ContainerID = cid
	inst.Status = model.StatusStopped
	m.instances[inst.ID] = &inst
	m.mu.Unlock()

	if opt.Start {
		if err := m.rt.Start(ctx, inst.ContainerName()); err != nil {
			// The container exists; leave it recorded as stopped rather than
			// claiming it runs. Do not mark it running on a failed start.
			_ = m.save(false)
			return inst, fmt.Errorf("starting container: %w", err)
		}
		m.mu.Lock()
		inst.Status = model.StatusRunning
		m.instances[inst.ID] = &inst
		m.mu.Unlock()
	}

	if err := m.save(false); err != nil {
		return inst, err
	}
	return inst, nil
}

// rollback drops an instance we failed to finish creating.
func (m *Manager) rollback(id string) {
	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()
	if err := m.save(true); err != nil {
		m.log.Error("rollback save failed", "id", id, "error", err)
	}
}

// Start starts an existing instance.
func (m *Manager) Start(ctx context.Context, id string) error {
	return m.transition(ctx, id, func(inst *model.Instance) error {
		if err := m.rt.Start(ctx, inst.ContainerName()); err != nil {
			if errors.Is(err, runtime.ErrNotFound) {
				inst.Status = model.StatusMissing
				return fmt.Errorf("%w: %s (run `dbctl rm %s` then recreate)", ErrContainerMissing, id, id)
			}
			return err
		}
		inst.Status = model.StatusRunning
		inst.Desired = model.DesiredRunning
		inst.LastExitCode = 0
		return nil
	})
}

// RestartInstance stops and starts an instance. Restarting is the most common
// thing to want after changing a config or recovering from a wedge, and doing
// it as one call keeps desired state consistent throughout -- a stop followed
// by a start would briefly record the instance as deliberately stopped.
func (m *Manager) RestartInstance(ctx context.Context, id string, timeoutSecs uint) error {
	lock := m.lockFor(id)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	inst, ok := m.instances[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrInstanceNotFound, id)
	}

	name := inst.ContainerName()
	if err := m.rt.Stop(ctx, name, m.stopTimeoutFor(*inst, timeoutSecs)); err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			inst.Status = model.StatusMissing
			_ = m.save(false)
			return fmt.Errorf("%w: %s", ErrContainerMissing, id)
		}
		return err
	}
	if err := m.rt.Start(ctx, name); err != nil {
		inst.Status = model.StatusStopped
		_ = m.save(false)
		return fmt.Errorf("restarting %s: %w", id, err)
	}

	inst.Status = model.StatusRunning
	inst.Desired = model.DesiredRunning
	inst.LastExitCode = 0
	return m.save(false)
}

// stopTimeoutFor returns the engine's shutdown budget, falling back to a safe
// default for an engine we no longer recognise.
func (m *Manager) stopTimeoutFor(inst model.Instance, requested uint) uint {
	if requested > 0 {
		return requested
	}
	if e, err := engines.Get(inst.Engine); err == nil && e.StopTimeoutSecs > 0 {
		return e.StopTimeoutSecs
	}
	return 30
}

// Stop stops an instance.
//
// A timeoutSecs of 0 means "use the engine's own budget", which is almost
// always what the caller wants: too short a timeout means SIGKILL mid-write
// and crash recovery on the next start.
func (m *Manager) Stop(ctx context.Context, id string, timeoutSecs uint) error {
	return m.transition(ctx, id, func(inst *model.Instance) error {
		if err := m.rt.Stop(ctx, inst.ContainerName(), m.stopTimeoutFor(*inst, timeoutSecs)); err != nil {
			if errors.Is(err, runtime.ErrNotFound) {
				inst.Status = model.StatusMissing
				return fmt.Errorf("%w: %s", ErrContainerMissing, id)
			}
			return err
		}
		inst.Status = model.StatusStopped
		// An explicit stop is a durable decision: it must survive a reboot,
		// so nothing auto-starts this instance again until the user says so.
		inst.Desired = model.DesiredStopped
		return nil
	})
}

func (m *Manager) transition(ctx context.Context, id string, fn func(*model.Instance) error) error {
	lock := m.lockFor(id)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	inst, ok := m.instances[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrInstanceNotFound, id)
	}

	err := fn(inst)
	if saveErr := m.save(false); saveErr != nil && err == nil {
		err = saveErr
	}
	return err
}

// RemoveOptions controls destruction.
type RemoveOptions struct {
	// WipeData deletes the data directory as well as the container. This is
	// the one irreversible action in DBForge; the CLI requires an explicit
	// flag and a typed confirmation for it (spec 7, phase 3).
	WipeData bool
	Force    bool
}

// Remove destroys an instance's container, and optionally its data.
func (m *Manager) Remove(ctx context.Context, id string, opt RemoveOptions) error {
	lock := m.lockFor(id)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	inst, ok := m.instances[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrInstanceNotFound, id)
	}

	err := m.rt.Remove(ctx, inst.ContainerName(), opt.Force)
	if err != nil && !errors.Is(err, runtime.ErrNotFound) {
		// Podman refuses to remove a running container, and says so in terms
		// of container state. Translate that into the two things the user can
		// actually do about it.
		if isRunningErr(err) {
			return fmt.Errorf("%s is still running: stop it first with `dbctl stop %s`, "+
				"or pass --force to remove it while running", id, id)
		}
		return fmt.Errorf("removing container: %w", err)
	}

	// Only after the container is gone do we consider the data. Deleting data
	// while a container still holds it would be the worst possible ordering.
	if opt.WipeData {
		if inst.DataDir == "" || !filepath.IsAbs(inst.DataDir) {
			return fmt.Errorf("refusing to wipe suspicious data dir %q", inst.DataDir)
		}
		// Delegated to the runtime, not os.RemoveAll: under rootless Podman
		// the engine's files are owned by a subuid we cannot unlink directly.
		if err := m.rt.RemovePath(ctx, inst.DataDir); err != nil {
			return fmt.Errorf("removing data directory: %w", err)
		}
		m.log.Warn("wiped instance data", "id", id, "dir", inst.DataDir)
	}

	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()
	return m.save(false)
}

// Logs streams an instance's container logs.
func (m *Manager) Logs(ctx context.Context, id string, follow bool, tail int, w io.Writer) error {
	inst, err := m.Get(id)
	if err != nil {
		return err
	}
	return m.rt.Logs(ctx, inst.ContainerName(), follow, tail, w)
}

// ConnString returns a ready-to-use connection string for an instance.
func (m *Manager) ConnString(id string) (string, error) {
	inst, err := m.Get(id)
	if err != nil {
		return "", err
	}
	eng, err := engines.Get(inst.Engine)
	if err != nil {
		return "", err
	}
	pw := ""
	for _, k := range []string{"POSTGRES_PASSWORD", "MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD"} {
		if v, ok := inst.Env[k]; ok {
			pw = v
			break
		}
	}
	return eng.ConnString(inst.Port, pw), nil
}

// isRunningErr reports whether a removal failed only because the container is
// still running.
func isRunningErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "is running") || strings.Contains(msg, "containers cannot be removed")
}

func desiredFor(start bool) model.DesiredState {
	if start {
		return model.DesiredRunning
	}
	return model.DesiredStopped
}

// RestoreReport says what Restore did.
type RestoreReport struct {
	Started []string
	Failed  map[string]string
	// Skipped lists instances that could have started but were deliberately
	// left alone, with the reason.
	Skipped map[string]string
}

// Restore brings back instances that should be running.
//
// This is what makes an instance survive a reboot. Rootless Podman does not
// start containers at boot on its own (that needs podman-restart.service or a
// generated unit per container), so the daemon does it from recorded intent
// when the user's systemd session starts it.
//
// It only ever starts instances whose Desired state is running, so a database
// the user stopped stays stopped across reboots.
func (m *Manager) Restore(ctx context.Context) (RestoreReport, error) {
	rep := RestoreReport{Failed: map[string]string{}, Skipped: map[string]string{}}

	m.mu.Lock()
	candidates := make([]model.Instance, 0, len(m.instances))
	for _, i := range m.instances {
		candidates = append(candidates, *i)
	}
	m.mu.Unlock()

	sort.Slice(candidates, func(a, b int) bool { return candidates[a].ID < candidates[b].ID })

	for _, inst := range candidates {
		if inst.Status == model.StatusRunning {
			continue
		}
		if inst.Status == model.StatusMissing {
			rep.Skipped[inst.ID] = "container is missing"
			continue
		}
		if !inst.ShouldAutoStart() {
			rep.Skipped[inst.ID] = fmt.Sprintf("restart=%s desired=%s", inst.Restart, inst.Desired)
			continue
		}

		if inst.LastExitCode != 0 {
			m.log.Warn("restoring an instance that exited uncleanly",
				"id", inst.ID, "exit_code", inst.LastExitCode,
				"hint", "the engine may run crash recovery; see `dbctl logs "+inst.ID+"`")
		}

		if err := m.Start(ctx, inst.ID); err != nil {
			rep.Failed[inst.ID] = err.Error()
			m.log.Error("failed to restore instance", "id", inst.ID, "error", err)
			continue
		}
		rep.Started = append(rep.Started, inst.ID)
		m.log.Info("restored instance", "id", inst.ID, "restart", inst.Restart)
	}
	sort.Strings(rep.Started)
	return rep, nil
}

// Supervise reconciles and restores on an interval until ctx is cancelled.
//
// Restoring only at startup would make "always" mean "always, as of the last
// time the daemon started". This is what makes a crashed instance come back
// while the host stays up -- the job podman's own restart policy would do, if
// it could tell a crash from a deliberate stop.
func (m *Manager) Supervise(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.Reconcile(ctx); err != nil {
				m.log.Warn("supervision reconcile failed", "error", err)
				continue
			}
			rep, err := m.Restore(ctx)
			if err != nil {
				m.log.Warn("supervision restore failed", "error", err)
				continue
			}
			if len(rep.Started) > 0 {
				m.log.Info("supervision restarted instances", "started", rep.Started)
			}
		}
	}
}

// SetRestartPolicy changes an instance's policy.
//
// The policy lives entirely in DBForge's own state; container-level restart
// policy is always "no". So this takes effect immediately, with no need to
// recreate the container.
func (m *Manager) SetRestartPolicy(ctx context.Context, id string, p model.RestartPolicy) error {
	if !model.ValidRestartPolicy(p) {
		return fmt.Errorf("invalid restart policy %q (want one of: no, on-failure, always)", p)
	}
	return m.transition(ctx, id, func(inst *model.Instance) error {
		inst.Restart = p
		return nil
	})
}

func validateID(id string) error {
	if id == "" {
		return errors.New("instance id must not be empty")
	}
	if len(id) > 48 {
		return errors.New("instance id must be 48 characters or fewer")
	}
	for _, r := range id {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("instance id %q may only contain letters, digits, - and _", id)
		}
	}
	return nil
}

func generatePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
