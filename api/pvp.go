// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pagefaultgames/rogueserver/api/savedata"
	"github.com/pagefaultgames/rogueserver/db"
	"github.com/pagefaultgames/rogueserver/defs"
	"github.com/pagefaultgames/rogueserver/pvp"
	"net/http"
	"strconv"
)

var streakService *pvp.Service
var streakRegistry *pvp.Registry

func initPVP(mux *http.ServeMux) error {
	var e error
	streakRegistry, e = pvp.NewRegistry(db.PVPDatabase())
	if e != nil {
		return e
	}
	streakService = streakRegistry.Default()
	mux.HandleFunc("GET /pvp/status", handlePVP)
	mux.HandleFunc("GET /pvp/upgrade-status", handlePVP)
	mux.HandleFunc("GET /pvp/leaderboard", handlePVP)
	mux.HandleFunc("GET /pvp/leaderboard/team", handlePVP)
	mux.HandleFunc("GET /pvp/team", handlePVP)
	mux.HandleFunc("GET /pvp/teams", handlePVP)
	mux.HandleFunc("GET /pvp/attempt/team", handlePVP)
	for _, path := range []string{"runs", "anchors", "register", "matches", "commit", "finish", "egg-tickets", "end-streak"} {
		mux.HandleFunc("POST /pvp/"+path, handlePVP)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, map[string]any{"ok": true, "gameVersion": streakService.GameVersion(), "gameVersions": streakRegistry.Versions()})
	})
	return nil
}
func handlePVP(w http.ResponseWriter, r *http.Request) {
	id, e := uuidFromRequest(r)
	if e != nil {
		httpError(w, r, e, http.StatusUnauthorized)
		return
	}
	var req struct {
		Version  string           `json:"version"`
		Contract *pvp.RunContract `json:"contract"`
		Starters []pvp.Starter    `json:"starters"`
		RunID    string           `json:"runId"`
		Wave     int              `json:"wave"`
		Head     string           `json:"head"`
		Logs     []pvp.Log        `json:"logs"`
		Practice bool             `json:"practice"`
		TeamID   string           `json:"teamId"`
		MatchID  string           `json:"matchId"`
		pvp.Decision
		Result   string `json:"result"`
		PlayerHP []int  `json:"playerHP"`
		EnemyHP  []int  `json:"enemyHP"`
	}
	if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, pvp.MaxAuditRequestBytes)
		if e = json.NewDecoder(r.Body).Decode(&req); e != nil {
			httpError(w, r, fmt.Errorf("invalid request: %w", e), http.StatusBadRequest)
			return
		}
	}
	version := r.Header.Get("PKR-Client-Version")
	if version != "" && req.Version != "" && version != req.Version {
		httpError(w, r, fmt.Errorf("게임 버전 요청이 일치하지 않습니다"), http.StatusConflict)
		return
	}
	if version == "" {
		version = req.Version
	}
	service, ok := streakRegistry.Lookup(version)
	if !ok {
		httpError(w, r, fmt.Errorf("지원하지 않는 게임 버전입니다"), http.StatusConflict)
		return
	}
	if source := r.Header.Get("PKR-PVP-Source-Version"); source != "" {
		engine := service
		if !battleSourceRouteAllowed(r.URL.Path) {
			httpError(w, r, fmt.Errorf("대전 전용 화면에서는 일반 진행을 변경할 수 없습니다"), http.StatusConflict)
			return
		}
		service, ok = streakRegistry.Lookup(source)
		if !ok {
			httpError(w, r, fmt.Errorf("지원하지 않는 파티 출처 버전입니다"), http.StatusConflict)
			return
		}
		if service.BattleVersion() != engine.BattleVersion() || r.URL.Path == "/pvp/matches" && version != service.BattleVersion() {
			httpError(w, r, fmt.Errorf("호환되는 공통 전투 버전으로 접속하세요"), http.StatusConflict)
			return
		}
	}
	if r.URL.Path == "/pvp/commit" || r.URL.Path == "/pvp/finish" && req.Result != "forfeit" {
		if e = service.ValidateBattleEngine(id, version); e != nil {
			httpError(w, r, e, http.StatusConflict)
			return
		}
	}
	var out any = map[string]bool{"ok": true}
	switch r.URL.Path {
	case "/pvp/upgrade-status":
		blocked := []map[string]any{}
		for slot := 0; slot < defs.SessionSlotCount; slot++ {
			session, err := savedata.GetSession(db.Store, id, slot)
			if errors.Is(err, savedata.ErrSaveNotExist) {
				continue
			}
			if err != nil {
				e = err
				break
			}
			if session.GameVersion != version {
				path := "/"
				if source, ok := streakRegistry.Lookup(session.GameVersion); ok {
					path = source.ClientPath()
				}
				blocked = append(blocked, map[string]any{"slot": slot, "version": session.GameVersion, "path": path})
			}
		}
		out = map[string]any{"blocked": blocked, "ready": len(blocked) == 0}
		if e == nil {
			for _, sourceVersion := range streakRegistry.Versions() {
				source, _ := streakRegistry.Lookup(sourceVersion)
				active, err := source.HasLegacyBattle(id, version)
				if err != nil {
					e = err
					break
				}
				if active {
					blocked = append(blocked, map[string]any{"version": sourceVersion, "path": source.ClientPath(), "battle": true})
				}
			}
			out = map[string]any{"blocked": blocked, "ready": len(blocked) == 0}
		}
	case "/pvp/leaderboard":
		page := 0
		if value := r.URL.Query().Get("page"); value != "" {
			page, e = strconv.Atoi(value)
			if e != nil {
				break
			}
		}
		out, e = service.Leaderboard(id, page)
	case "/pvp/leaderboard/team":
		out, e = service.RankedTeam(r.URL.Query().Get("id"))
	case "/pvp/end-streak":
		e = service.EndStreak(id)
	case "/pvp/egg-tickets":
		out, e = service.EggTicketsFor(id)
	case "/pvp/team":
		out, e = service.RegisteredTeam(id)
	case "/pvp/teams":
		out, e = service.RegisteredParties(id)
	case "/pvp/attempt/team":
		out, e = service.AttemptTeam(id)
	case "/pvp/status":
		out, e = service.Status(id)
		if e == nil {
			if system, err := savedata.GetSystem(db.Store, id); err == nil {
				out.(pvp.Object)["playerIdentity"] = map[string]int{"trainerId": system.TrainerId, "secretId": system.SecretId, "gender": system.Gender}
			}
		}
	case "/pvp/runs":
		if e = service.ValidateRunContract(req.Contract); e != nil {
			break
		}
		releaseSave := lockAccountSaves(id)
		defer releaseSave()
		system, err := savedata.GetSystem(db.Store, id)
		if err != nil {
			e = err
		} else {
			out, e = service.StartRun(id, req.Version, req.Starters, system)
		}
	case "/pvp/anchors":
		e = service.Anchor(id, req.RunID, req.Wave, req.Head)
	case "/pvp/register":
		e = service.Register(id, req.RunID, req.Logs)
	case "/pvp/matches":
		out, e = service.StartMatch(id, req.Practice, req.TeamID)
	case "/pvp/commit":
		out, e = service.Commit(id, req.MatchID, req.Decision, version)
	case "/pvp/finish":
		out, e = service.Finish(id, req.MatchID, req.Result, req.PlayerHP, req.EnemyHP, req.Index, version)
	}
	if e != nil {
		httpError(w, r, e, http.StatusConflict)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, out)
}

func battleSourceRouteAllowed(path string) bool {
	switch path {
	case "/pvp/status", "/pvp/matches", "/pvp/commit", "/pvp/finish", "/pvp/end-streak", "/pvp/register", "/pvp/leaderboard", "/pvp/leaderboard/team", "/pvp/team", "/pvp/teams", "/pvp/attempt/team":
		return true
	default:
		return false
	}
}

// Ordinary uploads always continue, including unsupported versions; the default
// validator records their unqualified state without rejecting the save itself.
func streakForSystem(version string) *pvp.Service {
	if service, ok := streakRegistry.Lookup(version); ok {
		return service
	}
	return streakService
}
