// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/pagefaultgames/rogueserver/defs"
	"io"
	"math/bits"
	"sort"
	"strings"
	"time"
)

var passiveCosts = []int{40, 40, 35, 30, 25, 20, 15, 10, 10, 10}
var reductionCosts = [][]int{{25, 60}, {25, 60}, {20, 50}, {15, 40}, {12, 35}, {10, 30}, {8, 20}, {5, 15}, {5, 15}, {5, 15}}
var eggCosts = [][]int{{30, 27, 22, 15}, {30, 27, 22, 15}, {25, 22, 18, 12}, {20, 18, 15, 10}, {18, 16, 13, 9}, {15, 13, 11, 7}, {12, 10, 9, 6}, {10, 9, 7, 5}, {10, 9, 7, 5}, {10, 9, 7, 5}}
var eggThresholds = [][]int{{20, 40, 80}, {20, 40, 80}, {20, 40, 80}, {15, 30, 60}, {15, 30, 60}, {15, 30, 60}, {10, 20, 40}, {10, 20, 40}, {10, 20, 40}, {8, 16, 32}}

func candyRules(rule Rule) CandyCosts {
	if rule.Candy != nil {
		return *rule.Candy
	}
	// Compatibility for callers supplying legacy Rule values. Service startup
	// requires the exported tables, so production never relies on this fallback.
	i := rule.Cost - 1
	return CandyCosts{FriendshipCap: []int{25, 50, 75, 100, 150, 200, 300, 450, 450, 600}[i], FriendshipMultiplier: 3, Passive: passiveCosts[i], Reduction: reductionCosts[i], EggCosts: eggCosts[i], EggThresholds: eggThresholds[i]}
}

