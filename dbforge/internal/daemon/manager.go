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
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// ManagedLabel marks a container as ours. Reconciliation keys off this.
const ManagedLabel = "io.dbforge.managed"

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
}

// Manager is the single source of truth for instance state.
type Manager struct {
	rt    runtime.Runtime
	st    *store.Store
	cfg   Config
	log   *slog.Logger
	clock func() time.Time

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
	return &Manager{
		rt: rt, st: st, cfg: cfg, log: cfg.Log,
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

	m.mu.Lock()
	m.instances = next
	m.mu.Unlock()

	if len(rep.Adopted) > 0 || len(rep.Pruned) > 0 || len(rep.Missing) > 0 {
		// Reconciliation changed our view, so the index is stale. Force the
		// write: we have just read the world, and our view is authoritative.
		if err := m.save(true); err != nil {
			return rep, err
		}
	}
	return rep, nil
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
	return model.Instance{
		ID:          id,
		Engine:      c.Labels["io.dbforge.engine"],
		Version:     c.Labels["io.dbforge.version"],
		DataDir:     c.Labels["io.dbforge.data_dir"],
		Port:        port,
		ContainerID: c.ID,
	}, nil
}

func (m *Manager) save(force bool) error {
	m.mu.Lock()
	out := make([]model.Instance, 0, len(m.instances))
	for _, i := range m.instances {
		out = append(out, *i)
	}
	m.mu.Unlock()

	err := m.st.Save(out, force)
	if errors.Is(err, store.ErrChangedOnDisk) {
		// The user edited the file while we were running. Our in-memory view
		// was just reconciled against Podman, so it is the better truth --
		// but say so loudly rather than clobbering silently (spec 7, phase 2).
		m.log.Warn("config changed on disk; overwriting with reconciled state",
			"path", m.st.Path())
		return m.st.Save(out, true)
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
		TZ: os.Getenv("TZ"),
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
		return nil
	})
}

// Stop stops an instance.
func (m *Manager) Stop(ctx context.Context, id string, timeoutSecs uint) error {
	return m.transition(ctx, id, func(inst *model.Instance) error {
		if err := m.rt.Stop(ctx, inst.ContainerName(), timeoutSecs); err != nil {
			if errors.Is(err, runtime.ErrNotFound) {
				inst.Status = model.StatusMissing
				return fmt.Errorf("%w: %s", ErrContainerMissing, id)
			}
			return err
		}
		inst.Status = model.StatusStopped
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
