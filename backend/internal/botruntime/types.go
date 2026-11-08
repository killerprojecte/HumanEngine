// Package botruntime owns native scene/turn orchestration. It does not substitute
// test authorization for the unfinished durable source, provider or plugin domains.
package botruntime

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/intltime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/promptcache"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/topicroute"
	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type Scene struct {
	dispatch.SceneRef
	ParticipationEnabled, LearningEnabled, ProactiveEnabled bool
}
type Settings struct {
	Persona, Timezone                                                                        string
	CacheEnabled, PromptContextWindowEnabled, ReplySuggestionsEnabled, TopicAdmissionEnabled bool
	CacheHours                                                                               float64
	DebounceMS, MaxWaitMS, GroupCooldownMS, DirectCooldownMS                                 float64
	SceneConcurrency, TopicHistoryMaxMessages                                                int
	Window                                                                                   promptcache.Options
	PlannerEvidenceMaxBytes, ReplyEvidenceMaxBytes                                           float64
	PlannerTimeoutMS, ReplyerTimeoutMS                                                       float64
	// The configured timezone provider supplies exactly Intl's h23 hour. No host
	// timezone inference or Node subprocess is performed by the runtime.
	Hour                 func(nowMS float64, timezone string) (int, error)
	ParticipationWindows []ParticipationWindow
}
type ParticipationWindow struct {
	StartHour, EndHour int
	Inclination        string
}

// DefaultSettings retains source product defaults. Configuration/service routes
// still validate the complete settings schema before publishing a replacement.
func DefaultSettings() Settings {
	schemas, err := contract.SharedSchemas()
	if err != nil {
		panic(err)
	}
	defaults, err := schemas.Parse("Settings", contract.DefaultSettingsInput())
	if err != nil {
		panic(err)
	}
	number := func(key string) float64 { return defaults[key].(float64) }
	c := Settings{
		Persona: defaults["persona"].(string), Timezone: defaults["timezone"].(string),
		CacheEnabled: defaults["cacheEnabled"].(bool), PromptContextWindowEnabled: defaults["promptContextWindowEnabled"].(bool), ReplySuggestionsEnabled: defaults["replySuggestionsEnabled"].(bool), TopicAdmissionEnabled: defaults["topicAdmissionEnabled"].(bool),
		CacheHours: number("cacheHours"), DebounceMS: number("debounceMs"), MaxWaitMS: number("maxWaitMs"), GroupCooldownMS: number("groupTurnCooldownMs"), DirectCooldownMS: number("directTurnCooldownMs"), SceneConcurrency: int(number("sceneTurnConcurrency")), TopicHistoryMaxMessages: int(number("topicHistoryMaxMessages")),
		Window: promptcache.Options{MaxMessages: int(number("promptContextWindowMaxMessages")), RetainMessages: int(number("promptContextWindowRetainMessages")), MaxBytes: int(number("promptContextWindowMaxBytes"))}, PlannerEvidenceMaxBytes: number("plannerEvidenceMaxBytes"), ReplyEvidenceMaxBytes: number("replyEvidenceMaxBytes"), PlannerTimeoutMS: 180000, ReplyerTimeoutMS: 180000,
	}
	for _, value := range defaults["participationWindows"].([]any) {
		w := value.(map[string]any)
		c.ParticipationWindows = append(c.ParticipationWindows, ParticipationWindow{int(w["startHour"].(float64)), int(w["endHour"].(float64)), w["inclination"].(string)})
	}
	return c
}

// Authority is a real service contract, NOT an optional permissive guard. Its
// fixed synchronous reads must take fresh recovery/source snapshots. It may not
// cache an authorization token across callbacks or model/tool waits. Implementors
// own enrollment, identity normalization, ordered edit CAS, tombstones and ACKs.
// Observe returns the trusted replacement body, including its sourceEventRef.
// It must ignore caller-forged sourceEventRef on ordinary ingress.
// Recall invokes prepare before its root tombstone, apply after it, within the
// same durable authority transaction. Even prepare failure must record the root
// recall intent and quarantine unacknowledged state, matching Store.recall.
type ReadAuthority interface {
	Ready() error
	SceneParticipationEnabled(Scene) (bool, error)
	ValidateSources(Scene, []*Object, []string) (bool, error)
	IsRecalled(Scene, string) (bool, error)
	PromptAuthority(Scene, []*Object) (any, error)
	ProjectDisplay(Scene, []*Object, []string) ([]*Object, error)
}
type Authority interface {
	ReadAuthority
	// RouteIngress discovers the scene and observes identity before admission.
	RouteIngress(context.Context, Scene, *Object) error
	Observe(context.Context, Scene, *Object, *Object) (status string, trusted *Object, err error)
	Recall(context.Context, Scene, string, func(ReadAuthority) (func() error, error), func() error) error
}

