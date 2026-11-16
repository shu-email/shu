package mail

import (
	"regexp"
	"strings"
)

// A Kind is one thing a message says that a list can show without the
// reading pane: something the reader acts on at once, rather than reads.
type Kind string

const (
	// KindCode is a one-time code, the thing a message is often only sent
	// for.
	KindCode Kind = "code"
	// KindAmount is money the message names.
	KindAmount Kind = "amount"
	// KindDate is a day the message asks something of: a deadline, a due
	// date.
	KindDate Kind = "date"
)

// An Extract is one such thing, read from what the cache keeps of the
// message. It is a hint a view shows beside the message, never a fact the
// app acts on: mail is untrusted, and a wrong amount or date costs more
// than a wrong label.
type Extract struct {
	Kind Kind
	// Text is what a view shows, as the message wrote it.
	Text string
	// Copy is what a view puts on the clipboard when the user asks.
	Copy string
}

// The extractors read a message's preview, which is a few hundred bytes of
// its text and all a list holds. They are rules, not a model: a code is
// exact and short-lived, and nothing about a code should leave the device
// to be read. The patterns are small on purpose; a miss only means the
// reader opens the message, and a wrong hit is worse than a miss.
var (
	// Groups of digits a code may be written in.
	codeRun = regexp.MustCompile(`\b(\d{4,8})\b|\b(\d{3})[ -](\d{3})\b`)
	// Words that make a near number a code rather than any other number.
	codeWord = regexp.MustCompile(`(?i)\b(code|otp|passcode|verification|one[- ]time|security)\b|验证码|校验码|动态码|确认码|认证码|口令|密码`)

	// Money: a symbol before a number, or a number before a currency word.
	amountBefore = regexp.MustCompile(`(?:[$€£¥￥]|USD|EUR|CNY|RMB)\s?(\d[\d,]*(?:\.\d{1,2})?)`)
	amountAfter  = regexp.MustCompile(`(\d[\d,]*(?:\.\d{1,2})?)\s?(?:元|美元|欧元|英镑|dollars?|euros?|yuan)`)

	// A day, only where the message says something is due on it.
	dueWord = regexp.MustCompile(`(?i)\b(due|deadline|expires?|by|before|until|no later than)\b|截止|到期|最迟|之前|限期`)
	dateRun = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}|\d{1,2}/\d{1,2}(?:/\d{2,4})?|\d{4}年\d{1,2}月\d{1,2}日|\d{1,2}月\d{1,2}日|\b\d{1,3}(?:st|nd|rd|th)?\s+(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*|\b(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+\d{1,3}\b)`)
	// The closest a due word may be to the day it asks about.
	dueDistance = 40
)

// Extracted reads the things a list can show of one message's preview. A
// preview that says none of them returns none.
func Extracted(preview string) []Extract {
	text := strings.TrimSpace(preview)
	if text == "" {
		return nil
	}
	var out []Extract
	if e, ok := extractCode(text); ok {
		out = append(out, e)
	}
	if e, ok := extractAmount(text); ok {
		out = append(out, e)
	}
	if e, ok := extractDate(text); ok {
		out = append(out, e)
	}
	return out
}

// extractCode finds a code: a run of digits near a word that says they are
// one. A year is not a code, and neither is a number a currency symbol
// stands before.
func extractCode(text string) (Extract, bool) {
	for _, m := range codeRun.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[0], m[1]
		digits := strings.NewReplacer(" ", "", "-", "").Replace(text[start:end])
		if looksLikeYear(digits) || isAmountAt(text, start) {
			continue
		}
		if !nearWord(text, codeWord, start, end, 60) {
			continue
		}
		return Extract{Kind: KindCode, Text: text[start:end], Copy: digits}, true
	}
	return Extract{}, false
}

func extractAmount(text string) (Extract, bool) {
	if m := amountBefore.FindStringSubmatchIndex(text); m != nil {
		return Extract{Kind: KindAmount, Text: strings.TrimSpace(text[m[0]:m[1]]), Copy: strings.TrimSpace(text[m[0]:m[1]])}, true
	}
	if m := amountAfter.FindStringSubmatchIndex(text); m != nil {
		return Extract{Kind: KindAmount, Text: strings.TrimSpace(text[m[0]:m[1]]), Copy: strings.TrimSpace(text[m[0]:m[1]])}, true
	}
	return Extract{}, false
}

func extractDate(text string) (Extract, bool) {
	for _, m := range dateRun.FindAllStringSubmatchIndex(text, -1) {
		if !nearWord(text, dueWord, m[0], m[1], dueDistance) {
			continue
		}
		day := text[m[0]:m[1]]
		return Extract{Kind: KindDate, Text: day, Copy: day}, true
	}
	return Extract{}, false
}

// nearWord reports that w matches within distance bytes of the span
// [start, end).
func nearWord(text string, w *regexp.Regexp, start, end, distance int) bool {
	from, to := start-distance, end+distance
	if from < 0 {
		from = 0
	}
	if to > len(text) {
		to = len(text)
	}
	return w.MatchString(text[from:start]) || w.MatchString(text[end:to])
}

// looksLikeYear reports a four-digit number a reader would read as a year.
func looksLikeYear(digits string) bool {
	if len(digits) != 4 {
		return false
	}
	return digits[:1] == "1" || digits[:1] == "2"
}

// isAmountAt reports that a currency symbol stands just before the span.
func isAmountAt(text string, start int) bool {
	before := text[:start]
	before = strings.TrimSpace(before)
	for _, s := range []string{"$", "€", "£", "¥", "￥"} {
		if strings.HasSuffix(before, s) {
			return true
		}
	}
	return false
}
