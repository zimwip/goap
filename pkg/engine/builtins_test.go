package engine

import (
	"testing"

	"github.com/zimwip/goap/pkg/builtins"
)

func TestBuiltinRegister(t *testing.T) {
	b := DefaultBuiltins()
	for _, n := range []string{builtins.GraphPropagate, builtins.GraphApply, builtins.DecisionInvestigate, builtins.ProcessStep} {
		if !b.HasBuiltin(n) {
			t.Errorf("%s not registered", n)
		}
	}
	if b.HasBuiltin(builtins.ObserveAnalyze) {
		t.Error("the engine registers no self-improvement builtin")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a duplicate registration must panic")
		}
	}()
	b.Register(builtins.GraphApply, ApplyChange)
}
