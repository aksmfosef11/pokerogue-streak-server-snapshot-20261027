// SPDX-License-Identifier: AGPL-3.0-or-later
package db

import "database/sql"

// PVPDatabase reuses the account database; PvP has its own additive schema.
func PVPDatabase() *sql.DB { return handle }
