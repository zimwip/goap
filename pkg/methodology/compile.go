package methodology

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/robfig/cron/v3"

	"github.com/zimwip/goap/pkg/builtins"
	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/mcp"
)

// CompileOptions sets the policy of the one compile pipeline (ADR 0072).
type CompileOptions struct {
	// Lenient builds what stands and returns it with the issues, instead of nothing: the flow of a draft being
	// edited. A Compiled that comes with issues is for reading, not for running.
	Lenient bool
	// Stored also requires what a stored methodology (the registry) must have: its target namespace, unless it
	// applies to other methodologies. A draft being edited does not need it yet.
	Stored bool
}

// CompileWith is the one compile pipeline: Compile, CompileLenient, Validate and ValidateStored are policies of it.
// The methodology itself is not modified. With issues, the Compiled is nil unless opt.Lenient; it is nil too when
// nothing at all can be built. A compiler panic is a bug and is not caught here.
func (m *Methodology) CompileWith(opt CompileOptions) (*Compiled, def.Issues) {
	c, issues := m.compileWith(opt.Lenient)
	if c != nil && !issues.HasErrors() {
		// Remarks come last and only on a methodology that stands: on a partial one they would be noise.
		for _, h := range c.Hints() {
			h.Severity = def.SeverityWarning
			h.Activity = m.ActivityOf(h.Path)
			issues = append(issues, h)
		}
	}
	if opt.Stored && m.Namespace == "" && len(m.AppliesTo) == 0 {
		issues = append(def.Issues{{Path: "namespace", Message: "namespace required: the namespace (domain) the changes of the methodology act on"}}, issues...)
		if !opt.Lenient {
			c = nil
		}
	}
	return c, issues
}

// Validate returns every problem of the definition (empty when valid).
func (m *Methodology) Validate() def.Issues {
	_, issues := m.CompileWith(CompileOptions{})
	return issues
}

// ValidateStored is Validate plus what a stored methodology must also have (the registry): its target namespace.
func (m *Methodology) ValidateStored() def.Issues {
	_, issues := m.CompileWith(CompileOptions{Stored: true})
	return issues
}

// Compile validates the methodology and compiles its conditions, including the conditions generated from action
// expectations. The methodology itself is not modified; generated effects live in the compiled actions only.
func (m *Methodology) Compile() (*Compiled, error) {
	c, issues := m.CompileWith(CompileOptions{})
	if issues.HasErrors() {
		return nil, fmt.Errorf("methodology %s: %w", m.Name, issues.Errors())
	}
	return c, nil
}

// CompileLenient compiles what can be compiled: it reports every issue like Compile does, but returns the
// methodology built without the elements that have one (never nil unless nothing at all can be built), so that what
// still stands can be drawn and the issues shown where they are (the flow of a draft being edited). A Compiled that
// comes with issues is for reading, not for running.
func (m *Methodology) CompileLenient() (*Compiled, def.Issues) {
	return m.CompileWith(CompileOptions{Lenient: true})
}

// compileState holds what the phases of a compilation share. Each phase is one method, run in the order of
// compileWith: the order matters (the expect:* conditions are known once the actions are compiled, the agents are
// complete before methods and processes are merged in).
type compileState struct {
	m         *Methodology
	issues    def.Issues
	defs      []condition.Definition
	known     map[string]bool
	actions   map[string]Action
	utilities []condition.Definition
	whens     []condition.Definition
	goals     map[string]bool
	agents    map[string]Agent
	roles     map[string]bool
	meths     compiledMethods
	procs     compiledProcesses
}

