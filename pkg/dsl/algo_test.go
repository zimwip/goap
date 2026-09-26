package dsl

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
)

func bound(t algo.Usage, lang, code string, params map[string]any) algo.Bound {
	return algo.Bound{Instance: "i", Algorithm: "a", Type: t, Language: lang, Code: code, Params: params}
}

func TestValidatorJS(t *testing.T) {
	b := bound(algo.UsagePropertyValidator, "javascript", `
		var re = new RegExp(ctx.param("pattern"));
		if (!re.test(String(ctx.value()))) ctx.fail(ctx.property() + " does not match " + ctx.param("pattern"));`,
		map[string]any{"pattern": "^REQ-[0-9]+$"})
	out, err := RunAlgorithm(context.Background(), b, AlgorithmInput{Property: "id", Value: "REQ-12"})
	if err != nil || !out.OK() {
		t.Fatalf("valid value rejected: %v %v", err, out.Failures)
	}
	out, err = RunAlgorithm(context.Background(), b, AlgorithmInput{Property: "id", Value: "x"})
	if err != nil || out.OK() || !strings.Contains(out.Failures[0], "does not match") {
		t.Fatalf("invalid value accepted: %v %v", err, out.Failures)
	}
}

func TestValidatorJSReturn(t *testing.T) {
	b := bound(algo.UsagePropertyValidator, "js", `return ctx.value() ? "" : "required"`, nil)
	if out, _ := RunAlgorithm(context.Background(), b, AlgorithmInput{}); out.OK() || out.Failures[0] != "required" {
		t.Fatalf("got %v", out.Failures)
	}
	if out, _ := RunAlgorithm(context.Background(), b, AlgorithmInput{Value: "x"}); !out.OK() {
		t.Fatalf("got %v", out.Failures)
	}
}

func TestValidatorGo(t *testing.T) {
	code := `package validator

import (
	"regexp"
	"github.com/zimwip/goap/pkg/dsl"
)

func Run(ctx *dsl.ValidatorCtx) error {
	re := regexp.MustCompile(ctx.Param("pattern").(string))
	if s, _ := ctx.Value().(string); !re.MatchString(s) {
		ctx.Fail("bad " + ctx.Property())
	}
	return nil
}`
	b := bound(algo.UsagePropertyValidator, "go", code, map[string]any{"pattern": "^a+$"})
	if out, err := RunAlgorithm(context.Background(), b, AlgorithmInput{Property: "p", Value: "aaa"}); err != nil || !out.OK() {
		t.Fatalf("%v %v", err, out.Failures)
	}
	if out, err := RunAlgorithm(context.Background(), b, AlgorithmInput{Property: "p", Value: "b"}); err != nil || out.OK() || out.Failures[0] != "bad p" {
		t.Fatalf("%v %v", err, out.Failures)
	}
}

func TestGuardJS(t *testing.T) {
	b := bound(algo.UsageTransitionGuard, "javascript", `
		for (const c of ctx.children()) if (c.state !== ctx.param("state")) ctx.fail(c.key + " is " + c.state);`,
		map[string]any{"state": "approved"})
	in := AlgorithmInput{Children: []Node{{Key: "a", State: "approved"}, {Key: "b", State: "draft"}}, Transition: TransitionInfo{Name: "release"}}
	out, err := RunAlgorithm(context.Background(), b, in)
	if err != nil || len(out.Failures) != 1 || out.Failures[0] != "b is draft" {
		t.Fatalf("%v %v", err, out.Failures)
	}
}

func TestTransitionActionBoth(t *testing.T) {
	js := bound(algo.UsageTransitionAction, "javascript", `ctx.setProp("approvedBy", ctx.param("who")); ctx.removeProp("draftNote")`, map[string]any{"who": "bot"})
	out, err := RunAlgorithm(context.Background(), js, AlgorithmInput{})
	if err != nil || out.Set["approvedBy"] != "bot" || len(out.Unset) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	goCode := `package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.TransitionCtx) error {
	ctx.SetProp("state", ctx.Transition().To)
	return nil
}`
	out, err = RunAlgorithm(context.Background(), bound(algo.UsageTransitionAction, "go", goCode, nil), AlgorithmInput{Transition: TransitionInfo{To: "approved"}})
	if err != nil || out.Set["state"] != "approved" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestAlgorithmErrors(t *testing.T) {
	if _, err := RunAlgorithm(context.Background(), bound(algo.UsagePropertyValidator, "js", `throw new Error("boom")`, nil), AlgorithmInput{}); err == nil {
		t.Fatal("throw must be an error")
	}
	if _, err := RunAlgorithm(context.Background(), bound(algo.UsageAction, "js", `1`, nil), AlgorithmInput{}); err == nil {
		t.Fatal("action usage cannot be run as an algorithm")
	}
	if err := CheckAlgorithmCode("js", "function ("); err == nil {
		t.Fatal("syntax error expected")
	}
	if err := CheckAlgorithmCode("go", "package x\nfunc Other() {}"); err == nil {
		t.Fatal("missing Run expected")
	}
	if err := CheckAlgorithmCode("go", "package x\nfunc Run() error { return nil }"); err != nil {
		t.Fatal(err)
	}
}
