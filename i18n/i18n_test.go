package i18n

import (
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		prefs []string
		want  Lang
	}{
		{nil, English},
		{[]string{""}, English},
		{[]string{"garbage;;q"}, English},
		{[]string{"en-US,en;q=0.9"}, English},
		{[]string{"pt-BR,pt;q=0.9,en;q=0.8"}, Portuguese},
		{[]string{"pt"}, Portuguese},
		{[]string{"pt-PT"}, Portuguese},
		{[]string{"pt_BR.UTF-8"}, Portuguese},
		{[]string{"de-DE"}, English},
		{[]string{"fr-FR,pt;q=0.5"}, Portuguese},
		{[]string{"en;q=0.5,pt;q=0.9"}, Portuguese},
		{[]string{"de", "pt-BR"}, Portuguese},
	} {
		assert.Equal(t, tc.want, Match(tc.prefs...), "%q", tc.prefs)
	}
}

func TestTranslate(t *testing.T) {
	assert.Equal(t, "Fila", Portuguese.T("Queue"))
	assert.Equal(t, "Queue", English.T("Queue"))
	assert.Equal(t, "Queue", Lang("").T("Queue"), "the zero value is English")
	assert.Equal(t, "Not in any catalogue", Portuguese.T("Not in any catalogue"), "a missing message falls back to English")
	assert.Equal(t, "100% sure", English.T("100% sure"), "a message without arguments is not a format")
	assert.Equal(t, "Música 12", Portuguese.T("Song %s", "12"))
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{0: "0 músicas", 1: "1 música", 2: "2 músicas"} {
		assert.Equal(t, want, Portuguese.N(n, "%d song", "%d songs"))
	}
	assert.Equal(t, "1 song", English.N(1, "%d song", "%d songs"))
}

// verbs lists a format string's verbs, by argument: "%[2]d of %[1]s" and
// "%s, %d" both have a string first and an integer second.
var verb = regexp.MustCompile(`%(?:\[(\d+)\])?([a-zA-Z%])`)

func verbs(format string) []string {
	var out []string
	next := 1
	for _, m := range verb.FindAllStringSubmatch(format, -1) {
		if m[2] == "%" {
			continue
		}
		i := next
		if m[1] != "" {
			i = int(m[1][0] - '0')
		}
		next = i + 1
		for len(out) < i {
			out = append(out, "")
		}
		out[i-1] = m[2]
	}
	return out
}

// A translation that drops, adds or retypes a verb prints %!d(MISSING) or
// similar in the middle of the page.
func TestTranslationsKeepTheirVerbs(t *testing.T) {
	for _, lang := range Supported {
		for msg, tr := range catalogues[lang] {
			assert.True(t, slices.Equal(verbs(msg), verbs(tr)),
				"%s: %q has verbs %v, its translation %q has %v", lang, msg, verbs(msg), tr, verbs(tr))
		}
	}
}
