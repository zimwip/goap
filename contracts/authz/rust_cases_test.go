package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// rustCases is where the Rust graph service reads the answers of this package's Casbin (ADR 0163): its port of
// govaluate and of the enforcement must give the same ones.
const rustCases = "../../rust/crates/graph-script/src/casbin_cases.gen.json"

// rustRules are rules as an administrator may write them, well or badly: each is validated, then asked of the
// requests of rustRequests.
var rustRules = []string{
	`hasRole(r.sub, "admin")`,
	`!isAnonymous(r.sub) && ((r.obj.Org != "" && r.obj.Org == r.sub.Org) || onProject(r.sub))`,
	`hasRoleIn(r.sub, r.obj.Role, r.obj)`,
	`mayRun(r.sub, r.obj)`,
	`isProjectAdmin(r.sub)`,
	`isProjectOwner(r.sub)`,
	`r.sub.Subject == r.obj.Owner && !hasRole(r.sub, "admin")`,
	`hasAnyRole(r.sub, "dev", "ops")`,
	`hasAnyRole(r.sub)`,
	`hasRole(r.sub)`,
	`isAnonymous()`,
	`r.sub.Subject == "alice"`,
	`r.sub.Subject != "alice"`,
	`r.sub.Subject`,
	`r.sub.Nope == 1`,
	`r.sub.subject == "x"`,
	`r.obj.Type.Foo == 1`,
	`r.sub.Anonymous`,
	`r.sub.Anonymous()`,
	`!r.sub.Anonymous()`,
	`r.sub.HasRole("dev")`,
	`r.sub.HasRole("dev") || r.sub.Subject == "probe"`,
	`(r.sub.HasRole("dev")) || r.sub.Subject == "probe"`,
	`r.sub.HasRole(1)`,
	`r.sub.HasRole()`,
	`r.sub.HasRole("a", "b")`,
	`r.sub.HasRoleIn("dev", r.obj)`,
	`r.sub.HasRoleIn("dev", r.sub)`,
	`r.sub.MayRun(r.obj)`,
	`r.sub.EndUser() == "bob"`,
	`r.sub.System()`,
	`r.sub.AllRoles() == r.sub.Roles`,
	`r.sub.For("bob").OnBehalfOf == "bob"`,
	`r.sub.OnBehalfOf == "bob"`,
	`r.sub.Project == "PROJ-A"`,
	`r.sub.ProjectAdmin`,
	`r.obj.Roles`,
	`"dev" in r.sub.Roles`,
	`r.obj.Type in ("change", "node")`,
	`r.obj.Type in ("change")`,
	`r.obj.Type IN ("change", "node")`,
	`r.act in ()`,
	`r.sub in (r.sub)`,
	`r.sub.Roles in (r.sub.Roles)`,
	`1 in (1, 2)`,
	`r.sub.Subject =~ "^al"`,
	`r.sub.Subject !~ "^al"`,
	`r.sub.Subject =~ r.obj.Name`,
	`r.sub.Subject =~ "a("`,
	`r.sub.Subject =~ "[\\d]+"`,
	`r.sub.Subject =~ 5`,
	`regexMatch(r.sub.Subject, "^a.*e$")`,
	`regexMatch(r.sub.Subject, "a(")`,
	`regexMatch(r.sub.Subject)`,
	`regexMatch(r.sub.Subject, 1)`,
	`keyMatch(r.obj.Name, "/api/*")`,
	`keyMatch2(r.obj.Name, "/api/:id")`,
	`keyMatch3(r.obj.Name, "/api/{id}")`,
	`keyMatch4(r.obj.Name, "/api/{id}/x/{id}")`,
	`keyMatch5(r.obj.Name, "/api/{id}")`,
	`keyGet(r.obj.Name, "/api/*") == "1"`,
	`keyGet2(r.obj.Name, "/api/:id", "id") == "1"`,
	`keyGet3(r.obj.Name, "/api/{id}", "id") == "1"`,
	`ipMatch(r.obj.ID, "10.0.0.0/8")`,
	`ipMatch(r.obj.ID, "10.0.0.1")`,
	`ipMatch(r.obj.Name, "10.0.0.0/8")`,
	`globMatch(r.obj.Name, "/api/**")`,
	`globMatch(r.obj.Name, "/api/[")`,
	`globMatch(r.obj.Name, "/api/{a,1}")`,
	`eval("true")`,
	`eval(r.sub.Subject)`,
	`eval("r.sub.Subject == \"alice\"")`,
	`eval()`,
	`eval(1)`,
	`nope(r.sub)`,
	`1 + 1 == 2`,
	`1 + "a" == "1a"`,
	`"a" + true == "atrue"`,
	`10 - 2 - 3 == 5`,
	`2 ** 3 ** 2 == 64`,
	`7 % 3 == 1`,
	`8 / 4 / 2 == 1`,
	`-1 < 0`,
	`- 1 < 0`,
	`1 - -1 == 2`,
	`!true == false`,
	`~1 == -2`,
	`3 & 5 == 1`,
	`3 | 5 == 7`,
	`3 ^ 5 == 6`,
	`1 << 4 == 16`,
	`256 >> 4 == 16`,
	`0x1F == 31`,
	`0x == 0`,
	`1.5 * 2 == 3`,
	`1..2 == 1`,
	`.5 == 0.5`,
	`1e3 == 1000`,
	`1000000 == 1e6`,
	`1000000 + "x"`,
	`0.00001 + "x"`,
	`123456789 + "x"`,
	`1 / 0 > 1000`,
	`"b" > "a"`,
	`"b" > 1`,
	`r.sub.Roles > 1`,
	`true ? true : false`,
	`false ? true : false`,
	`r.sub.Subject == "alice" ? true : false`,
	`1 ? true : false`,
	`(false ? true) ?? true`,
	`(true ? "x") == "x"`,
	`r.sub.Subject ?? false`,
	`true && 1`,
	`1 && true`,
	`false && 1`,
	`true || 1`,
	`r.sub.Subject == "2006-01-02"`,
	`"2006-01-02" == 1136160000`,
	`"2006-01-02T15:04:05Z" > 0`,
	`"3:04PM" < 0`,
	`'single' == "single"`,
	`'mixed" == "mixed"`,
	`"esc\"aped" == 'esc"aped'`,
	`"a\\b" == "a\b"`,
	`"unclosed`,
	`(r.sub.Subject == "a"`,
	`r.sub.Subject == "a")`,
	`r.sub.Subject ==`,
	`== r.sub.Subject`,
	`r.sub.`,
	`r.sub..Subject == 1`,
	`[r.sub] == 1`,
	`[unclosed`,
	`r_sub.Subject == "alice"`,
	`p.act == "*"`,
	`p.eft == "allow"`,
	`p.sub_rule != ""`,
	`r.nope == 1`,
	`x == 1`,
	`r.sub.Subject == "alice" &&! true`,
	`r.sub.Subject == "alice" && ! true`,
	`a.b.c`,
	`hasRole(r.sub, "admin") hasRole(r.sub, "dev")`,
	`hasRole == 1`,
	`1 2`,
	`"a" "b"`,
	`true true`,
	`()`,
	`(())`,
	`(true)`,
	`((true))`,
	`true,false`,
	`(true,false)`,
	`r.sub.Subject == 'alice' # a comment`,
	`@`,
	`r.sub.Subject == "é"`,
	`ré == 1`,
	`r.obj.OrgChain`,
	`"UNIT" in r.obj.OrgChain`,
	`r.obj.ProjectID == "PROJ-A"`,
	`r.obj.Accountable == "lead"`,
	`len(r.sub.Roles) > 0`,
	`r.sub.Roles[0] == "dev"`,
	`r.sub.Subject.Length == 5`,
	`true ? 1`,
	`1 ?? 2 == 1`,
	`"x" =~ "a{"`,
	`"a{" =~ "a{"`,
	`"x.y" =~ "\\Q.y\\E"`,
	`"A" =~ "\\101"`,
	`"aaa" =~ "a**"`,
	`"a" =~ "*a"`,
	`"a" =~ "[z-a]"`,
	`"é" =~ "\\w"`,
	`"é" =~ "."`,
	`"x" =~ "(?i)X"`,
	`"x" =~ "(?x)x"`,
}

