// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"github.com/pagefaultgames/rogueserver/defs"
	"math"
	"strconv"
)

// Credits survive system-only uploads. The five-wave reserve covers rewards
// obtained between ordinary X1/X6 uploads; submitting another save never refills
// it. Unused observed credit is bounded to the same reserve after validation.
type IncomeBudget struct {
	CandyCarry      map[int]int `json:"candyCarry,omitempty"`
	Initialized     bool        `json:"initialized"`
	Captures        int         `json:"captures"`
	Friendship      int         `json:"friendship"`
	Pulls           int         `json:"pulls"`
	EventEggs       int         `json:"eventEggs"`
	EventEggVersion int         `json:"eventEggVersion,omitempty"`
	Dreams          int         `json:"dreams"`
	HatchDebt       map[int]int `json:"hatchDebt,omitempty"`
}

// The pinned native Breeder converts at most 30 points into five initial
// common eggs, one rare per four remaining points, and common remainders.
// The maximum is 13 at 28 points, reproduced in native-system-income.json.
// Level 1 does not observe every ME selection between X1/X6 uploads, so this
// remains a source upper bound, shared by all event eggs in the observed waves.
const maxEventEggsPerWave = 13
const eventEggBudgetVersion = 1

func (b *IncomeBudget) migrateEventEggBudget() {
	if b.EventEggVersion >= eventEggBudgetVersion {
		return
	}
	if b.Initialized {
		// Upgrade the existing five-wave reserve once. Preserve eggs already
		// spent under the old one-per-wave limit and all other income budgets.
		b.EventEggs = min(5*maxEventEggsPerWave, max(0, b.EventEggs)+5*(maxEventEggsPerWave-1))
	}
	b.EventEggVersion = eventEggBudgetVersion
}

type EggStatistics struct {
	VerifiedEggs     int     `json:"verifiedEggs"`
	VerifiedRare     int     `json:"verifiedRare"`
	VerifiedExpected float64 `json:"verifiedExpected"`
	VerifiedOutlier  bool    `json:"verifiedOutlier"`
	Hatched          int     `json:"hatched"`
	NewRareUnlocks   int     `json:"newRareUnlocks"`
	ExpectedRare     float64 `json:"expectedRare"`
	Outlier          bool    `json:"outlier"`
}

func (st *EggStatistics) verified(egg defs.EggData) {
	tier := min(3, max(0, egg.Tier))
	if egg.Species == 489 || egg.Species == 490 {
		tier = 2
	}
	rates := []int{48, 24, 12, 6}
	if egg.SourceType == 0 || egg.SourceType == 3 {
		rates = []int{16, 12, 6, 3}
	}
	st.VerifiedEggs++
	if egg.EggMoveIndex == 3 {
		st.VerifiedRare++
	}
	st.VerifiedExpected += 1 / float64(rates[tier])
	k, mu := float64(st.VerifiedRare), st.VerifiedExpected
	st.VerifiedOutlier = st.VerifiedEggs >= 30 && k >= 6 && k > mu && (mu == 0 || k-mu+k*math.Log(mu/k) < math.Log(0.000001))
}

