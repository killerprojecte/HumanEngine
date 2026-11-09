package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/contextuallearning"
	"github.com/killerprojecte/HumanEngine/backend/internal/mediatasks"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/runtime"
	"net/http"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/httpapi"
	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningowner"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"github.com/killerprojecte/HumanEngine/backend/internal/settingsstore"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"github.com/killerprojecte/HumanEngine/backend/internal/usageview"
	"github.com/killerprojecte/HumanEngine/backend/internal/v1adapterruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// Application owns the safe native graph. Unsupported media fetch/history,
// exposure domains and builtins are recorded in SCOPE.md, not replaced by stubs.
// Consumers use Application as HTTP handler, never bypass its admission barrier.
type Application struct {
	snowRegistry        *snow.Registry
	StateExposures      *stateExposureOwners
	IdentityExposure    *memoryservice.IdentityExposure
	MemoryExposure      *memoryservice.ModelMemoryExposure
	RelationExposure    *memoryservice.RelationExposure
	KnowledgeExposure   *memoryservice.KnowledgeExposure
	WorkspaceSelections *memoryservice.WorkspaceSelectionTracker
	Contextual          *contextuallearning.Proposals
	MediaTasks          *mediatasks.Owner
	Experience          *experienceservice.Service
	ExperienceHints     *experienceservice.RuntimeHints
	ExperienceBinding   *experienceservice.RuntimeBinding
	Engine              *memeengine.Service
	HTTP                *httpapi.Server
	Plugins             *pluginmanager.Manager
	Bridge              *v1adapterruntime.Bridge
	WASM                *plugins.WASMHost
	Diagnostics         *Diagnostics
	SemanticExposure    *memoryservice.SemanticExposure
	Semantic            *memoryservice.SemanticRuntime
	Learning            *learningowner.Runtime
	Maintenance         *workingset.Maintenance
	models              *providertransport.NativeModels
	readPublisher       *recovery.ReadPublisher
	requests            quiescence.Group
	releaseInstallation func() error
	options             Options
	shutdownGate        chan struct{}
	finalized           bool
	finalErr            error
}

