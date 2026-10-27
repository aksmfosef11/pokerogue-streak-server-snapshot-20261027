// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import "sync"

// The two save endpoints must compare, write and soft-validate one account's
// system state in the same order. A fixed stripe array bounds memory even when
// many accounts connect; unrelated colliding accounts only wait briefly.
var accountSaveLocks [256]sync.Mutex

func lockAccountSaves(id []byte) func() {
	var stripe byte
	for _, b := range id {
		stripe = stripe*31 + b
	}
	lock := &accountSaveLocks[stripe]
	lock.Lock()
	return lock.Unlock
}
