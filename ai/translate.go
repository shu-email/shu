package ai

import (
	"context"
	"regexp"
	"strings"
)

// Language is a language a translation may be written in.
type Language struct {
	// Tag names it as BCP 47 does, and is what the settings keep.
	Tag string
	// Name is its English name, which the instruction uses: a model reads
	// "Simplified Chinese" more surely than it reads a tag.
	Name string
	// Label is the language's own name, as the settings show it.
	Label string
}

// TranslateLanguages are the languages the settings offer, in the order
// they list them.
var TranslateLanguages = []Language{
	{Tag: "en", Name: "English", Label: "English"},
	{Tag: "zh-Hans", Name: "Simplified Chinese", Label: "简体中文"},
	{Tag: "zh-Hant", Name: "Traditional Chinese", Label: "繁體中文"},
	{Tag: "ja", Name: "Japanese", Label: "日本語"},
	{Tag: "ko", Name: "Korean", Label: "한국어"},
	{Tag: "fr", Name: "French", Label: "Français"},
	{Tag: "de", Name: "German", Label: "Deutsch"},
	{Tag: "es", Name: "Spanish", Label: "Español"},
	{Tag: "pt", Name: "Portuguese", Label: "Português"},
	{Tag: "it", Name: "Italian", Label: "Italiano"},
	{Tag: "ru", Name: "Russian", Label: "Русский"},
	{Tag: "ar", Name: "Arabic", Label: "العربية"},
}

// LanguageName is the English name of a language tag, which an
// instruction names. An unknown tag is returned as it is.
func LanguageName(tag string) string {
	tag = strings.TrimSpace(tag)
	for _, l := range TranslateLanguages {
		if l.Tag == tag {
			return l.Name
		}
	}
	return tag
}

// LanguageLabel is the language's own name, as the settings show it. An
// unknown tag is returned as it is.
func LanguageLabel(tag string) string {
	tag = strings.TrimSpace(tag)
	for _, l := range TranslateLanguages {
		if l.Tag == tag {
			return l.Label
		}
	}
	return tag
}

// translateTokens is the answer budget of a translation: a translated
// message is about as long as the one it came from, and TranslateLimit
// characters of it can take more than the default budget.
const translateTokens = 4096

func translateRequest(m Message, target string) (request, Excerpt) {
	e := MakeExcerpt(m.Body, TranslateLimit)
	if e.Text == "" {
		return request{feature: FeatureTranslate}, e
	}
	return request{
		feature: FeatureTranslate,
		system: "You translate one email for the person who received it. The lines above the blank line name the message: do not translate them. Translate everything below it into " +
			target + ", keeping its paragraph breaks. If it is already in " + target + ", copy it unchanged. Keep names, addresses, numbers, dates and code as they are. Start with one line reading \"Source: <the language the email is written in>\" and a blank line, then the translation. Never add notes, headings or explanations." + untrusted,
		user:      header(m) + "\n" + e.Text,
		maxTokens: translateTokens,
	}, e
}

// TranslatePayload shows what Translate would send, without sending it.
func (s *Service) TranslatePayload(ctx context.Context, m Message, target string) (Payload, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return Payload{}, err
	}
	req, e := translateRequest(m, LanguageName(target))
	if req.user == "" {
		return Payload{}, &Error{Kind: KindEmpty}
	}
	return payload(cfg, req, TranslateLimit, 1, e), nil
}

// Translation is a translated message and what produced it.
type Translation struct {
	Result
	// Source names the language the email was written in, as the model
	// said; empty when it did not say.
	Source string
}

// Translate writes one message in another language. The answer is cached
// by its input, so translating the same message into the same language
// again makes no request. Nothing in the mailbox changes.
func (s *Service) Translate(ctx context.Context, m Message, target string) (Translation, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return Translation{}, err
	}
	if _, err := s.ready(cfg, FeatureTranslate); err != nil {
		return Translation{}, err
	}
	name := LanguageName(target)
	if name == "" {
		return Translation{}, &Error{Kind: KindBadConfig}
	}
	req, _ := translateRequest(m, name)
	if req.user == "" {
		return Translation{}, &Error{Kind: KindEmpty}
	}
	result, err := s.cachedAnswer(ctx, cfg, req, m.Scope)
	if err != nil {
		return Translation{}, err
	}
	source, text := splitTranslation(result.Text)
	result.Text = text
	return Translation{Result: result, Source: source}, nil
}

// sourceLine is the first line the instruction asks for: the language the
// email is written in.
var sourceLine = regexp.MustCompile(`(?i)^source\s*:\s*(.{1,40})$`)

// splitTranslation reads the language the model said the email is in, and
// the translation after it. A model that left the line out still gives a
// usable answer: the whole text is then the translation.
func splitTranslation(text string) (source, body string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if m := sourceLine.FindStringSubmatch(strings.TrimSpace(lines[0])); m != nil {
		source = strings.TrimSpace(m[1])
		rest := lines[1:]
		for len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
			rest = rest[1:]
		}
		if translated := strings.TrimSpace(strings.Join(rest, "\n")); translated != "" {
			return source, translated
		}
	}
	return "", strings.TrimSpace(text)
}
