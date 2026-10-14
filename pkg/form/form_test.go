package form

import (
	"strings"
	"testing"
)

func linkForm() Form {
	return Form{Fields: []Field{
		{Name: "requirement", Widget: WidgetNode, Required: true, Constraints: map[string]any{"types": []any{"alm@Requirement"}}},
		{Name: "realizedBy", Widget: WidgetLink, Constraints: map[string]any{"from": "requirement", "linkTypes": []any{"alm@realized_by"},
			"toTypes": []any{"alm@Application"}, "max": 2}},
		{Name: "why", Widget: WidgetTextarea, Required: true, Constraints: map[string]any{"minLength": 3}},
		{Name: "level", Widget: WidgetSelect, Default: "low", Constraints: map[string]any{"options": []any{"low", "high"}}},
		{Name: "intro", Widget: WidgetMarkdown, Constraints: map[string]any{"text": "Pick a requirement."}},
	}}
}

func env() Env {
	nodes := map[string]Node{
		"REQ-1": {Key: "REQ-1", Type: "alm@Requirement", State: "approved"},
		"APP-1": {Key: "APP-1", Type: "alm@WebApplication"},
		"APP-2": {Key: "APP-2", Type: "alm@Application"},
		"CMP-1": {Key: "CMP-1", Type: "alm@Component"},
	}
	return Env{
		Node: func(k string) (Node, bool) { n, ok := nodes[k]; return n, ok },
		IsA: func(t, s string) bool {
			return t == s || t == "alm@WebApplication" && s == "alm@Application"
		},
	}
}

