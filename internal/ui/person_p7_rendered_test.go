package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Public inert skin fixture. Real person proof/grant behavior is separately
// exercised by TestP7LivePersonGrantViews and the client delivery regressions.
type p7RenderFixture struct {
	*Fixture
	approved bool
	actions  []Action
	person   PersonView
}

func (f *p7RenderFixture) Overview() (Overview, error) {
	o, e := f.Fixture.Overview()
	o.People = append(o.People, f.person)
	return o, e
}
func (f *p7RenderFixture) Thread(id string) (Thread, error) {
	v, e := f.Fixture.Thread(id)
	if e != nil {
		return v, e
	}
	if v.Peer == f.person.Address {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := f.person
		v.PermissionPerson = &p
		v.Approved = f.approved
		if f.approved {
			v.QuestionTarget = p.Person
		}
		v.TaskGrant = "active"
		v.TaskTarget = p.Person
	}
	return v, nil
}
func (f *p7RenderFixture) Act(x Action) (string, error) {
	if x.Do == DoApprove || x.Do == DoUnapprove {
		if x.ID != f.person.Person {
			return "", Refuse("fixture requires exact person ID")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.approved = x.Do == DoApprove
		f.actions = append(f.actions, x)
		f.bump()
		return "Sergey's permission changed.", nil
	}
	return f.Fixture.Act(x)
}
func (f *p7RenderFixture) Approvals() (ApprovalsView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := ApprovalsView{Questions: []QuestionApproval{}, Tasks: []TaskGrantView{{Person: f.person.Person, Label: f.person.Label, Status: "active"}}, Participations: []ParticipationGrant{}}
	if f.approved {
		v.Questions = append(v.Questions, QuestionApproval{Person: f.person.Person, Label: f.person.Label, Status: "active"})
	}
	return v, nil
}

func TestP7PersonApprovalsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_P7_RENDERED") != "1" {
		t.Skip("opt-in P7 inert rendered fixture")
	}
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	base := NewFixture(time.Now)
	base.CreatePerson("Vitalii")
	f := &p7RenderFixture{Fixture: base, person: PersonView{Person: strings.Repeat("f", 32), Label: "Sergey", Address: "dave/srv", State: PersonPinned, Fingerprint: "77d01c5a-00000000-00000000-00000000", Devices: []DeviceView{{Address: "dave/srv", Name: "Srv", Fingerprint: "77d01c5a-00000000-00000000-00000000", Human: true}}}}
	var threadID string
	for _, th := range base.threads {
		if th.peer == f.person.Address && th.msgs[0].Kind == KindQuestion {
			threadID = th.msgs[0].ID
			for _, m := range th.msgs {
				if m.Kind == KindQuestion {
					m.Body = "P7 person permission question"
				}
			}
			break
		}
	}
	var server *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/p7/reset":
			f.mu.Lock()
			f.approved = false
			f.bump()
			f.mu.Unlock()
			w.WriteHeader(204)
			return
		case "/p7/actions":
			f.mu.Lock()
			defer f.mu.Unlock()
			json.NewEncoder(w).Encode(f.actions)
			return
		}
		server.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	server = New(f, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.Command(node, "testdata/person_p7_rendered.cjs")
	cmd.Env = append(os.Environ(), "P7_URL="+ts.URL+"/?t="+testToken, "P7_THREAD="+threadID, "P7_PERSON="+f.person.Person)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	if !strings.Contains(string(out), "P7 person approval rendered PASS") {
		t.Fatal(string(out))
	}
	t.Log(string(out))
}
