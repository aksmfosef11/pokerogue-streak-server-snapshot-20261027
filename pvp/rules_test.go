// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"github.com/pagefaultgames/rogueserver/defs"
	"os"
	"testing"
)

func TestExportedCandyCostsAndEggThresholds(t *testing.T) {
	data, err := os.ReadFile("rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var rules Rules
	if err = json.Unmarshal(data, &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules) < 500 {
		t.Fatal("incomplete balance export")
	}
	for id, r := range rules {
		if r.Root <= 0 || len(r.StarterMoves) < 1 {
			t.Fatalf("missing registry-derived starter data: %d", id)
		}
		c := r.Candy
		if c == nil || c.FriendshipCap <= 0 || len(c.Reduction) != 2 || len(c.EggCosts) != len(c.EggThresholds)+1 {
			t.Fatalf("invalid candy rules: %d", id)
		}
		previous := 0
		for _, threshold := range c.EggThresholds {
			if threshold <= previous {
				t.Fatal("unsorted egg cost thresholds")
			}
			previous = threshold
		}
	}
	if rules[25].Root != 172 {
		t.Fatal("evolved starter would double-count catches/hatches")
	}
	if rules[1].Candy.FriendshipCap != 75 || rules[1].Candy.Passive != 35 || rules[1].Candy.Reduction[0] != 20 {
		t.Fatal("Bulbasaur balance drift")
	}
}

func BenchmarkSystemValidationFullDex(b *testing.B) {
	raw, err := os.ReadFile("rules.json")
	if err != nil {
		b.Fatal(err)
	}
	var rules Rules
	if err = json.Unmarshal(raw, &rules); err != nil {
		b.Fatal(err)
	}
	save := incomeSave()
	for id, rule := range rules {
		mask := 0
		for slot, move := range rule.EggMoves {
			if move > 0 {
				mask |= 1 << slot
			}
		}
		save.StarterData[id] = defs.StarterEntry{CandyCount: 100, ValueReduction: 1, PassiveAttr: 3, EggMoves: mask}
		save.DexData[id] = defs.DexEntry{CaughtCount: 100, HatchedCount: 100}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if flags := ValidateSystem(save, save, rules, 0); len(flags) > 0 {
			b.Fatal(flags)
		}
	}
}