func assemble(ctx context.Context, owners Owners, options Options, collator *topiccollation.Collator, release func() error) (app *Application, err error) {
	app = &Application{options: options, releaseInstallation: release, shutdownGate: make(chan struct{}, 1)}
	defer func() {
		if err != nil {
			// If physical termination cannot be established, return the partial owner
			// alongside the error. The caller can retry Shutdown; never close its DB early.
			closeErr := app.Shutdown(context.Background())
			if closeErr == nil {
				app = nil
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	config, err := readConfiguration(ctx, owners.Main, owners.Authority)
	if err != nil {
		return app, err
	}
	sched := scheduler.NewModelScheduler()
	if err = sched.Configure(config.settings["modelConcurrency"].(float64), config.settings["modelCallsPerMinute"].(float64)); err != nil {
		return app, err
	}
	client := &providertransport.Client{HTTP: options.HTTP, Credentials: providertransport.StoredCredentials{Store: owners.Credentials, LookupEnv: options.LookupEnv}, Budgets: owners.Budgets, Images: providertransport.NativeImageSerializer{Compressor: owners.Codec}, Counter: options.Counter}
	if config.settings["budgetExperimentEnabled"] == true {
		cap := int(config.settings["budgetMaxOutputTokens"].(float64))
		client.MaxOutputTokens = &cap
	}
	app.models, err = modelConfiguration(config.models, client, modelservice.Options{Scheduler: sched, Observe: func(e modelservice.Event) {
		emit(options.Logs, observability.Event{Level: "info", Module: "model", Code: "model." + e.Kind, Message: e.Kind, Fields: map[string]any{"modelId": e.ModelID, "attempt": e.Attempt, "reason": e.Code}})
	}})
	if err != nil {
		return app, err
	}
	app.Diagnostics, err = NewDiagnostics(ctx, owners, options)
	if err != nil {
		return app, err
	}
	app.models.Diagnostics = app.Diagnostics.Store
	app.models.Router = func(call context.Context) (*providertransport.NativeModels, error) {
		latest, e := readConfiguration(call, owners.Main, owners.Authority)
		if e != nil {
			return nil, e
		}
		nextClient := *client
		nextClient.MaxOutputTokens = nil
		if latest.settings["budgetExperimentEnabled"] == true {
			cap := int(latest.settings["budgetMaxOutputTokens"].(float64))
			nextClient.MaxOutputTokens = &cap
		}
		next, e := modelConfiguration(latest.models, &nextClient, modelservice.Options{Scheduler: sched, Observe: func(event modelservice.Event) {
			emit(options.Logs, observability.Event{Level: "info", Module: "model", Code: "model." + event.Kind, Message: event.Kind, Fields: map[string]any{"modelId": event.ModelID, "attempt": event.Attempt, "reason": event.Code}})
		}})
		if e != nil {
			return nil, e
		}
		next.Host = app.models.Host
		next.Diagnostics = app.models.Diagnostics
		next.Calls = app.models.Calls
		return next, nil
	}

	calls := modelcalls.New(owners.Main)
	app.models.Calls = calls
	// Capture the actual Experience owner, installed below before any ingress
	// producer starts. Recall participates in the existing source writer, never
	// reopening a root transaction from a scoped runtime callback.
	runtimeAuthority := owners.RuntimeAuthority.AppendRecallPreparation(func(ctx context.Context, ws *recovery.WriteSession, reads botruntime.ReadAuthority, scene botruntime.Scene, id string) (func() error, error) {
		if app.Experience == nil {
			return nil, errors.New("experience_owner_not_installed")
		}
		return app.Experience.RuntimeRecallPreparation()(ctx, ws, reads, scene, id)
	})
	app.Engine, err = memeengine.NewRuntime(owners.Main, owners.Authority, owners.Memes, owners.Habits, owners.Assertions, owners.Sources, owners.Codec, app.models, runtimeAuthority, runtimeSettings(config.settings), owners.Dates, owners.Compare, botruntime.Extension{})
	if err != nil {
		return app, err
	}

	// Public safe Meme has no remote-image/vision-history owner; don't import the
	// private media graph or bind an API absent from the accepted service.
	app.Engine.Runtime.Observe(app.observeRuntime)
	memory, err := app.composeMemory(ctx, owners, config.settings, collator)
	if err != nil {
		return app, err
	}
	// Experience administration and its exact retained-message writer ports are
	// real owners. Model-facing tools still require the full exposure gate suite.
	experiencePorts, e := experienceservice.NativeRuntimePorts(app.Engine.Runtime, owners.Sources, owners.Compare)
	if e != nil {
		return app, e
	}
	app.Experience, e = experienceservice.New(ctx, owners.Main, owners.Authority, experiencePorts)
	if e != nil {
		return app, e
	}
	app.ExperienceHints, e = experienceservice.NewRuntimeHints(app.Experience, owners.Sources, app.Engine.Runtime.PreviewMessage)
	if e != nil {
		return app, e
	}
	experienceExtension, binding, e := app.ExperienceHints.ComposeLifecycle(botruntime.Extension{PlannerContextData: app.experiencePlanner}, func(code string, _ error) {
		emit(options.Logs, observability.Event{Level: "info", Module: "memory", Code: code, Message: code})
	})
	if e != nil {
		return app, e
	}
	app.ExperienceBinding = binding
	// Scene pause is coordinated explicitly before RegisterScene, not twice.
	experienceExtension.SceneChanged = nil
	app.Engine.Runtime.AddExtension(experienceExtension)
	app.MediaTasks = mediatasks.New()
	app.Engine.Runtime.AddExtension(app.mediaTaskExtension())
	app.Learning, err = learningowner.New(learningowner.Dependencies{ManagedSceneChanges: true, Main: owners.Main, Authority: owners.Authority, Assertions: owners.Assertions, Sources: owners.Sources, Memory: app.Semantic.Memory, Habits: owners.Habits, Models: app.models, Read: app.Engine.Read, Foreground: app.Engine.Runtime, Logs: options.Logs, Options: learningruntime.Options{Collator: collator}})
	if err != nil {
		return app, err
	}
	app.Contextual, err = app.Learning.Owner.NewContextual(owners.Compare)
	if err != nil {
		return app, err
	}
	app.Engine.Runtime.AddExtension(app.contextualExtension())
	if err = app.composeModelMemory(owners, memory); err != nil {
		return app, err
	}
	if err = app.composeRelationExposure(owners); err != nil {
		return app, err
	}
	if err = app.composeStateMemory(owners); err != nil {
		return app, err
	}
	if err = app.composeKnowledgeMemory(owners); err != nil {
		return app, err
	}
	if err = app.composeIdentityMemory(owners); err != nil {
		return app, err
	}
	app.registerKnowledgeCapture()
	app.registerContextualTool(memory)
	if err = app.composeSemanticMemory(owners, memory); err != nil {
		return app, err
	}
	app.registerStateMemoryTools(memory)
	app.registerRelationTool(memory)
	effects, err := NewNativeSettingsEffects(memory.projections, memory.workspace, app.Maintenance, app, app.Diagnostics, options.Logs)
	if err != nil {
		return app, err
	}
	effects.snapshot = memory.snapshot
	settingsRuntime, err := NewSettingsRuntime(app.Engine.Runtime, sched, effects)
	if err != nil {
		return app, err
	}
	app.Bridge, err = v1adapterruntime.New(app.Engine, options.Node, options.Diagnostics, options.Host)
	if err != nil {
		return app, err
	}
	app.models.Host = app.Bridge.Host
	app.Plugins, err = pluginmanager.New(owners.Main, owners.Authority, app.Bridge.Host, app.Engine.Runtime, options.Node, options.PluginRoot, options.Diagnostics, options.Logs)
	if err != nil {
		return app, err
	}
	if err = app.Plugins.WithCredentials(owners.Credentials); err != nil {
		return app, err
	}
	app.WASM, err = plugins.NewWASMHost(ctx)
	if err != nil {
		return app, err
	}
	if err = app.Plugins.WithWASM(app.WASM); err != nil {
		return app, err
	}
	builtins := options.Builtins
	if builtins == nil {
		builtins = []string{"typing-delay", "snowluma"}
	}
	for _, id := range builtins {
		if id == "web-read" {
			if err = app.bindWebRead(ctx); err != nil {
				return app, err
			}
		}
		if id == "snowluma" {
			if err = app.bindSnowLuma(ctx, owners); err != nil {
				return app, err
			}
		}
		if err = app.Plugins.RegisterBuiltin(id); err != nil {
			return app, err
		}
	}
	app.readPublisher, err = recovery.NewReadPublisherWithInitializer(ctx, owners.Main, owners.Authority, installation.UsageReadInitializer(owners.Dates))
	if err != nil {
		return app, err
	}
	app.HTTP, err = httpapi.NewWithMemoryAdministration(owners.Main, owners.Authority, owners.Habits, app.Experience, options.AdminToken, options.Logs)
	if err != nil {
		return app, err
	}
	for _, bind := range []func() error{
		func() error { return app.HTTP.WithMemes(app.Engine) },
		func() error { return app.HTTP.WithDiagnostics(app.Diagnostics, app.readPublisher) },
		func() error { return app.HTTP.WithModelCalls(calls, app.readPublisher) },
		func() error { return app.HTTP.WithProviderReads(app.models, app.readPublisher) },
		func() error {
			return app.HTTP.WithRuntimeStatus(app.Engine.Runtime, sched, app.readPublisher, owners.VectorVersion, time.Now())
		},
		func() error {
			return app.HTTP.WithUsage(&usageview.Store{Main: owners.Main, Dates: owners.Dates}, app.readPublisher)
		},
		func() error {
			return app.HTTP.WithConfiguration(owners.Credentials, &settingsstore.Store{Main: owners.Main, Authority: owners.Authority}, settingsRuntime)
		},
		func() error { return app.HTTP.WithPlugins(app.Plugins) },
		func() error {
			return app.HTTP.WithSceneConfiguration(&scenestore.Store{Main: owners.Main, Authority: owners.Authority}, app)
		},
	} {
		if err = bind(); err != nil {
			return app, err
		}
	}
	entries, err := localEntries(ctx, owners, options)
	if err != nil {
		return app, err
	}
	for _, entry := range entries {
		if err = app.Plugins.RegisterV1(ctx, entry); err != nil {
			if ctx.Err() != nil {
				return app, ctx.Err()
			}
			emit(options.Logs, observability.Event{Level: "error", Module: "plugin", Code: "local_load_failed", Message: "Local plugin failed to load", Fields: map[string]any{"operation": "plugin.load", "phase": "import"}})
		}
	}
	// No ingress is enabled before every consumer and shutdown owner is installed.
	if err = app.restorePlugins(ctx, owners); err != nil {
		return app, err
	}
	app.Maintenance.Start()
	emit(options.Logs, observability.Event{Level: "info", Module: "app", Code: "app.safe_graph_started", Message: "Safe native application graph started; remaining migration scope is explicit"})
	return app, nil
}
func (a *Application) LearningSettingsChanged(ctx context.Context) error {
	return a.Learning.SettingsChanged(ctx)
}
func (a *Application) ConfigureSemantic(ctx context.Context) error {
	return a.Semantic.ConfigureSemantic(ctx)
}
func (a *Application) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	done, ok := a.requests.Start()
	if !ok {
		a.HTTP.ServeUnavailable(w, r)
		return
	}
	defer done()
	a.HTTP.ServeHTTP(w, r)
}
func (a *Application) Shutdown(ctx context.Context) error {
	a.requests.Seal()
	select {
	case a.shutdownGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-a.shutdownGate }()
	if a.finalized {
		return a.finalErr
	}
	// Stop plugin producers first. Accepted HTTP is then joined before sealing
	// owner callbacks that a committed settings request can still invoke.
	stages := []struct {
		name  string
		close func(context.Context) error
	}{}
	if a.Plugins != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"plugins", a.Plugins.Close})
	}
	if a.WASM != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"wasm", a.WASM.Close})
	}
	if a.Bridge != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"bridge", a.Bridge.CloseAndWait})
	}
	stages = append(stages, struct {
		name  string
		close func(context.Context) error
	}{"http", a.requests.Wait})
	if a.Engine != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"runtime", a.Engine.Runtime.CloseAndWait})
	}
	if a.ExperienceBinding != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"experience", a.ExperienceBinding.CloseAndWait})
	}
	if a.Contextual != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"contextual", a.Contextual.CloseAndWait})
	}
	if a.MediaTasks != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"media_tasks", a.MediaTasks.CloseAndWait})
	}
	if a.Learning != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"learning", a.Learning.CloseAndWait})
	}
	if a.Semantic != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"semantic", a.Semantic.CloseAndWait})
	}
	if a.Maintenance != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"maintenance", a.Maintenance.CloseAndWait})
	}
	if a.models != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"models", a.models.CloseAndWait})
	}
	if a.readPublisher != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"read_publication", a.readPublisher.CloseAndWait})
	}
	if a.Diagnostics != nil {
		stages = append(stages, struct {
			name  string
			close func(context.Context) error
		}{"diagnostics", a.Diagnostics.CloseAndWait})
	}
	for _, stage := range stages {
		emit(a.options.Logs, observability.Event{Level: "info", Module: "app", Code: "app.shutdown_wait", Stage: stage.name, Message: "Waiting for native owner drain"})
		if err := stage.close(ctx); err != nil {
			emit(a.options.Logs, observability.Event{Level: "error", Module: "app", Code: "app.shutdown_incomplete", Stage: stage.name, Message: "Physical owner remains; installation retained"})
			return err
		}
	}
	if a.releaseInstallation != nil {
		a.finalErr = a.releaseInstallation()
	}
	a.finalized = true
	emit(a.options.Logs, observability.Event{Level: "info", Module: "app", Code: "app.drained", Message: "Native application owners drained"})
	// Logger is borrowed by Open; its caller flushes after receiving shutdown's
	// terminal result. This allows a failed startup to report its final diagnostic.
	return a.finalErr
}
func emit(logs *observability.Logger, event observability.Event) {
	if logs != nil {
		_ = logs.Emit(event)
	}
}

// ShutdownAndFlush is the owning launcher boundary. Shutdown alone deliberately
// borrows the logger (including when several test/app lifetimes share it). A
// launcher owning that logger calls this method and receives real flush errors;
// cancellation never turns incomplete physical shutdown into a successful exit.
func (a *Application) ShutdownAndFlush(ctx context.Context) error {
	if err := a.Shutdown(ctx); err != nil {
		return err
	}
	if a.options.Logs != nil {
		return errors.Join(a.finalErr, a.options.Logs.Close(ctx))
	}
	return a.finalErr
}

func (a *Application) observeRuntime(e botruntime.Event) {
	level := e.Level
	if level == "" {
		level = "info"
	}
	fields := map[string]any{"counts": e.Counts, "tool": e.ToolName, "failureCode": e.FailureCode}
	if e.ReceiptState != "" {
		fields["receiptState"] = e.ReceiptState
	}
	emit(a.options.Logs, observability.Event{Level: level, Module: "runtime", Code: "runtime." + e.Stage + "." + e.Status, Message: e.Reason, SceneID: e.SceneID, TurnID: e.TurnID, Stage: e.Stage, Fields: fields})
}
