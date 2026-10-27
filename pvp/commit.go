// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

// Only a new committed decision extends the deadline. Replaying or repeatedly
// fetching an earlier decision must not keep an abandoned match alive.
func lockDecisionAt(match *Match, decision Decision, seed string, now int64) (Decision, error) {
	fresh := decision.Index == len(match.Decisions)
	out, err := LockDecision(match, decision, seed)
	if err == nil && fresh {
		match.Expires = now + 30*60
	}
	return out, err
}

func invalidateMatch(state *State) {
	if state.Match == nil {
		return
	}
	state.Match.Status = "invalid"
	if state.Match.Practice {
		state.PracticeStreak = 0
	} else {
		endStreak(state, "invalid")
	}
}
