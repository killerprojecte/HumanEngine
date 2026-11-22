//go:build linux && sqlite_fts5 && humanengine_icu && bundled_wasm

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/testsupport/snowstop"
)

func TestRuntimePreviewSnowLumaPartialACKSignalDrainRestart(t *testing.T) {
	for _, protocol := range []string{"v1", "openai-chat"} {
		t.Run(protocol, func(t *testing.T) {
			args := launcherArgs(t)
			plugin := ""
			if protocol == "v1" {
				fixture, err := os.ReadFile("../../internal/app/testdata/snowluma-model.mjs")
				if err != nil {
					t.Fatal(err)
				}
				plugin = "snow-model.mjs"
				for i, arg := range args {
					if arg == "--plugin-root" {
						if err = os.WriteFile(filepath.Join(args[i+1], plugin), fixture, 0600); err != nil {
							t.Fatal(err)
						}
						break
					}
				}
			}
			snowstop.Run(t, "127.0.0.1", "synthetic-launcher-token", plugin != "", func() snowstop.Process {
				cmd, logs, done := runtimeCommandWithPlugin(t, args, plugin, os.Getenv("HE_RUNTIME_PREVIEW_BINARY"))
				return snowstop.Process{Address: func() string { return runtimeAddress(t, logs, done) }, Stop: func() string {
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
					waitCommandExit(t, logs, done)
					return logs.snapshot()
				}}
			})
		})
	}
}
