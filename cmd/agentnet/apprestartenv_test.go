package main

import (
	"reflect"
	"testing"
)

func TestAppRestartEnvPreservesHostPathEntries(t *testing.T) {
	t.Setenv("APPDIR", "/tmp/.mount_AgentNet")
	env := []string{
		"APPDIR=/tmp/.mount_AgentNet", "APPIMAGE=/home/a/AgentNet.AppImage",
		"AGENTNET_APP=1", "AGENTNET_APP_EXE=/home/a/AgentNet.AppImage",
		"PATH=/tmp/.mount_AgentNet/usr/bin:/home/a/bin:/usr/bin:/bin",
		"LD_LIBRARY_PATH=/tmp/.mount_AgentNet/usr/lib:/host/lib",
		"XDG_DATA_DIRS=/tmp/.mount_AgentNet/usr/share:/usr/local/share:/usr/share",
		"GTK_DATA_PREFIX=/tmp/.mount_AgentNet", "GTK_THEME=Adwaita:dark",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
	}
	want := []string{
		"PATH=/home/a/bin:/usr/bin:/bin", "LD_LIBRARY_PATH=/host/lib",
		"XDG_DATA_DIRS=/usr/local/share:/usr/share", "GTK_THEME=Adwaita:dark",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
	}
	if got := appRestartEnv(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("restart environment = %q; want %q", got, want)
	}
}

func TestAppRestartEnvDropsMountOnlyLists(t *testing.T) {
	t.Setenv("APPDIR", "/tmp/.mount_AgentNet")
	env := []string{"PATH=/tmp/.mount_AgentNet/usr/bin", "GTK_PATH=/tmp/.mount_AgentNet/usr/lib", "LANG=en_US.UTF-8"}
	if got := appRestartEnv(env); !reflect.DeepEqual(got, []string{"LANG=en_US.UTF-8"}) {
		t.Fatalf("mount-only entries kept: %q", got)
	}
}
