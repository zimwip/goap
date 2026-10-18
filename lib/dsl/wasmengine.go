package dsl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fastschema/qjs"
)

// wasmEngine runs JavaScript with QuickJS compiled to WebAssembly (github.com/fastschema/qjs), executed by wazero in
// the Go process, with no cgo: the whole tool chain, the compiler of the scripts included, is the WebAssembly module
// embedded in the binary. A script is compiled once to QuickJS bytecode (at save, CheckAlgorithmCode, else at its first
// run) and kept by the hash of its source; it runs in a runtime of a pool, its memory capped (wasmMemoryLimit) and
// interrupted for real at the deadline (the module is closed, the runtime dropped).
//
// What the script sees is the same as with goja: `ctx` with the methods of the context object in lower camel case,
// `console.log` / `warn`, the optional function run(ctx). Values cross the boundary as JSON: a Go struct is an object
// named by its json tags, an integer number an int64. The code runs in strict mode (QuickJS evaluates so). A runtime
// whose global object a script changed is not reused.
type wasmEngine struct {
	once    sync.Once
	initErr error
	opt     qjs.Option

	compileMu sync.Mutex
	compiler  *qjs.Runtime
	bytecode  compiledCache[[]byte]

	pool chan *wasmVM
}

const (
	wasmMemoryLimit = 256 << 20
	// wasmMaxRuns recycles a runtime after so many runs (what a script may leave behind in the intrinsics).
	wasmMaxRuns = 1000
)

func newWasmEngine() *wasmEngine {
	return &wasmEngine{pool: make(chan *wasmVM, 2*runtime.GOMAXPROCS(0))}
}

// init prepares the options shared by the runtimes: an empty directory as the only file system the module sees, the
// compilation of the QuickJS module cached on disk (the user cache directory) when it can be.
func (e *wasmEngine) init() error {
	e.once.Do(func() {
		dir, err := os.MkdirTemp("", "goap-qjs-")
		if err != nil {
			e.initErr = fmt.Errorf("wasm engine: %w", err)
			return
		}
		e.opt = qjs.Option{CWD: dir, Context: context.Background(), CloseOnContextDone: true,
			MemoryLimit: wasmMemoryLimit, Stdout: io.Discard, Stderr: io.Discard}
		if c, err := os.UserCacheDir(); err == nil {
			c = filepath.Join(c, "goap", "qjs")
			if os.MkdirAll(c, 0o700) == nil {
				e.opt.CacheDir = c
			}
		}
		e.compiler, e.initErr = qjs.New(e.opt)
		if e.initErr != nil {
			e.initErr = fmt.Errorf("wasm engine: %w", e.initErr)
		}
	})
	return e.initErr
}

// wrap makes the script the body of a function (its declarations stay its own) that calls run(ctx) when the script
// declares it, and answers {v, g} in JSON: the value of run, the number of globals once done; or {e} with what the
// script threw (caught there: an exception never crosses into qjs, whose exception path is not sound). On the first
// line, so that the lines of an error are the script's.
func wrap(code string) string {
	return `(function(){try{const __goap_r=(function(ctx){` + code +
		"\n;return typeof run==='function'?run(ctx):undefined;})(globalThis.ctx);" +
		`return JSON.stringify({v:__goap_r===undefined?null:__goap_r,g:Object.getOwnPropertyNames(globalThis).length});` +
		`}catch(e){return JSON.stringify({e:String(e),s:e&&typeof e.stack==="string"?e.stack:""});}})()`
}

func (e *wasmEngine) program(code string) ([]byte, error) {
	if err := e.init(); err != nil {
		return nil, err
	}
	return e.bytecode.get(code, func() (bc []byte, err error) {
		e.compileMu.Lock()
		defer e.compileMu.Unlock()
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("wasm engine: compile: %v", r)
			}
		}()
		bc, err = e.compiler.Context().Compile("script.js", qjs.Code(wrap(code)))
		if err != nil {
			// the compiler went through the exception path of qjs: start a new one
			e.compiler.Close()
			if e.compiler, e.initErr = qjs.New(e.opt); e.initErr != nil {
				e.initErr = fmt.Errorf("wasm engine: %w", e.initErr)
			}
			return nil, wasmError(err)
		}
		return bc, nil
	})
}

