package persistenthost

import "sync/atomic"

// SpawnAdmission is the optional warm-host budget hook installed by the daemon.
// persistenthost stays free of budget package imports; the daemon closes over
// chathostbudget.Budget.
type SpawnAdmission struct {
	// BeforeSpawn reserves capacity for a new host process. Returning a
	// non-nil error aborts spawnDetached (adopt paths never call this).
	BeforeSpawn func(sessionID string) error
	// NoteAdopt records an already-live host without a new reservation.
	NoteAdopt func(sessionID string)
	// AfterFailedSpawn runs when spawn was attempted but no live host remains
	// (release the reservation). When hostStillAlive is true the slot must stay.
	AfterFailedSpawn func(sessionID string, hostStillAlive bool)
}

var spawnAdmission atomic.Pointer[SpawnAdmission]

// SetSpawnAdmission installs or clears the daemon-wide spawn gate.
func SetSpawnAdmission(a *SpawnAdmission) {
	spawnAdmission.Store(a)
}

func admission() *SpawnAdmission {
	return spawnAdmission.Load()
}
