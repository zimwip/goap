package form

import (
	"encoding/json"
	"os"
	"testing"
)

// webCatalog is the copy of the built-in widgets the form designer of the web reads (ADR 0115): their values and
// constraints, written from the registry so that both stay the same.
const webCatalog = "../../web/src/lib/forms/widgetCatalog.gen.ts"

func TestWebCatalogIsCurrent(t *testing.T) {
	raw, err := json.MarshalIndent(All(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := "// Code generated from the widget registry of pkg/form (GOAP_UPDATE_WEB=1 go test ./pkg/form/). DO NOT EDIT.\n" +
		"import type { WidgetSpec } from './types';\n\n" +
		"export const WIDGET_CATALOG: WidgetSpec[] = " + string(raw) + ";\n"
	if os.Getenv("GOAP_UPDATE_WEB") != "" {
		if err := os.WriteFile(webCatalog, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(webCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is not the registry's: run GOAP_UPDATE_WEB=1 go test ./pkg/form/", webCatalog)
	}
}
