package pbconv

import (
	"encoding/json"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zimwip/goap/contracts/form"
)

// FormToPB carries the form of a human action (ADR 0115) as a Struct (nil: none).
func FormToPB(f *form.Form) *structpb.Struct {
	if f == nil {
		return nil
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return Struct(m)
}

// FormFromPB reads the form of a human action from its Struct (nil: none).
func FormFromPB(s *structpb.Struct) *form.Form {
	if s == nil {
		return nil
	}
	raw, err := json.Marshal(s.AsMap())
	if err != nil {
		return nil
	}
	var f form.Form
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return &f
}
