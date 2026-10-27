// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"fmt"
	"github.com/pagefaultgames/rogueserver/defs"
	"slices"
)

func validateStarterMoves(p Starter, rule Rule, trusted TrustedStarter) error {
	base, ok := rule.StarterMoves[p.FormIndex]
	if !ok || len(p.Moves) < 1 || len(p.Moves) > 4 {
		return fmt.Errorf("스타터 폼/기술 수가 올바르지 않습니다")
	}
	allowed := map[int]bool{}
	for _, move := range base {
		if move > 0 {
			allowed[move] = true
		}
	}
	for slot, move := range rule.EggMoves {
		if move > 0 && trusted.EggMoves&(1<<slot) != 0 {
			allowed[move] = true
		}
	}
	seen := map[int]bool{}
	for _, move := range p.Moves {
		if !allowed[move] || seen[move] {
			return fmt.Errorf("미인증 스타터 기술: %d", move)
		}
		seen[move] = true
	}
	return nil
}

func validateInitialParty(starters []Starter, party []defs.PokemonData) error {
	if len(starters) == 0 || len(starters) != len(party) {
		return fmt.Errorf("최초 파티가 서버 시드 발급 시 선택과 다릅니다")
	}
	ids := map[int]bool{}
	for i, data := range party {
		p := asObject(data)
		chosen := starters[i]
		id := int(num(p["id"]))
		passive, _ := p["passive"].(bool)
		if id <= 0 || ids[id] || int(num(p["species"])) != chosen.Species || int(num(p["formIndex"])) != chosen.FormIndex || num(p["level"]) != 5 || passive != chosen.Passive || num(p["fusionSpecies"]) > 0 {
			return fmt.Errorf("최초 스타터 개체/레벨/폼/패시브 불일치: %d", i)
		}
		ids[id] = true
		moves := []int{}
		if set, ok := p["moveset"].([]any); ok {
			for _, move := range set {
				moves = append(moves, int(num(asObject(move)["moveId"])))
			}
		}
		if !slices.Equal(moves, chosen.Moves) {
			return fmt.Errorf("최초 스타터 기술이 서버 선택과 다릅니다: %d", i)
		}
	}
	return nil
}
