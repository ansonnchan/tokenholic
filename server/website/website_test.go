package website

import "testing"

func TestParseAcceptsOnlyAnHTTPSAddress(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		address      string
		origin, home string
	}{
		{"https://tokenholic.example", "https://tokenholic.example", "https://tokenholic.example/"},
		{" https://Tokenholic.Example/ ", "https://tokenholic.example", "https://tokenholic.example/"},
		{"https://pages.example/tokenholic", "https://pages.example", "https://pages.example/tokenholic/"},
		{"https://pages.example/tokenholic/", "https://pages.example", "https://pages.example/tokenholic/"},
		{"https://pages.example:8443/a/b_c.d", "https://pages.example:8443", "https://pages.example:8443/a/b_c.d/"},
	} {
		site, err := Parse(test.address)
		if err != nil || !site.Configured() || site.Origin() != test.origin || site.Home() != test.home {
			t.Errorf("Parse(%q) = origin %q, home %q, %v; want %q, %q", test.address, site.Origin(), site.Home(), err, test.origin, test.home)
		}
	}
	for _, address := range []string{
		"", "tokenholic.example", "http://tokenholic.example", "https://", "https://user@tokenholic.example",
		"https://tokenholic.example?x=1", "https://tokenholic.example/?", "https://tokenholic.example/#top",
		"https://tokenholic.example/a/../b", "https://tokenholic.example/a//b", "https://tokenholic.example/a b",
		"https://tokenholic.example/%2e%2e", "*", "https://*", "https://*.example", "https://tokenholic..example",
		"https://-a.example", "https://tokenholic.example:", "https://[::1]",
	} {
		if site, err := Parse(address); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", address, site)
		}
	}
	if (Site{}).Configured() {
		t.Error("the zero Site reports itself configured")
	}
}

func TestLinksKeepTheBasePathAndEscapeTheirArgument(t *testing.T) {
	t.Parallel()
	site, err := Parse("https://pages.example/tokenholic/")
	if err != nil {
		t.Fatal(err)
	}
	for got, want := range map[string]string{
		site.Invite("ovt_abc-DEF"): "https://pages.example/tokenholic/invite/#ovt_abc-DEF",
		site.Profile("dana"):       "https://pages.example/tokenholic/u/?login=dana",
		site.Profile("a&b=c#d"):    "https://pages.example/tokenholic/u/?login=a%26b%3Dc%23d",
		site.Board("g-nightshift"): "https://pages.example/tokenholic/b/?slug=g-nightshift",
		site.Invite("x/../y?z"):    "https://pages.example/tokenholic/invite/#x%2F..%2Fy%3Fz",
		site.Home():                "https://pages.example/tokenholic/",
	} {
		if got != want {
			t.Errorf("link = %q, want %q", got, want)
		}
	}
}
