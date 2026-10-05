package main

import (
	"os"
	"path/filepath"
	"strings"
)

// powerSupplies is the kernel's power supply class (tests use a fake one).
var powerSupplies = "/sys/class/power_supply"

// hasBattery reports a system battery: a power supply of type Battery
// whose scope is not Device. A wireless mouse or keyboard reports its own
// battery with scope Device and does not make a desktop a laptop.
func hasBattery() bool {
	entries, err := os.ReadDir(powerSupplies)
	if err != nil {
		return false
	}
	for _, e := range entries {
		dir := filepath.Join(powerSupplies, e.Name())
		kind, err := os.ReadFile(filepath.Join(dir, "type"))
		if err != nil || strings.TrimSpace(string(kind)) != "Battery" {
			continue
		}
		if scope, err := os.ReadFile(filepath.Join(dir, "scope")); err == nil && strings.TrimSpace(string(scope)) == "Device" {
			continue
		}
		return true
	}
	return false
}
