package pluginmanager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

type item struct {
	admissionGate      sync.Mutex
	hostGeneration     uint64
	builtin            bool
	manifest           *jsjson.Object
	path               string
	enabled, desired   bool
	revision           uint64
	ctx                context.Context
	cancel             context.CancelFunc
	tail               chan struct{}
	cleanups           []func()
	pages              []any
	actions            map[string]string
	failure, requestID string
}
type Manager struct {
	removeHostObserver func()
	credentials        *credentials.Store
	wasm               *plugins.WASMHost
	logs               *observability.Logger
	life               context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	main               *sql.DB
	authority          *recovery.Authority
	host               *plugins.V1Host
	runtime            *botruntime.Runtime
	node               string
	diagnostics        io.Writer
	root               string
	items              map[string]*item
	order              []string
	cache              *toolresults.Cache
	removeCache        func()
	work               quiescence.Group
	closed             bool
	closeOnce          sync.Once
	closeDone          chan struct{}
	closeErr           error
}

// New borrows the application's exact existing host/runtime/database owners.
// Close drains this manager's work; the application still owns Bridge.CloseAndWait.
func New(main *sql.DB, a *recovery.Authority, host *plugins.V1Host, runtime *botruntime.Runtime, node, root string, diagnostics io.Writer, logs ...*observability.Logger) (*Manager, error) {
	if main == nil || a == nil || host == nil || runtime == nil || node == "" {
		return nil, errors.New("plugin_manager_dependencies_required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	m := &Manager{life: life, cancel: cancel, main: main, authority: a, host: host, runtime: runtime, node: node, root: root, diagnostics: diagnostics, items: map[string]*item{}, cache: toolresults.New(toolresults.Config{}), closeDone: make(chan struct{})}
	if len(logs) > 0 {
		m.logs = logs[0]
	}
	m.removeCache = runtime.AddExtension(m.cacheExtension())
	m.removeHostObserver = host.ObserveRuntimeStops(m.runtimeStopped)
	return m, nil
}
func (m *Manager) OwnedBy(main *sql.DB, a *recovery.Authority) bool {
	return m != nil && m.main == main && m.authority == a
}
func prop(o *jsjson.Object, key string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(key)
	return v
}
func text(o *jsjson.Object, key string) string { v, _ := prop(o, key).(string); return v }
func schema(i *item) *jsjson.Object {
	v, _ := prop(i.manifest, "configSchema").(*jsjson.Object)
	return v
}
func clone(o *jsjson.Object) *jsjson.Object {
	raw, _ := jsjson.Stringify(o)
	v, _ := jsjson.Parse(raw)
	return v.(*jsjson.Object)
}
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func (m *Manager) require(id string) (*item, error) {
	i := m.items[id]
	if i == nil {
		return nil, errors.New("plugin_not_found")
	}
	return i, nil
}
func (m *Manager) admission() (func(), error) {
	done, ok := m.work.Start()
	if !ok {
		return nil, errors.New("plugin_manager_closed")
	}
	return done, nil
}
func reserve(i *item) (<-chan struct{}, func()) {
	previous := i.tail
	next := make(chan struct{})
	i.tail = next
	return previous, func() { close(next) }
}

// RegisterV1 is explicit local installation/inspection. It starts only a temporary
// inspector, never the shared runtime. Entry is confined to the configured root.
func (m *Manager) RegisterV1(ctx context.Context, entry string) error {
	done, err := m.admission()
	if err != nil {
		return err
	}
	defer done()
	ctx, ownerStop := lifetime(ctx, m.life)
	defer ownerStop()
	path := filepath.Join(m.root, entry)
	relative, err := filepath.Rel(m.root, path)
	if err != nil || strings.HasPrefix(relative, "..") || !strings.HasSuffix(path, ".mjs") {
		return errors.New("invalid_local_plugin_path")
	}
	result, err := m.host.InspectExplicitV1(ctx, plugins.InspectionRequest{Operation: "manifest", Path: path})
	if err != nil {
		return err
	}
	if result.Failed {
		return inspectError(result, "")
	}
	value, err := jsjson.Parse(string(result.Manifest))
	if err != nil {
		return err
	}
	manifest, ok := value.(*jsjson.Object)
	if !ok {
		return errors.New("invalid_plugin_module")
	}
	id := text(manifest, "id")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("plugin_manager_closed")
	}
	if !validID(id) || m.items[id] != nil {
		return errors.New("invalid_plugin_id")
	}
	tail := make(chan struct{})
	close(tail)
	life, cancel := context.WithCancel(context.Background())
	m.items[id] = &item{manifest: manifest, path: path, tail: tail, ctx: life, cancel: cancel, pages: []any{}, actions: map[string]string{}}
	m.order = append(m.order, id)
	return nil
}
func inspectError(result plugins.Inspection, id string) error {
	if result.Configuration {
		issues := []Issue{}
		for _, issue := range result.Issues {
			issues = append(issues, Issue{Field: issue.Field, Reason: issue.Reason})
		}
		return &ConfigurationError{RequestID: id, Issues: issues}
	}
	e := NewStartupError(result.Code, map[string]any{"systemCode": result.SystemCode, "httpStatus": result.HTTPStatus})
	e.RequestID = id
	return e
}
func (m *Manager) inspect(ctx context.Context, id string, i *item, operation string, incoming, previous *jsjson.Object, requestID string) (plugins.Inspection, error) {
	m.reconcileRuntime(id, i)
	raw, err := jsjson.Marshal(incoming)
	if err != nil {
		return plugins.Inspection{}, err
	}
	old, err := jsjson.Marshal(previous)
	if err != nil {
		return plugins.Inspection{}, err
	}
	if i.builtin {
		return m.inspectBuiltin(i, operation, incoming, previous, requestID)
	}
	request := plugins.InspectionRequest{Operation: operation, ID: id, Path: i.path, Config: raw, Previous: old}
	m.mu.Lock()
	enabled := i.enabled
	revision := i.revision
	m.mu.Unlock()
	var result plugins.Inspection
	if enabled {
		result, err = m.host.InspectLoadedV1(ctx, id, request)
		if errors.Is(err, plugins.ErrNotLoaded) && ctx.Err() == nil {
			m.reconcileRuntime(id, i)
			m.mu.Lock()
			stillEnabled := i.enabled
			m.mu.Unlock()
			if !stillEnabled {
				result, err = m.host.InspectExplicitV1(ctx, request)
			}
		}
	} else {
		result, err = m.host.InspectExplicitV1(ctx, request)
	}
	if err != nil && ctx.Err() != nil && enabled {
		m.retireNativeGeneration(id, i, revision)
	}
	if err == nil && result.Failed {
		err = inspectError(result, requestID)
	}
	return result, err
}

// List is an explicit management read. Disabled configuredSecrets hooks run in
// a one-shot inspector; no background poll or resident V1 process is created.
func (m *Manager) List(ctx context.Context) ([]any, error) {
	done, err := m.admission()
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, ownerStop := lifetime(ctx, m.life)
	defer ownerStop()
	m.mu.Lock()
	ids := append([]string{}, m.order...)
	m.mu.Unlock()
	out := []any{}
	for _, id := range ids {
		m.mu.Lock()
		i := m.items[id]
		m.mu.Unlock()
		inspected, err := m.inspect(ctx, id, i, "secrets", new(jsjson.Object), new(jsjson.Object), "")
		if err != nil {
			return nil, err
		}
		flags, err := jsjson.Parse(string(inspected.ConfiguredSecrets))
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		entry := clone(i.manifest)
		version := float64(1)
		if i.builtin {
			version = 2
		}
		_ = entry.Set("apiVersion", version)
		_ = entry.Set("configSchema", pluginconfig.PublicSchema(schema(i)))
		_ = entry.Set("builtin", i.builtin)
		_ = entry.Set("enabled", i.enabled)
		_ = entry.Set("configuredSecrets", flags)
		pages := []any{}
		for _, page := range i.pages {
			pages = append(pages, clone(page.(*jsjson.Object)))
		}
		_ = entry.Set("pages", pages)
		if i.failure != "" {
			_ = entry.Set("error", i.failure)
			_ = entry.Set("errorRequestId", i.requestID)
		}
		m.mu.Unlock()
		out = append(out, entry)
	}
	return out, nil
}

func (m *Manager) Config(ctx context.Context, id string) (any, error) {
	done, err := m.admission()
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, ownerStop := lifetime(ctx, m.life)
	defer ownerStop()
	m.mu.Lock()
	i, err := m.require(id)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	value, err := m.readConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	safe, _ := pluginconfig.Redact(value, schema(i))
	return safe, nil
}
func (m *Manager) Configure(ctx context.Context, id string, input *jsjson.Object, requestID string) (any, error) {
	done, err := m.admission()
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, ownerStop := lifetime(ctx, m.life)
	defer ownerStop()
	m.mu.Lock()
	i, err := m.require(id)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	i.admissionGate.Lock()
	gateHeld := true
	releaseGate := func() {
		if gateHeld {
			gateHeld = false
			i.admissionGate.Unlock()
		}
	}
	defer releaseGate()
	input = clone(input)
	previous, err := m.readConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	operation, err := m.beginConfiguration(ctx, id, i, input, previous, requestID)
	if err != nil {
		return nil, err
	}
	defer operation.close()
	m.mu.Lock()
	i.revision++
	revision := i.revision
	i.cancel()
	prior, finish := reserve(i)
	m.mu.Unlock()
	releaseGate()
	if !i.builtin {
		m.host.AbortPlugin(id)
	}
	select {
	case <-prior:
		defer func() { operation.close(); finish() }()
	case <-ctx.Done():
		// Keep the queue fence ordered after the cancelled operation's predecessor,
		// but release its inspector now rather than keeping Node while queued.
		done, _ := m.work.Child()
		go func() {
			if done != nil {
				defer done()
			}
			<-prior
			finish()
		}()
		return nil, ctx.Err()
	}
	previous, err = m.readConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	err = operation.validate(previous)
	if err == nil {
		if i.builtin {
			var value any
			value, err = operation.prepare()
			if err == nil {
				err = m.writeBuiltinConfiguration(ctx, id, value.(*jsjson.Object), previous)
			}
		} else {
			err = m.writePreparedConfig(ctx, id, operation.prepare)
		}
	}

	if err != nil {
		var configuration *ConfigurationError
		if errors.As(err, &configuration) {
			return nil, err
		}
		m.cleanup(id, i)
		m.mu.Lock()
		i.desired = false
		m.mu.Unlock()
		_ = m.writeEnabled(context.Background(), id, false)
		return nil, m.failure(i, err, requestID, "plugin.configure", "persist_configuration")
	}
	if err = m.cleanup(id, i); err != nil {
		return nil, err
	}
	i.admissionGate.Lock()
	gateHeld = true
	m.mu.Lock()
	restart := i.desired && i.revision == revision
	m.mu.Unlock()
	if restart {
		if err = m.startWithSession(ctx, id, i, revision, requestID, operation, releaseGate); err != nil {
			return nil, err
		}
	} else {
		operation.close()
		releaseGate()
	}
	releaseGate()
	value, err := m.readConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	safe, _ := pluginconfig.Redact(value, schema(i))
	return safe, nil
}
func (m *Manager) Enable(ctx context.Context, id, requestID string) error {
	done, err := m.admission()
	if err != nil {
		return err
	}
	defer done()
	ctx, ownerStop := lifetime(ctx, m.life)
	defer ownerStop()
	m.mu.Lock()
	i, err := m.require(id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.reconcileRuntime(id, i)
	i.admissionGate.Lock()
	m.mu.Lock()
	revision := i.revision
	i.desired = true
	prior, finish := reserve(i)
	m.mu.Unlock()
	i.admissionGate.Unlock()
	defer finish()
	<-prior
	m.mu.Lock()
	current := i.desired && i.revision == revision
	enabled := i.enabled
	m.mu.Unlock()
	if !current {
		return NewStartupError("plugin_disabled", nil)
	}
	if enabled {
		return nil
	}
	i.admissionGate.Lock()
	var once sync.Once
	release := func() { once.Do(i.admissionGate.Unlock) }
	defer release()
	m.mu.Lock()
	valid := i.desired && i.revision == revision
	m.mu.Unlock()
	if !valid {
		return NewStartupError("plugin_disabled", nil)
	}
	return m.startWithSession(ctx, id, i, revision, requestID, nil, release)
}
func (m *Manager) Disable(ctx context.Context, id, requestID string) error {
	done, err := m.admission()
	if err != nil {
		return err
	}
	defer done()
	ctx, ownerStop := lifetime(ctx, m.life)
	defer ownerStop()
	err = m.disable(ctx, id, true)
	if err == nil {
		m.log("info", id, "plugin_disabled", requestID, "plugin.disable", "cleanup")
	}
	return err
}
func (m *Manager) disable(ctx context.Context, id string, persist bool) error {
	m.mu.Lock()
	i, err := m.require(id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	i.admissionGate.Lock()
	m.mu.Lock()
	i.desired = false
	i.revision++
	i.cancel()
	i.enabled = false
	prior, finish := reserve(i)
	m.mu.Unlock()
	if !i.builtin {
		m.host.DetachConfigurationModule(id)
	}
	i.admissionGate.Unlock()
	defer finish()
	if persist {
		err = m.writeEnabled(ctx, id, false)
	}
	<-prior
	return errors.Join(err, m.cleanup(id, i))
}
func (m *Manager) failure(i *item, err error, requestID, operation, phase string) error {
	var config *ConfigurationError
	var failure *StartupError
	if errors.As(err, &config) {
		config.RequestID = requestID
	} else if !errors.As(err, &failure) {
		failure = NewStartupError("plugin_setup_failed", nil)
		failure.RequestID = requestID
		err = failure
	} else {
		failure.RequestID = requestID
	}
	m.mu.Lock()
	i.failure = err.Error()
	i.requestID = requestID
	i.desired = false
	m.mu.Unlock()
	m.log("error", text(i.manifest, "id"), err.Error(), requestID, operation, phase)
	return err
}
func (m *Manager) cleanup(id string, i *item) error {
	m.mu.Lock()
	i.hostGeneration = 0
	i.enabled = false
	i.cancel()
	cleanups := i.cleanups
	i.cleanups = nil
	i.pages = []any{}
	i.actions = map[string]string{}
	m.mu.Unlock()
	m.cache.InvalidatePlugin(id)
	for index := len(cleanups) - 1; index >= 0; index-- {
		cleanups[index]()
	}
	if i.builtin {
		return m.wasm.Disable(context.Background(), id)
	}
	return m.host.RetireManagedRuntime(context.Background(), id)
}
func (m *Manager) start(ctx context.Context, id string, i *item, revision uint64, requestID string) error {
	return m.startWithSession(ctx, id, i, revision, requestID, nil, nil)
}
func (m *Manager) startWithSession(ctx context.Context, id string, i *item, revision uint64, requestID string, operation *configurationSession, admitted func()) (err error) {
	m.log("info", id, "plugin_starting", requestID, "plugin.enable", "setup")
	m.mu.Lock()
	life, cancel := context.WithCancel(context.Background())
	i.ctx = life
	i.cancel = cancel
	i.failure = ""
	i.requestID = ""
	m.mu.Unlock()
	defer func() {
		if err != nil {
			m.cleanup(id, i)
			m.mu.Lock()
			current := i.revision == revision && i.desired
			m.mu.Unlock()
			if current {
				_ = m.writeEnabled(context.Background(), id, false)
				err = m.failure(i, err, requestID, "plugin.enable", "setup")
			} else {
				failure := NewStartupError("plugin_disabled", nil)
				failure.RequestID = requestID
				err = failure
			}
		}
	}()
	config, err := m.readConfig(ctx, id)
	if err != nil {
		return err
	}
	raw, err := jsjson.Marshal(config)
	if err != nil {
		return err
	}
	load, cancelLoad := context.WithCancel(ctx)
	stop := context.AfterFunc(life, cancelLoad)
	defer stop()
	defer cancelLoad()
	if life.Err() != nil {
		cancelLoad()
	}
	if i.builtin {
		if admitted != nil {
			admitted()
		}
		err = m.startBuiltin(load, id, i, revision, raw)
	} else {
		install := func(registrations json.RawMessage) error { return m.install(id, i, revision, registrations) }
		preserve := func() bool { return ctx.Err() == nil && !current(m, i, revision) }
		var err error
		if operation != nil && operation.operation != nil && operation.operation.CanResume() {
			_, err = operation.operation.Resume(load, raw, requestID, install, admitted, preserve)
		} else {
			_, err = m.host.EnableManagedJSONWithAdmission(load, id, i.path, raw, requestID, install, admitted, preserve)
		}
		if err != nil {
			var invalid *plugins.V1ConfigurationError
			if errors.As(err, &invalid) {
				return &ConfigurationError{RequestID: requestID, Issues: []Issue{}}
			}
			return err
		}
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	if !i.desired || i.revision != revision || life.Err() != nil {
		m.mu.Unlock()
		return NewStartupError("plugin_disabled", nil)
	}
	// Store.set is synchronous in the source. Keep enable publication and its
	// durable flag indivisible relative to a new disable admission.
	err = m.writeEnabled(ctx, id, true)
	if err == nil {
		i.enabled = true
	}
	m.mu.Unlock()
	if err == nil {
		m.log("info", id, "plugin_started", requestID, "plugin.enable", "ready")
	}

	return err
}
func (m *Manager) Action(ctx context.Context, id, name string, input *jsjson.Object) (any, error) {
	done, err := m.admission()
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, ownerStop := lifetime(ctx, m.life)
	defer ownerStop()
	m.mu.Lock()
	i, err := m.require(id)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	life := i.ctx
	generation := i.hostGeneration
	handle := i.actions[name]
	enabled := i.enabled && life.Err() == nil
	m.mu.Unlock()
	if !enabled {
		return nil, NewStartupError("plugin_disabled", nil)
	}
	if handle == "" {
		return nil, errors.New("action_missing")
	}
	raw, err := jsjson.Marshal(input)
	if err != nil {
		return nil, err
	}
	call, cancel := lifetime(ctx, life)
	defer cancel()
	result, err := m.host.CallGeneration(call, id, generation, handle, []any{json.RawMessage(raw)})
	if life.Err() != nil {
		return nil, NewStartupError("plugin_disabled", nil)
	}
	if err != nil {
		return nil, err
	}
	return jsjson.Parse(string(result))
}
func (m *Manager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.work.Seal()
		m.cancel()
		m.mu.Lock()
		m.closed = true
		ids := append([]string{}, m.order...)
		for _, i := range m.items {
			i.cancel()
		}
		m.mu.Unlock()
		go func() {
			for _, id := range ids {
				m.closeErr = errors.Join(m.closeErr, m.disable(context.Background(), id, false))
			}
			m.closeErr = errors.Join(m.closeErr, m.work.Wait(context.Background()))
			m.removeHostObserver()
			m.removeCache()
			m.closeErr = errors.Join(m.closeErr, m.cache.Dispose())
			close(m.closeDone)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.closeDone:
		return m.closeErr
	}
}

func (m *Manager) log(level, id, code, requestID, operation, phase string) {
	if m.logs != nil {
		_ = m.logs.Emit(observability.Event{Level: level, Module: "plugin:" + id, Code: code, RequestID: requestID, Fields: map[string]any{"operation": operation, "phase": phase}})
	}
}