func (s *Service) ObserveSystem(id []byte, old, next defs.SystemSaveData, session *defs.SessionSaveData) error {
	err := s.update(id, func(st *State) error {
		if err := s.inheritQualification(id, st, old); err != nil {
			return err
		}
		if next.GameVersion != s.GameVersion() {
			st.flag("unsupported-system-version")
		}
		defer func() {
			s.observeEggProofs(st, old, next)
			st.SystemHash = systemFingerprint(next)
			st.SystemHashSchema = 2
			if len(st.Flags) == 0 {
				trustStarters(st, next)
			}
		}()
		if st.SystemHashSchema == 2 && st.SystemHash != "" && st.SystemHash != systemFingerprint(old) {
			st.flag("unobserved-system-update")
		}
		if st.Baseline && st.TrustSchema == 0 && hasStarterEnhancements(old) {
			st.flag("imported-baseline-unverified")
		}
		now := time.Now().Unix()
		waves := 0
		if session != nil {
			prev := st.Observed[session.Seed]
			if prev == 0 && session.WaveIndex > 1 {
				prev = session.WaveIndex
			}
			waves = session.WaveIndex - prev
			if waves < 0 {
				waves = 0
			}
			if len(st.Observed) >= 16 {
				for k := range st.Observed {
					if k != session.Seed {
						delete(st.Observed, k)
						break
					}
				}
			}
			if session.WaveIndex > prev || prev == session.WaveIndex {
				st.Observed[session.Seed] = session.WaveIndex
			}
		}
		if !st.Baseline {
			st.Baseline = true
			if hasStarterEnhancements(next) {
				st.flag("imported-baseline-unverified")
			}
			st.LastUpload = now
			st.LastWaveAt = now
			st.WaveCredit = initialWaveCredit
			return nil
		}
		st.observeWaveArrival(now, waves)
		if st.Income == nil {
			st.Income = &IncomeBudget{}
		}
		st.Income.migrateEventEggBudget()
		if !st.Income.Initialized && session != nil {
			st.Income.Initialized = true
			st.Income.grant(5, s.Rules)
		}
		flags := validateSystem(old, next, s.Rules, waves, st.Income)
		for _, f := range flags {
			st.flag(f)
		}
		st.EggStatistics.observe(old, next, s.Rules)
		st.LastUpload = now
		return nil
	})
	if err == nil && (len(next.PVPEggProofs) > 0 || old.GameVersion != next.GameVersion) {
		s.enqueueAudit(id)
	}
	return err
}
func systemFingerprint(data defs.SystemSaveData) string {
	// Gob (the ordinary save store) canonicalizes empty slices to nil. Bind
	// qualification to semantic validation inputs, not their wire encoding.
	var starters map[int][5]int
	if len(data.StarterData) > 0 {
		starters = map[int][5]int{}
	}
	for id, p := range data.StarterData {
		starters[id] = [5]int{p.CandyCount, p.Friendship, p.ValueReduction, p.PassiveAttr, p.EggMoves}
	}
	var dex map[int][2]int
	if len(data.DexData) > 0 {
		dex = map[int][2]int{}
	}
	for id, p := range data.DexData {
		dex[id] = [2]int{p.CaughtCount, p.HatchedCount}
	}
	eggs := data.Eggs
	if len(eggs) == 0 {
		eggs = nil
	}
	eggPity := data.EggPity
	if len(eggPity) == 0 {
		eggPity = nil
	}
	unlockPity := data.UnlockPity
	if len(unlockPity) == 0 {
		unlockPity = nil
	}
	vouchers := data.VoucherCounts
	if len(vouchers) == 0 {
		vouchers = nil
	}
	raw, err := json.Marshal(struct {
		TrainerID  int
		SecretID   int
		Version    string
		Starters   map[int][5]int
		Dex        map[int][2]int
		Eggs       []defs.EggData
		EggPity    []int
		UnlockPity []int
		Vouchers   defs.VoucherCounts
	}{data.TrainerId, data.SecretId, data.GameVersion, starters, dex, eggs, eggPity, unlockPity, vouchers})
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func hasStarterEnhancements(data defs.SystemSaveData) bool {
	for _, entry := range data.StarterData {
		if entry.CandyCount > 0 || entry.ValueReduction > 0 || entry.PassiveAttr&1 != 0 || entry.EggMoves != 0 {
			return true
		}
	}
	return false
}
func trustStarters(st *State, data defs.SystemSaveData) {
	trusted := map[int]TrustedStarter{}
	for id, entry := range data.StarterData {
		trusted[id] = TrustedStarter{ValueReduction: entry.ValueReduction, PassiveAttr: entry.PassiveAttr, EggMoves: st.VerifiedEggMoves[id] & entry.EggMoves}
	}
	st.TrustSchema = 1
	st.TrustedStarters = trusted
}
func ValidateSystem(old, next defs.SystemSaveData, rules Rules, waves int) []string {
	return validateSystem(old, next, rules, waves, &IncomeBudget{})
}
func validateSystem(old, next defs.SystemSaveData, rules Rules, waves int, budget *IncomeBudget) []string {
	reasons := map[string]bool{}
	flag := func(s string) { reasons[s] = true }
	budget.grant(waves, rules)
	if budget.CandyCarry == nil || waves > 0 {
		budget.CandyCarry = map[int]int{}
	}
	defer budget.trim(rules)
	caught, hatched := rootCounterDeltas(old, next, rules)
	oldEggs := map[int]bool{}
	for _, e := range old.Eggs {
		oldEggs[e.Id] = true
	}
	newEggs := map[int]int{}
	for _, e := range next.Eggs {
		if !oldEggs[e.Id] && e.SourceType == 3 {
			newEggs[e.Species]++
		}
	}
	for id, n := range next.DexData {
		o := old.DexData[id]
		if n.CaughtCount < o.CaughtCount || n.HatchedCount < o.HatchedCount || n.CaughtCount < 0 || n.HatchedCount < 0 || n.CaughtCount > 1000000000 || n.HatchedCount > 1000000000 {
			flag("dex-counters-decreased")
		}
		if _, ok := rules[id]; !ok && n.HatchedCount > o.HatchedCount {
			flag("nonstarter-hatch-counter")
		}
	}
	for id, o := range old.DexData {
		if _, ok := next.DexData[id]; !ok && (o.CaughtCount != 0 || o.HatchedCount != 0) {
			flag("dex-entry-removed")
		}
	}
	if !debit(&budget.Captures, caught) {
		flag("capture-count-bound")
	}
	validateEggFlow(old, next, rules, waves, hatched, budget, flag)
	for id, o := range old.StarterData {
		if _, ok := next.StarterData[id]; !ok && (o.CandyCount != 0 || o.Friendship != 0 || o.ValueReduction != 0 || o.PassiveAttr != 0 || o.EggMoves != 0) {
			flag("starter-entry-removed")
		}
	}
	friendshipPoints, dreamMoves := 0, 0
	for id, n := range next.StarterData {
		o := old.StarterData[id]
		r, ok := rules[id]
		if !ok {
			if n.CandyCount != 0 || n.Friendship != 0 || n.ValueReduction != 0 || n.PassiveAttr != 0 || n.EggMoves != 0 {
				flag("unknown-starter-enhancement")
			}
			continue
		}
		cost := r.Cost - 1
		if cost < 0 || cost >= 10 {
			continue
		}
		prices := candyRules(r)
		if n.CandyCount < 0 || n.CandyCount > 9999 || n.Friendship < 0 || n.Friendship >= prices.FriendshipCap || n.ValueReduction < o.ValueReduction || n.ValueReduction > 2 || n.ValueReduction < 0 || n.PassiveAttr&^3 != 0 || (n.PassiveAttr&2 != 0 && n.PassiveAttr&1 == 0) {
			flag("starter-value-range")
			continue
		}
		spent := 0
		for i := o.ValueReduction; i < n.ValueReduction; i++ {
			if i >= 0 && i < 2 {
				spent += prices.Reduction[i]
			}
		}
		if o.PassiveAttr&1 == 0 && n.PassiveAttr&1 != 0 {
			spent += prices.Passive
		}
		if o.PassiveAttr&1 != 0 && n.PassiveAttr&1 == 0 {
			flag("passive-removed")
		}
		ei := 0
		for _, threshold := range prices.EggThresholds {
			if next.DexData[id].HatchedCount >= threshold {
				ei++
			}
		}
		spent += newEggs[id] * prices.EggCosts[ei]
		gains := max(0, next.DexData[id].CaughtCount-old.DexData[id].CaughtCount) + max(0, next.DexData[id].HatchedCount-old.DexData[id].HatchedCount)
		if r.Root != 0 && r.Root != id {
			gains = 0
		} // Candy is awarded only at the final prevolution.
		earned := n.CandyCount - o.CandyCount + spent
		if earned < 0 {
			flag("candy-purchase-mismatch")
		}
		if spent > 0 && waves == 0 && gains == 0 && earned > max(0, 4-budget.CandyCarry[id]) {
			flag("candy-purchase-mismatch")
		}
		if waves == 0 {
			budget.CandyCarry[id] += max(0, earned-gains*(40+prices.CatchBonus))
		}
		// Capture/hatch income is at most epic shiny (20) times egg/boss (2),
		// plus the exported event bonus. Convert the remaining income to actual
		// species-specific friendship points, including the saved remainder.
		friendshipCandies := max(0, earned-gains*(40+prices.CatchBonus))
		if n.Friendship < o.Friendship {
			friendshipCandies = max(friendshipCandies, 1)
		}
		points := max(0, friendshipCandies*prices.FriendshipCap+n.Friendship-o.Friendship)
		// Weird Dream grants at most one direct candy per changed party member.
		if points > budget.Friendship && earned > 0 && budget.Dreams > 0 {
			points = max(0, points-prices.FriendshipCap)
			budget.Dreams--
		}
		friendshipPoints += points
		validEggMoves := 0
		for slot, move := range r.EggMoves {
			if move > 0 {
				validEggMoves |= 1 << slot
			}
		}
		if n.EggMoves < 0 || n.EggMoves&^validEggMoves != 0 || n.EggMoves&o.EggMoves != o.EggMoves {
			flag("egg-move-range")
		}
		unlocked := n.EggMoves &^ o.EggMoves
		dh := next.DexData[id].HatchedCount - old.DexData[id].HatchedCount
		dreamDelta := max(0, bits.OnesCount(uint(unlocked))-max(0, dh))
		if dreamDelta > 1 {
			flag("egg-move-hatch-bound")
		}
		dreamMoves += dreamDelta
	}
	if !debit(&budget.Friendship, friendshipPoints) {
		flag("candy-income-bound")
	}
	if !debit(&budget.Dreams, dreamMoves) {
		flag("egg-move-hatch-bound")
	}
	out := []string{}
	for f := range reasons {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
func digest(prev, payload string) string {
	h := sha256.Sum256([]byte(prev + "\n" + payload))
	return hex.EncodeToString(h[:])
}

// Hashes cover the transmitted bytes. Decode only after checking the chain,
// and cap expanded data so a compressed registration cannot exhaust memory.
func DecodeLog(payload string) ([]byte, error) {
	if !strings.HasPrefix(payload, "gz:") {
		if len(payload) > MaxAuditWaveBytes {
			return nil, fmt.Errorf("로그 크기 초과")
		}
		return []byte(payload), nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(payload, "gz:"))
	if err != nil {
		return nil, fmt.Errorf("로그 압축 인코딩 오류")
	}
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("로그 압축 형식 오류")
	}
	defer z.Close()
	out, err := io.ReadAll(io.LimitReader(z, MaxAuditWaveBytes+1))
	if err != nil || len(out) > MaxAuditWaveBytes {
		return nil, fmt.Errorf("로그 압축 해제 크기 초과/오류")
	}
	return out, nil
}
func ValidateLogs(r *Run, logs []Log) error {
	if r.Candidate == nil || len(logs) != 200 {
		return fmt.Errorf("클리어 또는 웨이브 기록 없음")
	}
	if len(r.Anchors) < 2 || r.Anchors[0].Wave > 6 || r.Anchors[len(r.Anchors)-1].Wave < 196 {
		return fmt.Errorf("서버 앵커가 부족합니다")
	}
	heads := map[int]string{}
	known := map[int]bool{}
	previousParty := map[int]Object{}
	head := ""
	ledger := map[string]int{}
	previousTimestamp := int64(0)
	for i, l := range logs {
		if l.Wave != i+1 || len(l.Payload) > 65536 {
			return fmt.Errorf("웨이브 순서/크기 오류")
		}
		if digest(head, l.Payload) != l.Head {
			return fmt.Errorf("해시체인 불일치: %d", l.Wave)
		}
		head = l.Head
		heads[l.Wave] = head
		var frame struct {
			Wave       int      `json:"wave"`
			Party      []Object `json:"party"`
			EnemyParty []Object `json:"enemyParty"`
			Money      int      `json:"money"`
			Generation struct {
				Basis defs.SessionSaveData `json:"basis"`
			} `json:"generation"`
			Generated struct {
				BattleType           int `json:"battleType"`
				MysteryEncounterType int `json:"mysteryEncounterType"`
			} `json:"generated"`
			Captures []struct {
				Pokemon Object `json:"pokemon"`
				Source  string `json:"source"`
			} `json:"captures"`
			Items []struct {
				Kind   string `json:"kind"`
				Data   Object `json:"data"`
				Source string `json:"source"`
			} `json:"items"`
		}
		raw, e := DecodeLog(l.Payload)
		if e != nil {
			return e
		}
		if e := json.Unmarshal(raw, &frame); e != nil {
			return e
		}
		if frame.Wave != l.Wave || len(frame.Party) > 6 || len(frame.EnemyParty) > 6 || frame.Money < 0 || frame.Money > MaxAuditMoney {
			return fmt.Errorf("웨이브 상태 상한 초과")
		}
		timestamp := int64(frame.Generation.Basis.Timestamp)
		if err := validateGenerationTime(r, l.Wave, timestamp, previousTimestamp); err != nil {
			return err
		}
		previousTimestamp = timestamp
		if i == 0 {
			if err := validateInitialParty(r.Starters, frame.Generation.Basis.Party); err != nil {
				return err
			}
			for _, p := range frame.Generation.Basis.Party {
				obj := asObject(p)
				id := int(num(obj["id"]))
				known[id], previousParty[id] = true, obj
			}
		}
		registerDerivedParty(known, previousParty, frame.Party)
		partyIDs := map[int]bool{}
		for _, p := range frame.Party {
			id := int(num(p["id"]))
			if partyIDs[id] {
				return fmt.Errorf("중복 파티 개체 ID")
			}
			partyIDs[id] = true
			if !known[int(num(p["id"]))] {
				return fmt.Errorf("포획 출처가 없는 개체")
			}
		}
		for _, p := range frame.Party {
			previousParty[int(num(p["id"]))] = p
		}
		if len(frame.Captures) > 32 {
			return fmt.Errorf("웨이브 포획 기록 상한 초과")
		}
		for _, capture := range frame.Captures {
			if err := validateCapture(capture.Pokemon, capture.Source, frame.EnemyParty, frame.Generated.BattleType, frame.Generated.MysteryEncounterType); err != nil {
				return err
			}
			known[int(num(capture.Pokemon["id"]))] = true
			previousParty[int(num(capture.Pokemon["id"]))] = capture.Pokemon
		}
		if len(frame.Items) > 4096 {
			return fmt.Errorf("아이템 이벤트 수 상한 초과")
		}
		for _, item := range frame.Items {
			if item.Source == "" {
				return fmt.Errorf("아이템 출처 없음")
			}
			if item.Kind == "state" || item.Kind == "battle-end" || item.Kind == "shop" {
				continue // The pinned engine verifies ordered state deltas and lifetimes.
			}
			if (item.Kind != "" && item.Kind != "acquire") || item.Data == nil {
				return fmt.Errorf("아이템 이벤트 형식 오류")
			}
			ledger[itemKey(item.Data)] += max(1, int(num(item.Data["stackCount"])))
		}
	}
	for _, a := range r.Anchors {
		if heads[a.Wave] != a.Head {
			return fmt.Errorf("서버 앵커 불일치")
		}
		if r.Created > 0 && a.At > 0 && !waveArrivalAllowed(r.Created, a.At, a.Wave) {
			return fmt.Errorf("웨이브 도착 시간 상한 초과")
		}
	}
	if len(r.Candidate.Party) < 1 || len(r.Candidate.Party) > 6 || r.Candidate.Money < 0 || r.Candidate.Money > MaxAuditMoney {
		return fmt.Errorf("최종 파티 상한 오류")
	}
	finalIDs := map[int]bool{}
	finalParty := []Object{}
	for _, p := range r.Candidate.Party {
		finalParty = append(finalParty, asObject(p))
	}
	registerDerivedParty(known, previousParty, finalParty)
	for _, p := range r.Candidate.Party {
		m := asObject(p)
		id := int(num(m["id"]))
		if finalIDs[id] || !known[id] || num(m["level"]) < 1 || num(m["level"]) > MaxAuditLevel {
			return fmt.Errorf("최종 파티 출처/레벨 오류")
		}
		finalIDs[id] = true
	}
	if len(r.Candidate.Modifiers) > MaxAuditItems {
		return fmt.Errorf("아이템 수 상한 초과")
	}
	finalLedger := map[string]int{}
	for _, m := range r.Candidate.Modifiers {
		obj := asObject(m)
		count := int(num(obj["stackCount"]))
		key := itemKey(obj)
		finalLedger[key] += count
		budget := ledger[key]
		if obj["typeId"] == "MYSTERY_ENCOUNTER_MACHO_BRACE" {
			budget *= 50 // Native per-defeat growth is checked by the engine ledger.
		}
		if count < 1 || count > 999 || budget < finalLedger[key] {
			return fmt.Errorf("아이템 장부 불일치")
		}
	}
	return nil
}

func validateGenerationTime(r *Run, wave int, timestamp, previous int64) error {
	// Event bonuses and forms depend on the replay clock. The client may have
	// a modest clock skew, but cannot select a date outside the server-observed
	// run or move a committed wave beyond its first covering anchor's arrival.
	const skew = int64(300000)
	if r.Created <= 0 || r.ClearedAt < r.Created || timestamp <= 0 {
		return fmt.Errorf("서버 생성/클리어 시각이 없는 런입니다")
	}
	latest := r.ClearedAt * 1000
	for _, a := range r.Anchors {
		if a.At < r.Created {
			return fmt.Errorf("서버 앵커 시각 오류")
		}
		if a.Wave >= wave {
			latest = min(latest, a.At*1000)
		}
	}
	if timestamp < r.Created*1000-skew || timestamp > latest+skew || (previous > 0 && timestamp < previous-skew) {
		return fmt.Errorf("웨이브 %d: 생성 시각이 서버 관측 범위를 벗어났습니다", wave)
	}
	return nil
}
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}
func asObject(v any) Object {
	raw, _ := json.Marshal(v)
	var m Object
	_ = json.Unmarshal(raw, &m)
	return m
}
func itemKey(m Object) string {
	// The bridge is a coarse total check. Owner transfers and duration changes
	// alter native saved args; their exact semantics require the engine ledger.
	raw, _ := json.Marshal([]any{m["className"], m["typeId"], m["typePregenArgs"]})
	return string(raw)
}

// Callers supply a current-policy certified party or the server's own sample.
// Preserve earned permanent types and abilities. The current-policy engine
// verifies their sources before publication. 319 is the pinned AbilityId limit.
func normalizeCustomData(raw any, mint bool) Object {
	custom := asObject(raw)
	out := Object{}
	if ability, ok := custom["ability"].(float64); ok && ability >= 0 && ability <= 319 && ability == float64(int(ability)) {
		out["ability"] = ability
	}
	if nature, ok := custom["nature"].(float64); mint && ok && nature >= 0 && nature <= 24 && nature == float64(int(nature)) {
		out["nature"] = nature
	}
	if types, ok := custom["types"].([]any); ok && len(types) > 0 && len(types) <= 2 {
		valid, changed := true, false
		for _, value := range types {
			if value == nil {
				continue
			}
			n, ok := value.(float64)
			if !ok || n < 0 || n > 17 || n != float64(int(n)) {
				valid = false
				break
			}
			changed = true
		}
		if valid && changed {
			out["types"] = append([]any(nil), types...)
		}
	}
	return out
}
func NormalizeParty(party []defs.PokemonData, player bool) []defs.PokemonData {
	out := []defs.PokemonData{}
	for _, p := range party {
		m := asObject(p)
		if m == nil {
			continue
		}
		m["player"] = player
		m["ivs"] = []int{31, 31, 31, 31, 31, 31}
		m["stats"] = []int{1, 1, 1, 1, 1, 1}
		m["hp"] = 1
		m["boss"] = false
		m["bossSegments"] = 0
		m["status"] = nil
		m["summonData"] = Object{}
		m["battleData"] = Object{}
		m["customPokemonData"] = normalizeCustomData(m["customPokemonData"], true)
		fusionCustom := normalizeCustomData(m["fusionCustomPokemonData"], false)
		m["fusionCustomPokemonData"] = Object{}
		if num(m["fusionSpecies"]) > 0 {
			m["fusionCustomPokemonData"] = fusionCustom
		}
		m["isTerastallized"] = false
		m["stellarTypesBoosted"] = []int{}
		normalMoves := []Object{}
		if moves, ok := m["moveset"].([]any); ok {
			for _, v := range moves {
				mv := asObject(v)
				if mv == nil {
					continue
				}
				delete(mv, "maxPpOverride")
				mv["ppUsed"] = 0
				normalMoves = append(normalMoves, mv)
			}
		}
		m["moveset"] = normalMoves
		out = append(out, m)
	}
	return out
}
func SyntheticSession(own, enemy defs.SessionSaveData, seed string) Object {
	playerParty, playerMods := matchSide(own, true, 1)
	enemyParty, enemyMods := matchSide(enemy, false, 101)
	return asObject(Object{"seed": seed, "gameMode": 0, "gameVersion": Version, "party": playerParty, "enemyParty": enemyParty, "modifiers": playerMods, "enemyModifiers": enemyMods, "arena": Object{"biome": 1, "tags": []any{}, "positionalTags": []any{}}, "pokeballCounts": Object{}, "money": 0, "score": 0, "waveIndex": 100, "battleType": 1, "trainer": Object{"trainerType": 1, "variant": 0}, "challenges": []any{}, "mysteryEncounterType": -1, "mysteryEncounterSaveData": Object{}, "timestamp": time.Now().UnixMilli(), "playerFaints": 0})
}

// Temporary battle bonuses never become permanent properties of a registered team.
func NormalizeModifiers(modifiers []defs.PersistentModifierData) []defs.PersistentModifierData {
	out := []defs.PersistentModifierData{}
	for _, raw := range modifiers {
		m := asObject(raw)
		switch m["className"] {
		case "TempStatStageBoosterModifier", "TempCritBoosterModifier", "DoubleBattleChanceBoosterModifier", "TempExtraModifierModifier":
			continue
		}
		if m["typeId"] == "TEMP_STAT_STAGE_BOOSTER" || m["typeId"] == "DIRE_HIT" {
			continue
		}
		out = append(out, m)
	}
	return out
}

//go:embed held-modifiers.json
var heldModifierJSON []byte

var heldModifierClasses = func() map[string]bool {
	var names []string
	if err := json.Unmarshal(heldModifierJSON, &names); err != nil {
		panic(err)
	}
	classes := map[string]bool{}
	for _, name := range names {
		classes[name] = true
	}
	return classes
}()

func matchSide(session defs.SessionSaveData, player bool, start int) ([]defs.PokemonData, []defs.PersistentModifierData) {
	party := NormalizeParty(session.Party, player)
	ids := map[int]int{}
	for i, p := range party {
		m := p.(Object)
		ids[int(num(m["id"]))] = start + i
		m["id"] = start + i
	}
	mods := []defs.PersistentModifierData{}
	for _, v := range NormalizeModifiers(session.Modifiers) {
		m := asObject(v)
		m["player"] = player
		className, _ := m["className"].(string)
		if args, ok := m["args"].([]any); ok && len(args) > 0 && heldModifierClasses[className] {
			if id, exists := ids[int(num(args[0]))]; exists {
				args[0] = id
			}
		}
		mods = append(mods, m)
	}
	return party, mods
}
func PracticeTeam() Team {
	party := []defs.PokemonData{}
	for i, species := range []int{3, 6, 9} {
		moves := [][]int{{75, 188, 235, 36}, {53, 403, 163, 108}, {57, 58, 44, 182}}[i]
		ms := []Object{}
		for _, m := range moves {
			ms = append(ms, Object{"moveId": m, "ppUsed": 0, "ppUp": 0})
		}
		party = append(party, Object{"id": 1001 + i, "species": species, "formIndex": 0, "abilityIndex": 0, "level": 100, "nature": 0, "gender": 0, "moveset": ms, "metBiome": -1, "metSpecies": species, "metWave": -1, "friendship": 255, "teraType": 0, "variant": 0})
	}
	return Team{Session: defs.SessionSaveData{Party: party, Modifiers: []defs.PersistentModifierData{}}}
}
