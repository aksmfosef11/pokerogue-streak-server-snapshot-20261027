// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"github.com/pagefaultgames/rogueserver/defs"
	"testing"
)

func TestStarterMoveSources(t *testing.T) {
	r := Rule{StarterMoves: map[int][]int{0: {33, 45}, 1: {33, 86}}, EggMoves: []int{71, 45, 0, 895}}
	tests := []struct {
		name  string
		p     Starter
		mask  int
		valid bool
	}{
		{"ordinary", Starter{Moves: []int{33, 45}}, 0, true},
		{"base also egg needs no egg unlock", Starter{Moves: []int{45}}, 0, true},
		{"verified egg", Starter{Moves: []int{71}}, 1, true},
		{"locked egg", Starter{Moves: []int{71}}, 0, false},
		{"arbitrary non egg", Starter{Moves: []int{165}}, 15, false},
		{"form move", Starter{FormIndex: 1, Moves: []int{86}}, 0, true},
		{"other form move", Starter{Moves: []int{86}}, 0, false},
		{"missing form", Starter{FormIndex: 99, Moves: []int{33}}, 0, false},
		{"missing moves", Starter{}, 0, false},
		{"duplicates", Starter{Moves: []int{33, 33}}, 0, false},
		{"none", Starter{Moves: []int{0}}, 15, false},
		{"too many", Starter{Moves: []int{33, 45, 71, 895, 86}}, 15, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateStarterMoves(tt.p, r, TrustedStarter{EggMoves: tt.mask}); (err == nil) != tt.valid {
				t.Fatalf("valid=%v: %v", tt.valid, err)
			}
		})
	}
}

func TestInitialPartyBoundToServerStarterChoice(t *testing.T) {
	starters := []Starter{{Species: 1, FormIndex: 0, Passive: false, Moves: []int{33}}}
	party := []defs.PokemonData{Object{"id": 123, "species": 1, "formIndex": 0, "level": 5, "passive": false, "moveset": []Object{{"moveId": 33}}}}
	if err := validateInitialParty(starters, party); err != nil {
		t.Fatal(err)
	}
	for _, change := range []Object{{"species": 150}, {"level": 200}, {"formIndex": 3}, {"passive": true}, {"fusionSpecies": 384}, {"moveset": []Object{{"moveId": 165}}}} {
		for key, value := range change {
			old := party[0].(Object)[key]
			party[0].(Object)[key] = value
			if err := validateInitialParty(starters, party); err == nil {
				t.Fatalf("initial party forgery accepted: %s", key)
			}
			party[0].(Object)[key] = old
		}
	}
	if err := validateInitialParty(nil, party); err == nil {
		t.Fatal("missing server starter choice accepted")
	}
}

func TestRunBindsSystemVersion(t *testing.T) {
	if _, err := (&Service{}).StartRun(nil, Version, []Starter{{Species: 1}}, defs.SystemSaveData{GameVersion: "1.12.1.2"}); err == nil {
		t.Fatal("mixed-version system accepted")
	}
}

func TestWeirdDreamEggMoveCertificationAndTrust(t *testing.T) {
	r := Rule{
		StarterMoves: map[int][]int{0: {33, 45}},
		EggMoves:     []int{71, 45, 0, 895},
	}
	var st State
	initEggTrust(&st)

	// Before Weird Dream audit, species 1 has no verified egg moves
	if err := validateStarterMoves(Starter{Species: 1, Moves: []int{71}}, r, st.TrustedStarters[1]); err == nil {
		t.Fatal("unverified egg move accepted before dream audit")
	}

	// Dream audit succeeds and certifies egg move slot 0 for species 1
	dreamMoves := []VerifiedDreamMove{
		{Species: 1, MoveIndex: 0},
	}
	for _, dm := range dreamMoves {
		if dm.Species > 0 && dm.MoveIndex >= 0 && dm.MoveIndex < 4 {
			st.VerifiedEggMoves[dm.Species] |= 1 << dm.MoveIndex
			entry := st.TrustedStarters[dm.Species]
			entry.EggMoves = st.VerifiedEggMoves[dm.Species]
			st.TrustedStarters[dm.Species] = entry
		}
	}

	// Verified dream egg move 71 is now accepted
	if err := validateStarterMoves(Starter{Species: 1, Moves: []int{71}}, r, st.TrustedStarters[1]); err != nil {
		t.Fatalf("certified dream egg move rejected: %v", err)
	}

	// Unverified egg move 895 (slot 3) is still rejected
	if err := validateStarterMoves(Starter{Species: 1, Moves: []int{895}}, r, st.TrustedStarters[1]); err == nil {
		t.Fatal("unverified egg move 895 accepted")
	}

	// Client uploads system save claiming both egg move 0 (1) and egg move 3 (8)
	sys := defs.SystemSaveData{
		StarterData: defs.StarterData{
			1: {EggMoves: (1 << 0) | (1 << 3)},
		},
	}
	trustStarters(&st, sys)

	// Verified move 0 remains trusted
	if st.TrustedStarters[1].EggMoves&(1<<0) == 0 {
		t.Fatal("certified dream egg move lost during trustStarters")
	}
	// Forged move 3 is filtered out because it is not in st.VerifiedEggMoves
	if st.TrustedStarters[1].EggMoves&(1<<3) != 0 {
		t.Fatal("unverified dream egg move trusted from client system save")
	}

	// Subsequent dream audit certifies slot 3
	secondDream := []VerifiedDreamMove{{Species: 1, MoveIndex: 3}}
	for _, dm := range secondDream {
		st.VerifiedEggMoves[dm.Species] |= 1 << dm.MoveIndex
		entry := st.TrustedStarters[dm.Species]
		entry.EggMoves = st.VerifiedEggMoves[dm.Species]
		st.TrustedStarters[dm.Species] = entry
	}
	trustStarters(&st, sys)
	if st.TrustedStarters[1].EggMoves != ((1 << 0) | (1 << 3)) {
		t.Fatalf("both certified dream egg moves should be trusted, got %d", st.TrustedStarters[1].EggMoves)
	}
	if err := validateStarterMoves(Starter{Species: 1, Moves: []int{895}}, r, st.TrustedStarters[1]); err != nil {
		t.Fatalf("certified dream move 895 rejected: %v", err)
	}
}
