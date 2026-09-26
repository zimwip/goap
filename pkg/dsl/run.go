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
	err := runScript(ctx, job.Language, job.Code, c, c, func(v goja.Value) {
		if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) && c.result == nil {
			c.result = v.Export()
		}
	}, func(entry any) (func() error, error) {
		run, ok := entry.(func(*Ctx) error)
		if !ok {
			return nil, fmt.Errorf("go script: Run must have signature func(*dsl.Ctx) error, got %T", entry)
		}
		return func() error { return run(c) }, nil
	})
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

// ---- generic script engine ------------------------------------------------------

// scriptLogger receives the console / stdout output of a script.
type scriptLogger interface {
	logf(level, msg string)
}

// runScript runs a script in the given language against a context object: the
// script sees it as `ctx` (JavaScript) or receives it as the argument of Run
// (Go). Every usage of the DSL (actions, algorithms) goes through it; only the
// context object and the Go entry point signature differ.
//
// JavaScript: the code runs, then the optional function run(ctx) is called and
// its value passed to onReturn. Go: the code must declare func Run(...) whose
// signature bind checks, returning the call to make.
func runScript(ctx context.Context, language, code string, obj any, lg scriptLogger,
	onReturn func(goja.Value), bind func(entry any) (func() error, error)) error {
	switch language {
	case "javascript", "js":
		return runJS(ctx, obj, lg, code, onReturn)
	case "go":
		return runGo(ctx, lg, code, bind)
	}
	return fmt.Errorf("unsupported language %q", language)
}

// ---- JavaScript (goja) --------------------------------------------------

func runJS(ctx context.Context, obj any, lg scriptLogger, code string, onReturn func(goja.Value)) error {
	vm := goja.New()
	vm.SetFieldNameMapper(jsNames{})
	if err := vm.Set("ctx", obj); err != nil {
		return err
	}
	console := vm.NewObject()
	_ = console.Set("log", func(args ...any) { lg.logf("info", fmt.Sprint(args...)) })
	_ = console.Set("warn", func(args ...any) { lg.logf("warn", fmt.Sprint(args...)) })
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
		onReturn(v)
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
		"ValidatorCtx":    reflect.ValueOf((*ValidatorCtx)(nil)),
		"GuardCtx":        reflect.ValueOf((*GuardCtx)(nil)),
		"TransitionCtx":   reflect.ValueOf((*TransitionCtx)(nil)),
		"ChangeInfo":      reflect.ValueOf((*ChangeInfo)(nil)),
		"TransitionInfo":  reflect.ValueOf((*TransitionInfo)(nil)),
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
	c     scriptLogger
	level string
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		w.c.logf(w.level, line)
	}
	return len(p), nil
}

func runGo(ctx context.Context, lg scriptLogger, code string, bind func(entry any) (func() error, error)) error {
	f, err := parser.ParseFile(token.NewFileSet(), "action.go", code, parser.PackageClauseOnly)
	if err != nil {
		return fmt.Errorf("go script: %w", err)
	}
	pkg := f.Name.Name
	i := interp.New(interp.Options{Stdout: logWriter{lg, "info"}, Stderr: logWriter{lg, "warn"}, Env: []string{}})
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
	call, err := bind(v.Interface())
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("go script panic: %v", r)
			}
		}()
		done <- call()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// the interpreted goroutine cannot be killed: the sandbox is recycled
		return fmt.Errorf("script interrupted: %w", context.Cause(ctx))
	}
}