type Completion struct {
	replyplan.Completion
	Text string
}
type ModelRequest struct {
	Role, Stage     string
	Messages, Tools []*Object
	Turn            *TurnContext
	FirstContent    func()
}

// Start/Execute must initiate promptly and settle once. An uncooperative pending
// operation cannot retain scene ownership or block cancellation/query publication.
type Models interface {
	Start(context.Context, ModelRequest, func(Completion, error))
}
type ModelFunc func(context.Context, ModelRequest, func(Completion, error))

func (f ModelFunc) Start(c context.Context, r ModelRequest, s func(Completion, error)) { f(c, r, s) }

type Extension struct {
	// SentSequencers reserve only controlled bookkeeping alongside ACK publication.
	SentSequencers []*SentSequencer
	// Sent runs after retained-row publication, without scene ownership. Queue domain work here.
	Sent               func(Scene, string, string)
	Closed             func()
	SceneChanged       func(Scene)
	Tools              []*Object
	ContextPrompt      func(*TurnContext) (string, error)
	ContextData        func(*TurnContext) (any, error)
	PlannerContextData func(*TurnContext, []*Object) (any, error)
	DescribeImages     func(context.Context, *TurnContext, []*Object, func(any, error))
	Execute            func(context.Context, string, any, *TurnContext, func(any, error))
	Delivered          func(string, any, *TurnContext, any) (any, error)
	ContextSelected    func(*TurnContext, *Object) error
	Received           func(*Object, Scene) error
	Edited             func(*Object, *Object, Scene) error
	PrepareRecall      func(string, string) (func() error, error)
	Recalled           func(string, string) error
	// Scoped hooks receive fixed reads of the recall transaction, never SQL.
	PrepareRecallScoped func(ReadAuthority, string, string) (func() error, error)
	RecalledScoped      func(ReadAuthority, string, string) error
}
type Hook struct {
	ID       string
	Priority float64
	Run      func(context.Context, []dispatch.Segment, *TurnContext, func(HookResult, error))
}
type HookResult struct {
	Cancel   bool
	Segments []dispatch.Segment
}
type Adapter struct {
	Transport  *dispatch.Adapter
	BotUserID  string
	History    func(context.Context, Scene, int, func(*Object, error))
	GetMessage func(context.Context, Scene, string, func(*Object, error))
	Dispose    func()
}
type Services struct {
	Models      Models
	Policy      *replyplan.Policy
	Dates       *jsdate.Parser
	Compare     topicroute.LocaleCompare
	Authority   Authority // Required for recovery/config/source reads in BOTH modes.
	Clock       modelservice.Clock
	Trace       func(Event)
	TypingDelay dispatch.TypingDelay
	// PromptBreakpoints is the real model configuration's capability decision.
	PromptBreakpoints func(role string) bool
}
type Event struct {
	Level, Module, Code                              string
	SceneID, TurnID, Stage, Status, Reason, ToolName string
	Counts                                           map[string]int
}
type TurnResult struct {
	TurnID, Status, Reason string
	Counts                 map[string]int
	Receipts               []dispatch.Receipt
	Err                    error
	CleanupErr             error
}
type TurnHandle struct {
	Done   <-chan struct{}
	result *TurnResult
}

func (h *TurnHandle) Wait() TurnResult { <-h.Done; return *h.result }

type Runtime struct {
	observer                      atomic.Pointer[func(Event)]
	work                          quiescence.Group
	sequence                      atomic.Uint64
	services                      Services
	settings                      atomic.Pointer[Settings]
	lifecycleMu                   sync.Mutex // only registration/owner publication and close transition
	transports                    sync.Map   // string -> *sceneRegistration, metadata without runtime state
	scenes                        sync.Map   // string -> *sceneState, never held over external work
	adapters                      sync.Map   // string -> *Adapter, immutable registration identity
	extensionMu                   sync.RWMutex
	extensions                    []*Extension
	hooks                         []Hook
	cacheMu                       sync.Mutex
	windows                       promptcache.Windows
	boundaries                    promptcache.Boundaries
	closed                        atomic.Bool
	lastReply                     atomic.Int64
	operationFirst, operationLast *receiptOperation
	operationMu                   sync.Mutex
	operations                    topicroute.OrderedMap[string, *receiptOperation]
	cleanupMu                     sync.Mutex
	cleanup                       modelservice.Timer
}
type topic struct {
	id                string
	version           atomic.Uint64
	messageIDs        []string
	lastAt, attention float64
}
type sceneRegistration struct {
	mu    sync.Mutex
	scene atomic.Pointer[Scene]
}

