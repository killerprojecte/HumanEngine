package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestExplicitInspectionUsesHostExactSDKAliases(t *testing.T) {
	root := t.TempDir()
	alias := (&url.URL{Scheme: "file", Path: filepath.Join(root, "deployment", "model-protocols.mjs")}).String()
	other := filepath.Join(root, "other", "model-protocols.mjs")
	if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(`export class ModelProtocolError extends Error {}`), 0600); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(root, "pid")
	quotedPID, _ := json.Marshal(pidPath)
	quotedAlias, _ := json.Marshal(alias)
	source := `import{writeFileSync}from'node:fs';import{ModelProtocolError as A}from ` + string(quotedAlias) + `;import{ModelProtocolError as B}from'humanengine:v1-model-protocols';import{ModelProtocolError as Other}from'./other/model-protocols.mjs';if(A!==B||A===Other)throw Error('sdk_identity');writeFileSync(` + string(quotedPID) + `,String(process.pid));export default{manifest:{id:'sdk',configSchema:{type:'object'}},configuredSecrets(){return{shared:A===B,other:A===Other}},setup(){throw Error('inspection_must_not_setup')}}`
	path := filepath.Join(root, "plugin.mjs")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	host, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{ModelProtocolModuleURLs: []string{alias}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	for _, operation := range []string{"manifest", "secrets"} {
		result, err := host.InspectExplicitV1(context.Background(), InspectionRequest{Operation: operation, Path: path})
		if err != nil || result.Failed {
			t.Fatal(operation, result, err)
		}
		if operation == "secrets" && string(result.ConfiguredSecrets) != `{"shared":true,"other":false}` {
			t.Fatal(string(result.ConfiguredSecrets))
		}
		pidBytes, err := os.ReadFile(pidPath)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			t.Fatal(err)
		}
		if host.PID() != 0 || syscall.Kill(pid, 0) == nil {
			t.Fatal("inspector not physically reaped")
		}
	}
	// A matching basename without an explicit alias is not a deployment grant.
	plain := NewV1Host("node", io.Discard)
	defer plain.Close(context.Background())
	result, err := plain.InspectExplicitV1(context.Background(), InspectionRequest{Operation: "manifest", Path: path})
	if err == nil && !result.Failed {
		t.Fatal("unconfigured alias resolved")
	}
}
