package api

import "testing"

func TestBattleOnlySourceRoutes(t *testing.T) {
	for _, path := range []string{"/pvp/status", "/pvp/matches", "/pvp/commit", "/pvp/finish", "/pvp/register", "/pvp/leaderboard", "/pvp/leaderboard/team", "/pvp/team", "/pvp/end-streak"} {
		if !battleSourceRouteAllowed(path) {
			t.Fatal("battle route rejected", path)
		}
	}
	for _, path := range []string{"/pvp/runs", "/pvp/anchors", "/pvp/egg-tickets", "/pvp/upgrade-status", "/savedata/system/update", "/savedata/updateall"} {
		if battleSourceRouteAllowed(path) {
			t.Fatal("campaign route allowed in battle-only source mode", path)
		}
	}
}
