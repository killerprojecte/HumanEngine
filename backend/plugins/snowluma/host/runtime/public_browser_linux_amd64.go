//go:build cgo

package snowlumaruntime

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
)

const publicBrowserImageSupported = true

func (s *Sender) readPublicBrowserImage(ctx context.Context, locator string, before func(context.Context) error) ([]byte, error) {
	done, ok := s.work.Start()
	if !ok {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	defer done()
	ctx, cancel := context.WithCancel(ctx)
	unlink := context.AfterFunc(s.life, cancel)
	defer cancel()
	defer unlink()
	if s.current() != nil {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	reader := publicImageReader{lookup: pinnedhop.LookupChild, beginWork: s.work.Start}
	return reader.read(ctx, locator, before)
}
