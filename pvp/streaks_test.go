// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/pagefaultgames/rogueserver/defs"
)

func heldItemSession() defs.SessionSaveData {
	s := PracticeTeam().Session
	p := asObject(s.Party[0])
	p["nature"] = 3
	s.Party[0] = p
	for stat := 0; stat < 6; stat++ {
		s.Modifiers = append(s.Modifiers, Object{"className": "BaseStatModifier", "typeId": "BASE_STAT_BOOSTER", "typePregenArgs": []int{stat}, "args": []int{1001, stat}, "stackCount": 7})
	}
	for _, item := range []struct {
		class, id string
		count     int
	}{{"PokemonNatureWeightModifier", "SOUL_DEW", 3}, {"HitHealModifier", "SHELL_BELL", 2}, {"TurnHealModifier", "LEFTOVERS", 1}} {
		s.Modifiers = append(s.Modifiers, Object{"className": item.class, "typeId": item.id, "args": []int{1001}, "stackCount": item.count})
	}
	for _, stat := range []int{2, 5} {
		s.Modifiers = append(s.Modifiers, Object{"className": "TempStatStageBoosterModifier", "typeId": "TEMP_STAT_STAGE_BOOSTER", "typePregenArgs": []int{stat}, "args": []int{stat, 5, 5}, "stackCount": 1})
	}
	s.Modifiers = append(s.Modifiers, Object{"className": "TempCritBoosterModifier", "typeId": "DIRE_HIT", "args": []int{5, 5}, "stackCount": 1})
	return s
}

func TestRegisteredStreakItemsAndTemporaryExclusion(t *testing.T) {
	source := heldItemSession()
	before, _ := json.Marshal(source)
	normal := NormalizeModifiers(source.Modifiers)
	if len(normal) != 9 {
		t.Fatalf("permanent stacks lost or temporary bonuses retained: %d", len(normal))
	}
	input := source
	input.Modifiers = normal
	session := SyntheticSession(input, input, "streak-held-item-fixture")
	for _, side := range []struct {
		party, mods string
		owner       int
	}{{"party", "modifiers", 1}, {"enemyParty", "enemyModifiers", 101}} {
		mods := session[side.mods].([]any)
		if len(mods) != 9 {
			t.Fatal("items lost in synthetic session")
		}
		for _, raw := range mods {
			m := asObject(raw)
			args := m["args"].([]any)
			if num(args[0]) != float64(side.owner) {
				t.Fatalf("wrong item owner: %v", m)
			}
			if m["typeId"] == "BASE_STAT_BOOSTER" && !reflect.DeepEqual(m["typePregenArgs"], []any{args[1]}) {
				t.Fatal("vitamin stat changed")
			}
		}
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("registration changed the ordinary campaign save")
	}
	if path := os.Getenv("PVP_ITEM_SESSION_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(mustJSON(session), '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func mustJSON(v any) []byte {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return raw
}

func TestEndStreakPreservesFinalScore(t *testing.T) {
	st := State{Streak: 7, Best: 9, StreakAttempt: &StreakAttempt{Status: "active", Wins: 7}}
	endStreak(&st, "lost")
	ended := st.StreakAttempt.EndedAt
	endStreak(&st, "forfeit")
	if st.Streak != 0 || st.Best != 9 || st.StreakAttempt.Wins != 7 || st.StreakAttempt.Status != "lost" || ended == 0 || ended != st.StreakAttempt.EndedAt {
		t.Fatalf("final record changed: %+v", st)
	}
}

func TestGlobalModifierArgumentsAreNotPokemonOwners(t *testing.T) {
	session := PracticeTeam().Session
	p := asObject(session.Party[0])
	p["id"] = 1
	session.Party[0] = p
	session.Modifiers = []defs.PersistentModifierData{Object{"className": "ExpBoosterModifier", "typeId": "EXP_CHARM", "args": []int{1}, "stackCount": 1}}
	_, mods := matchSide(session, false, 101)
	if num(asObject(mods[0])["args"].([]any)[0]) != 1 {
		t.Fatal("global item parameter was rewritten as an enemy ID")
	}
}
