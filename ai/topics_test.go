package ai

import (
	"testing"
)

func TestParseTopicsGroupsByTheModelsWords(t *testing.T) {
	items := []TopicItem{{ID: "a", Subject: "Invoice 12"}, {ID: "b", Subject: "Lunch?"}, {ID: "c", Subject: "Invoice 13"}}
	got := parseTopics("1: Invoices\n2: Lunch\n3: invoices\n", items)
	if len(got) != 2 || got[0].Name != "Invoices" || len(got[0].IDs) != 2 || got[0].IDs[0] != "a" || got[0].IDs[1] != "c" {
		t.Fatalf("groups = %+v", got)
	}
	if got[1].Name != "Lunch" || got[1].IDs[0] != "b" {
		t.Fatalf("second group = %+v", got[1])
	}
}

func TestParseTopicsIgnoresWhatItCannotAnswer(t *testing.T) {
	items := []TopicItem{{ID: "a"}, {ID: "b"}}
	for _, text := range []string{"", "no numbering here", "9: Nowhere", "1:\n2:  ", "```\n1: Trips\n```"} {
		got := parseTopics(text, items)
		for _, g := range got {
			for _, id := range g.IDs {
				if id != "a" && id != "b" {
					t.Fatalf("%q: a message that was not listed: %+v", text, got)
				}
			}
		}
	}
}

func TestTopicsRequestSendsSubjectsOnly(t *testing.T) {
	items := []TopicItem{{ID: "a", Subject: "  Invoice  12  "}, {ID: "b", Subject: ""}}
	req, _, sent := topicsRequest(items, "English")
	if req.feature != FeatureTopics || len(sent) != 2 {
		t.Fatalf("request = %+v, sent = %d", req, len(sent))
	}
	if want := "1. Invoice 12\n2. (no subject)"; req.user != want {
		t.Fatalf("user = %q, want %q", req.user, want)
	}
	if req.system == "" || len(items) != 2 {
		t.Fatal("the prompt says nothing")
	}
}

func TestTopicsRequestCapsThePage(t *testing.T) {
	var items []TopicItem
	for i := 0; i < TopicLimit+10; i++ {
		items = append(items, TopicItem{ID: "id", Subject: "Subject"})
	}
	_, _, sent := topicsRequest(items, "")
	if len(sent) != TopicLimit {
		t.Fatalf("sent %d, want %d", len(sent), TopicLimit)
	}
}
