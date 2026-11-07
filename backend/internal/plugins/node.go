package plugins

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"sync"
)

//go:embed v1-host.mjs
var nodeHostSource string

//go:embed v1-config.mjs
var nodeConfigSource string

//go:embed v1-worker.mjs
var nodeWorkerSource string

//go:embed v1-model-protocol-sdk.mjs
var nodeModelProtocolSDK string

//go:embed v1-model-error-classify.mjs
var nodeModelErrorClassifier string

type nodeReply struct {
	ID              string           `json:"id"`
	Result          json.RawMessage  `json:"result"`
	Failed          bool             `json:"failed"`
	Error           string           `json:"error"`
	Event           string           `json:"event"`
	Plugin          string           `json:"plugin"`
	ProtocolFailure *protocolFailure `json:"protocolFailure"`
}
type nodeProcess struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	write      sync.Mutex
	writeQueue [][]byte
	writeWake  chan struct{}
	writeErr   error
	mu         sync.Mutex
	next       uint64
	pending    map[string]chan nodeReply
	done       chan struct{}
	err        error
}
type nodeLease struct {
	key               string
	process           *nodeProcess
	closing           bool
	ready             bool
	dead              bool
	generation        uint64
	registrationOrder uint64
	protocols         []ModelProtocolMetadata
	handles           map[string]string
	modelCompletions  map[string]bool
	turnStops         map[string]func() bool
	done              chan struct{}
	err               error
}
type V1Host struct {
	lifecycle               sync.Mutex
	process                 *nodeProcess
	enabled                 map[string]*nodeLease
	sequence                uint64
	registrySequence        uint64
	node                    string
	diagnostics             io.Writer
	closed                  bool
	modelProtocolModuleURLs []string
}

func NewV1Host(node string, diagnostics io.Writer) *V1Host {
	return &V1Host{node: node, diagnostics: diagnostics, enabled: map[string]*nodeLease{}}
}

// V1HostOptions supplies deployment-owned resolution, not plugin metadata.
// Module URLs must identify the actual legacy model-protocols SDK entry points.
// No basename/path inference or runtime repository dependency is introduced.
type V1HostOptions struct {
	ModelProtocolModuleURLs []string
}

// NewV1HostWithOptions retains lazy startup even with explicit SDK aliases.
// The original constructor remains valid and installs no deployment aliases.
func NewV1HostWithOptions(node string, diagnostics io.Writer, options V1HostOptions) (*V1Host, error) {
	h := NewV1Host(node, diagnostics)
	for _, value := range options.ModelProtocolModuleURLs {
		parsed, err := url.Parse(value)
		if err != nil || !parsed.IsAbs() || parsed.Scheme == "" || parsed.User != nil {
			return nil, errors.New("invalid_model_protocol_module_url")
		}
		h.modelProtocolModuleURLs = append(h.modelProtocolModuleURLs, value)
	}
	return h, nil
}
func (h *V1Host) PID() int {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.process == nil {
		return 0
	}
	select {
	case <-h.process.done:
		return 0
	default:
		return h.process.cmd.Process.Pid
	}
}
func startNode(ctx context.Context, node string, diagnostics io.Writer, onWorkerExit func(*nodeProcess, string), moduleURLs []string) (*nodeProcess, error) {
	cmd := exec.Command(node, "--input-type=module", "--eval", nodeHostSource)
	cmd.Stderr = diagnostics
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &nodeProcess{cmd: cmd, stdin: input, pending: map[string]chan nodeReply{}, done: make(chan struct{}), writeWake: make(chan struct{}, 1)}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		reader := bufio.NewReader(output)
		for {
			line, e := reader.ReadBytes('\n')
			if e != nil {
				break
			}
			var reply nodeReply
			if json.Unmarshal(line, &reply) != nil {
				continue
			}
			if reply.Event == "worker_exit" {
				onWorkerExit(p, reply.Plugin)
				continue
			}
			p.mu.Lock()
			channel := p.pending[reply.ID]
			delete(p.pending, reply.ID)
			p.mu.Unlock()
			if channel != nil {
				select {
				case channel <- reply:
				default:
				}
			}
		}
		err := cmd.Wait()
		_ = input.Close()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		p.write.Lock()
		p.writeErr = errors.New("v1_host_exited")
		p.writeQueue = nil
		close(p.done)
		p.write.Unlock()
	}()
	go p.writeLoop()
	if _, err = p.call(ctx, map[string]any{"op": "initialize", "source": nodeConfigSource + "\n" + nodeModelErrorClassifier + "\n" + nodeWorkerSource, "modelProtocolSDK": nodeModelProtocolSDK, "modelProtocolModuleURLs": moduleURLs}); err != nil {
		input.Close()
		<-p.done
		return nil, err
	}
	return p, nil
}

