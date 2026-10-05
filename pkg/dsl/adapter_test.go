package dsl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
)

type recorder struct {
	ops   []string
	args  []map[string]any
	fail  error
	reply map[string]any
}

func (r *recorder) call(_ context.Context, op string, args map[string]any) (map[string]any, error) {
	r.ops = append(r.ops, op)
	r.args = append(r.args, args)
	if r.fail != nil {
		return nil, r.fail
	}
	if r.reply != nil {
		return r.reply, nil
	}
	return map[string]any{"text": "ok"}, nil
}

func adapter(lang, code string, params map[string]any) algo.Bound {
	return algo.Bound{Instance: "test", Algorithm: "a", Language: lang, Code: code, Params: params}
}

const jsAdapter = `
switch (ctx.tool()) {
  case "read":  return ctx.call("read_file", { path: ctx.args().path, base: ctx.param("root") });
  case "size":  return 42;
}
ctx.fail("not implemented: " + ctx.tool());
`

func TestJavaScriptAdapterMapsTheToolOntoTheConnector(t *testing.T) {
	rec := &recorder{reply: map[string]any{"text": "hello"}}
	in := AdapterInput{Tool: "read", Args: map[string]any{"path": "a.txt"}, Operations: []string{"read_file"}, Call: rec.call}
	out, err := RunAdapter(context.Background(), adapter("javascript", jsAdapter, map[string]any{"root": "/data"}), in)
	if err != nil || !out.OK() || out.Result["text"] != "hello" || out.Calls != 1 {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if rec.ops[0] != "read_file" || rec.args[0]["path"] != "a.txt" || rec.args[0]["base"] != "/data" {
		t.Fatalf("connector call = %v %v", rec.ops, rec.args)
	}

	// a scalar result is wrapped, an unmapped tool is rejected
	in.Tool = "size"
	if out, err = RunAdapter(context.Background(), adapter("javascript", jsAdapter, nil), in); err != nil || out.Result["value"] != int64(42) && out.Result["value"] != float64(42) {
		t.Fatalf("scalar = %+v, %v", out, err)
	}
	in.Tool = "delete"
	if out, err = RunAdapter(context.Background(), adapter("javascript", jsAdapter, nil), in); err != nil || out.OK() || !strings.Contains(out.Failures[0], "not implemented") {
		t.Fatalf("unmapped tool = %+v, %v", out, err)
	}
}

func TestAdapterWithoutArguments(t *testing.T) {
	rec := &recorder{}
	in := AdapterInput{Tool: "list", Operations: []string{"list_dir"}, Call: rec.call} // nil arguments
	out, err := RunAdapter(context.Background(), adapter("javascript", `return ctx.call("list_dir", { path: ctx.args().path || "" })`, nil), in)
	if err != nil || !out.OK() || rec.args[0]["path"] != "" {
		t.Fatalf("outcome = %+v, %v (calls %v)", out, err, rec.args)
	}
}

func TestAdapterCallLimits(t *testing.T) {
	rec := &recorder{}
	in := AdapterInput{Tool: "x", Operations: []string{"read_file"}, Call: rec.call}
	ctx := context.Background()

	// an operation the connector does not expose
	_, err := RunAdapter(ctx, adapter("javascript", `return ctx.call("rm_rf", {})`, nil), in)
	if err == nil || !strings.Contains(err.Error(), `no operation "rm_rf"`) || len(rec.ops) != 0 {
		t.Fatalf("unknown operation = %v (calls %v)", err, rec.ops)
	}
	// a connector failure throws in the script, which may catch it
	rec.fail = errors.New("no such file")
	if _, err = RunAdapter(ctx, adapter("javascript", `return ctx.call("read_file", {})`, nil), in); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("connector failure = %v", err)
	}
	out, err := RunAdapter(ctx, adapter("javascript", `try { ctx.call("read_file", {}) } catch (e) { return {error: String(e)} }`, nil), in)
	if err != nil || !strings.Contains(out.Result["error"].(string), "no such file") {
		t.Fatalf("caught failure = %+v, %v", out, err)
	}
	// too many calls
	rec.fail = nil
	_, err = RunAdapter(ctx, adapter("javascript", `for (var i = 0; i < 100; i++) ctx.call("read_file", {})`, nil), in)
	if err == nil || !strings.Contains(err.Error(), "connector calls") || len(rec.ops) > MaxAdapterCalls+2 {
		t.Fatalf("call limit = %v (%d calls)", err, len(rec.ops))
	}
}

func TestGoAdapter(t *testing.T) {
	rec := &recorder{reply: map[string]any{"text": "from go"}}
	code := `package main

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.AdapterCtx) error {
	res, err := ctx.Call("read_file", map[string]any{"path": ctx.Args()["path"]})
	if err != nil {
		return err
	}
	ctx.Return(res)
	return nil
}
`
	in := AdapterInput{Tool: "read", Args: map[string]any{"path": "b.txt"}, Operations: []string{"read_file"}, Call: rec.call}
	out, err := RunAdapter(context.Background(), adapter("go", code, nil), in)
	if err != nil || out.Result["text"] != "from go" || rec.args[0]["path"] != "b.txt" {
		t.Fatalf("go adapter = %+v, %v (calls %v)", out, err, rec.args)
	}
}

func TestAdapterCodeCompiles(t *testing.T) {
	if err := CheckAlgorithmCode(algo.JavaScript, "return ctx.call('x', {})"); err != nil {
		t.Fatal(err)
	}
}
