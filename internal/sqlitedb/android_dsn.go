package sqlitedb

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// androidSQLiteDSN translates only the pragma forms used by our existing
// stores and read-only helpers. The pure translation is desktop-testable;
// only the Android driver invokes it. Unknown settings fail explicitly.
func androidSQLiteDSN(dsn string) (string, error) {
	path, query, _ := strings.Cut(dsn, "?")
	params, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}
	for _, pragma := range params["_pragma"] {
		name, value, ok := strings.Cut(pragma, "(")
		if !ok || !strings.HasSuffix(value, ")") {
			return "", fmt.Errorf("unsupported Android SQLite pragma %q", pragma)
		}
		value = strings.TrimSuffix(value, ")")
		key := ""
		switch name {
		case "journal_mode":
			if value != "WAL" {
				return "", fmt.Errorf("unsupported Android SQLite journal mode %q", value)
			}
			key = "_journal_mode"
		case "busy_timeout":
			ms, err := strconv.ParseInt(value, 10, 32)
			if err != nil || ms < 0 {
				return "", fmt.Errorf("invalid Android SQLite busy timeout %q", value)
			}
			key = "_busy_timeout"
		case "foreign_keys":
			if value != "1" {
				return "", fmt.Errorf("unsupported Android SQLite foreign key setting %q", value)
			}
			key = "_foreign_keys"
		default:
			return "", fmt.Errorf("unsupported Android SQLite pragma %q", pragma)
		}
		if existing := params.Get(key); existing != "" && existing != value {
			return "", fmt.Errorf("conflicting Android SQLite setting %q", key)
		}
		params.Set(key, value)
	}
	params.Del("_pragma")
	// mattn defaults to NORMAL. Preserve the existing SQLite FULL durability
	// explicitly instead, including when no pragma was requested by a helper.
	if sync := params.Get("_synchronous"); sync != "" && sync != "FULL" {
		return "", fmt.Errorf("Android SQLite requires FULL synchronous mode")
	}
	if sync := params.Get("_sync"); sync != "" && sync != "FULL" {
		return "", fmt.Errorf("Android SQLite requires FULL synchronous mode")
	}
	params.Set("_synchronous", "FULL")
	return path + "?" + params.Encode(), nil
}
