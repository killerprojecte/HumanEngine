package snowlumaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestBrowser403RefreshActualSource(t *testing.T) {
	var fixture struct {
		Scene botruntime.Scene
		File  string
		Cases []struct {
			Name, Asset, First, Second, Bytes, Error string
			Cancel, GoLimited                        bool
			Reply                                    json.RawMessage
			URLs                                     []string
			Actions                                  []struct{ Action, File string }
		}
	}
	raw, err := os.ReadFile("testdata/browser-403-refresh-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 10 {
		t.Fatalf("invalid stale source fixture: %v", err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			media, err := nativeImageDescriptor(fixture.Scene, tc.Asset)
			if err != nil {
				t.Fatal(err)
			}
			reply := any(obj("file", "base64://Yg=="))
			if len(tc.Reply) != 0 {
				reply, err = jsjson.ParseBytes(tc.Reply)
				if err != nil {
					t.Fatal(err)
				}
			}
			wantActions, wantURLs, wantBytes, wantError := len(tc.Actions), tc.URLs, tc.Bytes, tc.Error
			if tc.GoLimited {
				wantActions, wantURLs, wantBytes, wantError = 0, []string{fixture.File}, "", tc.First
			}
			actions := 0
			var urls []string
			bytes, err := readPublicImageWithTokenRefresh(ctx, media, fixture.File, func(_ context.Context, token string) (any, error) {
				actions++
				if actions != 1 || wantActions != 1 || tc.Actions[0].Action != "get_image" || tc.Actions[0].File != token {
					t.Fatalf("unexpected token %q", token)
				}
				return reply, nil
			}, func(_ context.Context, locator string) ([]byte, error) {
				urls = append(urls, locator)
				failure, body := tc.First, "first bytes"
				if len(urls) > 1 {
					failure, body = tc.Second, "fresh bytes"
				}
				if tc.Cancel {
					cancel()
				}
				switch failure {
				case "":
					return []byte(body), nil
				case "public_http_403":
					return nil, publicImageHTTPStatus(http.StatusForbidden)
				case "public_http_404":
					return nil, publicImageHTTPStatus(http.StatusNotFound)
				default:
					return nil, errors.New(failure)
				}
			})
			if actions != wantActions || !slices.Equal(urls, wantURLs) || string(bytes) != wantBytes || errorString(err) != wantError {
				t.Fatalf("actions=%d links=%v bytes=%q err=%v; want %d %v %q %q", actions, urls, bytes, err, wantActions, wantURLs, wantBytes, wantError)
			}
		})
	}
}

func TestBrowser403RefreshPreservesSourceDenial(t *testing.T) {
	ctx := t.Context()
	media := obj("fileId", "opaque")
	const stale = "https://fixture.invalid/expired"
	for _, failure := range []error{botruntime.ErrDisplayImageUnavailable, fmt.Errorf("public_http_%d", http.StatusForbidden)} {
		_, err := readPublicImageWithTokenRefresh(ctx, media, stale, func(context.Context, string) (any, error) {
			t.Fatal("untyped HTTP text or source denial reached token path")
			return nil, nil
		}, func(context.Context, string) ([]byte, error) { return nil, failure })
		if err != failure {
			t.Fatalf("lost exact initial cause: %v", err)
		}
	}
	_, err := readPublicImageWithTokenRefresh(ctx, media, stale, func(context.Context, string) (any, error) {
		// Real sender lookup uses this guard before its authenticated wire send.
		return nil, botruntime.ErrDisplayImageUnavailable
	}, func(_ context.Context, url string) ([]byte, error) {
		if url != stale {
			t.Fatalf("source denial reached returned locator %s", url)
		}
		return nil, publicImageHTTPStatus(http.StatusForbidden)
	})
	if !errors.Is(err, botruntime.ErrDisplayImageUnavailable) {
		t.Fatalf("authority denial between phases was not preserved: %v", err)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
