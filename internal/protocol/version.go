package protocol

import (
	"regexp"
	"strconv"
)

// Release recommendations use published vX.Y.Z tags. Development stamps
// have at least their base version's changes; the same base is not newer.
var recommendedVersion = regexp.MustCompile(`^v(\d{1,6})\.(\d{1,6})\.(\d{1,6})$`)
var runningVersion = regexp.MustCompile(`^v(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:-\d{1,9}-g[0-9a-f]{4,40}|\+[0-9A-Za-z.]{1,64})?(?:-dirty)?$`)

// Newer reports whether candidate is a release strictly newer than running
// (a release or a development build stamped from one). Unknown formats
// never trigger an update recommendation. A preview published as vX.Y.Z
// uses that version, independent of the release channel it was published on.
func Newer(candidate, running string) bool {
	a, b := recommendedVersion.FindStringSubmatch(candidate), runningVersion.FindStringSubmatch(running)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x > y
		}
	}
	return false
}
