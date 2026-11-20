//go:build web_read_candidate

package host

import "github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"

type OSResolver = pinnedhop.OSResolver

var ErrOSDNSUnavailable = pinnedhop.ErrOSDNSUnavailable
var ErrOSDNSClosed = pinnedhop.ErrOSDNSClosed

func NewOSResolver() (*OSResolver, error) { return pinnedhop.NewOSResolver() }
