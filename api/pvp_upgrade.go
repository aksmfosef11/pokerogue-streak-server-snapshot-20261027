// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import (
	"errors"
	"fmt"
	"github.com/pagefaultgames/rogueserver/api/savedata"
	"github.com/pagefaultgames/rogueserver/db"
	"github.com/pagefaultgames/rogueserver/defs"
)

// Called under the account save lock, before either half of updateall is stored.
func validateCampaignUpgrade(id []byte, old, next defs.SystemSaveData) error {
	// Registered PvP clients retain the normal downgrade protection even in the
	// local beta backend. A stale old tab must never overwrite an upgraded save.
	if streakRegistry != nil && old.GameVersion != "" {
		_, knownOld := streakRegistry.Lookup(old.GameVersion)
		_, knownNew := streakRegistry.Lookup(next.GameVersion)
		if knownOld && knownNew {
			if err := validateSystemVersion(next, old); err != nil {
				return err
			}
		}
	}
	if next.GameVersion != "1.12.2.0" || old.GameVersion == next.GameVersion {
		return nil
	}
	for slot := 0; slot < defs.SessionSlotCount; slot++ {
		session, err := savedata.GetSession(db.Store, id, slot)
		if errors.Is(err, savedata.ErrSaveNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if session.GameVersion != next.GameVersion {
			return fmt.Errorf("진행 중인 구버전 런을 원래 버전에서 완료한 뒤 업데이트하세요 (슬롯 %d)", slot+1)
		}
	}
	if streakRegistry != nil {
		for _, version := range streakRegistry.Versions() {
			source, _ := streakRegistry.Lookup(version)
			active, err := source.HasLegacyBattle(id, next.GameVersion)
			if err != nil {
				return err
			}
			if active {
				return fmt.Errorf("진행 중인 구버전 대전을 원래 엔진으로 완료한 뒤 업데이트하세요")
			}
		}
	}
	return nil
}
