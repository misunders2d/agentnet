//go:build !windows

package notify

import "errors"

func ShowWithSound(title, body string, argv []string, onClick func(), _ []byte, _ bool) error {
	return ShowAction(title, body, argv, onClick)
}

func PreviewSound([]byte) error { return errors.New("Windows notification sound is unavailable") }
