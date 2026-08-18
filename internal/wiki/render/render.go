// Пакет render — рендеринг вики-страниц: извлечение вики-ссылок [[Цель]],
// подстановка ссылок на страницы и конвертация markdown в безопасный HTML.
package render

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
)

// linkRe — вики-ссылка вида [[Цель]].
var linkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// mdRenderer — конвертер markdown в HTML с типовым набором расширений.
var mdRenderer = goldmark.New(goldmark.WithExtensions())

// sanitizer — политика UGC: типовые теги разрешены, скрипты и iframe вырезаются.
var sanitizer = bluemonday.UGCPolicy()

// ExtractLinks возвращает цели вики-ссылок [[Цель]] в порядке первого
// вхождения, обрезанные по пробелам, без дублей (без учёта регистра).
// Текст без ссылок (или пустой) → nil.
func ExtractLinks(md string) []string {
	matches := linkRe.FindAllStringSubmatch(md, -1)
	if len(matches) == 0 {
		return nil
	}

	links := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		title := strings.TrimSpace(m[1])
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		links = append(links, title)
	}
	if len(links) == 0 {
		return nil
	}
	return links
}

// Render заменяет вики-ссылки [[Цель]] на markdown-ссылки на страницу вики
// (или на жирный текст, если цели нет в targets), конвертирует markdown
// в HTML и санитайзит результат.
func Render(md string, targets map[string]string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(replaceLinks(md, targets)), &buf); err != nil {
		return "", fmt.Errorf("goldmark: %w", err)
	}

	sanitized := sanitizer.SanitizeBytes(buf.Bytes())
	return template.HTML(sanitized), nil
}

// replaceLinks итеративно заменяет [[Цель]]: каждая найденная ссылка
// обрабатывается один раз, уже заменённый фрагмент повторно не сканируется.
func replaceLinks(md string, targets map[string]string) string {
	var b strings.Builder
	rest := md
	for {
		loc := linkRe.FindStringSubmatchIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			break
		}

		b.WriteString(rest[:loc[0]])
		title := strings.TrimSpace(rest[loc[2]:loc[3]])

		if canonical, ok := targets[strings.ToLower(title)]; ok {
			href := "/wiki/page?title=" + url.QueryEscape(canonical)
			b.WriteString("[" + canonical + "](" + href + ")")
		} else {
			b.WriteString("**" + title + "**")
		}
		rest = rest[loc[1]:]
	}
	return b.String()
}