func friendshipPerWave(rules Rules) int {
	multiplier := 3
	for _, rule := range rules {
		multiplier = max(multiplier, candyRules(rule).FriendshipMultiplier)
	}
	// Six enemies and six participating party members, three Soothe Bells,
	// one party-wide Rare Candy reward, and one Breeder visit. This deliberately
	// permits trainer/ME rewards which Level 1 cannot reproduce from save deltas.
	return (6*7*6 + 15*6 + 50) * multiplier
}
func (b *IncomeBudget) grant(waves int, rules Rules) {
	waves = max(0, min(waves, 100000))
	b.Captures += waves * 6
	b.Friendship += waves * friendshipPerWave(rules)
	b.Pulls += waves * 25
	b.EventEggs += waves * maxEventEggsPerWave
	b.Dreams += waves * 6
}
func (b *IncomeBudget) trim(rules Rules) {
	b.Captures = min(b.Captures, 5*6)
	b.Friendship = min(b.Friendship, 5*friendshipPerWave(rules))
	b.Pulls = min(b.Pulls, 5*25)
	b.EventEggs = min(b.EventEggs, 5*maxEventEggsPerWave)
	b.Dreams = min(b.Dreams, 5*6)
}
func debit(credit *int, count int) bool {
	if count <= 0 {
		return true
	}
	if count > *credit {
		*credit = 0
		return false
	}
	*credit -= count
	return true
}
func groupedCounterDeltas(old, next defs.SystemSaveData, rules Rules) (map[int]int, map[int]int) {
	caught, hatched := map[int]int{}, map[int]int{}
	// Evolved captures recursively increment every prevolution's counter. Count
	// each physical capture/hatch once, including Pikachu's starter exception.
	for id, rule := range rules {
		root := rule.Root
		if root == 0 {
			root = id
		}
		// The upstream first-catch/skip-message Pikachu path can omit Pichu's
		// counter. Taking the family maximum covers that path without counting
		// the same physical catch twice when both counters were incremented.
		caught[root] = max(caught[root], max(0, next.DexData[id].CaughtCount-old.DexData[id].CaughtCount))
		hatched[root] = max(hatched[root], max(0, next.DexData[id].HatchedCount-old.DexData[id].HatchedCount))
	}
	return caught, hatched
}
func rootCounterDeltas(old, next defs.SystemSaveData, rules Rules) (caught, hatched int) {
	c, h := groupedCounterDeltas(old, next, rules)
	for _, v := range c {
		caught += v
	}
	for _, v := range h {
		hatched += v
	}
	return
}
func validateEggFlow(old, next defs.SystemSaveData, rules Rules, waves, hatched int, budget *IncomeBudget, flag func(string)) {
	prior := map[int]defs.EggData{}
	for _, egg := range old.Eggs {
		prior[egg.Id] = egg
	}
	current := map[int]bool{}
	if len(next.Eggs) > 99 {
		flag("egg-stock-bound")
	}
	if budget.HatchDebt == nil {
		budget.HatchDebt = map[int]int{}
	}
	pulls, eventEggs := 0, 0
	for _, egg := range next.Eggs {
		if egg.Id <= 0 || current[egg.Id] || egg.Tier < 0 || egg.Tier > 3 || egg.SourceType < 0 || egg.SourceType > 4 || egg.EggMoveIndex < 0 || egg.EggMoveIndex > 3 || egg.VariantTier < 0 || egg.VariantTier > 2 || egg.HatchWaves < 0 || egg.HatchWaves > 100 {
			flag("egg-value-range")
		}
		current[egg.Id] = true
		if before, ok := prior[egg.Id]; ok {
			debt := max(0, budget.HatchDebt[egg.Id]+before.HatchWaves-egg.HatchWaves-waves)
			budget.HatchDebt[egg.Id] = debt
			if debt > 5 || egg.HatchWaves > before.HatchWaves {
				flag("egg-progress-bound")
			}
			before.HatchWaves = egg.HatchWaves
			if before != egg {
				flag("egg-properties-changed")
			}
		} else {
			if egg.Tier >= 0 && egg.Tier <= 3 {
				minimum := []int{10, 25, 50, 100}[egg.Tier]
				if egg.Species == 489 || egg.Species == 490 || (egg.Tier == 0 && egg.Species == 0 && egg.Id%204 == 0) {
					minimum = 50
					// A non-pulled event egg computes hatch waves before rolling
					// its species. Unlike gacha, it never applies the later
					// Manaphy correction in increasePullStatistic().
					if egg.SourceType == 4 && egg.Tier == 0 {
						minimum = 10
					}
				}
				budget.HatchDebt[egg.Id] = max(0, minimum-egg.HatchWaves)
				if budget.HatchDebt[egg.Id] > 5 {
					flag("egg-progress-bound")
				}
			}
			switch egg.SourceType {
			case 0, 1, 2:
				pulls++
			case 3:
				if _, ok := rules[egg.Species]; !ok {
					flag("same-species-egg-species")
				}
			case 4:
				eventEggs++
			}
		}
	}
	removed, mature := 0, 0
	for id, egg := range prior {
		if !current[id] {
			removed++
			if egg.HatchWaves+budget.HatchDebt[id] <= waves+5 {
				mature++
			}
			delete(budget.HatchDebt, id)
		}
	}
	if hatched > removed || hatched > mature {
		flag("hatch-count-bound")
	}
	// Value conservation: burning existing vouchers funds eggs. New vouchers
	// and free gacha eggs share a single observed-wave budget, so minting and
	// immediately spending a voucher cannot evade the inflow bound.
	weights := []int{1, 5, 10, 25}
	for kind, weight := range weights {
		key := strconv.Itoa(kind)
		if next.VoucherCounts[key] < 0 {
			flag("voucher-value-range")
		}
		pulls += (next.VoucherCounts[key] - old.VoucherCounts[key]) * weight
	}
	for key, count := range next.VoucherCounts {
		if _, err := strconv.Atoi(key); err != nil || (key != "0" && key != "1" && key != "2" && key != "3") || count > 1000000 {
			flag("voucher-value-range")
		}
	}
	if !debit(&budget.Pulls, pulls) {
		flag("egg-voucher-inflow-bound")
	}
	if !debit(&budget.EventEggs, eventEggs) {
		flag("event-egg-inflow-bound")
	}
}
func (st *EggStatistics) observe(old, next defs.SystemSaveData, rules Rules) {
	_, hatchGroups := groupedCounterDeltas(old, next, rules)
	for id, hatches := range hatchGroups {
		rule := rules[id]
		st.Hatched += hatches
		// New rare unlocks are a lower bound on rare rolls, not a count of all
		// rolls. Use the most generous normal rate (Move Up/same-species eggs).
		tier := rule.EggTier
		if id == 489 || id == 490 {
			tier = 2
		}
		tier = min(3, max(0, tier))
		st.ExpectedRare += float64(hatches) / float64([]int{16, 12, 6, 3}[tier])
	}
	for id := range rules {
		if next.StarterData[id].EggMoves&^old.StarterData[id].EggMoves&8 != 0 {
			st.NewRareUnlocks++
		}
	}
	// A conservative one-sided Chernoff bound. This is an operator anomaly,
	// separate from qualification flags: Weird Dream can also unlock a move.
	k, mu := float64(st.NewRareUnlocks), st.ExpectedRare
	st.Outlier = st.Hatched >= 30 && k >= 6 && k > mu && (mu == 0 || k-mu+k*math.Log(mu/k) < math.Log(0.000001))
}
