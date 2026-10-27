// SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/pagefaultgames/rogueserver/api/savedata"
	"github.com/pagefaultgames/rogueserver/db"
	"github.com/pagefaultgames/rogueserver/pvp"
	"os"
)

func init() {
	// Match the ordinary server's Gob save decoding registrations.
	gob.Register([]interface{}{})
	gob.Register(map[string]interface{}{})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: pvp-admin review USERNAME | approve-baseline -reason TEXT [-operator NAME] USERNAME")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	reason := flags.String("reason", "", "required review justification")
	operator := flags.String("operator", "local-admin", "local reviewer identifier")
	version := flags.String("version", "", "game version (default: saved system version)")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 1 || (command != "review" && command != "approve-baseline") {
		return fmt.Errorf("choose review or approve-baseline and one username")
	}
	if err := db.Init(os.Getenv("dbuser"), os.Getenv("dbpass"), "tcp", os.Getenv("dbaddr"), os.Getenv("dbname")); err != nil {
		return err
	}
	database := db.PVPDatabase()
	var id []byte
	if err := database.QueryRow(`SELECT uuid FROM accounts WHERE username=?`, flags.Arg(0)).Scan(&id); err != nil {
		return err
	}
	system, err := savedata.GetSystem(db.Store, id)
	if err != nil {
		return err
	}
	registry, err := pvp.OpenRegistry(database)
	if err != nil {
		return err
	}
	defer registry.Close()
	if *version == "" {
		*version = system.GameVersion
	}
	service, ok := registry.Lookup(*version)
	if !ok {
		return fmt.Errorf("unsupported PvP game version: %s", *version)
	}
	if command == "approve-baseline" {
		if err = service.ApproveBaseline(id, system, *operator, *reason); err != nil {
			return err
		}
	}
	report, err := service.ReviewBaseline(id, system)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
