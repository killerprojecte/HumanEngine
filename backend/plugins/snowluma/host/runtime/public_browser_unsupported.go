//go:build !linux || !amd64 || !cgo

package snowlumaruntime

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
)

func (*Sender) readPublicBrowserImage(context.Context, string, func(context.Context) error) ([]byte, error) {
	return nil, botruntime.ErrDisplayImageUnavailable
}
