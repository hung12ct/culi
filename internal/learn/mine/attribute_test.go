package mine

import (
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/transcript"
	"github.com/hung12ct/culi/internal/store"
)

func card(title, summary, body string) store.StoredCard {
	return store.StoredCard{Card: knowledge.Card{Title: title, Summary: summary, Body: body}}
}

func TestReusedGuards(t *testing.T) {
	const guidance = "never tag a release without the matching changelog entry"

	tests := []struct {
		name     string
		card     store.StoredCard
		userText string
		asstText string
		want     bool
		wantWhy  string
	}{
		{
			name:     "assistant restates the card's guidance",
			card:     card("Changelog on tag", "", guidance),
			userText: "can you cut a release",
			asstText: "I will not tag a release without the matching changelog entry, so first...",
			want:     true,
			wantWhy:  "five-word prose sequence reused without the user supplying it",
		},
		{
			// The circularity guard: if the user pasted the wording, the reply
			// echoing it says nothing about the injection.
			name:     "user supplied the phrase first",
			card:     card("Changelog on tag", "", guidance),
			userText: "remember: never tag a release without the matching changelog entry",
			asstText: "right, never tag a release without the matching changelog entry",
			want:     false,
			wantWhy:  "phrase came from the user, not the card",
		},
		{
			name:     "topic overlap alone is not reuse",
			card:     card("Changelog on tag", "", guidance),
			userText: "",
			asstText: "we should probably update the changelog and think about releases",
			want:     false,
			wantWhy:  "shares vocabulary but no five-word sequence",
		},
		{
			// Word-overlap scoring credited cards on words like these; phrase
			// matching must not.
			name:     "generic english does not credit",
			card:     card("Test-driven development", "Use this when starting a new feature", ""),
			userText: "",
			asstText: "starting a new feature here, this is standard development practice",
			want:     false,
			wantWhy:  "no shared five-word prose run — a four-word run would have collided",
		},
		{
			name:     "phrases inside fenced code are ignored",
			card:     card("Build", "", "Run the gate:\n```\nmake check fmt vet lint build test\n```\n"),
			userText: "",
			asstText: "I ran make check fmt vet lint build test and it passed",
			want:     false,
			wantWhy:  "code fences stripped before matching",
		},
		{
			name:     "shared file paths do not credit",
			card:     card("Storage", "", "The store lives at ~/.culi/knowledge/rules and is git-backed."),
			userText: "",
			asstText: "I looked in ~/.culi/knowledge/rules and found the file",
			want:     false,
			wantWhy:  "paths stripped — both texts naming a path is not influence",
		},
		{
			name:     "empty card never credits",
			card:     card("", "", ""),
			userText: "",
			asstText: "some long assistant reply about changelog entries and releases",
			want:     false,
			wantWhy:  "nothing to match",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user := shingle(tc.userText)
			asst := shingle(tc.asstText)
			if got := reused(tc.card, user, asst); got != tc.want {
				t.Errorf("reused() = %v, want %v (%s)", got, tc.want, tc.wantWhy)
			}
		})
	}
}

// A phrase of nothing but short function words is English, not a fingerprint.
func TestShingleRequiresSubstantialWord(t *testing.T) {
	got := shingle("all is one of the")
	if len(got) != 0 {
		t.Errorf("expected no shingles from function words alone, got %v", got)
	}
	if s := shingle("the granularity ladder degrades gracefully"); len(s) == 0 {
		t.Error("expected a shingle when a substantial word is present")
	}
}

// Punctuation and case must not affect matching, or the same sentence
// formatted two ways would fail to match itself.
func TestShingleNormalizesFormatting(t *testing.T) {
	a := shingle("— Never tag, without the *matching* changelog!")
	b := shingle("never tag without the matching changelog")
	for k := range b {
		if !a[k] {
			t.Errorf("formatting changed the shingle set: %q missing from %v", k, a)
		}
	}
}

// Tool output is excluded: a card's wording in a grep result is evidence about
// the repo, not about the card.
func TestSessionPhrasesSplitsRoles(t *testing.T) {
	entries := []transcript.Entry{
		{Role: "user", Text: "please explain how the packer works"},
		{Role: "assistant", Text: "the granularity ladder degrades gracefully under pressure"},
		{Role: "other", Text: "this tool output should be ignored entirely"},
	}
	user, asst := sessionPhrases(entries)

	if !user["please explain how the packer"] {
		t.Errorf("user phrase missing: %v", user)
	}
	if len(asst) == 0 {
		t.Fatal("assistant phrases empty")
	}
	for k := range asst {
		if strings.Contains(k, "packer") {
			t.Errorf("user text leaked into assistant phrases: %q", k)
		}
		if strings.Contains(k, "tool output") {
			t.Errorf("non user/assistant role was collected: %q", k)
		}
	}
}

func TestProseStripsCodeAndPaths(t *testing.T) {
	got := prose("keep `internal/hook` fast\n```\nmake check\n```\nsee https://example.com/docs and /Users/x/y.md now")
	for _, banned := range []string{"internal/hook", "make check", "example.com", "/Users/x"} {
		if strings.Contains(got, banned) {
			t.Errorf("prose() left %q in: %q", banned, got)
		}
	}
	if !strings.Contains(got, "fast") {
		t.Errorf("prose() dropped surrounding text: %q", got)
	}
}
