// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import "testing"

func TestMysteryGiftStructuralGateRequiresSpecificSource(t *testing.T) {
	for _, encounter := range []int{-1, 0, 13, 16, 22, 23, 28, 29, 999} {
		err := validateCapture(Object{"id": 1}, "MysteryEncounterOptionSelectedPhase", nil, 2, encounter)
		allowed := encounter == 13 || encounter == 16 || encounter == 22 || encounter == 23 || encounter == 28 || encounter == 29
		if (err == nil) != allowed {
			t.Fatalf("gift source policy mismatch for %d: %v", encounter, err)
		}
		if validateCapture(Object{"id": 1}, "AttemptCapturePhase", nil, 2, encounter) == nil {
			t.Fatal("gift source allowed an ungenerated ordinary capture")
		}
	}
}

func TestDerivedShedinjaPassesStructuralGate(t *testing.T) {
	for _, fusion := range []bool{false, true} {
		known := map[int]bool{1: true}
		before := Object{"id": 1, "species": 290}
		parent := Object{"id": 1, "species": 291, "level": 20}
		clone := Object{"id": 2, "species": 292, "level": 20}
		if fusion {
			before["species"], before["fusionSpecies"] = 1, 290
			parent["species"], parent["fusionSpecies"] = 1, 291
			clone["species"], clone["fusionSpecies"] = 1, 292
		}
		registerDerivedParty(known, map[int]Object{1: before}, []Object{parent, clone})
		if !known[2] {
			t.Fatal("derived Shedinja never reached the mandatory engine audit")
		}
		delete(known, 2)
		clone["level"] = 19
		registerDerivedParty(known, map[int]Object{1: before}, []Object{parent, clone})
		if known[2] {
			t.Fatal("too-early Shedinja accepted")
		}
	}
}

func TestCaptureRequiresGeneratedIdentityAndAttributes(t *testing.T) {
	enemy := Object{"id": 123, "species": 1, "level": 5, "abilityIndex": 0, "nature": 3, "shiny": false, "variant": 0}
	capture := Object{}
	for key, value := range enemy {
		capture[key] = value
	}
	if err := validateCapture(capture, "AttemptCapturePhase", []Object{enemy}, 0, -1); err != nil {
		t.Fatal(err)
	}
	for key, value := range (Object{"id": 999, "species": 150, "level": 200, "abilityIndex": 2, "nature": 4, "shiny": true, "variant": 2, "fusionSpecies": 384}) {
		old := capture[key]
		capture[key] = value
		if err := validateCapture(capture, "AttemptCapturePhase", []Object{enemy}, 0, -1); err == nil {
			t.Fatalf("forged capture %s accepted", key)
		}
		capture[key] = old
	}
	if err := validateCapture(capture, "AttemptCapturePhase", []Object{enemy}, 1, -1); err == nil {
		t.Fatal("trainer capture accepted")
	}
	if err := validateCapture(capture, "UnknownPhase", []Object{enemy}, 0, -1); err == nil {
		t.Fatal("unknown source accepted")
	}
}
