package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This test uses a real TCP listener/client. The richer model/cache/dispatch SQL
// integration is a separate test using the same HTTP handler and real Bridge.
func TestPluginManagementSocketLifecycleAndCancellation(t *testing.T) {
	_, db, _, gate := fixtureServer(t)
	authority, err := recovery.OpenWriter(context.Background(), gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	host := plugins.NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	root := t.TempDir()
	pidFile := filepath.Join(root, "pid")
	quoted, _ := json.Marshal(pidFile)
	source := `import{writeFileSync}from'node:fs';export default{manifest:{id:'socket',capabilities:['model-protocol'],configSchema:{type:'object',additionalProperties:true}},validateConfiguration(c){if(c.block){writeFileSync(` + string(quoted) + `,String(process.pid));while(true){}}},setup(c){c.registerAdminAction('echo',input=>({value:c.config.value,input}));c.registerAdminAction('crash',()=>process.exit(0));c.registerAdminPage({id:'page',title:'Page',entry:'/plugins/socket/page.js'});c.beforeSend({priority:0,handler:segments=>({action:'replace',segments})});c.registerTool({name:'socket.read',effect:'read',inputSchema:{type:'object'},execute:()=>42});c.registerModelProtocol('model','Model',{complete(){return{text:'',toolCalls:[]}},listModels(){return[{id:'actual'}]}})}}`
	if err = os.WriteFile(filepath.Join(root, "socket.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := pluginmanager.New(db, authority, host, new(botruntime.Runtime), "node", root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err = manager.RegisterV1(context.Background(), "socket.mjs"); err != nil {
		t.Fatal(err)
	}
	handler, err := New(db, authority, fixtureToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = handler.WithPlugins(manager); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	request := func(ctx context.Context, method, path, body string) (int, string, error) {
		req, e := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if e != nil {
			return 0, "", e
		}
		req.Header.Set("Authorization", "Bearer "+fixtureToken)
		req.Header.Set("Content-Type", "application/json")
		response, e := server.Client().Do(req)
		if e != nil {
			return 0, "", e
		}
		defer response.Body.Close()
		data, e := io.ReadAll(response.Body)
		return response.StatusCode, string(data), e
	}
	success := func(method, path, body string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		status, value, e := request(ctx, method, path, body)
		if e != nil || status != 200 {
			t.Fatal(status, value, e)
		}
		return value
	}
	success("PUT", "/api/plugins/socket/config", `{"value":17}`)
	if host.PID() != 0 {
		t.Fatal("disabled socket configuration retained Node")
	}
	success("PUT", "/api/plugins/socket/enabled", `{"enabled":true}`)
	pid := host.PID()
	if pid == 0 {
		t.Fatal("socket enable did not load plugin")
	}
	if body := success("POST", "/api/plugins/socket/actions/echo", `{"from":"TCP"}`); body != `{"value":17,"input":{"from":"TCP"}}` {
		t.Fatal(body)
	}

	crashContext, stopCrash := context.WithTimeout(context.Background(), 5*time.Second)
	status, body, crashErr := request(crashContext, "POST", "/api/plugins/socket/actions/crash", `{}`)
	stopCrash()
	if crashErr != nil || status < 400 {
		t.Fatal("worker exit did not fail HTTP action", status, body, crashErr)
	}
	waitExit := time.NewTimer(5 * time.Second)
	pollExit := time.NewTicker(time.Millisecond)
	for host.PID() != 0 {
		select {
		case <-waitExit.C:
			t.Fatal("unexpected worker process retained")
		case <-pollExit.C:
		}
	}
	waitExit.Stop()
	pollExit.Stop()
	listing := success("GET", "/api/plugins", "")
	if !strings.Contains(listing, `"enabled":false`) || !strings.Contains(listing, `"pages":[]`) {
		t.Fatal("HTTP list retained dead activation", listing)
	}
	success("PUT", "/api/plugins/socket/enabled", `{"enabled":true}`)
	if body = success("POST", "/api/plugins/socket/actions/echo", `{"from":"restart"}`); body != `{"value":17,"input":{"from":"restart"}}` {
		t.Fatal(body)
	}
	protocols := host.RegisteredModelProtocols()
	if len(protocols) != 1 {
		t.Fatal("re-enabled model unavailable", protocols)
	}
	models, e := host.CallModelProtocol(context.Background(), protocols[0].ID, protocols[0].Generation, "listModels", []any{map[string]any{}})
	if e != nil || string(models) != `[{"id":"actual"}]` {
		t.Fatal(string(models), e)
	}
	pid = host.PID()
	success("PUT", "/api/plugins/socket/enabled", `{"enabled":false}`)
	if host.PID() != 0 || syscall.Kill(pid, 0) == nil {
		t.Fatal("socket disable not physically complete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, _, e := request(ctx, "PUT", "/api/plugins/socket/config", `{"block":true}`); returned <- e }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	blockedPID := 0
	for blockedPID == 0 {
		select {
		case e := <-returned:
			t.Fatal("blocking configuration returned before cancellation", e)
		case <-timer.C:
			t.Fatal("configuration hook not entered")
		case <-ticker.C:
			raw, _ := os.ReadFile(pidFile)
			blockedPID, _ = strconv.Atoi(string(raw))
		}
	}
	cancel()
	select {
	case e := <-returned:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("socket cancellation did not return")
	}
	drained := time.NewTimer(5 * time.Second)
	defer drained.Stop()
	for {
		select {
		case <-drained.C:
			t.Fatal("cancelled HTTP handler retained physical Node")
		case <-ticker.C:
			if host.PID() == 0 && syscall.Kill(blockedPID, 0) != nil {
				return
			}
		}
	}
}
