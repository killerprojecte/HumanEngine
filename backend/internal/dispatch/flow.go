package dispatch

import "sync"

// FlowEvent is the deterministic diagnostic projection. IDs, duration and error
// rendering belong to the diagnostic sink; raw causes never enter the event.
type FlowDetails struct {
	Reason       string         `json:"reason,omitempty"`
	NextStep     string         `json:"nextStep,omitempty"`
	Counts       map[string]int `json:"counts,omitempty"`
	ReceiptState Status         `json:"receiptState,omitempty"`
}
type FlowEvent struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
	FlowDetails
	// Parent identifies actual nested work without exposing random span IDs in
	// the parity traces. Each dispatch is independent, with no global bot queue.
	ParentStage    string `json:"parentStage,omitempty"`
	ErrorOperation string `json:"errorOperation,omitempty"`
	ErrorStage     string `json:"errorStage,omitempty"`
}
type FlowWriter interface{ WriteFlow(FlowEvent) }
type FlowWriterFunc func(FlowEvent)

func (f FlowWriterFunc) WriteFlow(event FlowEvent) { f(event) }

type flow struct {
	mu                     sync.Mutex
	writer                 FlowWriter
	stage, parent          string
	details                FlowDetails
	ended, cancelRequested bool
	remove                 func()
}

func startFlow(writer FlowWriter, stage string, parent *flow, signal *Signal, details FlowDetails) *flow {
	f := &flow{writer: writer, stage: stage, details: details}
	if parent != nil {
		f.parent = parent.stage
	}
	f.emit("started", FlowDetails{}, nil)
	f.remove = signal.subscribe(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.ended || f.cancelRequested {
			return
		}
		f.cancelRequested = true
		f.emit("cancel_requested", FlowDetails{Reason: "cancellation_requested", NextStep: "wait_for_cleanup"}, nil)
	})
	return f
}
func (f *flow) finish(status string, extra FlowDetails, failure *Failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return
	}
	f.ended = true
	f.remove()
	f.emit(status, extra, failure)
}
func (f *flow) emit(status string, extra FlowDetails, failure *Failure) {
	if f.writer == nil {
		return
	}
	// Logging must not change business work or cause a retry.
	defer func() { _ = recover() }()
	details := f.details
	if extra.Reason != "" {
		details.Reason = extra.Reason
	}
	if extra.NextStep != "" {
		details.NextStep = extra.NextStep
	}
	if extra.ReceiptState != "" {
		details.ReceiptState = extra.ReceiptState
	}
	if details.Counts != nil || extra.Counts != nil {
		details.Counts = make(map[string]int, len(f.details.Counts)+len(extra.Counts))
		for key, value := range f.details.Counts {
			if value >= 0 {
				details.Counts[key] = value
			}
		}
		for key, value := range extra.Counts {
			if value >= 0 {
				details.Counts[key] = value
			}
		}
	}
	event := FlowEvent{Stage: f.stage, Status: status, FlowDetails: details, ParentStage: f.parent}
	if failure != nil {
		event.ErrorOperation, event.ErrorStage = safeContextLabel(failure.Context.Operation), safeContextLabel(failure.Context.Stage)
		if event.ErrorOperation == "" {
			event.ErrorOperation = f.stage
		}
		if event.ErrorStage == "" {
			event.ErrorStage = f.stage
		}
	}
	f.writer.WriteFlow(event)
}
