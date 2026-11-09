package observability

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

type StoreLoggerOptions struct {
	Database  string
	Registry  *logdelivery.Registry
	Stdout    *logdelivery.Lease
	LookupEnv func(string) (string, bool)
}
type logClose interface {
	Wait(context.Context) (*logdelivery.FlushResult, error)
}
type logLease interface {
	Enqueue(string, bool) bool
	Status() logdelivery.Status
	beginClose() logClose
	CloseSync(time.Duration) *logdelivery.FlushResult
	WaitPhysicalRelease(context.Context) error
}
type sourceLease struct{ *logdelivery.Lease }
type sourceClose struct{ *logdelivery.CloseOperation }

func (s sourceLease) beginClose() logClose {
	return sourceClose{s.BeginClose(logdelivery.DefaultTimeout)}
}
func (s sourceClose) Wait(ctx context.Context) (*logdelivery.FlushResult, error) {
	r, e := s.CloseOperation.Wait(ctx)
	return &r, e
}

// StoreLogger owns source retention and its two lazy shared-channel leases.
// It does not own a database and never starts a worker in its constructor.
type StoreLogger struct {
	mu                             sync.Mutex
	retained                       retainedLogs
	directory                      string
	stdout, file                   logLease
	closed                         bool
	shutdownComplete               *bool
	initializationFailed           bool
	unserializable, closedRejected uint64
	noticeAt                       int64
	noticeSet                      bool
	activeAdmissions               int
	admissionsIdle                 chan struct{}
	initializing                   chan struct{}
	closeInitiated                 chan struct{}
	closeInitiatedOnce             sync.Once
	acquire                        func(string) (logLease, error)
	now                            func() time.Time
	newID                          func() (string, error)
}

func NewStoreLogger(options StoreLoggerOptions) *StoreLogger {
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	database := options.Database
	if database == "" {
		database, _ = lookup("HUMANENGINE_DB")
		if database == "" {
			database = "data/humanengine.sqlite"
		}
	}
	l := &StoreLogger{now: time.Now, newID: logUUID, admissionsIdle: make(chan struct{})}
	close(l.admissionsIdle)
	if database != ":memory:" {
		l.directory, _ = lookup("HUMANENGINE_LOG_DIR")
		if l.directory == "" {
			l.directory = filepath.Join(filepath.Dir(database), "logs")
		}
	}
	l.acquire = func(directory string) (logLease, error) {
		var lease *logdelivery.Lease
		var err error
		if options.Registry == nil {
			lease, err = logdelivery.Acquire(directory)
		} else {
			lease, err = options.Registry.Acquire(directory)
		}
		if err != nil {
			return nil, err
		}
		return sourceLease{lease}, nil
	}
	if options.Stdout != nil {
		l.stdout = sourceLease{options.Stdout}
	}
	return l
}
func logUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}
func (l *StoreLogger) Emit(event Event) error {
	if event.ID == "" {
		id, err := l.newID()
		if err != nil {
			return errors.New("log_identity_failed")
		}
		event.ID = id
	}
	if event.At.IsZero() {
		event.At = l.now().UTC()
	}
	fields, err := snapshotLogFields(event.Fields)
	var row *jsjson.Object
	var encoded []byte
	if err == nil {
		row = sourceLogRow(event, fields)
		encoded, err = jsjson.Marshal(row)
	}
	if err != nil {
		l.mu.Lock()
		l.unserializable++
		l.mu.Unlock()
		l.loggingNotice()
		return ErrUnserializableLog
	}
	encoded = append(encoded, '\n')
	level, _ := retainedGet(row, "level").(string)
	l.mu.Lock()
	l.retained.add(encoded, level)
	if l.closed {
		l.closedRejected++
		l.mu.Unlock()
		l.loggingNotice()
		return ErrClosed
	}
	if l.activeAdmissions == 0 {
		l.admissionsIdle = make(chan struct{})
	}
	l.activeAdmissions++
	l.mu.Unlock()
	defer l.endAdmission()
	if err = l.deliver(row, encoded, event.Level == "warn" || event.Level == "error"); err != nil {
		l.mu.Lock()
		l.initializationFailed = true
		l.mu.Unlock()
	}
	l.loggingNotice()
	return nil
}
func (l *StoreLogger) endAdmission() {
	l.mu.Lock()
	l.activeAdmissions--
	if l.activeAdmissions == 0 {
		close(l.admissionsIdle)
	}
	l.mu.Unlock()
}
func (l *StoreLogger) deliver(row *jsjson.Object, encoded []byte, priority bool) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("log_initialization_failed")
		}
	}()
	out, file, err := l.ensureLeases()
	if err != nil {
		return err
	}
	stdout, err := sourceStdout(row, encoded)
	if err != nil {
		return err
	}
	if out != nil {
		out.Enqueue(stdout, priority)
	}
	if file != nil {
		file.Enqueue(string(encoded), priority)
	}
	return nil
}
func (l *StoreLogger) ensureLeases() (out, file logLease, err error) {
	for {
		l.mu.Lock()
		if l.stdout != nil && (l.directory == "" || l.file != nil) {
			out, file = l.stdout, l.file
			l.mu.Unlock()
			return
		}
		if pending := l.initializing; pending != nil {
			l.mu.Unlock()
			<-pending
			continue
		}
		pending := make(chan struct{})
		l.initializing = pending
		out, file = l.stdout, l.file
		directory := l.directory
		l.mu.Unlock()
		// All acquisition/path work and callbacks happen outside the Store mutex.
		// Pre-close Emit admission owns this setup even if close starts meanwhile.
		func() {
			defer func() { l.mu.Lock(); l.initializing = nil; close(pending); l.mu.Unlock() }()
			if out == nil {
				out, err = l.acquire("")
				if err != nil {
					return
				}
				l.mu.Lock()
				l.stdout = out
				l.mu.Unlock()
			}
			if directory != "" && file == nil {
				file, err = l.acquire(directory)
				if err != nil {
					return
				}
				l.mu.Lock()
				l.file = file
				l.mu.Unlock()
			}
		}()
		return
	}
}
func (l *StoreLogger) Retention() RetentionStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retained.status()
}
func (l *StoreLogger) RetainedJSON() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retained.snapshotJSON()
}
