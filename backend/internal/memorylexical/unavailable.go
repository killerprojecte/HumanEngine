//go:build !humanengine_icu || !cgo

package memorylexical

import "github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"

func Words(string) ([]string, error)         { return nil, nodeicu.ErrUnavailable }
func WordLikeWords(string) ([]string, error) { return nil, nodeicu.ErrUnavailable }
func tokenRuns(string) ([]string, error)     { return nil, nodeicu.ErrUnavailable }
func hanClass(string) (bool, bool, int)      { return false, false, 0 }

func TokenRuns(string) ([]string, error) { return nil, nodeicu.ErrUnavailable }
