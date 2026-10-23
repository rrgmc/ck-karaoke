package webui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"text/template/parse"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/i18n"
)

// templateMessages collects the literal messages passed to t, th and tn in
// every template.
func templateMessages(t *testing.T) []string {
	t.Helper()
	tmpl, err := parseTemplates(i18n.English)
	require.NoError(t, err)

	var out []string
	var walk func(parse.Node)
	walk = func(n parse.Node) {
		switch n := n.(type) {
		case *parse.ListNode:
			if n == nil {
				return
			}
			for _, c := range n.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			walk(n.Pipe)
		case *parse.PipeNode:
			if n == nil {
				return
			}
			for _, c := range n.Cmds {
				walk(c)
			}
		case *parse.CommandNode:
			if id, ok := n.Args[0].(*parse.IdentifierNode); ok {
				// The message arguments: t and th take it first, tn takes the
				// singular and plural after the count.
				var at []int
				switch id.Ident {
				case "t", "th":
					at = []int{1}
				case "tn":
					at = []int{2, 3}
				}
				for _, i := range at {
					if s, ok := n.Args[i].(*parse.StringNode); ok {
						out = append(out, s.Text)
					} else {
						t.Errorf("%s called with a non-literal message: %s", id.Ident, n)
					}
				}
			}
			for _, c := range n.Args {
				walk(c)
			}
		case *parse.IfNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.RangeNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.WithNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.TemplateNode:
			walk(n.Pipe)
		}
	}
	for _, tt := range tmpl.Templates() {
		if tt.Tree != nil {
			walk(tt.Tree.Root)
		}
	}
	return out
}

// goMessages collects the literal messages translated in Go source: the
// message arguments of T, N and notify.
func goMessages(t *testing.T, pattern string) []string {
	t.Helper()
	files, err := filepath.Glob(pattern)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no files match %s", pattern)

	var out []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				fn = f.Sel.Name
			case *ast.Ident:
				fn = f.Name
			}
			var at []int
			switch fn {
			case "T":
				at = []int{0}
			case "N":
				at = []int{1, 2}
			case "notify":
				at = []int{1}
			}
			for _, i := range at {
				if i >= len(call.Args) {
					continue
				}
				// A variable is a wrapper passing its own caller's message
				// through, such as notify or errorText; the literal is
				// found at that caller.
				lit, ok := call.Args[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				out = append(out, s)
			}
			return true
		})
	}
	return out
}

// usedMessages is every message the app translates: the web UI's templates and
// Go code, and the desktop port, which shares the catalogue.
func usedMessages(t *testing.T) []string {
	used := templateMessages(t)
	used = append(used, goMessages(t, "*.go")...)
	used = append(used, goMessages(t, "../ports/desktop/*.go")...)
	for _, k := range knownErrors {
		used = append(used, k.msg)
	}
	slices.Sort(used)
	return slices.Compact(used)
}

// Every message shown must have a translation, or a Portuguese page shows
// English in the middle of it.
func TestEveryMessageIsTranslated(t *testing.T) {
	used := usedMessages(t)
	require.NotEmpty(t, used)
	for _, lang := range i18n.Supported {
		for _, msg := range used {
			assert.True(t, lang.Translates(msg), "%s has no translation for %q", lang, msg)
		}
	}
}

// A translation nothing uses is a message that was reworded or removed, and
// left behind it would hide that the new wording has none.
func TestNoTranslationIsUnused(t *testing.T) {
	used := usedMessages(t)
	for _, lang := range i18n.Supported {
		for _, msg := range lang.Messages() {
			_, found := slices.BinarySearch(used, msg)
			assert.True(t, found, "%s translates %q, which nothing uses", lang, msg)
		}
	}
}

func TestLanguageFollowsTheBrowser(t *testing.T) {
	for header, want := range map[string]i18n.Lang{
		"":                        i18n.English,
		"en-US,en;q=0.9":          i18n.English,
		"pt-BR,pt;q=0.9,en;q=0.8": i18n.Portuguese,
		"pt-PT":                   i18n.Portuguese,
		"fr-FR,pt;q=0.5":          i18n.Portuguese,
		"de-DE":                   i18n.English,
		"en-GB,pt-BR;q=0.9":       i18n.English,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", header)
		assert.Equal(t, want, langOf(r), "Accept-Language %q", header)
	}
}

func TestChosenLanguageOverridesTheBrowser(t *testing.T) {
	for cookie, want := range map[string]i18n.Lang{
		"pt-BR": i18n.Portuguese,
		"en":    i18n.English,
		"fr":    i18n.Portuguese, // unknown: the browser decides
		"":      i18n.Portuguese,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", "pt-BR")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: langCookie, Value: cookie})
		}
		assert.Equal(t, want, langOf(r), "cookie %q", cookie)
	}
}

func TestSetLanguage(t *testing.T) {
	s := &Server{}
	choose := func(lang string, htmx bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/language", strings.NewReader(url.Values{"lang": {lang}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if htmx {
			r.Header.Set("HX-Request", "true")
		}
		w := httptest.NewRecorder()
		s.setLanguage(w, r)
		return w
	}

	w := choose("pt-BR", true)
	assert.Equal(t, "true", w.Header().Get("HX-Refresh"))
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, langCookie, cookies[0].Name)
	assert.Equal(t, "pt-BR", cookies[0].Value)
	assert.Positive(t, cookies[0].MaxAge)

	w = choose("", false)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	cookies = w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Negative(t, cookies[0].MaxAge, "system language clears the cookie")

	w = choose("fr", true)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, w.Result().Cookies())
}

func TestFolderListShowsTheLanguageChoice(t *testing.T) {
	s := &Server{tmpl: templates(t)}
	render := func(choice i18n.Lang) string {
		data := pageData{Browse: browseData{Mode: modeFavorites, LangChoice: choice}}
		var buf strings.Builder
		require.NoError(t, s.templates(i18n.English).ExecuteTemplate(&buf, "browse.html", data))
		return buf.String()
	}

	assert.Contains(t, render(""), `<option value="" selected>System language</option>`)
	assert.Contains(t, render(i18n.Portuguese), `<option value="pt-BR" lang="pt-BR" selected>Português</option>`)
}

// A Portuguese browser gets a Portuguese page, from the html lang attribute to
// the text, and the sentences built in Go follow it too.
func TestPagesRenderInTheRequestedLanguage(t *testing.T) {
	pt, err := parseTemplates(i18n.Portuguese)
	require.NoError(t, err)
	s := &Server{
		tmpl:   templates(t),
		byLang: map[i18n.Lang]*template.Template{i18n.Portuguese: pt},
	}

	data := pageData{Lang: i18n.Portuguese, Browse: browseData{Lang: i18n.Portuguese, Mode: modeSongs, Query: "xyz"}}
	var buf strings.Builder
	require.NoError(t, s.templates(i18n.Portuguese).ExecuteTemplate(&buf, "browse.html", data))
	out := buf.String()

	assert.Contains(t, out, `<html lang="pt-BR"`)
	assert.Contains(t, out, i18n.Portuguese.T("Queue"))
	assert.Contains(t, out, i18n.Portuguese.T("Nothing matches “%s”.", "xyz"))
	assert.NotContains(t, out, ">Queue<")
}