type sceneState struct {
	simulationAdmission       sync.Mutex
	byID                      map[string]*messageRecord
	byBody                    map[*Object]*messageRecord
	available                 map[string]bool
	earliest, historyEarliest float64
	historyDirty              bool
	parsedBodies, trimVisits  uint64
	operationKeys             map[string]bool
	sequence                  uint64
	mu                        sceneOwnerMutex
	publicationMu             sync.Mutex
	retentionDirty            atomic.Bool
	ingress                   sync.Mutex // durable change ordering only; queries never acquire it
	scene                     Scene
	closed                    atomic.Bool
	messages                  []*Object // immutable bodies, actor-owned ordered references
	history                   *messageBlock
	view                      atomic.Pointer[sceneView]
	topics                    topicroute.OrderedMap[string, *topic]
	routing                   topicroute.RoutingState
	historyIDs                topicroute.OrderedMap[string, float64]
	tasks                     map[string]*turn
	admissions                map[string]*turnwait.Coordinator
	timers                    map[string]*timerTicket
	queue                     topicroute.OrderedMap[string, struct{}]
	pending                   map[string]bool
	recalled                  map[string]bool
	lastTouched               float64
	lastAutomatic             map[string]float64
}
type timerTicket struct{ timer modelservice.Timer }
type receiptOperation struct {
	previous, next *receiptOperation
	key            string
	owner          *turn
	receipts       []dispatch.Receipt
}
type turn struct {
	drainDone    func()
	toolIssues   []error
	records      map[*Object]*messageRecord
	operations   topicroute.OrderedMap[string, *receiptOperation]
	outcomeCount int
	runtime      *Runtime
	state        *sceneState
	topic        *topic
	context      *TurnContext
	settings     Settings
	wake         *turnwait.Wake
	proactive    bool
	version      uint64
	done         chan struct{}
	result       *TurnResult
	waitUntil    *float64
	sendMu       sync.Mutex
	guard        *sendGuard
}
type TurnContext struct {
	extensionStateClosed bool
	extensionState       map[any]any
	Scene                Scene
	Mode, TurnID         string
	root                 *dispatch.Signal
	stage                atomic.Pointer[stage]
	owner                *turn
	mu                   sync.Mutex
	dependencies         map[string]bool
	dependencyOrder      []string
	validations          []func(ReadAuthority) (bool, error)
	sources              []*Object
	sourceTimes          []float64
	ImagePriorityIDs     []string // turn-owned; extension callbacks must not mutate
	deferMemoryExposure  atomic.Bool
}

func (c *TurnContext) Signal() *dispatch.Signal {
	if s := c.stage.Load(); s != nil {
		return s.signal
	}
	return c.root
}
func (c *TurnContext) AddDependency(id string) {
	c.mu.Lock()
	if !c.dependencies[id] {
		c.dependencies[id] = true
		c.dependencyOrder = append(c.dependencyOrder, id)
	}
	c.mu.Unlock()
}
func (c *TurnContext) AddValidation(f func() bool) {
	c.mu.Lock()
	c.validations = append(c.validations, func(ReadAuthority) (bool, error) { return f(), nil })
	c.mu.Unlock()
}
func (c *TurnContext) Sources() []*Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := []*Object{}
	for _, m := range c.sources {
		r = append(r, cloneJSON(m).(*Object))
	}
	return r
}
func (c *TurnContext) Send(ctx context.Context, input any) ([]dispatch.Receipt, error) {
	return c.owner.runtime.send(c.owner, ctx, input)
}
func (c *TurnContext) Eligible() (bool, error) { return c.owner.runtime.eligible(c) }