func (s *compileState) add(path, format string, args ...any) {
	s.issues = append(s.issues, def.Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (m *Methodology) compileWith(lenient bool) (*Compiled, def.Issues) {
	s := &compileState{m: m, known: map[string]bool{}, actions: map[string]Action{}, goals: map[string]bool{}, agents: map[string]Agent{}}
	s.checkHeader()
	s.compileConditions()
	s.injectLibraries()
	s.compileActions()
	s.checkSpecializations()
	s.checkReferences()
	s.compileGoals()
	s.compileAgents()
	s.checkTransverse()
	s.mergeRolesMethodsProcesses()
	s.checkMainGoal()
	return s.finish(lenient)
}

func (s *compileState) checkHeader() {
	m := s.m
	switch {
	case m.Name == "":
		s.add("name", "name required")
	case !def.NameRE.MatchString(m.Name):
		s.add("name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	if len(m.Goals) == 0 && len(m.Processes) == 0 {
		s.add("goals", "at least one goal or process required")
	}
	if m.Namespace != "" && !def.NameRE.MatchString(m.Namespace) {
		s.add("namespace", "namespace must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	s.checkCriticality()
	m.lintTypeRefs(s.add)
}

// checkMainGoal checks the main goal of the methodology (ADR 0096): a declared goal or a process.
func (s *compileState) checkMainGoal() {
	g := s.m.Goal
	if g == "" || s.goals[g] {
		return
	}
	for _, p := range s.m.Processes {
		if p.Name == g {
			return
		}
	}
	s.add("goal", "unknown goal %q: name a goal or a process of the methodology", g)
}

// compileConditions compiles each expression on its own to report every error.
func (s *compileState) compileConditions() {
	for i, c := range s.m.Conditions {
		path := fmt.Sprintf("conditions[%d]", i)
		switch {
		case c.Name == "":
			s.add(path+".name", "name required")
			continue
		case s.known[c.Name]:
			s.add(path+".name", "duplicate condition %s", c.Name)
			continue
		}
		s.known[c.Name] = true
		d := condition.Definition{Name: c.Name, Expr: c.Expr}
		if _, err := condition.Compile([]condition.Definition{d}); err != nil {
			s.add(path+".expr", "%s", strings.TrimPrefix(err.Error(), fmt.Sprintf("condition %q: ", c.Name)))
			continue
		}
		s.defs = append(s.defs, d)
	}
}

// injectLibraries adds the conditions of the libraries the methodology imports (ADR 0064), unless it declares its own.
func (s *compileState) injectLibraries() {
	seen := map[string]bool{}
	for i, name := range s.m.Imports {
		lib, ok := condition.Library(name)
		switch {
		case !ok:
			s.add(fmt.Sprintf("imports[%d]", i), "unknown condition library %q (one of %s)", name, strings.Join(condition.LibraryNames, ", "))
			continue
		case seen[name]:
			s.add(fmt.Sprintf("imports[%d]", i), "library %s imported twice", name)
			continue
		}
		seen[name] = true
		for _, d := range lib {
			if !s.known[d.Name] {
				s.known[d.Name] = true
				s.defs = append(s.defs, d)
			}
		}
	}
}

// compileActions checks every action and keeps a copy of it in s.actions (generated effects live in the copy only).
func (s *compileState) compileActions() {
	for i, src := range s.m.Actions {
		path := fmt.Sprintf("actions[%d]", i)
		a := src
		a.Effects = maps.Clone(src.Effects)
		if a.Name == "" {
			s.add(path+".name", "name required")
			continue
		}
		if _, dup := s.actions[a.Name]; dup {
			s.add(path+".name", "duplicate action %s", a.Name)
			continue
		}
		if !slices.Contains([]string{KindLLM, KindTool, KindHuman, KindBuiltin, KindScript, KindAbstract}, a.Kind) {
			s.add(path+".kind", "unknown kind %q", a.Kind)
		}
		s.checkSpecialization(path, a)
		s.checkImplementation(path, a)
		if a.Utility != "" {
			if err := condition.CheckNumber(a.Utility); err != nil {
				s.add(path+".utility", "%v", err)
			} else {
				s.utilities = append(s.utilities, condition.Definition{Name: a.Name, Expr: a.Utility})
			}
		}
		s.checkInvocation(path, a)
		s.checkVerify(path, a)
		a = s.compileExpects(path, a)
		if len(a.Effects) == 0 && !a.IsSpecialization() {
			s.add(path+".effects", "no effect: the action can never be planned")
		}
		s.actions[a.Name] = a
	}
}

// checkSpecialization checks the specialization side of an action (ADR 0035's specializes / when / priority).
func (s *compileState) checkSpecialization(path string, a Action) {
	if !a.IsSpecialization() {
		if a.When != "" || a.Priority != 0 {
			s.add(path+".when", "when and priority apply to specializations only")
		}
		return
	}
	if a.Kind == KindAbstract {
		s.add(path+".kind", "a specialization must be implemented")
	}
	if strings.Count(a.Specializes, "/") > 1 || strings.HasSuffix(a.Specializes, "/") || a.Specializes == a.Name {
		s.add(path+".specializes", "specializes must be <action> or <methodology>/<action>")
	}
	if len(a.Pre) > 0 || len(a.Effects) > 0 || a.Expects != nil {
		s.add(path+".specializes", "a specialization inherits pre, effects and expects from the action it specializes")
	}
	if a.When != "" {
		d := condition.Definition{Name: a.WhenCondition(), Expr: a.When}
		if _, err := condition.Compile([]condition.Definition{d}); err != nil {
			s.add(path+".when", "%s", strings.TrimPrefix(err.Error(), fmt.Sprintf("condition %q: ", d.Name)))
		} else {
			s.whens = append(s.whens, d)
		}
	}
}

// checkImplementation checks what an action of a kind needs to run (script language and code, prompt, tool, builtin).
func (s *compileState) checkImplementation(path string, a Action) {
	if a.Kind == KindScript {
		if a.Language != LangJavaScript && a.Language != LangGo {
			s.add(path+".language", "script language must be javascript or go")
		}
		if strings.TrimSpace(a.Code) == "" {
			s.add(path+".code", "script action requires code")
		}
	}
	switch {
	case a.Kind == KindLLM && a.Prompt == "":
		s.add(path+".prompt", "llm action requires a prompt")
	case a.Kind == KindTool && a.Tool == "":
		s.add(path+".tool", "tool action requires a tool")
	case a.Kind == KindTool:
		if _, _, err := mcp.SplitTool(a.Tool); err != nil {
			s.add(path+".tool", "tool must be <mcp>/<tool>")
		}
	case a.Kind == KindBuiltin && a.Builtin == "":
		s.add(path+".builtin", "builtin action requires a builtin")
	case a.Kind == KindBuiltin && s.m.Builtins != nil && a.Builtin != builtins.ProcessStep && !s.m.Builtins.HasBuiltin(a.Builtin):
		s.add(path+".builtin", "unknown builtin %q", a.Builtin)
	}
}

// checkCriticality checks the default criticality of the methodology (ADR 0075 §3): C1, C2 or C3, or none.
func (s *compileState) checkCriticality() {
	if c := s.m.Criticality; c != "" && !slices.Contains([]string{"C1", "C2", "C3"}, c) {
		s.add("criticality", "unknown criticality %q: C1, C2 or C3", c)
	}
}

// checkVerify checks the verification an action declares (ADR 0075): a known kind of oracle.
func (s *compileState) checkVerify(path string, a Action) {
	if a.Verify == nil {
		return
	}
	switch a.Verify.Oracle {
	case OracleTool, OracleHuman, OracleModel:
	default:
		s.add(path+".verify.oracle", "unknown oracle %q: tool, human or model", a.Verify.Oracle)
	}
}

// checkInvocation checks the MCPs and the permission an action names.
func (s *compileState) checkInvocation(path string, a Action) {
	for _, name := range a.MCPs {
		if !mcp.ValidName(name) {
			s.add(path+".mcps", "invalid MCP name %q", name)
		}
	}
	if len(a.MCPs) > 0 && a.Kind != KindLLM && a.Kind != KindScript {
		s.add(path+".mcps", "mcps apply to llm and script actions (a tool action names its tool)")
	}
	if a.Permission != "" && !strings.Contains(a.Permission, ":") {
		s.add(path+".permission", "permission must be <resource>:<action>")
	}
}

// compileExpects checks the expectation of an action and generates its expect:* condition, which the returned copy
// of the action has as an effect; the methodology's own action is left as written.
func (s *compileState) compileExpects(path string, a Action) Action {
	e := a.Expects
	if e == nil {
		return a
	}
	m := s.m
	if e.Produce.NodeType != "" {
		if msg := m.checkTypeRef(e.Produce.NodeType, false); msg != "" {
			s.add(path+".expects.produce.nodeType", "%s", msg)
		} else if r, _ := domain.ParseTypeRef(e.Produce.NodeType); m.Namespace != "" && r.Namespace != m.Namespace {
			s.add(path+".expects.produce.nodeType", "%s is not a type of %s: a methodology creates nodes of its namespace", e.Produce.NodeType, m.Namespace)
		}
	}
	if e.Link != nil {
		if msg := m.checkTypeRef(e.Link.Type, true); msg != "" {
			s.add(path+".expects.link.type", "%s", msg)
		}
	}
	expr, err := e.Expr()
	if err != nil {
		s.add(path+".expects", "%v", err)
		return a
	}
	d := condition.Definition{Name: a.ExpectCondition(), Expr: expr}
	if _, err := condition.Compile([]condition.Definition{d}); err != nil {
		s.add(path+".expects.where", "%v", err)
		return a
	}
	s.defs = append(s.defs, d)
	s.known[d.Name] = true
	if a.Effects == nil {
		a.Effects = map[string]bool{}
	}
	a.Effects[a.ExpectCondition()] = true
	return a
}

// checkSpecializations requires a local specialization to target a planned action of this methodology.
func (s *compileState) checkSpecializations() {
	for i, a := range s.m.Actions {
		target, local := a.SpecializedAction(s.m.Name)
		if !a.IsSpecialization() || !local {
			continue
		}
		if t, ok := s.actions[target]; !ok {
			s.add(fmt.Sprintf("actions[%d].specializes", i), "unknown action %q", target)
		} else if t.IsSpecialization() {
			s.add(fmt.Sprintf("actions[%d].specializes", i), "cannot specialize the specialization %q", target)
		}
	}
}

// checkReferences checks the conditions the actions name (the expect:* conditions are known once every action has
// been compiled) and that none is both an input and an output of one (ADR 0051).
func (s *compileState) checkReferences() {
	for i, a := range s.m.Actions {
		for k := range a.Pre {
			if !s.known[k] {
				s.add(fmt.Sprintf("actions[%d].pre.%s", i, k), "unknown condition %q", k)
			}
		}
		for k := range a.Effects {
			if !s.known[k] {
				s.add(fmt.Sprintf("actions[%d].effects.%s", i, k), "unknown condition %q", k)
			}
		}
		for _, k := range overlap(a.Pre, a.Effects) {
			s.add(fmt.Sprintf("actions[%d].pre.%s", i, k), "%s", inputAndOutput(k, "the action "+a.Name))
		}
	}
}

func (s *compileState) compileGoals() {
	for i, g := range s.m.Goals {
		path := fmt.Sprintf("goals[%d]", i)
		if g.Name == "" {
			s.add(path+".name", "name required")
		} else if s.goals[g.Name] {
			s.add(path+".name", "duplicate goal %s", g.Name)
		}
		s.goals[g.Name] = true
		if len(g.Pre) == 0 {
			s.add(path+".pre", "a goal needs at least one condition")
		}
		for k := range g.Pre {
			if !s.known[k] {
				s.add(path+".pre."+k, "unknown condition %q", k)
			}
		}
	}
}

// compileAgents checks the agents, and adds the implicit default agent of a methodology with goals and no agent.
func (s *compileState) compileAgents() {
	m := s.m
	for i, ag := range m.Agents {
		path := fmt.Sprintf("agents[%d]", i)
		switch {
		case ag.Name == "":
			s.add(path+".name", "name required")
		case !def.NameRE.MatchString(ag.Name):
			s.add(path+".name", "name must be lowercase letters, digits, '-' or '_'")
		case s.agents[ag.Name].Name != "":
			s.add(path+".name", "duplicate agent %s", ag.Name)
		}
		switch ag.Planner {
		case "", PlannerGOAP, PlannerUtility, PlannerHybrid:
		case PlannerLLM, PlannerLLMScoring:
			if ag.Model == "" {
				s.add(path+".model", "model is required for the %s planner", ag.Planner)
			}
		default:
			s.add(path+".planner", "planner must be goap, utility, hybrid, llm or llm-scoring")
		}
		for _, name := range ag.MCPs {
			if !mcp.ValidName(name) {
				s.add(path+".mcps", "invalid MCP name %q", name)
			}
		}
		for j, a := range ag.Actions {
			if act, ok := s.actions[a]; !ok {
				s.add(fmt.Sprintf("%s.actions[%d]", path, j), "unknown action %q", a)
			} else if act.IsSpecialization() {
				s.add(fmt.Sprintf("%s.actions[%d]", path, j), "%q is a specialization: list the action it specializes", a)
			}
		}
		for j, g := range ag.Goals {
			if !s.goals[g] {
				s.add(fmt.Sprintf("%s.goals[%d]", path, j), "unknown goal %q", g)
			}
		}
		names := map[string]bool{}
		for j, tr := range ag.Triggers {
			tp := fmt.Sprintf("%s.triggers[%d]", path, j)
			switch {
			case tr.Name == "":
				s.add(tp+".name", "name required")
			case names[tr.Name]:
				s.add(tp+".name", "duplicate trigger %s", tr.Name)
			}
			names[tr.Name] = true
			s.checkTrigger(tp, ag, tr)
		}
		if ag.Planner == "" {
			ag.Planner = PlannerGOAP
		}
		s.agents[ag.Name] = ag
	}
	if len(m.Agents) == 0 && len(m.Goals) > 0 {
		s.agents[DefaultAgent] = Agent{Name: DefaultAgent, Description: m.Description, Planner: PlannerGOAP}
	}
}

// checkTrigger checks the type, event, filter, schedule, target and goal of one trigger of an agent.
func (s *compileState) checkTrigger(tp string, ag Agent, tr Trigger) {
	switch tr.Type {
	case TriggerEvent:
		if !slices.Contains(TriggerEvents, tr.Event) {
			s.add(tp+".event", "event must be one of %s", strings.Join(TriggerEvents, ", "))
		}
		if _, err := condition.CompileEventFilter(tr.Filter); err != nil {
			s.add(tp+".filter", "%v", err)
		}
	case TriggerSchedule:
		if _, err := cron.ParseStandard(tr.Schedule); err != nil {
			s.add(tp+".schedule", "invalid cron expression: %v", err)
		}
	default:
		s.add(tp+".type", "type must be event or schedule")
	}
	switch tr.Target {
	case "", TargetNewChange:
	case TargetEventChange:
		if tr.Type != TriggerEvent || !(strings.HasPrefix(tr.Event, "change.") || strings.HasPrefix(tr.Event, "process.")) {
			s.add(tp+".target", "event_change requires a change.* or process.* event")
		}
	default:
		s.add(tp+".target", "target must be new_change or event_change")
	}
	if tr.Goal != "" && !s.goals[tr.Goal] {
		s.add(tp+".goal", "unknown goal %q", tr.Goal)
	} else if tr.Goal != "" && len(ag.Goals) > 0 && !slices.Contains(ag.Goals, tr.Goal) {
		s.add(tp+".goal", "goal %q is not a goal of the agent", tr.Goal)
	}
}

// checkTransverse checks appliesTo and on (ADR 0036 §3): a transverse methodology runs processes alongside others.
func (s *compileState) checkTransverse() {
	m := s.m
	for i, other := range m.AppliesTo {
		switch {
		case !def.NameRE.MatchString(other):
			s.add(fmt.Sprintf("appliesTo[%d]", i), "appliesTo names methodologies")
		case other == m.Name:
			s.add(fmt.Sprintf("appliesTo[%d]", i), "a methodology does not apply to itself")
		}
	}
	for i, sub := range m.On {
		path := fmt.Sprintf("on[%d]", i)
		if len(m.AppliesTo) == 0 {
			s.add(path, "on applies to transverse methodologies (appliesTo)")
		}
		if !slices.Contains(TriggerEvents, sub.Event) {
			s.add(path+".event", "event must be one of %s", strings.Join(TriggerEvents, ", "))
		}
		if _, err := condition.CompileEventFilter(sub.Filter); err != nil {
			s.add(path+".filter", "%v", err)
		}
	}
	if len(m.AppliesTo) > 0 && len(m.Processes) == 0 {
		s.add("appliesTo", "a transverse methodology runs its processes alongside the changes: declare at least one process")
	}
}

// mergeRolesMethodsProcesses compiles the roles, the methods and the processes, and merges the agents, actions and
// conditions generated for them in. The agents must be complete before.
func (s *compileState) mergeRolesMethodsProcesses() {
	m := s.m
	s.roles = m.compileRoles(s.add)
	m.checkElementRoles(s.add, s.roles)
	meths, methGen := m.compileMethods(s.add, s.actions, s.known, s.agents, s.roles)
	s.meths = meths
	// a method composing its own steps is run by an agent of its own name, merged in before processes compile so
	// a process cannot reuse that name either (same collision rule as between two processes).
	for _, ag := range methGen.agents {
		s.agents[ag.Name] = ag
	}
	// an agent performing a method has access to the activities that compose it (an agent open to every action already has)
	for name, x := range methGen.extend {
		ag := s.agents[name]
		if len(ag.Actions) > 0 {
			ag.Actions = append(slices.Clone(ag.Actions), x.actions...)
		}
		if len(ag.Goals) > 0 {
			ag.Goals = append(slices.Clone(ag.Goals), x.goals...)
		}
		s.agents[name] = ag
	}
	procs := m.compileProcesses(s.add, s.actions, s.known, s.agents, meths, s.roles)
	maps.Copy(procs.criteria, methGen.criteria)
	procs.conditions = append(procs.conditions, methGen.conditions...)
	procs.actions = append(procs.actions, methGen.actions...)
	procs.agents = append(procs.agents, methGen.agents...)
	procs.goals = append(procs.goals, methGen.goals...)
	s.procs = procs
	s.defs = append(s.defs, procs.conditions...)
	for _, a := range procs.actions {
		s.actions[a.Name] = a
	}
	for _, ag := range procs.agents {
		s.agents[ag.Name] = ag
	}
}

// finish ties the issues to their activity and sorts them (a stable output), then compiles the condition sets and
// builds the Compiled; with issues and not lenient, nothing is built.
func (s *compileState) finish(lenient bool) (*Compiled, def.Issues) {
	m := s.m
	if len(s.issues) > 0 {
		for i := range s.issues {
			s.issues[i].Activity = m.ActivityOf(s.issues[i].Path)
		}
		sort.SliceStable(s.issues, func(i, j int) bool { return s.issues[i].Path < s.issues[j].Path })
		if !lenient {
			return nil, s.issues
		}
	}
	set, err := condition.Compile(s.defs)
	if err != nil {
		return nil, def.Issues{{Message: err.Error()}}
	}
	uset, err := condition.CompileNumbers(s.utilities)
	if err != nil {
		return nil, def.Issues{{Message: err.Error()}}
	}
	wset, err := condition.Compile(s.whens)
	if err != nil {
		return nil, def.Issues{{Message: err.Error()}}
	}
	mset, err := condition.Compile(s.meths.guards)
	if err != nil {
		return nil, def.Issues{{Message: err.Error()}}
	}
	return &Compiled{Methodology: m, Conditions: set, actions: s.actions, utilities: uset, agents: s.agents, whens: wset, processes: s.procs,
		methods: s.meths, methodGuards: mset}, s.issues
}
