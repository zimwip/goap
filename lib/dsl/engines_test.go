package dsl

import (
	"context"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/algo"
)

// TestWasmEngine runs the tests of the DSL again on the wasm engine (the whole suite runs on it with
// GOAP_JS_ENGINE=wasm, make test-wasm).
func TestWasmEngine(t *testing.T) {
	if Engine() == EngineWasm {
		t.Skip("the suite already runs on the wasm engine")
	}
	prev, err := SetEngine(EngineWasm)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = SetEngine(prev) }()
	for name, f := range map[string]func(*testing.T){
		"JavaScript": TestJavaScript, "JavaScriptWithoutRun": TestJavaScriptWithoutRun, "SandboxingAndErrors": TestSandboxingAndErrors,
		"Suspension": TestSuspension, "ChangeImpactOperations": TestChangeImpactOperations,
		"ValidatorJS": TestValidatorJS, "ValidatorJSReturn": TestValidatorJSReturn, "ValidatorRegexp": TestValidatorRegexp,
		"GuardJS": TestGuardJS, "TransitionAction": TestTransitionAction, "AlgorithmErrors": TestAlgorithmErrors, "LinkValidator": TestLinkValidator,
		"AdapterMapsTheTool": TestJavaScriptAdapterMapsTheToolOntoTheConnector, "AdapterWithoutArguments": TestAdapterWithoutArguments,
		"AdapterCallLimits": TestAdapterCallLimits, "AdapterReturn": TestAdapterReturn, "AdapterCodeCompiles": TestAdapterCodeCompiles,
		"Isolation": testIsolation,
	} {
		t.Run(name, f)
	}
}

// testIsolation: what a script leaves on the global object never reaches the next run, and a run that times out
// leaves the engine usable.
func testIsolation(t *testing.T) {
	ctx := context.Background()
	b := algo.Bound{Instance: "i", Algorithm: "a", Type: algo.UsagePropertyValidator, Language: algo.JavaScript}
	b.Code = `globalThis.leak = 1; leaked = 2;`
	if _, err := RunAlgorithm(ctx, b, AlgorithmInput{}); err == nil {
		t.Fatal("assigning an undeclared variable must fail (strict mode)")
	}
	b.Code = `globalThis.leak = 1`
	if _, err := RunAlgorithm(ctx, b, AlgorithmInput{}); err != nil {
		t.Fatal(err)
	}
	b.Code = `if (typeof leak !== "undefined") ctx.fail("leaked")`
	for range 4 {
		if out, err := RunAlgorithm(ctx, b, AlgorithmInput{}); err != nil || !out.OK() {
			t.Fatalf("%v %v", err, out.Failures)
		}
	}
	res, err := Run(ctx, Job{Language: "javascript", Code: `while (true) {}`, Timeout: 100 * time.Millisecond}, &fakeHost{})
	if err == nil {
		t.Fatalf("expected a timeout, got %+v", res)
	}
	if out, err := RunAlgorithm(ctx, b, AlgorithmInput{}); err != nil || !out.OK() {
		t.Fatalf("after a timeout: %v %v", err, out.Failures)
	}
}

// BenchmarkValidator measures one run of a property validator, the hot path of the scripts (run in the commit
// transaction for every node of the type).
func BenchmarkValidator(b *testing.B) {
	for _, engine := range []string{EngineGoja, EngineWasm} {
		b.Run(engine, func(b *testing.B) {
			prev, _ := SetEngine(engine)
			defer func() { _, _ = SetEngine(prev) }()
			bound := algo.Bound{Instance: "i", Algorithm: "a", Type: algo.UsagePropertyValidator, Language: algo.JavaScript,
				Code: `const v = ctx.value(); if (typeof v !== "string" || v.trim() === "") ctx.fail(ctx.property() + " must not be blank");`}
			in := AlgorithmInput{Property: "title", Value: "Pay"}
			if _, err := RunAlgorithm(context.Background(), bound, in); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err := RunAlgorithm(context.Background(), bound, in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
