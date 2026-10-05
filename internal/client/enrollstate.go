package client

import (
	"encoding/json"
	"errors"
	"os"
)

// What a home holds, for the AgentNet app's first start (cmd/agentnet
// app.go): nothing yet, a join that did not finish, an enrolled agent, or
// one whose membership ended: its device link refused on the other device
// or approved by nobody in time, or (EnrollRemoved, known only from the
// Hub's answer, never from the home) revoked. An ended home can only start
// again with a new invitation or link.
const (
	EnrollNone       = "none"
	EnrollIncomplete = "incomplete"
	EnrollEnrolled   = "enrolled"
	EnrollRefused    = "refused"
	EnrollExpired    = "expired"
	EnrollRemoved    = "removed"
)

// EnrollEnded reports whether state is one an ended membership leaves.
func EnrollEnded(state string) bool {
	return state == EnrollRefused || state == EnrollExpired || state == EnrollRemoved
}

// EnrollmentState says what home holds. It opens the home's database, which
// brings an older one up to this program's schema, so it is called only by
// the process that holds the home's daemon lock: never while another
// daemon, perhaps an older program, uses the home. A database from a newer
// program is refused (sqlitedb.ErrNewerSchema).
func EnrollmentState(home string) (string, error) {
	idPath, dbPath := paths(home)
	_, errID := os.Stat(idPath)
	_, errDB := os.Stat(dbPath)
	if errors.Is(errID, os.ErrNotExist) && errors.Is(errDB, os.ErrNotExist) {
		return EnrollNone, nil
	}
	if errors.Is(errDB, os.ErrNotExist) {
		return EnrollIncomplete, nil // keys made, nothing asked of the Hub yet
	}
	st, err := openStore(dbPath)
	if err != nil {
		return "", err
	}
	defer st.db.Close()
	if enrolled, _ := st.config("enrolled"); enrolled == "1" {
		if errors.Is(errID, os.ErrNotExist) {
			return "", errors.New("this computer's AgentNet data has lost its key (identity.json)")
		}
		var link LinkStatus
		if raw, _ := st.config("link"); raw != "" {
			json.Unmarshal([]byte(raw), &link)
		}
		switch link.State {
		case LinkRefused:
			return EnrollRefused, nil
		case LinkExpired:
			return EnrollExpired, nil
		}
		return EnrollEnrolled, nil
	}
	return EnrollIncomplete, nil
}
