package client

func deliveryRank(state string) int {
	switch state {
	case "delivered":
		return 5
	case "custody":
		return 4
	case "waiting":
		return 2
	case "quarantined", "expired":
		return 1
	case "failed":
		return 0
	default:
		return 3
	}
}

// deliveryOf takes each other person's best copy, then the worst person.
// Own devices count only when there are no copies to another person.
func deliveryOf(copies []ConvCopy) string {
	others := false
	for _, c := range copies {
		others = others || !c.Own
	}
	people := map[string]string{}
	for _, c := range copies {
		if others && c.Own {
			continue
		}
		key := c.Person
		if key == "" {
			key = c.To
		}
		old, ok := people[key]
		if !ok || deliveryRank(c.State) > deliveryRank(old) || deliveryRank(c.State) == deliveryRank(old) && c.State < old {
			people[key] = c.State
		}
	}
	state := ""
	first := true
	for _, s := range people {
		if first || deliveryRank(s) < deliveryRank(state) || deliveryRank(s) == deliveryRank(state) && s < state {
			state = s
			first = false
		}
	}
	return state
}
func (s *store) devicePersons() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT address,person FROM person_devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var address, person string
		if err = rows.Scan(&address, &person); err != nil {
			return nil, err
		}
		out[address] = person
	}
	return out, rows.Err()
}
