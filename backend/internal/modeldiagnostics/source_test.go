package modeldiagnostics

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os/exec"
	"testing"
)

// Execute the actual source Store methods, not a hand-copied redaction oracle.
func TestDiagnosticActualSourceParity(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	script := `
const fs=require('fs'),ts=require('../../../node_modules/typescript'),vm=require('vm');
const source=fs.readFileSync('../../../src/store.ts','utf8');
const methods=source.slice(source.indexOf('  clearDiagnostics() {'),source.indexOf('  close() {',source.indexOf('  clearDiagnostics() {')));
if(!methods.includes('captureDiagnostic'))throw Error('source diagnostic methods missing');
const roles=fs.readFileSync('../../../src/model-roles.ts','utf8');
const code=ts.transpileModule(roles+'\nclass Store {\n'+methods+'\n}; globalThis.Store=Store;', {compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.CommonJS}}).outputText;
const sandbox={exports:{},randomUUID:()=> 'id',Date,setTimeout:()=>({unref(){}}),clearTimeout(){}};
vm.runInNewContext(code,sandbox);
const s=new sandbox.Store();s.settings={debugModelIO:true};s.secrets={redactionValues:()=>['abcdef','abc','quote"line\n']};s.models={providers:[]};s.diagnosticEntries=[];s.diagnosticTimers=new Map();
const m={role:'user',content:'abcdef abc quote"line\n',images:['hidden'],toolCalls:[{name:'x',arguments:{access_token:'hidden',text:'abcdef'}}]};
s.captureDiagnostic({modelId:'model',role:'thinker',messages:Array.from({length:14},(_,i)=>({...m,toolCallId:String(i)})),output:{authorization:'hidden',text:'abcdef','ſecret':'visible'}},[]);
const row=s.diagnosticEntries[0];process.stdout.write(JSON.stringify({input:row.input,output:row.output,role:row.role}));
`
	raw, e := exec.Command(node, "-e", script).CombinedOutput()
	if e != nil {
		t.Fatalf("source oracle: %v %s", e, raw)
	}
	var expected struct{ Input, Output, Role string }
	if e = json.Unmarshal(raw, &expected); e != nil {
		t.Fatal(e)
	}
	s, e := New(Ports{Enabled: func() bool { return true }, Secrets: func(context.Context) ([]string, error) { return []string{"abcdef", "abc", "quote\"line\n"}, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.CloseAndWait(context.Background())
	message, e := jsjson.Parse(`{"role":"user","content":"abcdef abc quote\"line\n","images":["hidden"],"toolCalls":[{"name":"x","arguments":{"access_token":"hidden","text":"abcdef"}}]}`)
	if e != nil {
		t.Fatal(e)
	}
	messages := []*jsjson.Object{}
	for i := 0; i < 14; i++ {
		m := message.(*jsjson.Object).Clone()
		id, _ := json.Marshal(i)
		m.Set("toolCallId", string(id))
		messages = append(messages, m)
	}
	output, _ := jsjson.Parse(`{"authorization":"hidden","text":"abcdef","ſecret":"visible"}`)
	if e = s.Capture(context.Background(), Entry{ModelID: "model", Role: "thinker", Messages: messages, Output: output}, nil); e != nil {
		t.Fatal(e)
	}
	got := s.Rows()[0]
	if got.Input != expected.Input || got.Output != expected.Output || got.Role != expected.Role {
		t.Fatalf("source mismatch\n%+v\n%+v", got, expected)
	}
}
