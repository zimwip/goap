package dsl

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
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
	var err error
	switch job.Language {
	case "javascript", "js":
		err = runJS(ctx, c, job.Code)
	case "go":
		err = runGo(ctx, c, job.Code)
	default:
		err = fmt.Errorf("unsupported language %q", job.Language)
	}
	res := Result{Logs: c.logs}
	if c.result != nil {
		res.Output = fmt.Sprint(c.result)
	}
	if c.suspended {
		res.Suspended = true
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Items = c.out
	if res.Output == "" {
		res.Output = fmt.Sprintf("%d item(s)", len(c.out))
	}
	return res, nil
}

// ---- JavaScript (goja) --------------------------------------------------

func runJS(ctx context.Context, c *Ctx, code string) error {
	vm := goja.New()
	vm.SetFieldNameMapper(jsNames{})
	if err := vm.Set("ctx", c); err != nil {
		return err
	}
	console := vm.NewObject()
	_ = console.Set("log", func(args ...any) { c.Log(fmt.Sprint(args...)) })
	_ = console.Set("warn", func(args ...any) { c.Warn(fmt.Sprint(args...)) })
	_ = vm.Set("console", console)
	stop := context.AfterFunc(ctx, func() { vm.Interrupt("script interrupted: " + context.Cause(ctx).Error()) })
	defer stop()
	if _, err := vm.RunString(code); err != nil {
		return jsError(err)
	}
	// optional entry point: function run(ctx) { ... }
	if fn, ok := goja.AssertFunction(vm.Get("run")); ok {
		v, err := fn(goja.Undefined(), vm.Get("ctx"))
		if err != nil {
			return jsError(err)
		}
		if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) && c.result == nil {
			c.result = v.Export()
		}
	}
	return nil
}

// jsNames exposes struct fields by their json tag and methods in camelCase
// (LLM → llm, AddImpact → addImpact).
type jsNames struct{}

func (jsNames) FieldName(_ reflect.Type, f reflect.StructField) string {
	if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
		return tag
	}
	return lowerFirst(f.Name)
}

func (jsNames) MethodName(_ reflect.Type, m reflect.Method) string { return lowerFirst(m.Name) }

func lowerFirst(s string) string {
	if s == strings.ToUpper(s) {
		return strings.ToLower(s)
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func jsError(err error) error {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return errors.New(ex.String())
	}
	var in *goja.InterruptedError
	if errors.As(err, &in) {
		return fmt.Errorf("%v", in.Value())
	}
	return err
}

// ---- Go (yaegi) -----------------------------------------------------------

// allowedStdlib is the subset of the standard library available to Go
// scripts: no os, net, syscall, unsafe, reflect or plugin.
var allowedStdlib = []string{
	"bytes/bytes", "encoding/json/json", "errors/errors", "fmt/fmt", "maps/maps", "math/math",
	"regexp/regexp", "slices/slices", "sort/sort", "strconv/strconv", "strings/strings",
	"time/time", "unicode/unicode", "unicode/utf8/utf8",
}

// Symbols exposes this package to Go scripts as "github.com/zimwip/goap/pkg/dsl".
var Symbols = interp.Exports{
	"github.com/zimwip/goap/pkg/dsl/dsl": {
		"Ctx":             reflect.ValueOf((*Ctx)(nil)),
		"Node":            reflect.ValueOf((*Node)(nil)),
		"Link":            reflect.ValueOf((*Link)(nil)),
		"LinkEnd":         reflect.ValueOf((*LinkEnd)(nil)),
		"Item":            reflect.ValueOf((*Item)(nil)),
		"CompleteRequest": reflect.ValueOf((*CompleteRequest)(nil)),
		"CompleteResult":  reflect.ValueOf((*CompleteResult)(nil)),
		"AgentResult":     reflect.ValueOf((*AgentResult)(nil)),
		"ErrSuspended":    reflect.ValueOf(&ErrSuspended).Elem(),
	},
}

type logWriter struct {
	c     *Ctx
	level string
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		w.c.logf(w.level, line)
	}
	return len(p), nil
}

func runGo(ctx context.Context, c *Ctx, code string) error {
	f, err := parser.ParseFile(token.NewFileSet(), "action.go", code, parser.PackageClauseOnly)
	if err != nil {
		return fmt.Errorf("go script: %w", err)
	}
	pkg := f.Name.Name
	i := interp.New(interp.Options{Stdout: logWriter{c, "info"}, Stderr: logWriter{c, "warn"}, Env: []string{}})
	allowed := interp.Exports{}
	for _, k := range allowedStdlib {
		if syms, ok := stdlib.Symbols[k]; ok {
			allowed[k] = syms
		}
	}
	if err := i.Use(allowed); err != nil {
		return err
	}
	if err := i.Use(Symbols); err != nil {
		return err
	}
	if _, err := i.EvalWithContext(ctx, code); err != nil {
		return fmt.Errorf("go script: %w", err)
	}
	v, err := i.EvalWithContext(ctx, pkg+".Run")
	if err != nil {
		return fmt.Errorf("go script must declare func Run(ctx *dsl.Ctx) error: %w", err)
	}
	run, ok := v.Interface().(func(*Ctx) error)
	if !ok {
		return fmt.Errorf("go script: Run must have signature func(*dsl.Ctx) error, got %s", v.Type())
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("go script panic: %v", r)
			}
		}()
		done <- run(c)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// the interpreted goroutine cannot be killed: the sandbox is recycled
		return fmt.Errorf("script interrupted: %w", context.Cause(ctx))
	}
}
