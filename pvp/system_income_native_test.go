// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"maps"
	"os"
	"testing"

	"github.com/pagefaultgames/rogueserver/defs"
)

type nativeIncomeSample struct {
	Name  string              `json:"name"`
	Waves int                 `json:"waves"`
	Old   defs.SystemSaveData `json:"old"`
	Next  defs.SystemSaveData `json:"next"`
	Facts struct {
		Root            int `json:"root"`
		PhysicalCatches int `json:"physicalCatches"`
		PhysicalHatches int `json:"physicalHatches"`
		EventEggs       int `json:"eventEggs"`
	} `json:"facts"`
}

func nativeIncomeControls(t *testing.T) (Rules, []nativeIncomeSample) {
	t.Helper()
	var fixture struct {
		Manifest RulesManifest        `json:"manifest"`
		Samples  []nativeIncomeSample `json:"samples"`
	}
	raw, err := os.ReadFile("testdata/native-system-income.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var manifest RulesManifest
	raw, err = os.ReadFile("rules.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if fixture.Manifest != manifest || len(fixture.Samples) != 8 {
		t.Fatal("native system income controls do not cover the current pinned rules")
	}
	var rules Rules
	raw, err = os.ReadFile("rules.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	return rules, fixture.Samples
}

func TestNativeSystemIncomeAccepted(t *testing.T) {
	rules, samples := nativeIncomeControls(t)
	for _, sample := range samples {
		t.Run(sample.Name, func(t *testing.T) {
			// Same finite reserve granted once by ObserveSystem when session
			// observation begins, including rewards before the next X1/X6 save.
			budget := &IncomeBudget{Initialized: true}
			budget.grant(5, rules)
			if flags := validateSystem(sample.Old, sample.Next, rules, sample.Waves, budget); len(flags) != 0 {
				t.Fatalf("native reward rejected: %v", flags)
			}
			caught, hatched := rootCounterDeltas(sample.Old, sample.Next, rules)
			if caught != sample.Facts.PhysicalCatches || hatched != sample.Facts.PhysicalHatches {
				t.Fatalf("physical count mismatch: %d/%d", caught, hatched)
			}
			if sample.Facts.EventEggs > 0 && len(sample.Next.Eggs)-len(sample.Old.Eggs) != sample.Facts.EventEggs {
				t.Fatal("missing native event egg burst")
			}
			before := nativeIncomeBudgetSnapshot(budget)
			if flags := validateSystem(sample.Next, sample.Next, rules, 0, budget); len(flags) != 0 {
				t.Fatalf("unchanged system upload rejected: %v", flags)
			}
			after := nativeIncomeBudgetSnapshot(budget)
			if string(before) != string(after) {
				t.Fatal("unchanged upload refilled or consumed an income budget")
			}
		})
	}
}

func nativeIncomeBudgetSnapshot(budget *IncomeBudget) []byte {
	copy := *budget
	copy.CandyCarry = maps.Clone(budget.CandyCarry)
	// A zero entry inserted while observing an unchanged starter is not credit.
	for id, value := range copy.CandyCarry {
		if value == 0 {
			delete(copy.CandyCarry, id)
		}
	}
	raw, _ := json.Marshal(copy)
	return raw
}

func TestNativeSystemIncomeCannotMintCandy(t *testing.T) {
	rules, samples := nativeIncomeControls(t)
	for _, sample := range samples {
		t.Run(sample.Name, func(t *testing.T) {
			next := sample.Next
			next.StarterData = maps.Clone(next.StarterData)
			for id, entry := range next.StarterData {
				entry.CandyCount += 1000
				next.StarterData[id] = entry
				break
			}
			budget := &IncomeBudget{Initialized: true}
			budget.grant(5, rules)
			requireFlag(t, validateSystem(sample.Old, next, rules, sample.Waves, budget), "candy-income-bound")
		})
	}
}

func TestNativeBreederEggBudgetDoesNotRefillOnUpload(t *testing.T) {
	rules, samples := nativeIncomeControls(t)
	for _, sample := range samples {
		if sample.Name != "breeder-thirteen-eggs" {
			continue
		}
		budget := &IncomeBudget{Initialized: true}
		if flags := validateSystem(sample.Old, sample.Next, rules, 1, budget); len(flags) != 0 {
			t.Fatalf("native one-wave Breeder reward rejected: %v", flags)
		}
		if budget.EventEggs != 0 {
			t.Fatal("native maximum did not consume its event egg budget")
		}
		if flags := validateSystem(sample.Next, sample.Next, rules, 0, budget); len(flags) != 0 {
			t.Fatal(flags)
		}
		if budget.EventEggs != 0 {
			t.Fatal("repeated upload renewed event eggs")
		}
		next := sample.Next
		next.Eggs = append([]defs.EggData(nil), next.Eggs...)
		for i, egg := range sample.Next.Eggs {
			egg.Id = 80000001 + i
			next.Eggs = append(next.Eggs, egg)
		}
		requireFlag(t, validateSystem(sample.Next, next, rules, 0, budget), "event-egg-inflow-bound")
		return
	}
	t.Fatal("native maximum Breeder control missing")
}

func TestEventEggBudgetUpgradeIsDurableAndDoesNotResetSpending(t *testing.T) {
	for _, remaining := range []int{0, 2, 5} {
		budget := &IncomeBudget{Initialized: true, EventEggs: remaining, Captures: 7, Friendship: 123, Pulls: 9, Dreams: 2}
		budget.migrateEventEggBudget()
		if budget.EventEggVersion != eventEggBudgetVersion || budget.EventEggs != remaining+60 {
			t.Fatalf("legacy reserve was not upgraded once: %+v", budget)
		}
		if budget.Captures != 7 || budget.Friendship != 123 || budget.Pulls != 9 || budget.Dreams != 2 {
			t.Fatal("unrelated income was replenished")
		}
		budget.EventEggs = 0 // all available native rewards have been consumed
		raw, err := json.Marshal(budget)
		if err != nil {
			t.Fatal(err)
		}
		var restored IncomeBudget
		if err = json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		restored.migrateEventEggBudget()
		if restored.EventEggs != 0 {
			t.Fatal("saved migration refilled the reserve on restart")
		}
	}
	fresh := &IncomeBudget{}
	fresh.migrateEventEggBudget()
	if fresh.EventEggs != 0 {
		t.Fatal("unobserved new account received income")
	}
}
