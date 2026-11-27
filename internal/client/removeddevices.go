package client

import (
	"encoding/json"
	"sort"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// RemovedDevices is presentation evidence only: a peer's exact current pinned
// key appeared in verified roster history and is no longer a current member.
// Address reuse, a pending replacement key and standalone services are not
// inferred to be removed. Saved grants and historical messages stay intact.
func (a *Agent) RemovedDevices() ([]string, error) { return a.removedDevices("('self','pinned')") }

// OwnRemovedDevices is RemovedDevices for this installation's own person
// only: devices the person removed from their own signed roster. Their
// device threads are history, never active chats (AGENTS.md: retired
// configurations stay searchable history). Display only, never authority.
func (a *Agent) OwnRemovedDevices() ([]string, error) { return a.removedDevices("('self')") }

func (a *Agent) removedDevices(states string) ([]string, error) {
	rows, err := a.store.db.Query(`SELECT p.address,p.public,c.record,n.record,coalesce(d.fingerprint,'')
 FROM peers p JOIN person_chain c ON EXISTS
 (SELECT 1 FROM json_each(c.record,'$.devices') h WHERE json_extract(h.value,'$.address')=p.address)
 JOIN persons n ON n.person=c.person AND n.state IN ` + states + `
 LEFT JOIN person_devices d ON d.address=p.address
 WHERE p.pending IS NULL ORDER BY p.address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	removed := map[string]bool{}
	for rows.Next() {
		var address, public, history, current, memberKey string
		if err := rows.Scan(&address, &public, &history, &current, &memberKey); err != nil {
			return nil, err
		}
		var pin identity.Public
		var old, now protocol.PersonRoster
		if err := json.Unmarshal([]byte(public), &pin); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(history), &old); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(current), &now); err != nil {
			return nil, err
		}
		fp := pin.Fingerprint()
		if pin.Address == address && memberKey != fp && old.Has(address, fp) && !now.Has(address, fp) {
			removed[address] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(removed))
	for address := range removed {
		out = append(out, address)
	}
	sort.Strings(out)
	return out, nil
}
