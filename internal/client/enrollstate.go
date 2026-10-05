package client

import (
	"errors"
	"os"
)

// What a home holds, for the AgentNet app's first start (cmd/agentnet
// app.go): nothing yet, a join that did not finish, or an enrolled agent.
const (
	EnrollNone       = "none"
	EnrollIncomplete = "incomplete"
	EnrollEnrolled   = "enrolled"
)

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
		return EnrollEnrolled, nil
	}
	return EnrollIncomplete, nil
}
