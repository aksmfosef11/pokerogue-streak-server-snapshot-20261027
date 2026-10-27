// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"github.com/pagefaultgames/rogueserver/defs"
	"strings"
	"testing"
)

func TestDecisionFirstWriteWins(t *testing.T) {
	m := &Match{}
	d := Decision{Index: 0, Kind: "turn", Choice: json.RawMessage(`{"command":1}`), Digest: strings.Repeat("a", 64)}
	a, e := LockDecision(m, d, "secret-seed")
	if e != nil {
		t.Fatal(e)
	}
	d.Choice = json.RawMessage(`{"command":2}`)
	b, e := LockDecision(m, d, "other-seed")
	if e != nil || string(a.Choice) != string(b.Choice) || b.Seed != "secret-seed" || len(m.Decisions) != 1 {
		t.Fatalf("decision changed: %+v %v", b, e)
	}
	d.Digest = strings.Repeat("b", 64)
	if _, e = LockDecision(m, d, "x"); e == nil {
		t.Fatal("replay divergence accepted")
	}
	d.Index = 2
	if _, e = LockDecision(m, d, "x"); e == nil {
		t.Fatal("out of order decision accepted")
	}
}
func TestHMACSeparation(t *testing.T) {
	s := &Service{Secret: []byte("test-secret")}
	if s.decisionSeed("a", 0) == s.decisionSeed("a", 1) || s.decisionSeed("a", 0) == s.decisionSeed("b", 0) {
		t.Fatal("seed reused")
	}
	if s.decisionSeed("a", 0) != s.decisionSeed("a", 0) {
		t.Fatal("seed unstable")
	}
}
func TestSystemPurchases(t *testing.T) {
	rules := Rules{1: {Cost: 3, EggMoves: []int{1, 2, 3, 4}}}
	old := defs.SystemSaveData{StarterData: defs.StarterData{1: {CandyCount: 100}}, DexData: defs.DexData{}}
	next := defs.SystemSaveData{StarterData: defs.StarterData{1: {CandyCount: 80, ValueReduction: 1}}, DexData: defs.DexData{}}
	if f := ValidateSystem(old, next, rules, 0); len(f) > 0 {
		t.Fatal(f)
	}
	n := next.StarterData[1]
	n.CandyCount = 81
	next.StarterData[1] = n
	if f := ValidateSystem(old, next, rules, 0); len(f) == 0 {
		t.Fatal("purchase without matching payment or observed income accepted")
	}
	budget := &IncomeBudget{}
	budget.grant(1, rules)
	if f := validateSystem(old, next, rules, 0, budget); len(f) > 0 {
		t.Fatalf("funded friendship carry rejected: %v", f)
	}
	n.CandyCount = 100
	next.StarterData[1] = n
	if f := ValidateSystem(old, next, rules, 0); len(f) == 0 {
		t.Fatal("free cost reduction accepted")
	}
	n.CandyCount = 80
	n.ValueReduction = 3
	next.StarterData[1] = n
	if len(ValidateSystem(old, next, rules, 0)) == 0 {
		t.Fatal("invalid reduction accepted")
	}
}
func TestCarryCannotBeFarmedWithRepeatedUploads(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	budget := &IncomeBudget{Friendship: 75 * 4}
	for i := 0; i < 5; i++ {
		old := defs.SystemSaveData{StarterData: defs.StarterData{1: {CandyCount: i}}, DexData: defs.DexData{}}
		next := defs.SystemSaveData{StarterData: defs.StarterData{1: {CandyCount: i + 1}}, DexData: defs.DexData{}}
		flags := validateSystem(old, next, rules, 0, budget)
		if i < 4 && len(flags) > 0 {
			t.Fatal(flags)
		}
		if i == 4 && len(flags) == 0 {
			t.Fatal("per-upload allowance was farmed")
		}
	}
}
func TestEggAndCandyBounds(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	old := defs.SystemSaveData{StarterData: defs.StarterData{1: {}}, DexData: defs.DexData{}}
	next := defs.SystemSaveData{StarterData: defs.StarterData{1: {EggMoves: 15, CandyCount: 9999}}, DexData: defs.DexData{}}
	if len(ValidateSystem(old, next, rules, 0)) < 2 {
		t.Fatal("forged egg moves/candies accepted")
	}
	next.StarterData[1] = defs.StarterEntry{CandyCount: 1}
	next.DexData[1] = defs.DexEntry{CaughtCount: 1}
	if f := ValidateSystem(old, next, rules, 5); len(f) > 0 {
		t.Fatal(f)
	}
}
func TestSameSpeciesEggPurchase(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	old := defs.SystemSaveData{StarterData: defs.StarterData{1: {CandyCount: 50}}, DexData: defs.DexData{}}
	next := defs.SystemSaveData{StarterData: defs.StarterData{1: {CandyCount: 25}}, DexData: defs.DexData{}, Eggs: []defs.EggData{{Id: 123, Species: 1, SourceType: 3, HatchWaves: 10}}}
	if f := ValidateSystem(old, next, rules, 0); len(f) > 0 {
		t.Fatal(f)
	}
	next.StarterData[1] = defs.StarterEntry{CandyCount: 50}
	if len(ValidateSystem(old, next, rules, 0)) == 0 {
		t.Fatal("free egg accepted")
	}
}
func TestSyntheticSessionNormalizesBothSides(t *testing.T) {
	team := PracticeTeam()
	m := SyntheticSession(team.Session, team.Session, "fixed-seed")
	own := m["party"].([]any)
	enemy := m["enemyParty"].([]any)
	for _, side := range [][]any{own, enemy} {
		for _, p := range side {
			v := p.(map[string]any)
			for _, iv := range v["ivs"].([]any) {
				if iv != float64(31) {
					t.Fatal("IV not normalized")
				}
			}
			if v["boss"] != false || v["status"] != nil {
				t.Fatal("battle state leaked")
			}
		}
	}
	if own[0].(map[string]any)["id"] == enemy[0].(map[string]any)["id"] {
		t.Fatal("cross-side IDs collide")
	}
	if _, exists := team.Session.Party[0].(Object)["ivs"]; exists {
		t.Fatal("source snapshot mutated")
	}
	if m["battleType"] != float64(1) {
		t.Fatal("not trainer battle")
	}
}
func TestMalformedPokemonDoesNotPanicWorker(t *testing.T) {
	party := NormalizeParty([]defs.PokemonData{nil, Object{"moveset": []any{nil, 42, Object{"moveId": 33, "ppUsed": 99, "maxPpOverride": 999}}}}, true)
	if len(party) != 1 {
		t.Fatal("null snapshot retained")
	}
	moves := party[0].(Object)["moveset"].([]Object)
	if len(moves) != 1 || moves[0]["ppUsed"] != 0 {
		t.Fatal("invalid moves retained")
	}
	if _, ok := moves[0]["maxPpOverride"]; ok {
		t.Fatal("PP override retained")
	}
}

