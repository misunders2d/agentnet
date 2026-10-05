package client

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDeliveryTickIgnoresOwnDevices(t *testing.T) {
	data, err := os.ReadFile("../ui/testdata/delivery_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string
		Copies []ConvCopy
		Want   string
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, v := range cases {
		if got := deliveryOf(v.Copies); got != v.Want {
			t.Errorf("%s: %q want %q", v.Name, got, v.Want)
		}
	}
}
