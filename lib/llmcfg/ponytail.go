package llmcfg

import "github.com/zimwip/goap/contracts/llm"

// PonytailAction is the action of the shipped methodologies that writes code in the workspace of a change (sdlc,
// ADR 0118): the calls the ponytail behaviour reaches by default.
const PonytailAction = "implement_component"

// PonytailBehavior is the built-in coding style of the coding actions, seeded disabled: the ruleset of Ponytail
// (https://github.com/DietrichGebert/ponytail, AGENTS.md, MIT License, Copyright (c) 2026 DietrichGebert), adapted to
// an action that ends with its items. It reaches the calls the engine makes for PonytailAction only; an administrator
// enables it, or scopes it to other actions, from the behaviours of the settings.
func PonytailBehavior() Behavior {
	return Behavior{
		Name:        "ponytail",
		Description: "Ponytail's minimal-code ruleset for the coding actions: the smallest complete change (github.com/DietrichGebert/ponytail, MIT)",
		Instruction: ponytailRules,
		Position:    PositionAppend,
		Order:       50,
		Sources:     []string{llm.SourceEngine},
		Actions:     []string{PonytailAction},
	}
}

const ponytailRules = `Coding style (Ponytail, lazy senior dev mode). The best code is the code never written: solve the whole problem with the least new code. In the summary of your work, say in one or two lines what you skipped or did not check, and any risk a reviewer must know.

Before you write: read the task and the code it touches. List every place your change must reach: callers, tests, fixtures, config, exports. Check what your change could break: data it would destroy or expose, callers that stop working. That is scope. Extra features are not.

Take the first option that fully works:
1. Does it need to exist? Skip features, options and flexibility nobody asked for, and name them in one line.
2. Already in this codebase (a helper, component, service, pattern)? Use it the way the surrounding code does.
3. Standard library or a platform feature? Use it, unless the project has its own.
4. An installed dependency? Use it. Never add a dependency for a few lines.
5. Can it be one line a reader gets at a glance? One line.
6. Otherwise: the minimum code that works.

- Be lazy about the solution, never about the change itself: finish every part the task needs, including the callers, tests and fixtures your change breaks.
- No abstraction, wrapper, type conversion, option, config, boilerplate or "for later" code nobody asked for. Deletion beats addition. Keep the structure the codebase already has: its layers, interfaces and conventions.
- The shortest working diff wins, once you know everything it must touch. A one-liner that needs decoding is not short.
- Comment only the why the code cannot show, in one line.
- Bug fix: grep every caller of the function you touch, then fix the root cause once in the shared code.
- Code you move or merge keeps its error handling and validation.
- Between options of equal size, take the one that is correct on edge cases.
- New non-trivial logic (a branch, a loop, a parser, money or security) leaves one small test. Trivial changes need none.
- A shortcut with a known limit gets a code comment in this form: ` + "`shortcut: <the limit>, <when to upgrade>`" + `.

Never cut: validation at trust boundaries, error handling that prevents data loss, security, accessibility, anything the task asks for.`
