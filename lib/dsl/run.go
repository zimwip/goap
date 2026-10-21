package dsl

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/algo"
	"github.com/zimwip/goap/lib/cache"
)

// DefaultTimeout bounds a script execution.
const DefaultTimeout = 60 * time.Second

// Run executes a script job. Writes are returned only when the script ends
// without error (and without suspension).
func Run(ctx context.Context, job Job, host Host) (Result, error) {
	timeout := job.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := newCtx(ctx, job, host)
	err := runScript(ctx, job.Language, job.Code, c, c, func(v any) {
		if v != nil && c.result == nil {
			c.result = v
		}
	})
	res := Result{Logs: c.logs, VarsSet: c.vars}
	if c.result != nil {
		res.Output = fmt.Sprint(c.result)
	}
	if c.suspended {
		res.Suspended = true
		res.WakeOn = c.wakeOn
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Items, res.Nodes = c.out, c.nodeOps
	if res.Output == "" {
		res.Output = fmt.Sprintf("%d item(s)", len(c.out))
		if len(c.nodeOps) > 0 {
			res.Output += fmt.Sprintf(", %d change impact operation(s)", len(c.nodeOps))
		}
	}
	return res, nil
}

// ---- generic script engine ------------------------------------------------------

// scriptLogger receives the console / stdout output of a script.
type scriptLogger interface {
	logf(level, msg string)
}

// jsEngine runs JavaScript. Two implementations: goja (in the Go process, the default) and wasm (QuickJS compiled to
// WebAssembly, run by wazero: memory capped, interrupted for real at the deadline). Both compile a script once and keep
// the result by the hash of its source, so the parse happens when the code is saved (CheckAlgorithmCode), not at
// every run.
type jsEngine interface {
	// compile checks the code and keeps its compiled form.
	compile(code string) error
	// run runs the code with obj as `ctx`, then the optional function run(ctx), whose value (exported to Go: nil
	// for undefined / null) goes to onReturn.
	run(ctx context.Context, code string, obj any, lg scriptLogger, onReturn func(any)) error
}

// Engine names (GOAP_JS_ENGINE).
const (
	EngineGoja = "goja"
	EngineWasm = "wasm"
)

var (
	engineMu sync.Mutex
	engines  = map[string]jsEngine{}
	// engineName is the engine of this process: GOAP_JS_ENGINE, goja by default; SetEngine in tests.
	engineName = func() string {
		if e := os.Getenv("GOAP_JS_ENGINE"); e != "" {
			return e
		}
		return EngineGoja
	}()
)

// SetEngine selects the JavaScript engine of the process (EngineGoja, EngineWasm) and returns the previous one.
func SetEngine(name string) (string, error) {
	if _, err := engineOf(name); err != nil {
		return "", err
	}
	engineMu.Lock()
	defer engineMu.Unlock()
	prev := engineName
	engineName = name
	return prev, nil
}

// Engine is the name of the JavaScript engine of the process.
func Engine() string {
	engineMu.Lock()
	defer engineMu.Unlock()
	return engineName
}

func engineOf(name string) (jsEngine, error) {
	engineMu.Lock()
	defer engineMu.Unlock()
	if e, ok := engines[name]; ok {
		return e, nil
	}
	var e jsEngine
	switch name {
	case EngineGoja:
		e = newGojaEngine()
	case EngineWasm:
		e = newWasmEngine()
	default:
		return nil, fmt.Errorf("unknown JavaScript engine %q (%s, %s)", name, EngineGoja, EngineWasm)
	}
	engines[name] = e
	return e, nil
}

func currentEngine() (jsEngine, error) { return engineOf(Engine()) }

func isJS(language string) bool { return language == algo.JavaScript || language == "js" }

// runScript runs a JavaScript script against a context object, seen by the script as `ctx`. Every usage of the DSL
// (actions, algorithms, adapters) goes through it; only the context object differs. The code runs, then the optional
// function run(ctx) is called and its value passed to onReturn.
func runScript(ctx context.Context, language, code string, obj any, lg scriptLogger, onReturn func(any)) error {
	if !isJS(language) {
		return fmt.Errorf("unsupported language %q", language)
	}
	e, err := currentEngine()
	if err != nil {
		return err
	}
	return e.run(ctx, code, obj, lg, onReturn)
}

// compiledCache keeps the compiled form of a script by the hash of its source (a lib/cache, scriptBounds; the scripts
// of the methodologies and domains in force are far fewer).
type compiledCache[T any] struct {
	once sync.Once
	m    *cache.Cache[[32]byte, compiledScript[T]]
}

type compiledScript[T any] struct {
	v     T
	bytes int64 // estimated
}

// scriptBounds bound the compiled scripts of one engine.
var scriptBounds = cache.Bounds{Bytes: 32 << 20, TTL: 10 * time.Minute}

// get is the compiled form of code, built and kept on a miss; a compiled form is taken to hold four times its source
// (an estimate).
func (c *compiledCache[T]) get(code string, build func() (T, error)) (T, error) {
	c.once.Do(func() {
		c.m = cache.New(cache.Fixed(scriptBounds), func(_ [32]byte, s compiledScript[T]) int64 { return s.bytes })
	})
	key := sha256.Sum256([]byte(code))
	if s, ok := c.m.Get(key); ok {
		return s.v, nil
	}
	v, err := build()
	if err != nil {
		return v, err
	}
	c.m.Put(key, compiledScript[T]{v: v, bytes: 4 * int64(len(code))})
	return v, nil
}

// jsName is the name of a struct field (its json tag, else its name in lower camel case) or of a method (lower camel
// case: LLM → llm, ImpactNode → impactNode) as JavaScript sees it, whatever the engine.
func jsFieldName(f reflect.StructField) string {
	if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
		return tag
	}
	return lowerFirst(f.Name)
}

func lowerFirst(s string) string {
	if s == strings.ToUpper(s) {
		return strings.ToLower(s)
	}
	return strings.ToLower(s[:1]) + s[1:]
}