func TestCheckForm(t *testing.T) {
	if is := CheckForm(linkForm()); len(is) > 0 {
		t.Fatalf("valid form: %v", is)
	}
	bad := Form{Fields: []Field{
		{Name: "1x", Widget: WidgetText},
		{Name: "a", Widget: "slider"},
		{Name: "a", Widget: WidgetSelect},
		{Name: "b", Widget: WidgetText, Constraints: map[string]any{"options": []any{"x"}, "pattern": "("}},
		{Name: "c", Widget: WidgetLink, Constraints: map[string]any{"from": "b"}},
		{Name: "d", Widget: WidgetLink},
		{Name: "e", Widget: WidgetNumber, Constraints: map[string]any{"min": "zero"}},
		{Name: "f", Widget: WidgetSelect, Default: "z", Constraints: map[string]any{"options": []any{"x"}}},
	}}
	got := CheckForm(bad).Error()
	for _, want := range []string{
		`fields[0].name: invalid field name "1x"`,
		`fields[1].widget: unknown widget "slider"`,
		`fields[2].name: duplicate field "a"`,
		`fields[2].constraints: widget select needs the constraint "options"`,
		`fields[3].constraints.options: widget text has no constraint "options"`,
		`fields[3]: invalid pattern`,
		`fields[4].constraints.from: field "b" is a text, not a node`,
		`fields[5]: a link field names its source`,
		`fields[6].constraints.min: expects a number`,
		`fields[7].default: "z" is not one of the options`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestValidate(t *testing.T) {
	f := linkForm()
	clean, is := Validate(f, map[string]any{
		"requirement": "REQ-1",
		"realizedBy":  []any{map[string]any{"to": "APP-1"}, map[string]any{"to": "APP-2", "type": "alm@realized_by"}},
		"why":         "because",
	}, env())
	if len(is) > 0 {
		t.Fatalf("issues: %v", is)
	}
	if clean["level"] != "low" {
		t.Errorf("default not applied: %v", clean["level"])
	}
	links := Links(f, clean)
	if len(links) != 2 || links[0] != (Link{Field: "realizedBy", From: "REQ-1", Type: "alm@realized_by", To: "APP-1"}) {
		t.Errorf("links: %+v", links)
	}

	_, is = Validate(f, map[string]any{
		"requirement": "APP-2",
		"realizedBy":  []any{map[string]any{"to": "CMP-1"}, map[string]any{"to": "NOPE"}, map[string]any{"to": "APP-2"}},
		"why":         "x",
		"level":       "medium",
		"extra":       1,
	}, env())
	got := is.Error()
	for _, want := range []string{
		"extra: not a field of the form",
		"requirement: APP-2 is a alm@Application, not one of alm@Requirement",
		"realizedBy: link 1: CMP-1 is a alm@Component, not one of alm@Application",
		`realizedBy: link 2: unknown node "NOPE"`,
		"realizedBy: at most 2 links",
		"why: at least 3 characters",
		`level: "medium" is not one of the options`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}

	_, is = Validate(f, map[string]any{}, env())
	if got := is.Error(); !strings.Contains(got, "requirement: required") || !strings.Contains(got, "why: required") {
		t.Errorf("required: %s", got)
	}
}

func TestValidateNodeConstraints(t *testing.T) {
	f := Form{Fields: []Field{{Name: "n", Widget: WidgetNode, Constraints: map[string]any{"multiple": true, "max": 1,
		"states": []any{"approved"}, "inChange": true}}}}
	_, is := Validate(f, map[string]any{"n": []any{"REQ-1", "APP-2"}}, env())
	got := is.Error()
	for _, want := range []string{"REQ-1 is not a node of the change", "APP-2 is in no state, not one of approved", "at most 1 nodes"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	Register(Widget{Name: WidgetText})
}

func TestConditionalFields(t *testing.T) {
	yes := true
	f := Form{Fields: []Field{
		{Name: "decision", Widget: WidgetSelect, Required: true, Constraints: map[string]any{"options": []any{"accept", "reject"}}},
		{Name: "reason", Widget: WidgetTextarea, VisibleWhen: []Condition{{Field: "decision", Equals: "reject"}}, Required: true},
		{Name: "tags", Widget: WidgetMultiselect, Constraints: map[string]any{"options": []any{"a", "b"}}},
		{Name: "note", Widget: WidgetText, RequiredWhen: []Condition{{Field: "tags", In: []any{"b"}}}},
		{Name: "extra", Widget: WidgetText, VisibleWhen: []Condition{{Field: "reason", Set: &yes}}},
	}}
	if is := CheckForm(f); len(is) > 0 {
		t.Fatalf("valid form: %v", is)
	}
	clean, is := Validate(f, map[string]any{"decision": "accept", "reason": "ignored", "extra": "gone"}, Env{})
	if len(is) > 0 || clean["reason"] != nil || clean["extra"] != nil {
		t.Fatalf("hidden fields: %v %v", clean, is)
	}
	_, is = Validate(f, map[string]any{"decision": "reject", "tags": []any{"a", "b"}}, Env{})
	if got := is.Error(); !strings.Contains(got, "reason: required") || !strings.Contains(got, "note: required") {
		t.Fatalf("required when: %v", got)
	}
	bad := Form{Fields: []Field{
		{Name: "a", Widget: WidgetText, VisibleWhen: []Condition{{Field: "b", Equals: "x"}}},
		{Name: "b", Widget: WidgetText, RequiredWhen: []Condition{{Field: "a"}}},
		{Name: "c", Widget: WidgetMarkdown, Constraints: map[string]any{"text": "t"}},
		{Name: "d", Widget: WidgetText, VisibleWhen: []Condition{{Field: "c", Set: &yes}}},
	}}
	got := CheckForm(bad).Error()
	for _, want := range []string{
		`fields[0].visibleWhen[0].field: no field "b" before this one`,
		`fields[1].requiredWhen[0]: a condition tests exactly one of equals, in or set`,
		`fields[3].visibleWhen[0].field: field "c" is a markdown`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestGroupField(t *testing.T) {
	f := Form{Fields: []Field{
		{Name: "risks", Widget: WidgetGroup, Constraints: map[string]any{"max": 2}, Fields: []Field{
			{Name: "title", Widget: WidgetText, Required: true},
			{Name: "level", Widget: WidgetSelect, Default: "low", Constraints: map[string]any{"options": []any{"low", "high"}}},
			{Name: "mitigation", Widget: WidgetText, RequiredWhen: []Condition{{Field: "level", Equals: "high"}}},
		}},
	}}
	if is := CheckForm(f); len(is) > 0 {
		t.Fatalf("valid form: %v", is)
	}
	clean, is := Validate(f, map[string]any{"risks": []any{map[string]any{"title": "outage"}}}, Env{})
	if len(is) > 0 {
		t.Fatal(is)
	}
	if e := clean["risks"].([]any)[0].(map[string]any); e["level"] != "low" {
		t.Fatalf("default of a sub-field: %v", e)
	}
	_, is = Validate(f, map[string]any{"risks": []any{
		map[string]any{"title": "a", "level": "high"}, map[string]any{"level": "low", "x": 1}, map[string]any{"title": "c"},
	}}, Env{})
	got := is.Error()
	for _, want := range []string{"entry 1, mitigation: required", "entry 2, title: required", "entry 2, x: not a field", "at most 2 entries"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	bad := Form{Fields: []Field{
		{Name: "g", Widget: WidgetGroup},
		{Name: "h", Widget: WidgetGroup, Fields: []Field{{Name: "l", Widget: WidgetLink, Constraints: map[string]any{"fromKey": "X"}}}},
		{Name: "t", Widget: WidgetText, Fields: []Field{{Name: "x", Widget: WidgetText}}},
	}}
	got = CheckForm(bad).Error()
	for _, want := range []string{
		"fields[0].fields: a group needs at least one sub-field",
		"fields[1].fields[0].widget: a group holds no link field",
		"fields[2].fields: only a group has sub-fields",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}