func (e *wasmEngine) compile(code string) error {
	_, err := e.program(code)
	return err
}

func (e *wasmEngine) run(ctx context.Context, code string, obj any, lg scriptLogger, onReturn func(any)) error {
	bc, err := e.program(code)
	if err != nil {
		return err
	}
	vm, err := e.get()
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		vm.cur = nil
		if keep {
			e.put(vm)
		} else {
			vm.close()
		}
	}()
	vm.cur = &wasmBinding{obj: reflect.ValueOf(obj), methods: methodsOf(reflect.TypeOf(obj)), lg: lg}
	out, err := vm.exec(ctx, bc)
	if err != nil {
		// a runtime is not reused after a failure (qjs does not always leave it sound after an exception)
		if ctx.Err() != nil {
			return fmt.Errorf("script interrupted: %w", context.Cause(ctx))
		}
		return err
	}
	var res struct {
		V json.RawMessage `json:"v"`
		G int             `json:"g"`
		E *string         `json:"e"`
		S string          `json:"s"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return fmt.Errorf("wasm engine: result: %w", err)
	}
	if res.E != nil {
		// as goja tells an exception: the message, then where it was thrown
		return errors.New(strings.TrimRight(*res.E+"\n"+res.S, "\n"))
	}
	vm.runs++
	keep = res.G == vm.globals && vm.runs < wasmMaxRuns
	v, err := decodeJS(res.V)
	if err != nil {
		return fmt.Errorf("wasm engine: result: %w", err)
	}
	if v != nil {
		onReturn(v)
	}
	return nil
}

func (e *wasmEngine) get() (*wasmVM, error) {
	select {
	case vm := <-e.pool:
		return vm, nil
	default:
	}
	return newWasmVM(e.opt)
}

func (e *wasmEngine) put(vm *wasmVM) {
	select {
	case e.pool <- vm:
	default:
		vm.close()
	}
}

// wasmVM is a QuickJS runtime of the pool, with the binding of the run in progress.
type wasmVM struct {
	rt      *qjs.Runtime
	bind    *qjs.Value
	bound   reflect.Type
	ctx     *runContext
	cur     *wasmBinding
	globals int
	runs    int
	dead    bool
}

// close frees the runtime. A runtime closed at the deadline cannot free its QuickJS state: its module is gone anyway.
func (vm *wasmVM) close() {
	defer func() { _ = recover() }()
	if vm.bind != nil && !vm.dead {
		vm.bind.Free()
	}
	vm.bind = nil
	vm.rt.Close()
}

// wasmPrelude installs, once per runtime, what every script sees: no std / os module, console, and __goap_bind, which
// gives `ctx` the methods of the context object of the run (each one calls __goap_call, the only way out).
const wasmPrelude = `
delete globalThis.std; delete globalThis.os; delete globalThis.bjson;
(() => {
  const call = globalThis.__goap_call, log = globalThis.__goap_log;
  delete globalThis.__goap_call; delete globalThis.__goap_log;
  const text = (a) => a.map((x) => { if (typeof x === 'string') return x; try { const s = JSON.stringify(x); return s === undefined ? String(x) : s; } catch (_) { return String(x); } }).join(' ');
  globalThis.console = { log: (...a) => log('info', text(a)), info: (...a) => log('info', text(a)), warn: (...a) => log('warn', text(a)), error: (...a) => log('warn', text(a)) };
  globalThis.ctx = {};
  globalThis.__goap_bind = (names) => {
    const ctx = {};
    for (const n of JSON.parse(names)) {
      ctx[n] = (...args) => { const r = JSON.parse(call(n, JSON.stringify(args))); if ('e' in r) throw new Error(r.e); return r.v; };
    }
    globalThis.ctx = ctx;
  };
})();
Object.getOwnPropertyNames(globalThis).length;
`

func newWasmVM(opt qjs.Option) (vm *wasmVM, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wasm engine: %v", r)
		}
	}()
	// wazero watches the context of a call from a goroutine of its own: the runtime gets a context of its own from the
	// start, whose deadline a run sets through an atomic pointer
	rc := newRunContext()
	opt.Context = rc
	rt, err := qjs.New(opt)
	if err != nil {
		return nil, fmt.Errorf("wasm engine: %w", err)
	}
	vm = &wasmVM{rt: rt, ctx: rc}
	c := rt.Context()
	c.SetFunc("__goap_call", func(this *qjs.This) (*qjs.Value, error) {
		args := this.Args()
		if len(args) != 2 || vm.cur == nil {
			return nil, errors.New("ctx is not available")
		}
		return this.Context().NewString(vm.cur.call(args[0].String(), args[1].String())), nil
	})
	c.SetFunc("__goap_log", func(this *qjs.This) (*qjs.Value, error) {
		args := this.Args()
		if len(args) == 2 && vm.cur != nil && vm.cur.lg != nil {
			vm.cur.lg.logf(args[0].String(), args[1].String())
		}
		return nil, nil
	})
	n, err := c.Eval("prelude.js", qjs.Code(wasmPrelude))
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("wasm engine: prelude: %w", err)
	}
	vm.globals = int(n.Int32())
	n.Free()
	vm.bind = c.Global().GetPropertyStr("__goap_bind")
	return vm, nil
}

// runContext is the context of the run in progress, for the calls of the module: a context.Context whose methods
// delegate to the one set last.
type runContext struct {
	cur atomic.Pointer[context.Context]
}

func newRunContext() *runContext {
	r := &runContext{}
	r.set(context.Background())
	return r
}

func (r *runContext) set(ctx context.Context)     { r.cur.Store(&ctx) }
func (r *runContext) get() context.Context        { return *r.cur.Load() }
func (r *runContext) Deadline() (time.Time, bool) { return r.get().Deadline() }
func (r *runContext) Done() <-chan struct{}       { return r.get().Done() }
func (r *runContext) Err() error                  { return r.get().Err() }
func (r *runContext) Value(key any) any           { return r.get().Value(key) }

// exec binds ctx then runs the bytecode, answering its JSON. A panic of qjs (the module closed at the deadline) marks
// the runtime dead.
func (vm *wasmVM) exec(ctx context.Context, bc []byte) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			vm.dead = true
			err = fmt.Errorf("wasm engine: %v", r)
		}
	}()
	c := vm.rt.Context()
	vm.ctx.set(ctx)
	defer vm.ctx.set(context.Background())
	// ctx keeps its methods while the runs are of the same type of context object
	if t := vm.cur.obj.Type(); t != vm.bound {
		names, _ := json.Marshal(vm.cur.names())
		arg := c.NewString(string(names))
		defer arg.Free()
		r, err := c.Invoke(vm.bind, c.Global(), arg)
		if err != nil {
			return "", wasmError(err)
		}
		r.Free()
		vm.bound = t
	}
	v, err := c.Eval("script.js", qjs.Bytecode(bc))
	if err != nil {
		return "", wasmError(err)
	}
	defer v.Free()
	return v.String(), nil
}

// wasmError is an error of qjs itself (a script that does not compile): its message, without the stack of qjs.
func wasmError(err error) error {
	msg, _, _ := strings.Cut(err.Error(), "\n    at ")
	return errors.New(msg)
}

// wasmBinding is the context object of a run, as __goap_call reaches it.
type wasmBinding struct {
	obj     reflect.Value
	methods map[string]int
	lg      scriptLogger
}

func (b *wasmBinding) names() []string {
	out := make([]string, 0, len(b.methods))
	for n := range b.methods {
		out = append(out, n)
	}
	return out
}

var methodCache sync.Map // reflect.Type → map[string]int

// methodsOf maps the JavaScript names of the exported methods of t to their index.
func methodsOf(t reflect.Type) map[string]int {
	if m, ok := methodCache.Load(t); ok {
		return m.(map[string]int)
	}
	m := map[string]int{}
	for i := range t.NumMethod() {
		m[lowerFirst(t.Method(i).Name)] = i
	}
	methodCache.Store(t, m)
	return m
}

// call runs a method with the arguments of the script (a JSON array) and answers {"v": value} or {"e": message}.
func (b *wasmBinding) call(name, argsJSON string) (out string) {
	fail := func(err error) string {
		s, _ := json.Marshal(map[string]string{"e": err.Error()})
		return string(s)
	}
	defer func() {
		if r := recover(); r != nil {
			out = fail(fmt.Errorf("%s: %v", name, r))
		}
	}()
	i, ok := b.methods[name]
	if !ok {
		return fail(fmt.Errorf("ctx.%s is not a function", name))
	}
	m := b.obj.Method(i)
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return fail(err)
	}
	args, err := goArgs(m.Type(), raw)
	if err != nil {
		return fail(fmt.Errorf("ctx.%s: %w", name, err))
	}
	var res []reflect.Value
	if m.Type().IsVariadic() {
		res = m.CallSlice(args)
	} else {
		res = m.Call(args)
	}
	if n := len(res); n > 0 && res[n-1].Type() == errorType {
		if !res[n-1].IsNil() {
			return fail(res[n-1].Interface().(error))
		}
		res = res[:n-1]
	}
	var v any
	switch len(res) {
	case 0:
	case 1:
		v = exportJS(res[0])
	default:
		l := make([]any, len(res))
		for i, r := range res {
			l[i] = exportJS(r)
		}
		v = l
	}
	s, err := json.Marshal(map[string]any{"v": v})
	if err != nil {
		return fail(fmt.Errorf("ctx.%s: %w", name, err))
	}
	return string(s)
}

var errorType = reflect.TypeFor[error]()

// goArgs converts the arguments of the script to the parameters of a method: missing ones are zero, extra ones
// ignored, a variadic method takes the rest.
func goArgs(t reflect.Type, raw []json.RawMessage) ([]reflect.Value, error) {
	n := t.NumIn()
	args := make([]reflect.Value, 0, n)
	for i := range n {
		pt := t.In(i)
		if t.IsVariadic() && i == n-1 {
			rest := reflect.MakeSlice(pt, 0, len(raw))
			for j := i; j < len(raw); j++ {
				v, err := goValue(raw[j], pt.Elem())
				if err != nil {
					return nil, fmt.Errorf("argument %d: %w", j+1, err)
				}
				rest = reflect.Append(rest, v)
			}
			return append(args, rest), nil
		}
		if i >= len(raw) {
			args = append(args, reflect.Zero(pt))
			continue
		}
		v, err := goValue(raw[i], pt)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", i+1, err)
		}
		args = append(args, v)
	}
	return args, nil
}

// goValue converts a JSON value to t: as the script value exported (integers int64, objects map[string]any) when that
// fits t, else decoded into t.
func goValue(raw json.RawMessage, t reflect.Type) (reflect.Value, error) {
	v, err := decodeJS(raw)
	if err != nil {
		return reflect.Value{}, err
	}
	if v == nil {
		return reflect.Zero(t), nil
	}
	rv := reflect.ValueOf(v)
	if rv.Type().AssignableTo(t) {
		return rv, nil
	}
	if rv.Type().ConvertibleTo(t) && rv.Kind() != reflect.String && t.Kind() != reflect.String {
		return rv.Convert(t), nil
	}
	p := reflect.New(t)
	if err := json.Unmarshal(raw, p.Interface()); err != nil {
		return reflect.Value{}, err
	}
	return p.Elem(), nil
}

// decodeJS decodes a JSON value of the script. A number is a float64, as when it went through JSON on its way to or
// from a connector (goja keeps a Go value as it was given and makes an int64 of an integral number the script
// computed: JSON cannot tell them apart, and the data of the scripts mostly goes through JSON anyway).
func decodeJS(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return numbers(v), nil
}

func numbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = numbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = numbers(e)
		}
	}
	return v
}

var marshalerType = reflect.TypeFor[json.Marshaler]()

// exportJS gives a Go value the shape the script sees with goja: struct fields named by jsFieldName.
func exportJS(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.Type().Implements(marshalerType) {
		return v.Interface()
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return exportJS(v.Elem())
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			out[jsFieldName(f)] = exportJS(v.Field(i))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := make(map[string]any, v.Len())
		it := v.MapRange()
		for it.Next() {
			out[fmt.Sprint(it.Key().Interface())] = exportJS(it.Value())
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		fallthrough
	case reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return v.Interface()
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = exportJS(v.Index(i))
		}
		return out
	case reflect.Func, reflect.Chan:
		return nil
	}
	return v.Interface()
}
