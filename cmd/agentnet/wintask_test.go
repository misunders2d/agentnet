package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

const sampleTask = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Principals><Principal id="Author"><UserId>RUNNER\bob</UserId><LogonType>InteractiveToken</LogonType></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy></Settings>
  <Actions Context="Author">
    <Exec><Command>"C:\Users\bob\AppData\Local\agentnet\bin\agentnet.exe"</Command><Arguments>--home "C:\agent home" daemon --ui 127.0.0.1:0</Arguments></Exec>
  </Actions>
</Task>`

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := []byte{0xff, 0xfe}
	for _, c := range u {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return b
}

// splitForTest splits like Windows for what the samples use: spaces, and
// double quotes around an argument.
func splitForTest(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	quoted, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted, have = !quoted, true
		case r == ' ' && !quoted:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out, nil
}

func sampleEnv() taskEnv {
	return taskEnv{
		exe:      `C:\Users\bob\AppData\Local\agentnet\bin\agentnet.exe`,
		args:     []string{"--home", `C:\agent home`, "daemon", "--ui", "127.0.0.1:0"},
		user:     `RUNNER\bob`,
		sid:      "S-1-5-21-1-2-3-1001",
		sameFile: func(a, b string) bool { return strings.EqualFold(a, b) },
		split:    splitForTest,
	}
}

// The switch uses the task only when it starts exactly this daemon.
func TestCheckTaskDef(t *testing.T) {
	for _, raw := range [][]byte{utf16LE(sampleTask), []byte(strings.Replace(sampleTask, "UTF-16", "UTF-8", 1))} {
		def, err := parseTaskDef(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkTaskDef(def, sampleEnv()); err != nil {
			t.Fatalf("matching task refused: %v", err)
		}
	}
	for _, c := range []struct {
		name, from, to string
		env            func(*taskEnv)
		want           string
	}{
		{"other program", `agentnet.exe"</Command>`, `other.exe"</Command>`, nil, "starts"},
		{"other home", `C:\agent home`, `C:\other home`, nil, "but this daemon runs as"},
		{"no page", ` --ui 127.0.0.1:0</Arguments>`, `</Arguments>`, nil, "but this daemon runs as"},
		{"extra argument", `127.0.0.1:0</Arguments>`, `127.0.0.1:0 --listen :7443</Arguments>`, nil, "but this daemon runs as"},
		{"other user", `RUNNER\bob</UserId>`, `RUNNER\alice</UserId>`, nil, "runs as"},
		{"two actions", `</Exec>`, `</Exec><Exec><Command>x.exe</Command></Exec>`, nil, "exactly one program"},
		{"not a program", `<Exec><Command>"C:\Users\bob\AppData\Local\agentnet\bin\agentnet.exe"</Command><Arguments>--home "C:\agent home" daemon --ui 127.0.0.1:0</Arguments></Exec>`,
			`<ComHandler><ClassId>{1}</ClassId></ComHandler>`, nil, "exactly one program"},
		{"stop existing", `IgnoreNew`, `StopExisting`, nil, "StopExisting"},
		{"sid principal", `RUNNER\bob</UserId>`, `S-1-5-21-1-2-3-1001</UserId>`, nil, ""},
		{"bare user", `RUNNER\bob</UserId>`, `bob</UserId>`, nil, ""},
		{"daemon started otherwise", "", "", func(e *taskEnv) { e.args = []string{"daemon"} }, "but this daemon runs as"},
	} {
		xmlText := sampleTask
		if c.from != "" {
			if !strings.Contains(xmlText, c.from) {
				t.Fatalf("%s: sample lacks %q", c.name, c.from)
			}
			xmlText = strings.Replace(xmlText, c.from, c.to, 1)
		}
		env := sampleEnv()
		if c.env != nil {
			c.env(&env)
		}
		def, err := parseTaskDef([]byte(xmlText))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		err = checkTaskDef(def, env)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
