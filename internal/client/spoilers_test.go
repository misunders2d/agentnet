package client

import (
	"testing"
)

func TestMaskSpoilers(t *testing.T) {
	for _, tc := range []struct{ body, want, line string }{
		{"ordinary **bold** and __old bold__", "ordinary **bold** and __old bold__", "ordinary **bold** and __old bold__"},
		{"visible ||private|| visible", "visible [Hidden text] visible", "visible [Hidden text] visible"},
		{"before ||first\n\nsecond|| after", "before [Hidden text] after", "before [Hidden text] after"},
		{"**before ||_nested_ and `||code||`|| after**", "**before [Hidden text] after**", "**before [Hidden text] after**"},
		{"escaped \\||literal\\|| then ||private||", "escaped \\||literal\\|| then [Hidden text]", "escaped \\||literal\\|| then [Hidden text]"},
		{"`||code||` then ||private||", "`||code||` then [Hidden text]", "`||code||` then [Hidden text]"},
		{"before\n```sh\n||sample||\n```\n||secret||", "before\n```sh\n||sample||\n```\n[Hidden text]", "before"},
		{"```\r\n||sample||\r\n``` \t\r\n||secret||", "```\r\n||sample||\r\n``` \t\r\n[Hidden text]", "```\r"},
		{"```\n||sample||\n```\uFEFF\n||also code||\n```\n||secret||", "```\n||sample||\n```\uFEFF\n||also code||\n```\n[Hidden text]", "```"},
		{"```\n||sample||\n```\u0085\n||also code||\n```\n||secret||", "```\n||sample||\n```\u0085\n||also code||\n```\n[Hidden text]", "```"},
		{"||a|| ||b|| |||literal||| ||unfinished", "[Hidden text] [Hidden text] |||literal||| ||unfinished", "[Hidden text] [Hidden text] |||literal||| ||unfinished"},
	} {
		if got := MaskSpoilers(tc.body); got != tc.want {
			t.Errorf("mask %q = %q, want %q", tc.body, got, tc.want)
		}
		if got := BodyPreview(tc.body); got != tc.line {
			t.Errorf("preview %q = %q, want %q", tc.body, got, tc.line)
		}
	}
}
