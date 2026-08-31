package main

import "testing"

func TestHTMLFallbackText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "paragraphs and line breaks become newlines",
			in:   "<p>Hello there.</p><p>Second line.<br/>Third.</p>",
			want: "Hello there.\n\nSecond line.\nThird.",
		},
		{
			name: "entities decode and tags vanish",
			in:   "<div>Fish &amp; <b>chips</b></div>",
			want: "Fish & chips",
		},
		{
			name: "list items keep their bullets",
			in:   "<ul><li>one</li><li>two</li></ul>",
			want: "- one\n- two",
		},
		{
			name: "case-insensitive markup, content case preserved",
			in:   "<P>Keep MY Case</P><BR>After the break",
			want: "Keep MY Case\n\nAfter the break",
		},
		{
			name: "attributes inside tags are dropped",
			in:   `<a href="https://x.test/r?u=1">Read &quot;this&quot;</a>`,
			want: `Read "this"`,
		},
		{
			name: "whitespace is trimmed",
			in:   "   <p>  padded  </p>   ",
			want: "padded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := htmlFallbackText(tc.in); got != tc.want {
				t.Errorf("htmlFallbackText(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOutboundRecipientRejectsHeaderInjection(t *testing.T) {
	for _, tc := range []struct{ to, subject string }{
		{"person@example.com\r\nBcc: victim@example.com", "hello"},
		{"person@example.com", "hello\nBcc: victim@example.com"},
		{"not an address", "hello"},
	} {
		if _, ok := outboundRecipient(tc.to, tc.subject); ok {
			t.Errorf("outboundRecipient(%q, %q) accepted", tc.to, tc.subject)
		}
	}
	got, ok := outboundRecipient("Person <person@example.com>", "hello")
	if !ok || got.Name != "Person" || got.Email != "person@example.com" {
		t.Fatalf("valid recipient = %+v, %v", got, ok)
	}
}