// The unbounded control queue keeps admission independent of pipe backpressure.
// One writer preserves queue order; process exit closes the pipe and frees pending work.
func (p *nodeProcess) writeLoop() {
	for {
		select {
		case <-p.done:
			return
		case <-p.writeWake:
		}
		for {
			p.write.Lock()
			batch := p.writeQueue
			p.writeQueue = nil
			p.write.Unlock()
			if len(batch) == 0 {
				break
			}
			for _, bytes := range batch {
				if _, err := p.stdin.Write(bytes); err != nil {
					p.write.Lock()
					p.writeErr = err
					p.writeQueue = nil
					p.write.Unlock()
					p.mu.Lock()
					for _, reply := range p.pending {
						select {
						case reply <- nodeReply{Failed: true, Error: "v1_host_pipe_failed"}:
						default:
						}
					}
					p.mu.Unlock()
					_ = p.stdin.Close()
					return
				}
			}
		}
	}
}

type pendingNodeCall struct {
	process *nodeProcess
	id      string
	reply   chan nodeReply
}

func (p *nodeProcess) begin(message map[string]any) (*pendingNodeCall, error) {
	p.mu.Lock()
	p.next++
	id := strconv.FormatUint(p.next, 10)
	message["id"] = id
	channel := make(chan nodeReply, 1)
	p.pending[id] = channel
	p.mu.Unlock()
	pending := &pendingNodeCall{p, id, channel}
	bytes, err := json.Marshal(message)
	if err == nil {
		p.write.Lock()
		err = p.writeErr
		if err == nil {
			p.writeQueue = append(p.writeQueue, append(bytes, '\n'))
		}
		p.write.Unlock()
		if err == nil {
			select {
			case p.writeWake <- struct{}{}:
			default:
			}
		}
	}
	if err != nil {
		pending.remove()
		return nil, err
	}
	return pending, nil
}
func (c *pendingNodeCall) remove() {
	c.process.mu.Lock()
	delete(c.process.pending, c.id)
	c.process.mu.Unlock()
}
func (c *pendingNodeCall) wait(ctx context.Context) (json.RawMessage, error) {
	defer c.remove()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.process.done:
		// The control process may exit immediately after its close acknowledgement.
		select {
		case reply := <-c.reply:
			if reply.Failed {
				return nil, nodeReplyError(reply)
			}
			return reply.Result, nil
		default:
			return nil, errors.New("v1_host_exited")
		}
	case reply := <-c.reply:
		if reply.Failed {
			return nil, nodeReplyError(reply)
		}
		return reply.Result, nil
	}
}
func (p *nodeProcess) call(ctx context.Context, message map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pending, err := p.begin(message)
	if err != nil {
		return nil, err
	}
	result, err := pending.wait(ctx)
	if ctx.Err() != nil && message["op"] == "invoke" {
		// Cancel this invocation, not the whole plugin or other concurrent calls.
		_, cancelErr := p.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": message["plugin"], "invocationId": pending.id})
		return nil, errors.Join(ctx.Err(), cancelErr)
	}
	return result, err
}
func (h *V1Host) Enable(ctx context.Context, id, path string, config map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Encode caller-owned configuration before lifecycle admission. MarshalJSON may
	// re-enter the host or be slow; neither may fence unrelated unload.
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	h.lifecycle.Lock()
	if err := ctx.Err(); err != nil {
		h.lifecycle.Unlock()
		return nil, err
	}
	if h.closed {
		h.lifecycle.Unlock()
		return nil, errors.New("v1_host_closed")
	}
	if previous := h.enabled[id]; previous != nil && (previous.closing || previous.dead) {
		h.lifecycle.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-previous.done:
		}
		return h.Enable(ctx, id, path, config)
	}
	if h.enabled[id] != nil {
		h.lifecycle.Unlock()
		return nil, errors.New("plugin_already_loaded")
	}
	if h.process == nil {
		h.process, err = startNode(ctx, h.node, h.diagnostics, h.workerExited, h.modelProtocolModuleURLs)
		if err != nil {
			h.lifecycle.Unlock()
			return nil, err
		}
	}
	h.sequence++
	lease := &nodeLease{key: fmt.Sprintf("%s:%d", id, h.sequence), process: h.process, generation: h.sequence, done: make(chan struct{}), handles: map[string]string{}, modelCompletions: map[string]bool{}, turnStops: map[string]func() bool{}}
	h.enabled[id] = lease
	// Queue lifecycle admission before making the lease visible to Disable.
	pending, err := lease.process.begin(map[string]any{"op": "enable", "plugin": lease.key, "pluginId": id, "path": path, "config": json.RawMessage(encodedConfig)})
	h.lifecycle.Unlock()
	var result json.RawMessage
	if err == nil {
		result, err = pending.wait(ctx)
	}
	if err != nil {
		_ = h.disableLease(ctx, id, lease)
		return nil, err
	}
	var loaded struct {
		Registrations []struct {
			Handle, Kind, Name, Label string
			Handles                   map[string]string
		}
	}
	if err = json.Unmarshal(result, &loaded); err != nil {
		_ = h.disableLease(ctx, id, lease)
		return nil, err
	}
	h.lifecycle.Lock()
	current := !h.closed && h.enabled[id] == lease && !lease.closing && protocolProcessLive(lease)
	if current {
		h.registrySequence++
		lease.registrationOrder = h.registrySequence
		for _, registration := range loaded.Registrations {
			lease.handles[registration.Handle] = registration.Kind
			if registration.Kind == "modelProtocol" {
				lease.modelCompletions[registration.Handles["complete"]] = true
				lease.protocols = append(lease.protocols, ModelProtocolMetadata{ID: id + ":" + registration.Name, Label: registration.Label, Generation: lease.generation, handles: registration.Handles})
			}
		}
		lease.ready = true
	}
	h.lifecycle.Unlock()
	if !current {
		return nil, ErrNotLoaded
	}
	return result, nil
}
func (h *V1Host) Call(ctx context.Context, id, handle string, args []any) (json.RawMessage, error) {
	return h.callLease(ctx, id, nil, handle, args)
}
func (h *V1Host) callLease(ctx context.Context, id string, expected *nodeLease, handle string, args []any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.lifecycle.Lock()
	leaseForHandle := h.enabled[id]
	isCompletion := leaseForHandle != nil && leaseForHandle.modelCompletions[handle]
	h.lifecycle.Unlock()
	if isCompletion {
		return nil, errTypedModelCompletionRequired
	}
	// Snapshot potentially large caller payloads before acquiring the lifecycle lock.
	// Custom marshalers must not execute while admission/unload is fenced.
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	h.lifecycle.Lock()
	if err := ctx.Err(); err != nil {
		h.lifecycle.Unlock()
		return nil, err
	}
	lease := h.enabled[id]
	if h.closed || lease == nil || lease.closing || !lease.ready || !protocolProcessLive(lease) || expected != nil && lease != expected {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	if lease.modelCompletions[handle] {
		h.lifecycle.Unlock()
		return nil, errTypedModelCompletionRequired
	}
	pending, err := lease.process.begin(map[string]any{"op": "invoke", "plugin": lease.key, "handle": handle, "args": json.RawMessage(encodedArgs), "retainTurnSignal": (lease.handles[handle] == "sendHook" || lease.handles[handle] == "modelMethod") && ctx.Done() != nil})
	if err != nil {
		h.lifecycle.Unlock()
		return nil, err
	}
	if (lease.handles[handle] == "sendHook" || lease.handles[handle] == "modelMethod") && ctx.Done() != nil {
		// A hook's context lasts for the turn, not merely its callback. Preserve its
		// cancellation link until the turn ends or the plugin is unloaded.
		lease.turnStops[pending.id] = context.AfterFunc(ctx, func() {
			h.lifecycle.Lock()
			delete(lease.turnStops, pending.id)
			h.lifecycle.Unlock()
			_, _ = lease.process.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": lease.key, "invocationId": pending.id})
		})
	}
	h.lifecycle.Unlock()
	result, err := pending.wait(ctx)
	if ctx.Err() != nil {
		_, cancelErr := lease.process.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": lease.key, "invocationId": pending.id})
		err = errors.Join(ctx.Err(), cancelErr)
		result = nil
	}
	h.lifecycle.Lock()
	current := !h.closed && h.enabled[id] == lease && !lease.closing && protocolProcessLive(lease)
	h.lifecycle.Unlock()
	if !current {
		return nil, ErrNotLoaded
	}
	return result, err
}
func (h *V1Host) Disable(ctx context.Context, id string) error {
	h.lifecycle.Lock()
	lease := h.enabled[id]
	h.lifecycle.Unlock()
	if lease == nil {
		return nil
	}
	return h.disableLease(ctx, id, lease)
}
func (h *V1Host) waitLease(ctx context.Context, lease *nodeLease) error {
	select {
	case <-lease.done:
		return lease.err
	case <-ctx.Done():
		_, terminateErr := lease.process.call(context.Background(), map[string]any{"op": "terminate", "plugin": lease.key})
		<-lease.done
		return errors.Join(ctx.Err(), terminateErr, lease.err)
	}
}
func (h *V1Host) disableLease(ctx context.Context, id string, lease *nodeLease) error {
	h.lifecycle.Lock()
	if lease.closing || h.enabled[id] != lease {
		h.lifecycle.Unlock()
		return h.waitLease(ctx, lease)
	}
	lease.closing = true
	for _, stop := range lease.turnStops {
		stop()
	}
	lease.turnStops = map[string]func() bool{}
	h.lifecycle.Unlock()
	_, err := lease.process.call(ctx, map[string]any{"op": "disable", "plugin": lease.key})
	if ctx.Err() != nil {
		// Explicit cancellation ends the isolate, without introducing a timeout cap.
		_, terminateErr := lease.process.call(context.Background(), map[string]any{"op": "terminate", "plugin": lease.key})
		err = errors.Join(err, terminateErr)
	}
	h.lifecycle.Lock()
	processLeases := 0
	for _, item := range h.enabled {
		if item.process == lease.process {
			processLeases++
		}
	}
	last := processLeases == 1 && h.process == lease.process
	if last {
		h.process = nil
	}
	if !last {
		delete(h.enabled, id)
		lease.err = err
		close(lease.done)
		h.lifecycle.Unlock()
		return err
	}
	h.lifecycle.Unlock()
	err = errors.Join(err, stopNode(lease.process))
	h.lifecycle.Lock()
	delete(h.enabled, id)
	lease.err = err
	close(lease.done)
	h.lifecycle.Unlock()
	return err
}
func stopNode(process *nodeProcess) error {
	if process == nil {
		return nil
	}
	_, err := process.call(context.Background(), map[string]any{"op": "close"})
	_ = process.stdin.Close()
	<-process.done
	return err
}
func (h *V1Host) Close(ctx context.Context) error {
	h.lifecycle.Lock()
	h.closed = true
	leases := make(map[string]*nodeLease, len(h.enabled))
	for id, lease := range h.enabled {
		leases[id] = lease
	}
	h.lifecycle.Unlock()
	var err error
	for id, lease := range leases {
		err = errors.Join(err, h.disableLease(ctx, id, lease))
	}
	return err
}

// Worker exit is an ordered control event: hide this exact isolate before the
// reader delivers subsequent call replies. Teardown runs separately so it cannot
// block that same reader waiting for control acknowledgements.
func (h *V1Host) workerExited(process *nodeProcess, key string) {
	h.lifecycle.Lock()
	var owner string
	var dead *nodeLease
	for id, lease := range h.enabled {
		if lease.process == process && lease.key == key {
			lease.dead = true
			lease.ready = false
			lease.protocols = nil
			for _, stop := range lease.turnStops {
				stop()
			}
			lease.turnStops = map[string]func() bool{}
			if !lease.closing {
				owner, dead = id, lease
			}
			break
		}
	}
	h.lifecycle.Unlock()
	if dead != nil {
		go func() { _ = h.disableLease(context.Background(), owner, dead) }()
	}
}
