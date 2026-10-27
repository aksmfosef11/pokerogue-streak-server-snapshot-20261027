// SPDX-License-Identifier: AGPL-3.0-or-later
package api

import (
	"sync"
	"testing"
	"time"
)

func TestAccountSaveReadWriteValidationAreSerialized(t *testing.T) {
	var group sync.WaitGroup
	value := 0
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < 50; n++ {
				release := lockAccountSaves([]byte("same-account"))
				before := value
				value = before + 1
				if value-before != 1 {
					t.Error("save comparison interleaved")
				}
				release()
			}
		}()
	}
	group.Wait()
	if value != 600 {
		t.Fatalf("lost saved state: %d", value)
	}
}
func TestDifferentAccountCanSaveWhileOneAccountWaits(t *testing.T) {
	release := lockAccountSaves([]byte{1})
	defer release()
	done := make(chan bool, 1)
	go func() { unlock := lockAccountSaves([]byte{2}); defer unlock(); done <- true }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unrelated save was blocked")
	}
}
