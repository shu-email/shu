package mail

import "testing"

func TestExtractedCode(t *testing.T) {
	cases := []struct {
		name    string
		preview string
		want    string
	}{
		{"english", "Your verification code is 482913. It expires in 10 minutes.", "482913"},
		{"grouped", "Your one-time passcode: 482 913 — do not share it.", "482913"},
		{"chinese", "【示例】您的验证码为 738291，10 分钟内有效。", "738291"},
		{"security word first", "Security code 918273", "918273"},
		{"no context", "Order 482913 has shipped.", ""},
		{"year near the word", "Verification of your 2024 statement is complete.", ""},
		{"amount near the word", "Your code payment of $482913 is due.", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ""
			for _, e := range Extracted(c.preview) {
				if e.Kind == KindCode {
					got = e.Copy
				}
			}
			if got != c.want {
				t.Fatalf("code = %q, want %q", got, c.want)
			}
		})
	}
}

func TestExtractedAmountAndDate(t *testing.T) {
	cases := []struct {
		name    string
		preview string
		kind    Kind
		want    string
	}{
		{"symbol", "Invoice total: $1,280.00 is due by 2026-03-14.", KindAmount, "$1,280.00"},
		{"chinese yuan", "本月账单金额 1280 元，请于 3月14日 前缴纳。", KindAmount, "1280 元"},
		{"due date", "Please return the form before 2026-03-14.", KindDate, "2026-03-14"},
		{"chinese due", "材料截止 3月14日 提交。", KindDate, "3月14日"},
		{"no due word", "We met on 2026-03-14 to talk about the launch.", KindDate, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ""
			for _, e := range Extracted(c.preview) {
				if e.Kind == c.kind {
					got = e.Text
				}
			}
			if got != c.want {
				t.Fatalf("%s = %q, want %q", c.kind, got, c.want)
			}
		})
	}
}

func TestExtractedEmpty(t *testing.T) {
	for _, preview := range []string{"", "   ", "See you tomorrow."} {
		if got := Extracted(preview); len(got) != 0 {
			t.Fatalf("Extracted(%q) = %v", preview, got)
		}
	}
}
