package static

import "io/fs"

// Windows installation permissions are inherited from the private home.
// POSIX mode bits do not describe Windows ACLs.
func skinOwned(info fs.FileInfo) bool { return info != nil }
