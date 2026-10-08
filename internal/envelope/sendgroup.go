package envelope

import "errors"

// CheckSendGroup restricts grouping to a human UI addressed request. It grants
// no authority and does not change the independently signed request identity.
func CheckSendGroup(in Inner) error {
	if in.SendGroup != "" && (!validID(in.SendGroup) || in.V != Version2 || in.Conv == "" || in.LID == "" || in.PID == "" || in.Target == nil || in.Origin != OriginUI || in.Human != nil && in.Human.AgentAuthor() || in.Sub != "" || in.Status != "" || in.Kind != KindQuestion && in.Kind != KindTask) {
		return errors.New("send group belongs only to a human UI addressed request")
	}
	return nil
}