func TestNormalizationPreservesCertifiedMintNatureOnly(t *testing.T) {
	for _, nature := range []any{3, -1, 25, 1.5, "3", true} {
		party := NormalizeParty([]defs.PokemonData{Object{"id": 1, "customPokemonData": Object{"nature": nature, "ability": 999, "types": []int{1, 18}}}}, true)
		custom := asObject(party[0].(Object)["customPokemonData"])
		if nature == 3 {
			if len(custom) != 1 || num(custom["nature"]) != 3 {
				t.Fatalf("mint effect lost: %v", custom)
			}
		} else if len(custom) != 0 {
			t.Fatalf("invalid/custom effect retained: %v", custom)
		}
	}
}
func TestNormalizationPreservesCertifiedPermanentTypesOnBothSides(t *testing.T) {
	for _, player := range []bool{true, false} {
		for pokemonType := 0; pokemonType <= 17; pokemonType++ {
			party := NormalizeParty([]defs.PokemonData{Object{"id": 1, "fusionSpecies": 2,
				"customPokemonData":       Object{"nature": 3, "types": []any{nil, pokemonType}, "ability": 10},
				"fusionCustomPokemonData": Object{"types": []int{pokemonType}, "ability": 25, "nature": 4},
			}}, player)
			p := party[0].(Object)
			custom := asObject(p["customPokemonData"])
			types := custom["types"].([]any)
			if len(types) != 2 || types[0] != nil || num(types[1]) != float64(pokemonType) || num(custom["nature"]) != 3 || num(custom["ability"]) != 10 || len(custom) != 3 {
				t.Fatalf("primary permanent effect lost: %v", custom)
			}
			fusion := asObject(p["fusionCustomPokemonData"])
			if len(fusion) != 2 || num(fusion["types"].([]any)[0]) != float64(pokemonType) || num(fusion["ability"]) != 25 {
				t.Fatalf("fusion permanent effect lost or extra effect retained: %v", fusion)
			}
		}
	}
	for _, types := range []any{[]any{nil, nil}, []int{-1}, []int{18}, []int{1, 2, 3}, []any{1.5}, []string{"1"}, "1"} {
		party := NormalizeParty([]defs.PokemonData{Object{"customPokemonData": Object{"types": types}}}, true)
		if len(asObject(party[0].(Object)["customPokemonData"])) != 0 {
			t.Fatalf("invalid permanent types retained: %v", types)
		}
	}
}
func TestNormalizationPreservesCertifiedPermanentAbilities(t *testing.T) {
	for _, player := range []bool{true, false} {
		for ability := 0; ability <= 319; ability++ {
			party := NormalizeParty([]defs.PokemonData{Object{"fusionSpecies": 2,
				"customPokemonData":       Object{"ability": ability, "passive": 10},
				"fusionCustomPokemonData": Object{"ability": ability, "passive": 25},
			}}, player)
			for _, field := range []string{"customPokemonData", "fusionCustomPokemonData"} {
				custom := asObject(party[0].(Object)[field])
				if len(custom) != 1 || num(custom["ability"]) != float64(ability) {
					t.Fatalf("certified ability lost or custom passive retained: %v", custom)
				}
			}
		}
	}
	for _, ability := range []any{-1, 320, 1.5, "3", true, nil} {
		party := NormalizeParty([]defs.PokemonData{Object{"customPokemonData": Object{"ability": ability}, "fusionCustomPokemonData": Object{"ability": 10}}}, true)
		for _, field := range []string{"customPokemonData", "fusionCustomPokemonData"} {
			if len(asObject(party[0].(Object)[field])) != 0 {
				t.Fatal("invalid ability or absent fusion effect retained")
			}
		}
	}
}
func TestLogAudit(t *testing.T) {
	r := &Run{Created: 1700000000, ClearedAt: 1700000200, Starters: []Starter{{Species: 1, Moves: []int{33}}}, Candidate: &defs.SessionSaveData{Party: []defs.PokemonData{Object{"id": 1, "level": 100}}, Modifiers: []defs.PersistentModifierData{}}}
	logs := []Log{}
	head := ""
	for i := 1; i <= 200; i++ {
		items := []any{}
		if i == 1 {
			items = append(items, Object{"source": "SelectModifierPhase", "data": Object{"className": "MapModifier", "typeId": "MAP", "stackCount": 1}})
		}
		raw, _ := json.Marshal(Object{"wave": i, "party": []Object{{"id": 1}}, "enemyParty": []Object{{"id": i + 10}}, "money": 0, "items": items, "generation": Object{"basis": Object{"timestamp": (r.Created + int64(i)) * 1000, "party": []Object{{"id": 1, "species": 1, "level": 5, "moveset": []Object{{"moveId": 33}}}}}}})
		head = digest(head, string(raw))
		logs = append(logs, Log{Wave: i, Payload: string(raw), Head: head})
	}
	r.Anchors = []Anchor{{Wave: 5, Head: logs[4].Head, At: r.Created + 5}, {Wave: 200, Head: head, At: r.ClearedAt}}
	if e := ValidateLogs(r, logs); e != nil {
		t.Fatal(e)
	}
	// Structural wallet limits must let legitimate high balances reach the
	// mandatory moneyVerified engine, which separately proves income sources.
	for _, amount := range []int{10000001, MaxAuditMoney} {
		r.Candidate.Money = amount
		if e := ValidateLogs(r, logs); e != nil {
			t.Fatalf("native wallet rejected before income auditing: %v", e)
		}
	}
	for _, amount := range []int{-1, MaxAuditMoney + 1} {
		r.Candidate.Money = amount
		if e := ValidateLogs(r, logs); e == nil {
			t.Fatal("non-native wallet amount accepted")
		}
	}
	r.Candidate.Money = 0
	// This gate checks shape/provenance only. The required levelsVerified engine
	// proof accounts for candy growth instead of imposing the old 250 cap.
	r.Candidate.Party[0].(Object)["level"] = 10100
	if e := ValidateLogs(r, logs); e != nil {
		t.Fatalf("native candy levels must reach engine auditing: %v", e)
	}
	r.Candidate.Party[0].(Object)["level"] = MaxAuditLevel + 1
	if e := ValidateLogs(r, logs); e == nil {
		t.Fatal("unbounded final level accepted")
	}
	r.Candidate.Party[0].(Object)["level"] = 100
	item := Object{"className": "MapModifier", "typeId": "MAP", "stackCount": 1}
	r.Candidate.Modifiers = []defs.PersistentModifierData{item}
	if e := ValidateLogs(r, logs); e != nil {
		t.Fatalf("single acquired item rejected: %v", e)
	}
	r.Candidate.Modifiers = append(r.Candidate.Modifiers, item)
	if e := ValidateLogs(r, logs); e == nil {
		t.Fatal("splitting duplicate final items bypassed the aggregate ledger")
	}
	r.Candidate.Modifiers = nil
	r.Candidate.Party = []defs.PokemonData{Object{"id": 11, "level": 100}}
	if e := ValidateLogs(r, logs); e == nil {
		t.Fatal("merely seeing an enemy authorized it without capture proof")
	}
	r.Candidate.Party = []defs.PokemonData{Object{"id": 1, "level": 100}, Object{"id": 1, "level": 100}}
	if e := ValidateLogs(r, logs); e == nil {
		t.Fatal("duplicated final party ID accepted")
	}
	r.Candidate.Party = []defs.PokemonData{Object{"id": 1, "level": 100}}
	logs[99].Payload += " "
	if e := ValidateLogs(r, logs); e == nil {
		t.Fatal("tampered payload accepted")
	}
	logs[99].Payload = strings.TrimSpace(logs[99].Payload)
	r.Anchors[0].Head = strings.Repeat("a", 64)
	if e := ValidateLogs(r, logs); e == nil {
		t.Fatal("anchor mismatch accepted")
	}
}
