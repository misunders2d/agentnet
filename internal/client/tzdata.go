package client

// Windows has no system IANA timezone database, so embed it for schedule validation.
import _ "time/tzdata"
