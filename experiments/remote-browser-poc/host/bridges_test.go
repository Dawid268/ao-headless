package main

import "testing"

// A page on the internet may not name a local address as its icon, in any
// form Chrome reads as one.
func TestLocalHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "app.localhost": true, "127.0.0.1": true, "::1": true,
		"10.0.0.8": true, "192.168.1.1": true, "169.254.169.254": true, "0.0.0.0": true,
		"example.com": false, "8.8.8.8": false, "": false, "localhost.example.com": false,
		// What the URL standard turns into 127.0.0.1, 0.0.0.0 or localhost.
		"2130706433": true, "127.1": true, "0x7f000001": true, "0177.0.0.1": true, "127.0.0.1.": true,
		"0": true, "0x": true, "0x7f.1": true, "10.1": true, "LOCALHOST": true, "localhost.": true,
		"App.LocalHost.": true, "::ffff:127.0.0.1": true, "１２７.０.０.１": true,
		// ...and what it does not: 8.8.8.8 as one number, too many parts, out of range.
		"134744072": false, "0x8.0x8.0x8.0x8": false, "1.2.3.4.5": false, "256.0.0.1": false,
		"4294967296": false, "08.0.0.1": false, "127..1": false, "example.1": false, "localhost..": false,
	} {
		if got := localHost(host); got != want {
			t.Errorf("localHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// A Basic challenge opens the sign-in card wherever it is among the header's challenges.
func TestBasicRealm(t *testing.T) {
	for value, want := range map[string]string{
		`Basic realm="x"`: "x", `basic`: "", "Negotiate\nBasic realm=\"x\"": "x",
		`Negotiate, Basic realm="x"`: "x", `Bearer realm="api", Basic realm="x"`: "x",
		`Bearer realm="api"`: "-", `Negotiate`: "-", `Basicx realm="x"`: "-", `Bearer realm="basic"`: "-",
	} {
		got := "-"
		if m := basicRealm.FindStringSubmatch(value); m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("realm of %q = %q, want %q", value, got, want)
		}
	}
}
