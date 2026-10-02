package methodology

import (
	"regexp"
	"strconv"
)

var activityPathRE = regexp.MustCompile(`^(processes|methods)\[(\d+)\]((?:\.steps\[\d+\])*)`)
var stepIndexRE = regexp.MustCompile(`\.steps\[(\d+)\]`)

// ActivityOf maps the definition path of an issue ("processes[0].steps[2].steps[1].pre") to the flow path of the
// activity it is about ("<process>/<step>/<sub-step>"); "" for an issue that is about no process, method or step.
func (m *Methodology) ActivityOf(path string) string {
	g := activityPathRE.FindStringSubmatch(path)
	if g == nil {
		return ""
	}
	i, _ := strconv.Atoi(g[2])
	var name string
	var steps []Step
	if g[1] == "processes" {
		if i >= len(m.Processes) {
			return ""
		}
		name, steps = m.Processes[i].Name, m.Processes[i].Steps
	} else {
		if i >= len(m.Methods) {
			return ""
		}
		name, steps = m.Methods[i].Name, m.Methods[i].Steps
	}
	out := name
	for _, idx := range stepIndexRE.FindAllStringSubmatch(g[3], -1) {
		j, _ := strconv.Atoi(idx[1])
		if j >= len(steps) {
			return out
		}
		out += "/" + steps[j].Name
		steps = steps[j].Steps
	}
	return out
}
