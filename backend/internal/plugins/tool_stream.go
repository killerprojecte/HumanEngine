package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

type toolChunk struct {
	bytes []byte
	final bool
}
type toolStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	chunks chan toolChunk
	done   chan struct{}
	result json.RawMessage
	err    error
	mu     sync.Mutex
	ended  bool
}

func (s *toolStream) Next(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case chunk := <-s.chunks:
		if chunk.final {
			return nil, io.EOF
		}
		return chunk.bytes, nil
	}
}
func (s *toolStream) Destroy()      { s.cancel() }
func (s *toolStream) Return() error { s.cancel(); return nil }
func (s *toolStream) push(ctx context.Context, chunk toolChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return errors.New("tool_result_cancelled")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.err
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.chunks <- chunk:
		if chunk.final {
			s.ended = true
		}
		return nil
	}
}
func parseResultOptions(raw json.RawMessage) (toolresults.Options, error) {
	var options toolresults.Options
	value, err := jsjson.Parse(string(raw))
	if err != nil {
		return options, err
	}
	obj, ok := value.(*jsjson.Object)
	if !ok {
		return options, errors.New("invalid_tool_result_options")
	}
	if v, ok := obj.Get("contentType"); ok && v != nil {
		str, ok := v.(string)
		if !ok {
			return options, errors.New("invalid_tool_result_options")
		}
		options.ContentType = &str
	}
	if v, ok := obj.Get("encoding"); ok && v != nil {
		str, ok := v.(string)
		if !ok {
			return options, errors.New("invalid_tool_result_options")
		}
		options.Encoding = toolresults.Encoding(str)
	}
	if v, ok := obj.Get("previewBytes"); ok && v != nil {
		num, ok := v.(float64)
		if !ok {
			return options, errors.New("invalid_tool_result_options")
		}
		options.PreviewBytes = &num
	}
	return options, nil
}
func (h *V1Host) toolCacheRPC(process *nodeProcess, lease *nodeLease, call *toolInvocation, message nodeReply) (json.RawMessage, error) {
	if call.services.CacheResult == nil {
		return nil, errors.New("tool_result_storage_failed")
	}
	if len(message.Args) == 0 {
		return nil, errors.New("invalid_tool_result_options")
	}
	var token string
	if json.Unmarshal(message.Args[0], &token) != nil || token == "" {
		return nil, errors.New("invalid_tool_result_options")
	}
	call.mu.Lock()
	if call.streams == nil {
		call.streams = map[string]*toolStream{}
	}
	stream := call.streams[token]
	if message.Operation == "cache_start" {
		if stream != nil || len(message.Args) != 2 {
			call.mu.Unlock()
			return nil, errors.New("invalid_tool_result_options")
		}
		options, err := parseResultOptions(message.Args[1])
		if err != nil {
			call.mu.Unlock()
			return nil, err
		}
		ctx, cancel := context.WithCancel(call.ctx)
		stream = &toolStream{ctx: ctx, cancel: cancel, chunks: make(chan toolChunk), done: make(chan struct{})}
		call.streams[token] = stream
		done, ok := lease.toolWork.Child()
		if !ok {
			delete(call.streams, token)
			call.mu.Unlock()
			cancel()
			return nil, ErrNotLoaded
		}
		call.mu.Unlock()
		go func() {
			defer done()
			defer cancel()
			result, err := call.services.CacheResult(ctx, stream, options)
			if err == nil {
				var value *jsjson.Object
				value, err = result.JSONValue()
				if err == nil {
					stream.result, err = jsjson.Marshal(value)
				}
			}
			stream.err = err
			close(stream.done)
			if err != nil {
				pending, e := process.begin(map[string]any{"op": "tool_stream_abort", "plugin": message.Plugin, "stream": token})
				if e == nil {
					pending.remove()
				}
			}
		}()
		return json.RawMessage(`true`), nil
	}
	call.mu.Unlock()
	if stream == nil {
		return nil, errors.New("tool_result_not_found_or_expired")
	}
	switch message.Operation {
	case "cache_chunk":
		if len(message.Args) != 2 {
			return nil, errors.New("invalid_tool_result_chunk")
		}
		var encoded string
		if json.Unmarshal(message.Args[1], &encoded) != nil {
			return nil, errors.New("invalid_tool_result_chunk")
		}
		chunk, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("invalid_tool_result_chunk")
		}
		err = stream.push(call.ctx, toolChunk{bytes: chunk})
		return json.RawMessage(`true`), err
	case "cache_finish":
		if err := stream.push(call.ctx, toolChunk{final: true}); err != nil {
			return nil, err
		}
		select {
		case <-stream.done:
			return stream.result, stream.err
		case <-call.ctx.Done():
			return nil, call.ctx.Err()
		}
	case "cache_abort":
		stream.cancel()
		return json.RawMessage(`true`), nil
	default:
		return nil, errors.New("invalid_tool_result_options")
	}
}