// rustRequests are the requests each rule is asked of: a person on a project, a platform service acting for one, an
// administrator, and a request the probe of Validate would not make.
var rustRequests = []Request{
	{
		Subject:  Principal{Subject: "alice", Org: "UNIT", Project: "PROJ-A", Roles: []string{"dev", "lead@UNIT"}},
		Action:   "read",
		Resource: Resource{Type: "change", ID: "10.1.2.3", Org: "UNIT", Owner: "alice", Name: "/api/1", Role: "dev", Accountable: "lead", OrgChain: []string{"UNIT", "ORG-DEFAULT"}, ProjectID: "PROJ-A", Roles: []string{"dev"}},
	},
	{
		Subject:  Principal{Subject: "system:registry", OnBehalfOf: "bob", ProjectAdmin: true},
		Action:   "apply",
		Resource: Resource{Type: "node", ID: "192.168.0.1", Namespace: "alm", Name: "/api/1/x/1", Roles: []string{"*"}},
	},
	{
		Subject:  Principal{Subject: "root", Global: []string{"admin"}, ProjectOwner: true},
		Action:   "move",
		Resource: Resource{Type: "project", Name: "/web/1", Owner: "bob"},
	},
}

// rustPolicySets are sets of several rules, for the order Casbin asks them in: a deny matching first stops, a rule
// failing after it is never asked, one failing before it is.
var rustPolicySets = [][]Policy{
	{{Rule: `true`, Resource: "*", Action: "*", Effect: "allow"}, {Rule: `r.sub.Subject == "alice"`, Resource: "*", Action: "*", Effect: "deny"}},
	{{Rule: `r.sub.Subject == "alice"`, Resource: "*", Action: "*", Effect: "deny"}, {Rule: `1 && true`, Resource: "*", Action: "*", Effect: "allow"}},
	{{Rule: `1 && true`, Resource: "*", Action: "*", Effect: "allow"}, {Rule: `true`, Resource: "*", Action: "*", Effect: "deny"}},
	{{Rule: `true`, Resource: "change", Action: "read", Effect: "allow"}, {Rule: `1 && true`, Resource: "node", Action: "*", Effect: "allow"}},
	{{Rule: `true`, Resource: "*", Action: "*", Effect: "allow"}, {Rule: `true`, Resource: "*", Action: "*", Effect: "allow"}},
	{{Rule: `false`, Resource: "*", Action: "*", Effect: "allow"}},
}

