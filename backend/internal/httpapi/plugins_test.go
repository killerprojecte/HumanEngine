package httpapi

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// This test exercises the real management/SQLite/Node boundary. It deliberately
// does not claim a bot-turn/model/transport integration result.
func TestPluginManagementHTTPRealLifecycle(t *testing.T) {
	_, main, _, gate := fixtureServer(t)
	authority, err := recovery.OpenWriter(context.Background(), gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	host := plugins.NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	root := t.TempDir()
	source := `export default{manifest:{id:'managed',name:'Managed',version:'1',description:'Fixture',configSchema:{type:'object',properties:{secret:{type:'string',secret:true,default:'MUST-NOT-LEAK'},value:{type:'integer'}},required:['secret']},capabilities:[]},configuredSecrets(){return{fixture:true}},setup(ctx){ctx.registerAdminPage({id:'page',title:'Page',entry:'/plugins/managed/page.js'});ctx.registerAdminAction('echo',async input=>({value:ctx.config.value,input}));ctx.registerTool({name:'managed.read',effect:'read',inputSchema:{type:'object'},execute:async()=>({value:ctx.config.value})})}}`
	if err = os.WriteFile(filepath.Join(root, "managed.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := pluginmanager.New(main, authority, host, new(botruntime.Runtime), "node", root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err = manager.RegisterV1(context.Background(), "managed.mjs"); err != nil {
		t.Fatal(err)
	}
	server, err := New(main, authority, fixtureToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = server.WithPlugins(manager); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+fixtureToken)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Request-ID", "plugin-fixture")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if status, body := request("GET", "/api/plugins", ""); status != 200 || strings.Contains(body, "MUST-NOT-LEAK") || !strings.Contains(body, `"enabled":false`) || !strings.Contains(body, `"fixture":true`) {
		t.Fatal(status, body)
	}
	if host.PID() != 0 {
		t.Fatal("disabled list retained shared Node")
	}
	if status, body := request("PUT", "/api/plugins/managed/config", `{"secret":"PRIVATE","value":1}`); status != 200 || body != `{"value":1}` {
		t.Fatal(status, body)
	}
	if host.PID() != 0 {
		t.Fatal("disabled configuration started shared Node")
	}
	if status, body := request("PUT", "/api/plugins/managed/enabled", `{"enabled":true}`); status != 200 {
		t.Fatal(status, body)
	}
	if host.PID() == 0 {
		t.Fatal("enabled plugin not loaded")
	}
	if status, body := request("POST", "/api/plugins/managed/actions/echo", `{"hello":"world"}`); status != 200 || body != `{"value":1,"input":{"hello":"world"}}` {
		t.Fatal(status, body)
	}
	if status, body := request("PUT", "/api/plugins/managed/config", `{"value":2}`); status != 200 || body != `{"value":2}` {
		t.Fatal(status, body)
	}
	if status, body := request("POST", "/api/plugins/managed/actions/echo", `{}`); status != 200 || body != `{"value":2,"input":{}}` {
		t.Fatal(status, body)
	}
	if status, body := request("PUT", "/api/plugins/managed/config", `{"value":"invalid"}`); status != 400 || !strings.Contains(body, `"invalid_plugin_configuration"`) {
		t.Fatal(status, body)
	}
	if status, body := request("PUT", "/api/plugins/managed/enabled", `{"enabled":false}`); status != 200 {
		t.Fatal(status, body)
	}
	if host.PID() != 0 {
		t.Fatal("last disable retained Node")
	}
	if status, body := request("POST", "/api/plugins/managed/actions/echo", `{}`); status != 503 {
		t.Fatal(status, body)
	}
	if status, body := request("GET", "/api/plugins", ""); status != 200 || !strings.Contains(body, `"pages":[]`) {
		t.Fatal(status, body)
	}
	var stored string
	if err = main.QueryRow("SELECT value FROM config WHERE key='plugin:managed'").Scan(&stored); err != nil || !strings.Contains(stored, `"secret":"PRIVATE"`) || !strings.Contains(stored, `"value":2`) {
		t.Fatal(stored, err)
	}
}
