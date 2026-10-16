package domainpb

import (
	"reflect"
	"testing"

	"github.com/zimwip/goap/lib/def"
)

// A domain crosses the RPC with its enums and its item types (ADR 0128).
func TestDomainItemTypesRoundTrip(t *testing.T) {
	d := def.Domain{Name: "itemtest", Version: "1.0.0", Schema: def.Schema{
		Enums: []def.Enum{{Name: "verdict", Values: []def.EnumValue{{Value: "pass"}, {Value: "fail"}}}},
		ItemTypes: []def.ItemType{{Name: "testResult", Description: "The result of a test run",
			Attributes: []def.Attribute{{Name: "verdict", Type: def.AttrEnum, Enum: "verdict"}}}},
	}}
	pb := DomainToPB(def.Record{Domain: d})
	if got := DomainFromPB(pb); !reflect.DeepEqual(got.ItemTypes, d.ItemTypes) || len(got.Enums) != 1 || len(got.Enums[0].Values) != 2 {
		t.Fatalf("round trip: %+v", got)
	}
	if s := DomainSummaryToPB(def.Record{Domain: d}); s.ItemTypeCount != 1 {
		t.Fatalf("summary: %v", s)
	}
}