// AddScopedValidation receives the current read scope, including an active recall transaction.
// A retained authority must not be used after the validation returns.
func (c *TurnContext) AddScopedValidation(f func(ReadAuthority) (bool, error)) {
	c.mu.Lock()
	c.validations = append(c.validations, f)
	c.mu.Unlock()
}
func (c *TurnContext) setSources(m []*Object) {
	c.mu.Lock()
	c.sources = append([]*Object{}, m...)
	c.sourceTimes = nil
	for _, body := range m {
		record := c.owner.records[body]
		if record == nil {
			panic("source_snapshot_record_missing")
		}
		c.sourceTimes = append(c.sourceTimes, record.receivedAt)
	}
	c.mu.Unlock()
}
func (c *TurnContext) setDependencies(base []string, m []*Object) {
	c.mu.Lock()
	c.dependencies = map[string]bool{}
	c.dependencyOrder = nil
	add := func(id string) {
		if !c.dependencies[id] {
			c.dependencies[id] = true
			c.dependencyOrder = append(c.dependencyOrder, id)
		}
	}
	for _, id := range base {
		add(id)
	}
	for _, x := range m {
		add(idOf(x))
	}
	c.mu.Unlock()
}
func (c *TurnContext) dependencyIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.dependencyOrder...)
}
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type wallClock struct{}

func (wallClock) Now() float64 { return float64(time.Now().UnixMilli()) }
func (wallClock) AfterFunc(ms float64, f func()) modelservice.Timer {
	if math.IsNaN(ms) || ms < 1 || ms > 2147483647 {
		ms = 1
	}
	return time.AfterFunc(time.Duration(math.Trunc(ms))*time.Millisecond, f)
}
func New(settings Settings, services Services) (*Runtime, error) {
	if services.Authority == nil {
		return nil, code("runtime_read_authority_required")
	}
	if services.Models == nil || services.Policy == nil || services.Dates == nil {
		return nil, code("runtime_services_required")
	}
	if services.Clock == nil {
		services.Clock = wallClock{}
	}
	if settings.SceneConcurrency < 1 {
		return nil, code("invalid_scene_concurrency")
	}
	if settings.Hour == nil {
		settings.Hour = func(ms float64, timezone string) (int, error) {
			if math.IsNaN(ms) || math.IsInf(ms, 0) || math.Abs(ms) > 8640000000000000 {
				return 0, code("runtime_invalid_clock")
			}
			return intltime.Hour(int64(math.Trunc(ms)), timezone)
		}
	}

	r := &Runtime{services: services}
	settings.ParticipationWindows = append([]ParticipationWindow{}, settings.ParticipationWindows...)
	r.settings.Store(&settings)
	r.armCleanup()
	return r, nil
}
func (r *Runtime) config() Settings { return *r.settings.Load() }
func (r *Runtime) trace(e Event) {
	if observer := r.observer.Load(); observer != nil {
		(*observer)(e)
	}
	if r.services.Trace != nil {
		r.services.Trace(e)
	}
}
func (r *Runtime) exts() []*Extension {
	r.extensionMu.RLock()
	defer r.extensionMu.RUnlock()
	return append([]*Extension{}, r.extensions...)
}

// Registration owns immutable tool descriptions. The returned removal callback
// changes the live prompt-authority tool set and never revives an old turn.
func (r *Runtime) AddExtension(e *Extension) func() {
	registered := *e
	registered.SentSequencers = append([]*SentSequencer(nil), e.SentSequencers...)
	registered.Tools = nil
	for _, tool := range e.Tools {
		registered.Tools = append(registered.Tools, cloneJSON(tool).(*Object))
	}
	r.extensionMu.Lock()
	r.extensions = append(r.extensions, &registered)
	r.extensionMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.extensionMu.Lock()
			defer r.extensionMu.Unlock()
			for i, entry := range r.extensions {
				if entry == &registered {
					r.extensions = append(r.extensions[:i], r.extensions[i+1:]...)
					break
				}
			}
		})
	}
}
func (r *Runtime) AddHook(h Hook) {
	r.extensionMu.Lock()
	defer r.extensionMu.Unlock()
	r.hooks = append(r.hooks, h)
	for i := len(r.hooks) - 1; i > 0 && r.hooks[i].Priority < r.hooks[i-1].Priority; i-- {
		r.hooks[i], r.hooks[i-1] = r.hooks[i-1], r.hooks[i]
	}
}

func (c *TurnContext) MemoryExposureDeferred() bool { return c.deferMemoryExposure.Load() }