type rustAnswer struct {
	Allowed bool   `json:"allowed"`
	Error   string `json:"error,omitempty"`
}

var goAddress = regexp.MustCompile(`0x[0-9a-f]+`)

// words is an error as the Rust service words it: a recovered panic without its stack, a function without the address
// only this process knows.
func words(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.Index(s, "panic: "); i >= 0 {
		if j := strings.Index(s[i:], "\n"); j >= 0 {
			s = s[:i+j]
		}
	}
	return goAddress.ReplaceAllString(s, "0x0")
}

func answer(policies []Policy, req Request) rustAnswer {
	c, err := NewCasbinWith(policies)
	if err != nil {
		return rustAnswer{Error: words(err)}
	}
	ok, err := c.Authorize(context.Background(), req)
	return rustAnswer{Allowed: ok, Error: words(err)}
}

func TestRustCasesAreCurrent(t *testing.T) {
	type ruleCase struct {
		Rule     string       `json:"rule"`
		Validate string       `json:"validate"`
		Answers  []rustAnswer `json:"answers"`
	}
	type setCase struct {
		Policies []Policy     `json:"policies"`
		Answers  []rustAnswer `json:"answers"`
	}
	var rules []ruleCase
	for _, r := range rustRules {
		p := Policy{Rule: r, Resource: "*", Action: "*", Effect: "allow"}
		c := ruleCase{Rule: r, Validate: words(Validate(p))}
		for _, req := range rustRequests {
			c.Answers = append(c.Answers, answer([]Policy{p}, req))
		}
		rules = append(rules, c)
	}
	var sets []setCase
	for _, ps := range rustPolicySets {
		c := setCase{Policies: ps}
		for _, req := range rustRequests {
			c.Answers = append(c.Answers, answer(ps, req))
		}
		sets = append(sets, c)
	}
	raw, err := json.MarshalIndent(map[string]any{
		"comment":  "Code generated from contracts/authz (GOAP_UPDATE_RUST=1 go test ./contracts/authz/ -run TestRustCasesAreCurrent). DO NOT EDIT.",
		"requests": rustRequests,
		"rules":    rules,
		"sets":     sets,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := string(raw) + "\n"
	if os.Getenv("GOAP_UPDATE_RUST") != "" {
		if err := os.WriteFile(rustCases, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(rustCases)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatal(fmt.Sprintf("%s is not what Casbin answers: run GOAP_UPDATE_RUST=1 go test ./contracts/authz/ -run TestRustCasesAreCurrent", rustCases))
	}
}
