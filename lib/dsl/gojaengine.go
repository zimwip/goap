package dsl

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/dop251/goja"
)

// gojaEngine runs JavaScript with goja, in the Go process: a VM per run (cheap), the program compiled once per source.
type gojaEngine struct {
	programs compiledCache[*goja.Program]
}

func newGojaEngine() *gojaEngine { return &gojaEngine{} }

func (e *gojaEngine) program(code string) (*goja.Program, error) {
	return e.programs.get(code, func() (*goja.Program, error) {
		p, err := goja.Compile("script.js", code, false)
		if err != nil {
			return nil, jsError(err)
		}
		return p, nil
	})
}

func (e *gojaEngine) compile(code string) error {
	_, err := e.program(code)
	return err
}

func (e *gojaEngine) run(ctx context.Context, code string, obj any, lg scriptLogger, onReturn func(any)) error {
	prog, err := e.program(code)
	if err != nil {
		return err
	}
	vm := goja.New()
	vm.SetFieldNameMapper(jsNames{})
	if err := vm.Set("ctx", obj); err != nil {
		return err
	}
	console := vm.NewObject()
	_ = console.Set("log", func(args ...any) { lg.logf("info", consoleText(args)) })
	_ = console.Set("warn", func(args ...any) { lg.logf("warn", consoleText(args)) })
	_ = vm.Set("console", console)
	stop := context.AfterFunc(ctx, func() { vm.Interrupt("script interrupted: " + context.Cause(ctx).Error()) })
	defer stop()
	if _, err := vm.RunProgram(prog); err != nil {
		return jsError(err)
	}
	// optional entry point: function run(ctx) { ... }
	if fn, ok := goja.AssertFunction(vm.Get("run")); ok {
		v, err := fn(goja.Undefined(), vm.Get("ctx"))
		if err != nil {
			return jsError(err)
		}
		if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
			onReturn(v.Export())
		}
	}
	return nil
}

// consoleText joins the arguments of console.log with spaces, as the wasm engine does.
func consoleText(args []any) string { return strings.TrimSuffix(fmt.Sprintln(args...), "\n") }

// jsNames exposes struct fields by their json tag and methods in camelCase
// (LLM → llm, ImpactNode → impactNode).
type jsNames struct{}

func (jsNames) FieldName(_ reflect.Type, f reflect.StructField) string { return jsFieldName(f) }

func (jsNames) MethodName(_ reflect.Type, m reflect.Method) string { return lowerFirst(m.Name) }

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
