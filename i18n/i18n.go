// Package i18n translates the text the app shows.
//
// The English text is the key. A message is written in English where it is
// used, and each other language has a catalogue mapping that English to its own
// wording. A message missing from a catalogue falls back to English, so a new
// string never shows up blank; the tests check that every catalogue is
// complete.
package i18n

import (
	"fmt"
	"strings"

	"golang.org/x/text/language"
)

// Lang is a language the app is translated into, as a BCP 47 tag.
//
// The zero value is English, so code that has not been told a language still
// produces readable text.
type Lang string

const (
	English    Lang = "en"
	Portuguese Lang = "pt-BR"
)

// Supported lists every language the app is translated into, English first as
// the fallback.
var Supported = []Lang{English, Portuguese}

// catalogues maps a language to its translations. English has none: its text
// is the key.
var catalogues = map[Lang]map[string]string{
	Portuguese: portuguese,
}

var matcher = language.NewMatcher([]language.Tag{
	language.English,
	language.BrazilianPortuguese,
})

// Match picks the supported language that best fits a list of preferences,
// such as an Accept-Language header ("pt-BR,pt;q=0.9,en;q=0.8") or a single
// tag ("pt_BR", "pt-PT"). Anything unparseable or unsupported gives English.
//
// The first preference the app supports wins, so "fr, pt" gets Portuguese
// rather than the fallback. Every variety of Portuguese gets the Brazilian
// translation, which reads better to a Portuguese speaker than English does.
func Match(prefs ...string) Lang {
	for _, p := range prefs {
		// POSIX locales use an underscore and may carry an encoding
		// ("pt_BR.UTF-8"), neither of which the parser accepts. A header's
		// weights have dots of their own ("q=0.5"), so only a lone locale is
		// cut.
		if !strings.ContainsAny(p, ",;") {
			p, _, _ = strings.Cut(p, ".")
		}
		p = strings.ReplaceAll(p, "_", "-")
		tags, _, err := language.ParseAcceptLanguage(p)
		if err != nil {
			continue
		}
		// Sorted by quality already, so the first match is the one preferred.
		for _, t := range tags {
			if _, i, conf := matcher.Match(t); conf != language.No {
				return Supported[i]
			}
		}
	}
	return English
}

// Tag is the language's BCP 47 tag, for an HTML lang attribute.
func (l Lang) Tag() string {
	if l == "" {
		return string(English)
	}
	return string(l)
}

// Messages lists the English messages a language translates, for tests that
// check a catalogue against the messages the code uses. English has none.
func (l Lang) Messages() []string {
	out := make([]string, 0, len(catalogues[l]))
	for msg := range catalogues[l] {
		out = append(out, msg)
	}
	return out
}

// Translates reports whether a language's catalogue has a message. English
// translates everything, being the text itself.
func (l Lang) Translates(msg string) bool {
	if l == English || l == "" {
		return true
	}
	_, ok := catalogues[l][msg]
	return ok
}

// T translates a message. With arguments, the translation is a fmt format
// string and the arguments are applied to it; without, it is returned as it is,
// so a message may contain a literal %.
func (l Lang) T(msg string, args ...any) string {
	if tr, ok := catalogues[l][msg]; ok {
		msg = tr
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// N translates a message that depends on a count. one and other are the
// English singular and plural, each its own key in the catalogues; n chooses
// between them and is passed to the format first, before args.
//
// English and Portuguese both use the singular for exactly one, so a single
// rule serves every supported language.
func (l Lang) N(n int, one, other string, args ...any) string {
	msg := other
	if n == 1 {
		msg = one
	}
	return l.T(msg, append([]any{n}, args...)...)
}
