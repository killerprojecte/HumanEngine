package httpapi

import (
	"database/sql"
	"errors"
	"io"
	"math"
	"net/http"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"golang.org/x/text/encoding/unicode"
)

const memoryWorkingSetPinPath = "/api/memory-working-set/pin"

func (s *Server) WithMemoryWorkingSetPin(publisher *recovery.ReadPublisher) error {
	if s.memoryDelete == nil {
		return errors.New("http_memory_working_set_owner_invalid")
	}
	pin, err := s.memoryDelete.BindWorkingSetPinAdmin(publisher)
	if err == nil {
		s.workingSetPin = pin
	}
	return err
}

func workingSetPinQuery(request *http.Request) (*memoryservice.WorkingSetPinQuery, error) {
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	contentType := request.Header.Get("Content-Type")
	if contentType == "" && len(raw) == 0 {
		return &memoryservice.WorkingSetPinQuery{}, nil
	}
	if !habitJSONContentType.MatchString(contentType) {
		return nil, errors.New("working_set_pin_unsupported_media")
	}
	if !utf8.Valid(raw) {
		raw, err = unicode.UTF8.NewDecoder().Bytes(raw)
		if err != nil {
			return nil, err
		}
	}
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		return nil, err
	}
	o, ok := value.(*jsjson.Object)
	if !ok || o.Len() != 4 {
		return nil, nil
	}
	sceneRaw, _ := o.Get("sceneId")
	scene, _ := sceneRaw.(string)
	sceneLen, sceneErr := jsjson.CodePointCount(scene)
	revisionRaw, _ := o.Get("expectedRevision")
	revision, revisionOK := revisionRaw.(float64)
	pinnedRaw, _ := o.Get("pinned")
	pinned, pinnedOK := pinnedRaw.(bool)
	selectionRaw, _ := o.Get("selection")
	if sceneErr != nil || sceneLen < 1 || sceneLen > 512 || !revisionOK || revision < 0 || revision > workingset.MaxSafe ||
		math.Trunc(revision) != revision || !pinnedOK {
		return nil, nil
	}
	selection, err := workingset.ParseSelection(selectionRaw)
	if err != nil {
		return nil, nil
	}
	return &memoryservice.WorkingSetPinQuery{SceneID: scene, Selection: selection, ExpectedRevision: revision, Pinned: pinned}, nil
}

func (s *Server) serveMemoryWorkingSetPin(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_working_set_pin.failed", func(emit func(int, []byte) error) (bool, error) {
		ctx := r.Context()
		checkGate := func() error {
			return s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
		}
		// The authenticated source checks recovery before parsing a request body.
		if err := checkGate(); err != nil {
			return false, err
		}
		q, err := workingSetPinQuery(r)
		if err != nil {
			return true, err
		}
		if err := s.workingSetPin(ctx, q, emit); err != nil {
			// A rolled-back main write can leave durable intent. Like the
			// source, distinguish that closed gate from an ordinary error.
			gateErr := checkGate()
			return gateErr == nil, errors.Join(err, gateErr)
		}
		return true, nil
	})
}
