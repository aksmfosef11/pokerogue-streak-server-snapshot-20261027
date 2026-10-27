// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplayCannotKeepAbandonedMatchAlive(t *testing.T) {
	match := &Match{}
	decision := Decision{Index: 0, Kind: "turn", Digest: strings.Repeat("a", 64), Choice: json.RawMessage(`{"command":0}`)}
	if _, err := lockDecisionAt(match, decision, "first-seed", 1000); err != nil {
		t.Fatal(err)
	}
	if match.Expires != 2800 {
		t.Fatal("new decision did not start deadline")
	}
	for _, now := range []int64{1100, 1500, 2700} {
		if _, err := lockDecisionAt(match, decision, "other-seed", now); err != nil {
			t.Fatal(err)
		}
		if match.Expires != 2800 {
			t.Fatal("replaying old decision extended deadline")
		}
	}
	decision.Index = 1
	if _, err := lockDecisionAt(match, decision, "next-seed", 2700); err != nil {
		t.Fatal(err)
	}
	if match.Expires != 4500 {
		t.Fatal("new decision did not extend deadline")
	}
}

func TestInvalidDecisionEndsTheMatchingStreak(t *testing.T) {
	for _, practice := range []bool{false, true} {
		state := &State{Match: &Match{Practice: practice, Status: "active"}, Streak: 7, Best: 9, PracticeStreak: 4, PracticeBest: 6}
		invalidateMatch(state)
		if state.Match.Status != "invalid" || state.Best != 9 || state.PracticeBest != 6 {
			t.Fatalf("%+v", state)
		}
		if practice && (state.PracticeStreak != 0 || state.Streak != 7) {
			t.Fatal("practice invalidation touched wrong streak")
		}
		if !practice && (state.Streak != 0 || state.PracticeStreak != 4) {
			t.Fatal("ranked invalidation touched wrong streak")
		}
	}
}
func TestExpiredMatchLosesOnlyTheMatchingStreak(t *testing.T) {
	for _, practice := range []bool{false, true} {
		state := &State{Match: &Match{Practice: practice, Status: "active", Expires: 1}, Streak: 7, Best: 9, PracticeStreak: 4, PracticeBest: 6}
		expire(state)
		if state.Match.Status != "timeout" || state.Best != 9 || state.PracticeBest != 6 {
			t.Fatalf("%+v", state)
		}
		if practice && (state.PracticeStreak != 0 || state.Streak != 7) {
			t.Fatal("practice timeout touched wrong streak")
		}
		if !practice && (state.Streak != 0 || state.PracticeStreak != 4) {
			t.Fatal("ranked timeout touched wrong streak")
		}
	}
}
func TestResultHPIncludesNativeHighLevelStats(t *testing.T) {
	// Native 10,100-level Blissey with HP vitamins exceeds the old 100,000
	// guard (see the client level-provenance native-stat test).
	if alive, err := resultPartyAlive([]int{0, 224331, 0}); err != nil || !alive {
		t.Fatalf("native high-level result rejected: %v", err)
	}
	if alive, err := resultPartyAlive([]int{0, 0}); err != nil || alive {
		t.Fatal("fainted party counted as alive")
	}
	for _, hp := range []int{-1, MaxResultHP + 1} {
		if _, err := resultPartyAlive([]int{hp}); err == nil {
			t.Fatal("invalid result integer accepted")
		}
	}
}
