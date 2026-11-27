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
func (a *Agent) RemovedDevices() ([]string, error) {
	owned, err := a.RemovedDeviceOwners()
	out := make([]string, 0, len(owned))
	for _, d := range owned {
		out = append(out, d.Address)
	}
	return out, err
}

// RemovedDevice is one removed device and the verified person (this
// installation's own, or a pinned one) whose signed roster dropped it.
type RemovedDevice struct {
	Address string `json:"address"`
	Person  string `json:"person"`
}

// RemovedDeviceOwners is RemovedDevices with each device's person. Their
// device threads are history, never active chats (AGENTS.md: retired
// configurations stay searchable history). Display only, never authority.
func (a *Agent) RemovedDeviceOwners() ([]RemovedDevice, error) {
	rows, err := a.store.db.Query(`SELECT p.address,n.person,p.public,c.record,n.record,coalesce(d.fingerprint,'')
 FROM peers p JOIN person_chain c ON EXISTS
 (SELECT 1 FROM json_each(c.record,'$.devices') h WHERE json_extract(h.value,'$.address')=p.address)
 JOIN persons n ON n.person=c.person AND n.state IN ('self','pinned')
 LEFT JOIN person_devices d ON d.address=p.address
 WHERE p.pending IS NULL ORDER BY p.address, n.state='self' DESC, n.person`) // first person per address wins: this one, else the lowest id
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	removed := map[string]string{}
	for rows.Next() {
		var address, person, public, history, current, memberKey string
		if err := rows.Scan(&address, &person, &public, &history, &current, &memberKey); err != nil {
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
		if pin.Address == address && memberKey == "" && old.Has(address, fp) && !now.Has(address, fp) { // an address a current roster names (re-enrolled, any key) is not removed
			if _, seen := removed[address]; !seen {
				removed[address] = person
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]RemovedDevice, 0, len(removed))
	for address, person := range removed {
		out = append(out, RemovedDevice{Address: address, Person: person})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}
