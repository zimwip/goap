package def

import (
	"strings"
	"testing"
)

func TestImporters(t *testing.T) {
	d := Domain{Name: "code", Version: "1.0.0", Schema: Schema{
		Enums: []Enum{{Name: "mode", Values: []EnumValue{{Value: "full"}}}},
		Importers: []Importer{
			{Name: "git", Params: []Attribute{{Name: "repository"}, {Name: "mode", Type: AttrEnum, Enum: "mode"}}},
			{Name: "git"},
			{Name: "Bad"},
			{Name: "svn", Params: []Attribute{{Name: "x"}, {Name: "x"}, {Name: "y", Type: AttrEnum, Enum: "nope"}}},
		}}}
	got := map[string]string{}
	for _, is := range d.Validate() {
		got[is.Path] = is.Message
	}
	for path, want := range map[string]string{
		"importers[1].name":           "duplicate importer",
		"importers[2].name":           "invalid importer name",
		"importers[3].params[1].name": "duplicate attribute",
		"importers[3].params[2].enum": "unknown enum",
	} {
		if !strings.Contains(got[path], want) {
			t.Errorf("%s: %q, want %q (issues %v)", path, got[path], want, got)
		}
	}
	if _, ok := got["importers[0].name"]; ok {
		t.Errorf("git is valid: %v", got)
	}
	if im, ok := d.Importer("git"); !ok || len(im.Params) != 2 {
		t.Errorf("Importer(git) = %+v, %v", im, ok)
	}
}
