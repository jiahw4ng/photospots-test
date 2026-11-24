package source

import "testing"

func TestParseTaken(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		hasTime bool
		ok      bool
	}{
		{"2011:09:12 18:42:03", "2011-09-12 18:42", true, true},
		{"2011-09-12T07:05", "2011-09-12 07:05", true, true},
		{"<time class=\"dtstart\" datetime=\"2019-03-01\">1 March 2019</time>", "2019-03-01 00:00", false, true},
		{"Taken on 12 September 2011", "2011-09-12 00:00", false, true},
		{"1970-01-01 00:00:00", "", true, false},
		{"unknown date", "", false, false},
	}
	for _, c := range cases {
		got, hasTime, ok := ParseTaken(c.in)
		if ok != c.ok || hasTime != c.hasTime {
			t.Errorf("%q: ok=%v hasTime=%v, want ok=%v hasTime=%v", c.in, ok, hasTime, c.ok, c.hasTime)
			continue
		}
		if ok && got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got.Format("2006-01-02 15:04"), c.want)
		}
	}
}

func TestStripHTML(t *testing.T) {
	in := `<a href="//commons.wikimedia.org/wiki/User:X" title="User:X">Jane &amp; Doe</a>`
	if got := StripHTML(in); got != "Jane & Doe" {
		t.Fatalf("got %q", got)
	}
}
