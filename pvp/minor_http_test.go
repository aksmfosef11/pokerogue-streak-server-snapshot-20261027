// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"os"
	"testing"
)

// Controlled approved snapshots for the isolated HTTP/browser compatibility test.
// Never run against the ordinary game DB; the caller supplies a staging network.
func TestMinorHTTPFixture(t *testing.T) {
	a, b := os.Getenv("PVP_HTTP_ACCOUNT_A"), os.Getenv("PVP_HTTP_ACCOUNT_B")
	if a == "" || b == "" {
		t.Skip("set staging HTTP test accounts")
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
	if _, err = database.Exec(`UPDATE accounts SET banned=1 WHERE username LIKE 'minor_%' AND username NOT IN (?,?)`, a, b); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, version string }{{a, "1.12.1.3"}, {b, "1.12.1.2"}} {
		var id []byte
		if err = database.QueryRow(`SELECT uuid FROM accounts WHERE username=?`, item.name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		source, ok := registry.Lookup(item.version)
		if !ok {
			t.Fatal("fixture source missing")
		}
		team := PracticeTeam()
		team.RunID = "http-compat-" + randomID()
		team.AuditPolicy = source.Policy()
		team.Session.GameVersion = source.GameVersion()
		if _, err = database.Exec(`INSERT INTO `+source.teamsTable()+`(uuid,version,data) VALUES(?,?,?) ON DUPLICATE KEY UPDATE data=VALUES(data)`, id, source.GameVersion(), mustJSON(team)); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("PVP_HTTP_RESET_MATCHES")=="true" {if err=source.update(id,func(state *State)error{state.Match=nil;state.StreakAttempt=nil;state.Streak=0;return nil});err!=nil{t.Fatal(err)}}
	}
}
