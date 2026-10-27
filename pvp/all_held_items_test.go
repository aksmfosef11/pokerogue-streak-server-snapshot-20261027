// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/pagefaultgames/rogueserver/defs"
)

func TestAllHeldItemClassesPreserveArguments(t *testing.T) {
	for class := range heldModifierClasses {
		t.Run(class, func(t *testing.T) {
			s := PracticeTeam().Session
			for owner := 0; owner < len(s.Party); owner++ {
				s.Modifiers = []defs.PersistentModifierData{Object{
					"className": class, "typeId": "CONTRACT_TEST", "stackCount": 3,
					"args": []any{1001 + owner, []int{1, 3}, 1.5, false}, "typePregenArgs": []any{2, "variant"},
				}}
				before := string(mustJSON(s))
				for _, side := range []struct {
					player bool
					start  int
				}{{true, 1}, {false, 101}} {
					_, mods := matchSide(s, side.player, side.start)
					got := asObject(mods[0])
					want := asObject(s.Modifiers[0])
					want["player"] = side.player
					want["args"].([]any)[0] = float64(side.start + owner)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("item payload changed: got %v, want %v", got, want)
					}
				}
				if string(mustJSON(s)) != before {
					t.Fatal("source save mutated")
				}
			}
		})
	}
}

// The native game exports every real item factory/variant; the native loader then
// consumes these actual SyntheticSession results. Opt in for the cross-runtime audit.
func TestNativeItemCatalogRoundTrip(t *testing.T) {
	path := os.Getenv("PVP_ALL_ITEM_CATALOG")
	if path == "" {
		t.Skip("set PVP_ALL_ITEM_CATALOG to the native catalog export")
	}
	var catalog struct {
		Cases []struct {
			Key       string               `json:"key"`
			Held      bool                 `json:"held"`
			Temporary bool                 `json:"temporary"`
			Owner     int                  `json:"owner"`
			Source    defs.SessionSaveData `json:"source"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Cases) < 200 {
		t.Fatal("incomplete native catalog")
	}
	rows := []Object{}
	for _, item := range catalog.Cases {
		t.Run(item.Key, func(t *testing.T) {
			before := string(mustJSON(item.Source))
			registered := item.Source
			registered.Modifiers = NormalizeModifiers(registered.Modifiers)
			session := SyntheticSession(registered, registered, "all-held-items")
			for _, side := range []struct {
				key    string
				player bool
				start  int
			}{{"modifiers", true, 1}, {"enemyModifiers", false, 101}} {
				mods := session[side.key].([]any)
				if item.Temporary {
					if len(mods) != 0 {
						t.Fatal("temporary item was saved")
					}
					continue
				}
				if len(mods) != 1 {
					t.Fatal("permanent item was lost")
				}
				want := asObject(item.Source.Modifiers[0])
				want["player"] = side.player
				if item.Held {
					if !heldModifierClasses[want["className"].(string)] {
						t.Fatal("missing held-item owner contract")
					}
					want["args"].([]any)[0] = float64(side.start + item.Owner)
				}
				if !reflect.DeepEqual(asObject(mods[0]), want) {
					t.Fatalf("native item changed: got %v, want %v", mods[0], want)
				}
			}
			if string(mustJSON(item.Source)) != before {
				t.Fatal("ordinary source save mutated")
			}
			rows = append(rows, Object{"item": item, "session": session})
		})
	}
	if path := os.Getenv("PVP_ALL_ITEM_ROUNDTRIP"); path != "" {
		if err := os.WriteFile(path, append(mustJSON(rows), '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
