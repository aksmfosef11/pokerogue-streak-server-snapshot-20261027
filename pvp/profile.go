// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This storage identity stays fixed when the default game version changes.
const legacyStorageVersion = "1.12.1.3"
const maxVersionProfiles = 8

type VersionProfile struct {
	Version       string
	BattleVersion string
	ClientPath    string
	AuditPolicy   string
	AuditURL      string
	Rules         Rules
	Manifest      RulesManifest
}
type versionProfileConfig struct {
	BattleVersion string `json:"battleVersion,omitempty"`
	ClientPath    string `json:"clientPath,omitempty"`
	RulesDir      string `json:"rulesDir"`
	AuditPolicy   string `json:"auditPolicy"`
	AuditURL      string `json:"auditURL"`
}
type versionRegistryConfig struct {
	DefaultVersion string                          `json:"defaultVersion"`
	Versions       map[string]versionProfileConfig `json:"versions"`
}

func loadVersionProfiles() (string, []VersionProfile, error) {
	path := os.Getenv("PVP_VERSION_REGISTRY")
	if path == "" {
		dir := os.Getenv("PVP_RULES_DIR")
		if dir == "" {
			dir = "pvp"
		}
		p, err := loadVersionProfile(Version, dir, AuditPolicy, os.Getenv("PVP_AUDIT_URL"))
		return Version, []VersionProfile{p}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var config versionRegistryConfig
	if err = json.Unmarshal(raw, &config); err != nil {
		return "", nil, err
	}
	if len(config.Versions) == 0 || len(config.Versions) > maxVersionProfiles {
		return "", nil, fmt.Errorf("PvP registry must contain 1..%d versions", maxVersionProfiles)
	}
	if _, ok := config.Versions[config.DefaultVersion]; !ok {
		return "", nil, fmt.Errorf("PvP default version is absent from registry")
	}
	names := make([]string, 0, len(config.Versions))
	for name := range config.Versions {
		names = append(names, name)
	}
	sort.Strings(names)
	profiles := make([]VersionProfile, 0, len(names))
	for _, name := range names {
		c := config.Versions[name]
		dir := c.RulesDir
		if dir == "" {
			return "", nil, fmt.Errorf("missing rulesDir for PvP version %s", name)
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(filepath.Dir(path), dir)
		}
		p, e := loadVersionProfile(name, dir, c.AuditPolicy, c.AuditURL)
		if e != nil {
			return "", nil, e
		}
		p.BattleVersion, p.ClientPath = c.BattleVersion, c.ClientPath
		if p.BattleVersion == "" {
			p.BattleVersion = name
		}
		if p.ClientPath != "" && (!regexp.MustCompile(`^/engines/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/$`).MatchString(p.ClientPath) || p.ClientPath != "/engines/"+name+"/") {
			return "", nil, fmt.Errorf("invalid PvP client path for %s", name)
		}
		profiles = append(profiles, p)
	}
	for _, p := range profiles {
		target, ok := config.Versions[p.BattleVersion]
		if !ok || target.ClientPath == "" && p.BattleVersion != p.Version || target.BattleVersion != "" && target.BattleVersion != p.BattleVersion || strings.Join(strings.Split(p.Version, ".")[:2], ".") != strings.Join(strings.Split(p.BattleVersion, ".")[:2], ".") {
			return "", nil, fmt.Errorf("unsupported PvP battle engine for %s", p.Version)
		}
	}
	return config.DefaultVersion, profiles, nil
}

func loadVersionProfile(version, dir, policy, workerURL string) (VersionProfile, error) {
	p := VersionProfile{Version: version, AuditPolicy: policy, AuditURL: strings.TrimRight(workerURL, "/")}
	if len(version) > 32 || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) || !strings.HasPrefix(policy, version+":generation-") || len(policy) <= len(version+":generation-") || len(policy) > 120 {
		return p, fmt.Errorf("invalid PvP version/policy: %s", version)
	}
	if workerURL != "" {
		u, err := url.Parse(p.AuditURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return p, fmt.Errorf("invalid PvP worker URL for %s", version)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "rules.json"))
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p.Rules); err != nil {
		return p, err
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "rules.manifest.json"))
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(manifest, &p.Manifest); err != nil {
		return p, err
	}
	sum := sha256.Sum256(raw)
	if p.Manifest.Version != version || p.Manifest.Schema != 1 || p.Manifest.AuditSchema != 2 || p.Manifest.Harness == "" || p.Manifest.RulesSHA256 != hex.EncodeToString(sum[:]) {
		return p, fmt.Errorf("PvP rules/version manifest mismatch for %s: regenerate with pnpm pvp:export", version)
	}
	if len(p.Rules) == 0 {
		return p, fmt.Errorf("empty PvP rules for %s", version)
	}
	for species, r := range p.Rules {
		if r.Candy == nil || r.Candy.FriendshipCap <= 0 || len(r.Candy.Reduction) != 2 || len(r.Candy.EggCosts) != len(r.Candy.EggThresholds)+1 || r.Root <= 0 || len(r.StarterMoves) == 0 {
			return p, fmt.Errorf("missing exported candy rules for species %d in %s", species, version)
		}
	}
	return p, nil
}

func (s *Service) GameVersion() string {
	if s.profile != nil {
		return s.profile.Version
	}
	return Version
}
func (s *Service) Policy() string {
	if s.profile != nil {
		return s.profile.AuditPolicy
	}
	return AuditPolicy
}

func (s *Service) BattleVersion() string {
	if s.profile != nil && s.profile.BattleVersion != "" {
		return s.profile.BattleVersion
	}
	return s.GameVersion()
}

func (s *Service) ClientPath() string {
	if s.profile != nil && s.profile.ClientPath != "" {
		return s.profile.ClientPath
	}
	return "/"
}

func (s *Service) battleProfiles() []*Service {
	if s.registry == nil {
		return []*Service{s}
	}
	var profiles []*Service
	for _, version := range s.registry.Versions() {
		candidate := s.registry.services[version]
		if candidate.BattleVersion() == s.BattleVersion() {
			profiles = append(profiles, candidate)
		}
	}
	return profiles
}

func (s *Service) bindBattleEngine(m *Match) {
	m.SourceVersion = s.GameVersion()
	m.Session["gameVersion"] = s.BattleVersion()
	if s.registry != nil {
		if target, ok := s.registry.Lookup(s.BattleVersion()); ok && target.profile != nil {
			m.BattlePath = target.profile.ClientPath
		}
	}
}
func (s *Service) storageSuffix() string {
	if s.GameVersion() == legacyStorageVersion {
		return ""
	}
	sum := sha256.Sum256([]byte(s.GameVersion()))
	return "_" + hex.EncodeToString(sum[:16])
}
func (s *Service) accountsTable() string { return "pvpAccounts" + s.storageSuffix() }
func (s *Service) teamsTable() string    { return "pvpTeams" + s.storageSuffix() }
