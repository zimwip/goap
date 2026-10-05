package events_test

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/events"
)

// The web keeps one copy of the trigger events (TRIGGER_EVENTS) and labels them in the triggers editor: both must
// list what the Go side lists.
func TestWebListsTheTriggerEvents(t *testing.T) {
	want := slices.Sorted(slices.Values(events.All))

	api, err := os.ReadFile("../../web/src/lib/api.ts")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)TRIGGER_EVENTS = \[(.*?)\] as const`).FindSubmatch(api)
	if block == nil {
		t.Fatal("TRIGGER_EVENTS not found in web/src/lib/api.ts")
	}
	var got []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(block[1], -1) {
		got = append(got, string(m[1]))
	}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Errorf("web TRIGGER_EVENTS %v, Go events.All %v", got, want)
	}

	editor, err := os.ReadFile("../../web/src/lib/views/editors/TriggersEditor.svelte")
	if err != nil {
		t.Fatal(err)
	}
	labels := regexp.MustCompile(`(?s)EVENT_LABELS: Record<string, string> = \{(.*?)\};`).FindSubmatch(editor)
	if labels == nil {
		t.Fatal("EVENT_LABELS not found in TriggersEditor.svelte")
	}
	got = nil
	for _, m := range regexp.MustCompile(`(?m)^\s*'([^']+)':`).FindAllSubmatch(labels[1], -1) {
		got = append(got, string(m[1]))
	}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Errorf("web EVENT_LABELS %v, Go events.All %v", got, want)
	}
}

func TestFromBroker(t *testing.T) {
	for name, want := range map[string]string{events.BrokerAttached: events.ProcessAttached, events.BrokerStepCompleted: events.StepCompleted} {
		if got, ok := events.FromBroker(name); !ok || got != want {
			t.Errorf("FromBroker(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if _, ok := events.FromBroker("completed"); ok {
		t.Error("a process status is not a broker alias")
	}
	if got := events.ChangeSubject("C1", events.ChangeItemAdded); got != "goap.change.C1.item_added" {
		t.Errorf("subject %q", got)
	}
}
