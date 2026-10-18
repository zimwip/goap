package dsl

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/algo"
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

func TestValidatorRegexp(t *testing.T) {
	b := bound(algo.UsagePropertyValidator, "javascript", `
if (!new RegExp(ctx.param("pattern")).test(ctx.value())) ctx.fail("bad " + ctx.property());`, map[string]any{"pattern": "^a+$"})
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

func TestTransitionAction(t *testing.T) {
	js := bound(algo.UsageTransitionAction, "javascript", `ctx.setProp("approvedBy", ctx.param("who")); ctx.removeProp("draftNote")`, map[string]any{"who": "bot"})
	out, err := RunAlgorithm(context.Background(), js, AlgorithmInput{})
	if err != nil || out.Set["approvedBy"] != "bot" || len(out.Unset) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	out, err = RunAlgorithm(context.Background(), bound(algo.UsageTransitionAction, "javascript", `ctx.setProp("state", ctx.transition().to)`, nil),
		AlgorithmInput{Transition: TransitionInfo{To: "approved"}})
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
	if err := CheckAlgorithmCode("go", "package x\nfunc Run() error { return nil }"); err == nil {
		t.Fatal("go is no longer a script language")
	}
}

func TestLinkValidator(t *testing.T) {
	in := AlgorithmInput{
		Link: LinkInfo{Type: "d@refs", Props: map[string]any{"weight": 3}},
		From: Node{Key: "A", Props: map[string]any{"colour": "red"}},
		To:   Node{Key: "B", Props: map[string]any{"colour": "blue"}},
	}
	js := bound(algo.UsageLinkValidator, "javascript", `
		if (ctx.from().props.colour !== ctx.to().props.colour) ctx.fail(ctx.link().type + " " + ctx.from().key + "->" + ctx.to().key + " w" + ctx.link().props.weight + " " + ctx.param("p"));`,
		map[string]any{"p": "x"})
	out, err := RunAlgorithm(context.Background(), js, in)
	if err != nil || len(out.Failures) != 1 || out.Failures[0] != "d@refs A->B w3 x" {
		t.Fatalf("js: %v %v", err, out.Failures)
	}
	in.To.Props["colour"] = "red"
	for _, b := range []algo.Bound{js} {
		if out, err := RunAlgorithm(context.Background(), b, in); err != nil || !out.OK() {
			t.Fatalf("%s accepted link rejected: %v %v", b.Language, err, out.Failures)
		}
	}
}
