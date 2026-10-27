// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

// Explicit QA-only fixture: emulate a match created before the battle pool
// moved to 1.12.2. The party still comes from the real registration auditor.
func TestPrepareLegacyRolloutMatch(t *testing.T) {
	name := os.Getenv("PVP_LEGACY_ROLLOUT_QA")
	if name == "" {
		t.Skip("explicit controlled rollout fixture only")
	}
	if !strings.HasPrefix(name, "qa11213") {
		t.Fatal("QA account required")
	}
	database, err := sql.Open("mysql", os.Getenv("PVP_INTEGRATION_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	registry, err := OpenRegistry(database)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	source, _ := registry.Lookup("1.12.1.3")
	profile := *source.profile
	profile.BattleVersion = profile.Version
	legacy := &Service{DB: source.DB, Secret: source.Secret, Rules: source.Rules, Manifest: source.Manifest, profile: &profile}
	var id []byte
	if err = database.QueryRow("SELECT uuid FROM accounts WHERE username=?", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	match, err := legacy.StartMatch(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if match.Session["gameVersion"] != "1.12.1.3" || match.OpponentVersion != "1.12.1.3" {
		t.Fatal("not a legacy native match")
	}
	t.Log("Prepared audited legacy match", match.ID, match.Session["gameVersion"])
}
