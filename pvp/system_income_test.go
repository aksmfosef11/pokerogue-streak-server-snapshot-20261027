// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"github.com/pagefaultgames/rogueserver/defs"
	"math"
	"slices"
	"testing"
)

func incomeSave() defs.SystemSaveData {
	return defs.SystemSaveData{StarterData: defs.StarterData{1: {}}, DexData: defs.DexData{}, VoucherCounts: defs.VoucherCounts{}}
}
func requireFlag(t *testing.T, flags []string, name string) {
	t.Helper()
	if !slices.Contains(flags, name) {
		t.Fatalf("missing %s in %v", name, flags)
	}
}
func TestPhysicalCountersDoNotDoubleCountPrevolutions(t *testing.T) {
	rules := Rules{25: {Cost: 4, Root: 172}, 172: {Cost: 3, Root: 172}}
	old, next := incomeSave(), incomeSave()
	next.DexData[25] = defs.DexEntry{CaughtCount: 1, HatchedCount: 1}
	next.DexData[172] = defs.DexEntry{CaughtCount: 1, HatchedCount: 1}
	if caught, hatched := rootCounterDeltas(old, next, rules); caught != 1 || hatched != 1 {
		t.Fatalf("%d/%d", caught, hatched)
	}
	delete(next.DexData, 172)
	if caught, hatched := rootCounterDeltas(old, next, rules); caught != 1 || hatched != 1 {
		t.Fatal("first Pikachu hatch bypassed physical counts")
	}
}
func TestHatchNeedsRemovedMatureStock(t *testing.T) {
	rules := Rules{1: {Cost: 3, EggMoves: []int{71, 72, 73, 74}}}
	old, next := incomeSave(), incomeSave()
	old.Eggs = []defs.EggData{{Id: 123, Species: 1, HatchWaves: 10}}
	next.DexData[1] = defs.DexEntry{HatchedCount: 1}
	next.StarterData[1] = defs.StarterEntry{CandyCount: 2, EggMoves: 1}
	requireFlag(t, ValidateSystem(old, next, rules, 0), "hatch-count-bound")
	if flags := ValidateSystem(old, next, rules, 5); len(flags) > 0 {
		t.Fatal(flags)
	}
	next.Eggs = old.Eggs
	requireFlag(t, ValidateSystem(old, next, rules, 5), "hatch-count-bound")
}
func TestRepeatedEggProgressCannotBorrowFiveWavesEveryUpload(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	old, next := incomeSave(), incomeSave()
	old.Eggs = []defs.EggData{{Id: 123, Species: 1, HatchWaves: 25, Tier: 1}}
	next.Eggs = []defs.EggData{{Id: 123, Species: 1, HatchWaves: 20, Tier: 1}}
	budget := &IncomeBudget{}
	if flags := validateSystem(old, next, rules, 0, budget); len(flags) > 0 {
		t.Fatal(flags)
	}
	old.Eggs = append([]defs.EggData(nil), next.Eggs...)
	next.Eggs[0].HatchWaves = 15
	requireFlag(t, validateSystem(old, next, rules, 0, budget), "egg-progress-bound")
}
func TestEggAttributesImmutableAndDuplicateIDsRejected(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	old, next := incomeSave(), incomeSave()
	old.Eggs = []defs.EggData{{Id: 123, Species: 1, HatchWaves: 10}}
	next.Eggs = []defs.EggData{{Id: 123, Species: 1, HatchWaves: 10, EggMoveIndex: 3}}
	requireFlag(t, ValidateSystem(old, next, rules, 0), "egg-properties-changed")
	next.Eggs = append(next.Eggs, next.Eggs[0])
	requireFlag(t, ValidateSystem(old, next, rules, 0), "egg-value-range")
}
func TestVoucherConservationAndStockMaturity(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	old, next := incomeSave(), incomeSave()
	old.VoucherCounts["1"] = 1
	for i := 0; i < 5; i++ {
		next.Eggs = append(next.Eggs, defs.EggData{Id: 123 + i, Species: 1, HatchWaves: 10})
	}
	if flags := ValidateSystem(old, next, rules, 0); len(flags) > 0 {
		t.Fatal(flags)
	}
	old.VoucherCounts["1"] = 0
	requireFlag(t, ValidateSystem(old, next, rules, 0), "egg-voucher-inflow-bound")
	next.Eggs[0].HatchWaves = 0
	requireFlag(t, ValidateSystem(old, next, rules, 5), "egg-progress-bound")
}
func TestEggPurchaseUsesHatchThresholdAtPurchase(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	for _, count := range []int{19, 20, 39, 40, 79, 80} {
		old, next := incomeSave(), incomeSave()
		old.DexData[1] = defs.DexEntry{HatchedCount: count}
		next.DexData[1] = old.DexData[1]
		old.StarterData[1] = defs.StarterEntry{CandyCount: 100}
		price := 25
		if count >= 80 {
			price = 12
		} else if count >= 40 {
			price = 18
		} else if count >= 20 {
			price = 22
		}
		next.StarterData[1] = defs.StarterEntry{CandyCount: 100 - price}
		next.Eggs = []defs.EggData{{Id: 123, SourceType: 3, Species: 1, HatchWaves: 10}}
		if flags := ValidateSystem(old, next, rules, 0); len(flags) > 0 {
			t.Fatalf("threshold %d: %v", count, flags)
		}
	}
}
func TestFriendshipCapsAndReserveAreSpeciesSpecific(t *testing.T) {
	rules := Rules{1: {Cost: 1}, 2: {Cost: 10}}
	old, next := incomeSave(), incomeSave()
	next.StarterData[1] = defs.StarterEntry{CandyCount: 4}
	budget := &IncomeBudget{Friendship: 100}
	if flags := validateSystem(old, next, rules, 0, budget); len(flags) > 0 {
		t.Fatal(flags)
	}
	next = incomeSave()
	next.StarterData[2] = defs.StarterEntry{CandyCount: 1}
	requireFlag(t, validateSystem(old, next, rules, 0, &IncomeBudget{Friendship: 100}), "candy-income-bound")
	// A repeated system-only upload has no new credit to spend.
	requireFlag(t, validateSystem(old, next, rules, 0, budget), "candy-income-bound")
}
func TestRemovedEntriesCannotResetValidationHistory(t *testing.T) {
	rules := Rules{1: {Cost: 3}}
	old, next := incomeSave(), incomeSave()
	old.StarterData[1] = defs.StarterEntry{ValueReduction: 1}
	old.DexData[1] = defs.DexEntry{HatchedCount: 20}
	delete(next.StarterData, 1)
	flags := ValidateSystem(old, next, rules, 0)
	requireFlag(t, flags, "starter-entry-removed")
	requireFlag(t, flags, "dex-entry-removed")
}
func TestRareUnlockStatisticsAreConservativeAndPersistent(t *testing.T) {
	stats := EggStatistics{}
	for i := 0; i < 60; i++ {
		old, next := incomeSave(), incomeSave()
		next.DexData[1] = defs.DexEntry{HatchedCount: 1}
		if i < 4 {
			next.StarterData[1] = defs.StarterEntry{EggMoves: 8}
		}
		stats.observe(old, next, Rules{1: {Cost: 3, EggTier: 0}})
	}
	if stats.Outlier {
		t.Fatal("normal rare rate marked as outlier")
	}
	for i := 0; i < 30; i++ {
		old, next := incomeSave(), incomeSave()
		next.DexData[1] = defs.DexEntry{HatchedCount: 1}
		next.StarterData[1] = defs.StarterEntry{EggMoves: 8}
		stats.observe(old, next, Rules{1: {Cost: 3, EggTier: 0}})
	}
	if !stats.Outlier || stats.Hatched != 90 || stats.NewRareUnlocks != 34 {
		t.Fatalf("%+v", stats)
	}
}

