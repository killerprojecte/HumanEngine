package publicpage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	whatwg "github.com/nlnwa/whatwg-url/url"
	"golang.org/x/net/http/httpguts"
)

var (
	ErrInput  = errors.New("invalid_network_request")
	ErrHeader = errors.New("invalid_network_header")
)

type requestInput struct {
	URL             string
	Method          string
	Headers         json.RawMessage
	Body            *string
	BodyEncoding    string
	FollowRedirects bool
	MaxRedirects    int64
}

type requestPlan struct {
	method  string
	headers http.Header
	body    []byte
	hasBody bool
	ordered bool
}

func (f Fetcher) Request(ctx context.Context, raw json.RawMessage) (Response, error) {
	input := requestInput{Method: http.MethodGet, BodyEncoding: "utf8", FollowRedirects: true, MaxRedirects: maxRedirects}
	if err := json.Unmarshal(raw, &input); err != nil || input.MaxRedirects < 0 || input.MaxRedirects > 9007199254740991 {
		return Response{}, ErrInput
	}
	switch input.Method {
	case http.MethodGet, http.MethodOptions:
	case http.MethodHead:
		if input.Body != nil {
			return Response{}, ErrInput
		}
	default:
		return Response{}, ErrInput
	}
	u, err := whatwg.Parse(input.URL)
	if err != nil {
		return Response{}, ErrURL
	}
	u.SetHash("")
	if !allowedURL(u, input.URL) {
		return Response{}, ErrURL
	}
	headers, err := requestHeaders(input.Headers)
	if err != nil {
		return Response{}, err
	}
	plan := requestPlan{method: input.Method, headers: headers, hasBody: input.Body != nil, ordered: true}
	switch input.BodyEncoding {
	case "utf8":
		if input.Body != nil {
			plan.body = []byte(*input.Body)
		}
	case "base64":
		if input.Body != nil {
			plan.body, err = base64.StdEncoding.DecodeString(*input.Body)
			if err != nil || strings.ContainsAny(*input.Body, "\r\n") {
				return Response{}, ErrInput
			}
		}
	default:
		return Response{}, ErrInput
	}
	for hop := int64(0); ; hop++ {
		response, err := f.open(ctx, u, plan)
		if err != nil {
			return Response{}, err
		}
		location := response.Header.Get("Location")
		if !input.FollowRedirects || location == "" {
			return response, nil
		}
		switch response.Status {
		case 301, 302, 303, 307, 308:
		default:
			return response, nil
		}
		response.Body.Finish()
		if hop == input.MaxRedirects {
			return Response{}, ErrRedirect
		}
		next, err := u.Parse(location)
		if err != nil {
			return Response{}, ErrRedirect
		}
		next.SetHash("")
		if !allowedURL(next, next.Href(false)) || u.Protocol() == "https:" && next.Protocol() != "https:" {
			return Response{}, ErrRedirect
		}
		if u.Protocol() != next.Protocol() || u.Host() != next.Host() {
			plan.headers = defaultHeaders()
		}
		if response.Status == 303 && plan.method != http.MethodHead {
			plan.method, plan.body, plan.hasBody = http.MethodGet, nil, false
			plan.headers.Del("Content-Type")
			plan.headers.Del("Content-Encoding")
		}
		u = next
	}
}

func defaultHeaders() http.Header {
	return http.Header{"Accept-Encoding": {"identity"}, "User-Agent": {""}}
}

func requestHeaders(raw json.RawMessage) (http.Header, error) {
	headers := defaultHeaders()
	if len(raw) == 0 {
		return headers, nil
	}
	value, err := jsjson.ParseBytes(raw)
	fields, ok := value.(*jsjson.Object)
	if err != nil || !ok {
		return nil, ErrHeader
	}
	for _, field := range fields.Members() {
		name, value := field.Key, field.Value
		switch strings.ToLower(name) {
		case "host", "connection", "proxy-connection", "proxy-authorization", "proxy-authenticate", "transfer-encoding", "content-length", "upgrade", "te", "trailer", "expect":
			return nil, ErrHeader
		}
		text, ok := value.(string)
		if !ok || !httpguts.ValidHeaderFieldName(name) {
			return nil, ErrHeader
		}
		var latin strings.Builder
		latin.Grow(len(text))
		for _, r := range text {
			if !(r == '\t' || r >= 32 && r <= 255 && r != 127) {
				return nil, ErrHeader
			}
			latin.WriteByte(byte(r))
		}
		headers.Set(name, latin.String())
	}
	return headers, nil
}
