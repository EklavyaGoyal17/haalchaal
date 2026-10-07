package main

import "testing"

func TestPasswordFromLine(t *testing.T) {
	for in, want := range map[string]string{
		"secret pass\n":                       "secret pass",
		"secret pass\r\n":                     "secret pass",
		"\xef\xbb\xbfsecret pass\r\n":         "secret pass", // Windows PowerShell pipe
		"secret pass":                         "secret pass",
		" spaced \n":                          " spaced ",
		"\xef\xbb\xbf\xef\xbb\xbftwo marks\n": "\xef\xbb\xbftwo marks",
	} {
		if got := passwordFromLine(in); got != want {
			t.Errorf("passwordFromLine(%q) = %q, want %q", in, got, want)
		}
	}
}