func TestVerifiedRareStatisticsUseActualEggSourceAndManaphyRates(t *testing.T) {
	for _, tc := range []struct{ source, tier, species, denominator int }{
		{0, 0, 1, 16}, {0, 1, 1, 12}, {3, 2, 1, 6}, {3, 3, 1, 3},
		{1, 0, 1, 48}, {2, 1, 1, 24}, {4, 2, 1, 12}, {1, 3, 1, 6},
		{0, 0, 489, 6}, {4, 0, 490, 12},
	} {
		stats := EggStatistics{}
		stats.verified(defs.EggData{SourceType: tc.source, Tier: tc.tier, Species: tc.species, EggMoveIndex: 3})
		if stats.VerifiedEggs != 1 || stats.VerifiedRare != 1 || math.Abs(stats.VerifiedExpected-1/float64(tc.denominator)) > 1e-12 || stats.VerifiedOutlier {
			t.Fatalf("%+v: %+v", tc, stats)
		}
	}
	stats := EggStatistics{}
	for n := 0; n < 60; n++ {
		index := 0
		if n%16 == 0 {
			index = 3
		}
		stats.verified(defs.EggData{SourceType: 0, EggMoveIndex: index})
	}
	if stats.VerifiedOutlier {
		t.Fatal("ordinary verified rare rate marked anomalous")
	}
	for n := 0; n < 30; n++ {
		stats.verified(defs.EggData{SourceType: 0, EggMoveIndex: 3})
	}
	if !stats.VerifiedOutlier {
		t.Fatal("extreme verified rare rate not reported")
	}
}

func TestEventManaphyRetainsUpstreamCommonHatchWaves(t *testing.T) {
	old, next := incomeSave(), incomeSave()
	next.Eggs = []defs.EggData{{Id: 204, SourceType: 4, Tier: 0, Species: 489, HatchWaves: 10}}
	flags := validateSystem(old, next, Rules{1: {Cost: 3}}, 1, &IncomeBudget{})
	if len(flags) != 0 {
		t.Fatal(flags)
	}
	next.Eggs[0].SourceType = 0
	requireFlag(t, ValidateSystem(old, next, Rules{1: {Cost: 3}}, 1), "egg-progress-bound")
}
