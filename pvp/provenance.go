// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"fmt"
	"reflect"
)

// Shedinja receives a new ID without going through the capture hook. This only
// opens the cheap structural gate; the mandatory engine party audit separately
// proves the evolution, clone attributes, moves and single-use parent.
func registerDerivedParty(known map[int]bool, previous map[int]Object, party []Object) {
	for _, clone := range party {
		id := int(num(clone["id"]))
		if known[id] || num(clone["level"]) < 20 {
			continue
		}
		for _, parent := range party {
			parentID := int(num(parent["id"]))
			before := previous[parentID]
			if !known[parentID] || before == nil {
				continue
			}
			base := num(before["species"]) == 290 && num(parent["species"]) == 291 && num(clone["species"]) == 292
			fusion := num(before["fusionSpecies"]) == 290 && num(parent["fusionSpecies"]) == 291 && num(clone["fusionSpecies"]) == 292 && num(clone["species"]) == num(before["species"])
			if base || fusion {
				known[id] = true
				break
			}
		}
	}
}

// Encounter summaries are independently compared with real generation by the
// worker. Merely seeing an enemy never authorizes a different party member.
func validateCapture(pokemon Object, source string, enemies []Object, battleType, encounter int) error {
	if source != "AttemptCapturePhase" {
		// These sources have a seed-reproduced gift individual in the mandatory
		// engine audit. The ordinary enemy-party precheck cannot see that offer.
		if source == "MysteryEncounterOptionSelectedPhase" && (encounter == 13 || encounter == 16 || encounter == 22 || encounter == 23 || encounter == 28 || encounter == 29) {
			return nil
		}
		return fmt.Errorf("미지원 특수 포획 출처: %s", source)
	}
	if battleType == 1 {
		return fmt.Errorf("트레이너 파티 포획 불가")
	}
	for _, enemy := range enemies {
		if num(enemy["id"]) != num(pokemon["id"]) {
			continue
		}
		// Form changes can occur during combat. IVs are deliberately outside
		// qualification and are normalized for both sides in the match session.
		for _, key := range []string{"species", "level", "abilityIndex", "nature", "shiny", "variant", "fusionSpecies"} {
			if !reflect.DeepEqual(enemy[key], pokemon[key]) {
				return fmt.Errorf("포획 개체 속성이 생성 결과와 다릅니다: %s", key)
			}
		}
		return nil
	}
	return fmt.Errorf("현재 웨이브 적 파티에 없는 포획 개체")
}
