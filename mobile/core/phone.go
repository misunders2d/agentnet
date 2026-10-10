package core

import (
	"context"
	"github.com/misunders2d/agentnet/internal/ui"
)

// phoneProvider retains the existing native provider and its authorization
// checks while refusing operations that configure execution on this phone.
type phoneProvider struct {
	*ui.Live
	available func() bool
	address   string
}

func phoneUnsupported() error {
	return ui.Refuse("This Android device cannot run or configure local assistants. Use your linked agent computer.")
}
func (p *phoneProvider) Act(x ui.Action) (string, error) {
	switch x.Do {
	case ui.DoAccept, ui.DoAcceptAlways, ui.DoApprove, ui.DoGrantTasks, ui.DoContinue, ui.DoCancel:
		return "", phoneUnsupported()
	}
	return p.Live.Act(x)
}
func (p *phoneProvider) SetResponder(ui.ResponderChange) (string, error) {
	return "", phoneUnsupported()
}
func (p *phoneProvider) ChangeAgent(context.Context, ui.AgentCatalogChange) (ui.AgentCatalogChangeResult, error) {
	return ui.AgentCatalogChangeResult{}, phoneUnsupported()
}
func (p *phoneProvider) Folders(string) (ui.FoldersView, error) {
	return ui.FoldersView{}, phoneUnsupported()
}
func (p *phoneProvider) AssistantSetup(context.Context, ui.AssistantSetupRequest) (ui.AssistantSetupView, error) {
	return ui.AssistantSetupView{Local: false, Harnesses: []ui.AssistantSetupHarness{}, Note: "Set up assistants on your linked agent computer."}, nil
}
func (p *phoneProvider) Overview() (ui.Overview, error) {
	v, e := p.Live.Overview()
	v.ReplyReceivers = false
	if v.Notify != nil {
		ok := p.available != nil && p.available()
		v.Notify.Available = ok
		v.Notify.Native = ok
	}
	return v, e
}
func (p *phoneProvider) Send(d ui.Draft) (ui.Sent, error) {
	if d.ReplyReceiver != nil && d.ReplyReceiver.Kind != "human" && (d.ReplyReceiver.Host == nil || d.ReplyReceiver.Host.Address == p.address) {
		return ui.Sent{}, phoneUnsupported()
	}
	return p.Live.Send(d)
}
func (p *phoneProvider) SendDM(d ui.DMDraft) (ui.Sent, error) {
	if d.ReplyReceiver != nil && d.ReplyReceiver.Kind != "human" && (d.ReplyReceiver.Host == nil || d.ReplyReceiver.Host.Address == p.address) {
		return ui.Sent{}, phoneUnsupported()
	}
	return p.Live.SendDM(d)
}

func (p *phoneProvider) NotifyEnable() (string, error) {
	if p.available == nil || !p.available() {
		return "", ui.Refuse("Native notifications are unavailable on this device.")
	}
	return p.Live.NotifyEnable()
}

func (p *phoneProvider) TopicOverview() (ui.Overview, error) {
	v, e := p.Live.TopicOverview()
	if v.Notify != nil {
		ok := p.available != nil && p.available()
		v.Notify.Available = ok
		v.Notify.Native = ok
	}
	return v, e
}
func (p *phoneProvider) AskAgent(d ui.AgentAsk) (ui.Sent, error) {
	if d.ReplyReceiver != nil && d.ReplyReceiver.Kind != "human" && (d.ReplyReceiver.Host == nil || d.ReplyReceiver.Host.Address == p.address) {
		return ui.Sent{}, phoneUnsupported()
	}
	return p.Live.AskAgent(d)
}
